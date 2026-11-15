//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type webContentHTTPFixture struct {
	store        *WebAPIStore
	storeOptions WebAPIStoreOptions
	server       *httptest.Server
	auth         *WebServer
	content      *WebAPIContentService
	backup       *WebAPIBackupService
	sources      *WebAPISourceService
	cookie       *http.Cookie
}

func newWebContentHTTPFixture(t *testing.T, client *http.Client, fault func(string) error) *webContentHTTPFixture {
	t.Helper()
	auth := newWebTestServer(t)
	options := WebAPIStoreOptions{DataDir: filepath.Join(t.TempDir(), "managed")}
	store, err := OpenWebAPIStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	mux := http.NewServeMux()
	RegisterWebAPIResourceRoutes(mux, store)
	contentOptions := WebAPIContentOptions{}
	if client != nil {
		contentOptions.SourceOptions = WebAPISourceFetchOptions{LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
		}}
	}
	content := RegisterWebAPIContentRoutes(mux, store, contentOptions)
	backup := RegisterWebAPIBackupRoutes(mux, store, WebAPIBackupOptions{Fault: fault})
	sources := RegisterWebAPISourceRoutes(mux, store, WebAPISourceOptions{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/auth/") {
			auth.ServeHTTP(w, r)
			return
		}
		key, session := auth.session(r)
		if session == nil || !auth.sameOrigin(r) {
			http.Error(w, "unauthorized", 401)
			return
		}
		ctx := WithWebAPIRequestAuthorization(r.Context(), hex.EncodeToString(key[:]), func(context.Context) (func(), error) {
			auth.mu.Lock()
			auth.purgeLocked()
			if auth.sessions[key] != session {
				auth.mu.Unlock()
				return nil, webAPIError(401, "unauthorized", "session expired")
			}
			return auth.mu.Unlock, nil
		})
		if r.Header.Get("X-Test-Drop-Response") == "1" {
			buffer := httptest.NewRecorder()
			mux.ServeHTTP(buffer, r.WithContext(ctx))
			if h, ok := w.(http.Hijacker); ok {
				connection, _, err := h.Hijack()
				if err == nil {
					connection.Close()
					return
				}
			}
			return
		}
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &webContentHTTPFixture{store: store, storeOptions: options, server: server, auth: auth, content: content, backup: backup, sources: sources, cookie: webLogin(t, auth)}
}

func (f *webContentHTTPFixture) request(t *testing.T, method, path string, body any, cookie *http.Cookie) (int, []byte) {
	t.Helper()
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response, err := f.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, result
}

func webContentImportRequest(operation, revision string) WebAPIProfileImport {
	return WebAPIProfileImport{WebAPIMutation: WebAPIMutation{ExpectedRevision: WebAPIRevision(revision), OperationID: operation}, Label: "Test", Source: WebAPIContentImportSource{Kind: "file", Bytes: []byte("proxies: []\nrules:\n  - MATCH,DIRECT\n")}}
}

func TestWebContentHTTPImportWriteCASReplayAndAuth(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	request := webContentImportRequest("import-one", "0")
	request.Label = "Test, exact profile label"
	if status, _ := f.request(t, "POST", "/api/v1/profiles:import", request, nil); status != 401 {
		t.Fatalf("unauthorized status %d", status)
	}
	status, first := f.request(t, "POST", "/api/v1/profiles:import", request, f.cookie)
	if status != 200 {
		t.Fatalf("import %d: %s", status, first)
	}
	var created WebAPIEnvelope[WebAPIProfile]
	if err := json.Unmarshal(first, &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.Label != request.Label {
		t.Fatal("structured import label changed")
	}
	status, replay := f.request(t, "POST", "/api/v1/profiles:import", request, f.cookie)
	if status != 200 || !bytes.Equal(first, replay) {
		t.Fatalf("replay %d: %s", status, replay)
	}
	other := webLogin(t, f.auth)
	request.OperationID = "import-two"
	if status, body := f.request(t, "POST", "/api/v1/profiles:import", request, other); status != 409 {
		t.Fatalf("stale second session %d: %s", status, body)
	}
	write := WebAPIContentWrite{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "write-one"}, Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: created.Data.ID}, Bytes: []byte("rules:\n - MATCH,REJECT\n")}
	if status, body := f.request(t, "POST", "/api/v1/content:write", write, other); status != 200 {
		t.Fatalf("write %d: %s", status, body)
	}
	status, body := f.request(t, "GET", "/api/v1/content/profile/"+string(created.Data.ID), nil, f.cookie)
	if status != 200 {
		t.Fatalf("get %d: %s", status, body)
	}
	var content WebAPIEnvelope[WebAPIContentResult]
	if err := json.Unmarshal(body, &content); err != nil {
		t.Fatal(err)
	}
	if content.Revision != "2" || !bytes.Equal(content.Data.Bytes, write.Bytes) {
		t.Fatalf("not committed snapshot: %s", body)
	}
	invalid := map[string]any{"expectedRevision": "2", "operationId": "bad-write", "key": write.Key, "bytes": write.Bytes, "savePath": "/tmp/outside"}
	if status, _ := f.request(t, "POST", "/api/v1/content:write", invalid, f.cookie); status != 400 {
		t.Fatalf("path field status %d", status)
	}
}

func TestWebContentHTTPResponseLossReplaysAfterReopen(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	request := webContentImportRequest("lost-response", "0")
	body, _ := json.Marshal(request)
	req, _ := http.NewRequest("POST", f.server.URL+"/api/v1/profiles:import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Drop-Response", "1")
	req.AddCookie(f.cookie)
	if response, err := f.server.Client().Do(req); err == nil {
		response.Body.Close()
		t.Fatal("expected disconnected response")
	}
	receipt, err := f.store.Receipt(context.Background(), request.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	status, replayed := f.request(t, "POST", "/api/v1/profiles:import", request, f.cookie)
	if status != 200 || !bytes.Equal(receipt.Body, replayed) {
		t.Fatalf("replay %d: %s", status, replayed)
	}
	f.server.Close()
	if err = f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWebAPIStore(f.storeOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	saved, err := reopened.Receipt(context.Background(), request.OperationID)
	if err != nil || !bytes.Equal(saved.Body, receipt.Body) {
		t.Fatalf("durable receipt %v", err)
	}
	_, err = reopened.View(context.Background(), func(tx *WebAPIReadTx) error {
		profiles, err := tx.Profiles()
		if err == nil && len(profiles) != 1 {
			t.Errorf("duplicate commit: %d", len(profiles))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebBackupHTTPAtomicFailureResetAndAuthUnchanged(t *testing.T) {
	var failMu sync.Mutex
	failStage := ""
	f := newWebContentHTTPFixture(t, nil, func(stage string) error {
		failMu.Lock()
		defer failMu.Unlock()
		if stage == failStage {
			return errors.New("synthetic fault")
		}
		return nil
	})
	if status, body := f.request(t, "POST", "/api/v1/profiles:import", webContentImportRequest("seed-profile", "0"), f.cookie); status != 200 {
		t.Fatalf("seed %d: %s", status, body)
	}
	authBefore, err := f.auth.authRoot.ReadFile(f.auth.authName)
	if err != nil {
		t.Fatal(err)
	}
	status, archive := f.request(t, "GET", "/api/v1/backups?format=flclash-v1", nil, f.cookie)
	if status != 200 {
		t.Fatalf("export %d: %s", status, archive)
	}
	restore := WebAPIBackupRestore{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "restore-fault"}, Intent: "restore-application-data", Strategy: "override", ArchiveBase64: archive}
	for _, stage := range []string{"before-write", "after-metadata", "after-content", "after-settings"} {
		failMu.Lock()
		failStage = stage
		failMu.Unlock()
		status, body := f.request(t, "POST", "/api/v1/backups:restore", restore, f.cookie)
		if status != 500 {
			t.Fatalf("fault %s %d: %s", stage, status, body)
		}
		rev, err := f.store.View(context.Background(), func(tx *WebAPIReadTx) error {
			profiles, e := tx.Profiles()
			if e != nil {
				return e
			}
			if len(profiles) != 1 {
				t.Fatalf("metadata lost at %s", stage)
			}
			contents, e := tx.Contents()
			if len(contents) != 1 {
				t.Fatalf("blob lost at %s", stage)
			}
			return e
		})
		if err != nil || rev != "1" {
			t.Fatalf("fault advanced revision %s %v", rev, err)
		}
	}
	failMu.Lock()
	failStage = ""
	failMu.Unlock()
	status, receipt := f.request(t, "POST", "/api/v1/backups:restore", restore, f.cookie)
	if status != 200 {
		t.Fatalf("restore %d: %s", status, receipt)
	}
	status, replay := f.request(t, "POST", "/api/v1/backups:restore", restore, f.cookie)
	if status != 200 || !bytes.Equal(receipt, replay) {
		t.Fatal("restore replay changed")
	}
	reset := WebAPIReset{WebAPIMutation: WebAPIMutation{ExpectedRevision: "2", OperationID: "reset-data"}, Intent: "reset-application-data"}
	if status, body := f.request(t, "POST", "/api/v1/app:reset", reset, f.cookie); status != 200 {
		t.Fatalf("reset %d: %s", status, body)
	}
	authAfter, err := f.auth.authRoot.ReadFile(f.auth.authName)
	if err != nil || !bytes.Equal(authBefore, authAfter) {
		t.Fatalf("auth changed: %v", err)
	}
	if status, _ := f.request(t, "GET", "/api/auth/session", nil, f.cookie); status != 200 {
		t.Fatal("restore/reset revoked auth")
	}
	if status, body := f.request(t, "POST", "/api/v1/backups:restore", restore, f.cookie); status != 200 || !bytes.Equal(receipt, body) {
		t.Fatal("reset erased prior receipts")
	}
	request := webContentImportRequest("seed-after-reset", "3")
	status, body := f.request(t, "POST", "/api/v1/profiles:import", request, f.cookie)
	if status != 200 {
		t.Fatalf("after reset %d: %s", status, body)
	}
	var profile WebAPIEnvelope[WebAPIProfile]
	json.Unmarshal(body, &profile)
	if profile.Data.ID == "1" {
		t.Fatal("reset reused identity")
	}
}

func TestWebContentRefreshUsesFixedGETAndPurposeBoundBasicAuth(t *testing.T) {
	var called int
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if r.Method != "GET" {
			t.Error("not GET")
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "synthetic" || password != "p:a ss" {
			t.Errorf("credentials not decoded: %q %q", user, password)
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" {
			t.Error("forwarded ambient credentials")
		}
		w.Header().Set("subscription-userinfo", "upload=9007199254740993; total=9223372036854775807")
		io.WriteString(w, "proxies: []\nrules:\n - MATCH,DIRECT\n")
	}))
	defer source.Close()
	f := newWebContentHTTPFixture(t, source.Client(), nil)
	request := webContentImportRequest("remote-source", "0")
	// This fixture resolves a synthetic public name and pins its dial to the
	// local test server. A literal loopback URL correctly requires a grant.
	publicSource := strings.Replace(source.URL, "127.0.0.1", "public.fixture.test", 1)
	request.Source = WebAPIContentImportSource{Kind: "url", URL: strings.Replace(publicSource, "http://", "http://synthetic:p%3Aa%20ss@", 1)}
	status, body := f.request(t, "POST", "/api/v1/profiles:import", request, f.cookie)
	if status != 200 {
		t.Fatalf("remote import %d: %s", status, body)
	}
	var created WebAPIEnvelope[WebAPIProfile]
	json.Unmarshal(body, &created)
	if created.Data.SubscriptionInfo.Upload != "9007199254740993" {
		t.Fatal("subscription count rounded")
	}
	if status, _ := f.request(t, "POST", "/api/v1/profiles:import", request, f.cookie); status != 200 || called != 1 {
		t.Fatalf("replay refetched (%d)", called)
	}
	refresh := WebAPIContentRefresh{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "remote-refresh"}, Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: created.Data.ID}, Purpose: "source"}
	if status, body := f.request(t, "POST", "/api/v1/content:refresh", refresh, f.cookie); status != 200 {
		t.Fatalf("refresh %d: %s", status, body)
	}
	if called != 2 {
		t.Fatalf("fetch count %d", called)
	}
}
