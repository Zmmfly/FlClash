//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	bolt "github.com/metacubex/bbolt"
)

func TestWebSourceDAVGrantUsesCommittedWholeTupleAndNeverGenericGet(t *testing.T) {
	for _, purpose := range []WebAPISourcePurpose{WebAPISourceDAVProbe, WebAPISourceDAVBackup, WebAPISourceDAVRestore} {
		t.Run(string(purpose), func(t *testing.T) {
			store := webSourceTestStore(t)
			dav := WebAPISettingsDAV{URI: "http://dav.test/base?token=synthetic", User: "synthetic-user", Password: "synthetic-password", FileName: "backup.zip"}
			_, err := store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "0", OperationID: "seed-dav"}, "test:seed", nil, func(tx *WebAPIWriteTx) (any, error) {
				settings, err := tx.Settings()
				if err != nil {
					return nil, err
				}
				settings.DAV = &dav
				return nil, tx.SaveSettings(settings)
			})
			if err != nil {
				t.Fatal(err)
			}
			service := NewWebAPISourceService(store, WebAPISourceOptions{WebAPISourceFetchOptions: WebAPISourceFetchOptions{LookupNetIP: webSourceTestDNS("10.1.2.3")}})
			ctx := webSourceTestContext("source-session")
			prepare := WebAPISourcePrepare{ExpectedRevision: "1", Purpose: purpose, Key: &WebAPISourceResource{Kind: "dav", ID: "settings"}}
			_, grant := webSourceTestAuthorize(t, service, ctx, prepare, "dav-grant")
			_, expectedURL, _ := webSourceCanonical(dav.URI)
			if grant.RequestDigest != webDAVTupleDigest(dav) || grant.URLDigest != expectedURL {
				t.Fatal("DAV authorization did not bind exact tuple and compatible URL identity")
			}
			for _, field := range []string{"URI", "User", "Password", "FileName"} {
				t.Run(field, func(t *testing.T) {
					changed := dav
					switch field {
					case "URI":
						changed.URI += "changed"
					case "User":
						changed.User += "changed"
					case "Password":
						changed.Password += "changed"
					case "FileName":
						changed.FileName = "other.zip"
					}
					action := WebAPIDAVAction(strings.TrimPrefix(string(purpose), "dav-"))
					transport, err := newWebDAVTransport(changed, action, grant.ID, &grant, WebAPIDAVOptions{LookupNetIP: webSourceTestDNS("10.1.2.3"), DialContext: func(context.Context, string, string) (net.Conn, error) {
						t.Error("changed DAV tuple dialed")
						return nil, fmt.Errorf("forbidden")
					}}, nil)
					if err != nil {
						t.Fatal(err)
					}
					_, err = transport.probe(ctx)
					webBackupRequireCode(t, err, "source_authorization_invalid")
				})
			}
			prepare.URL = "http://client-supplied.test/"
			_, _, err = service.Prepare(ctx, prepare)
			webBackupRequireCode(t, err, "invalid_source_request")
			_, err = WebAPIFetchSource(ctx, store, WebAPISourceRequest{Purpose: purpose, Key: prepare.Key, URL: dav.URI, AuthorizationID: grant.ID, RequestDigest: grant.RequestDigest}, WebAPISourceFetchOptions{})
			webBackupRequireCode(t, err, "invalid_source_purpose")
		})
	}
}

type webSourceBlockedWriter struct {
	http.ResponseWriter
	entered, release chan struct{}
}

func (w *webSourceBlockedWriter) Write(body []byte) (int, error) {
	close(w.entered)
	<-w.release
	return w.ResponseWriter.Write(body)
}

func TestWebSourceHTTPCommitLeaseEndsBeforeResponseWrite(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	f.sources.options.LookupNetIP = webSourceTestDNS("10.1.2.3")
	status, body := f.request(t, "POST", "/api/v1/sources:prepare", WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceProfileImport, URL: "http://lan.test/sub"}, f.cookie)
	if status != 200 {
		t.Fatalf("prepare %d: %s", status, body)
	}
	var preparation WebAPIEnvelope[WebAPISourcePreparation]
	if err := json.Unmarshal(body, &preparation); err != nil {
		t.Fatal(err)
	}
	commits := 0
	original := f.store.commit
	f.store.commit = func(tx *bolt.Tx) error {
		commits++
		if f.auth.mu.TryLock() {
			f.auth.mu.Unlock()
			t.Error("source commit released actual session authorization too early")
		}
		return original(tx)
	}
	defer func() { f.store.commit = original }()
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.server.Config.Handler.ServeHTTP(&webSourceBlockedWriter{w, entered, release}, r)
	}))
	defer server.Close()
	request := WebAPISourceAuthorize{ExpectedRevision: "0", OperationID: "blocked-source-writer", PreparationID: preparation.Data.PreparationID, URLDigest: preparation.Data.URLDigest, Intent: "authorize-server-private-source"}
	encoded, _ := json.Marshal(request)
	done := make(chan int, 1)
	go func() {
		r, _ := http.NewRequest("POST", server.URL+"/api/v1/sources:authorize", bytes.NewReader(encoded))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(f.cookie)
		response, err := server.Client().Do(r)
		if err != nil {
			done <- 0
			return
		}
		response.Body.Close()
		done <- response.StatusCode
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("response did not reach writer")
	}
	logout := make(chan int, 1)
	go func() { status, _ := f.request(t, "POST", "/api/auth/logout", nil, f.cookie); logout <- status }()
	select {
	case status := <-logout:
		if status != 204 {
			close(release)
			t.Fatalf("logout %d", status)
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("response writer retained session lease")
	}
	close(release)
	select {
	case status := <-done:
		if status != 200 || commits != 1 {
			t.Fatalf("authorize status=%d commits=%d", status, commits)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("response remained blocked")
	}
}

func TestWebSourceHTTPLogoutDuringDNSPreventsPrivateStage(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	f.sources.options.LookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		close(entered)
		<-release
		return []netip.Addr{netip.MustParseAddr("10.1.2.3")}, nil
	}
	done := make(chan int, 1)
	go func() {
		status, _ := f.request(t, "POST", "/api/v1/sources:prepare", WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceProfileImport, URL: "http://lan.test/sub"}, f.cookie)
		done <- status
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("DNS did not start")
	}
	if status, _ := f.request(t, "POST", "/api/auth/logout", nil, f.cookie); status != 204 {
		close(release)
		t.Fatal("logout blocked during DNS")
	}
	close(release)
	select {
	case status := <-done:
		if status != 401 {
			t.Fatalf("stale session prepared source: %d", status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("prepare remained blocked")
	}
	f.sources.mu.Lock()
	defer f.sources.mu.Unlock()
	if len(f.sources.stages) != 0 {
		t.Fatal("revoked session retained authorization stage")
	}
}

func TestWebSourceHTTPQueuedAuthorizationCannotCommitAfterLogout(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	f.sources.options.LookupNetIP = webSourceTestDNS("10.1.2.3")
	status, body := f.request(t, "POST", "/api/v1/sources:prepare", WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceProfileImport, URL: "http://lan.test/sub"}, f.cookie)
	if status != 200 {
		t.Fatal(status)
	}
	var preparation WebAPIEnvelope[WebAPISourcePreparation]
	json.Unmarshal(body, &preparation)
	request := WebAPISourceAuthorize{ExpectedRevision: "0", OperationID: "queued-source-authorize", PreparationID: preparation.Data.PreparationID, URLDigest: preparation.Data.URLDigest, Intent: "authorize-server-private-source"}
	encoded, _ := json.Marshal(request)
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); f.server.Config.Handler.ServeHTTP(w, r) }))
	defer server.Close()
	f.store.mu.Lock()
	done := make(chan int, 1)
	go func() {
		r, _ := http.NewRequest("POST", server.URL+"/api/v1/sources:authorize", bytes.NewReader(encoded))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(f.cookie)
		response, err := server.Client().Do(r)
		if err != nil {
			done <- 0
			return
		}
		response.Body.Close()
		done <- response.StatusCode
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		f.store.mu.Unlock()
		t.Fatal("queued authorize did not enter")
	}
	status, _ = f.request(t, "POST", "/api/auth/logout", nil, f.cookie)
	f.store.mu.Unlock()
	if status != 204 {
		t.Fatal(status)
	}
	select {
	case status := <-done:
		if status != 401 {
			t.Fatalf("queued revoked authorization: %d", status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued authorization remained blocked")
	}
	_, err := f.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		if tx.tx.Bucket([]byte("source-grants")).Stats().KeyN != 0 {
			t.Error("grant committed after logout")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebSourceHTTPIncomingCookieNeverBecomesSourceCredentials(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	wire := &webSourceTestWire{respond: func(r *http.Request, conn net.Conn) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("Origin") != "" || r.Header.Get("Referer") != "" {
			t.Error("browser authorization reached source")
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "synthetic-source" || password != "synthetic-password" {
			t.Error("purpose-bound URL credentials were replaced by browser authority")
		}
		body := "proxies: []\nrules:\n  - MATCH,DIRECT\n"
		webSourceTestResponse(conn, 200, nil, strings.NewReader(body), int64(len(body)))
	}}
	f.content.sourceOptions = wire.options()
	request := webContentImportRequest("http-cookie-source", "0")
	request.Source = WebAPIContentImportSource{Kind: "url", URL: "http://synthetic-source:synthetic-password@public.test/sub"}
	if status, body := f.request(t, "POST", "/api/v1/profiles:import", request, f.cookie); status != 200 {
		t.Fatalf("authenticated source import %d: %s", status, body)
	}
	_, observed := wire.observed()
	if len(observed) != 1 {
		t.Fatal("test did not exercise an authenticated HTTP source fetch")
	}
}

func TestWebSourceHTTPStrictControlBoundary(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	lookups := 0
	f.sources.options.LookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		lookups++
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	base := map[string]any{"expectedRevision": "0", "purpose": "profile-import", "url": "http://public.test/sub"}
	for _, method := range []string{"GET", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		if status, _ := f.request(t, method, "/api/v1/sources:prepare", base, f.cookie); status != 405 {
			t.Fatalf("method %s = %d", method, status)
		}
	}
	for _, query := range []string{"?limit=99", "?url=http://private.test/", "?purpose=profile-import&purpose=profile-import"} {
		if status, _ := f.request(t, "POST", "/api/v1/sources:prepare"+query, base, f.cookie); status != 400 {
			t.Fatalf("query = %d", status)
		}
	}
	for _, field := range []string{"method", "headers", "committedHeaders", "authorizationId", "TLSRootCAs", "timeout", "proxy"} {
		request := map[string]any{}
		for key, value := range base {
			request[key] = value
		}
		request[field] = "synthetic-secret"
		if status, body := f.request(t, "POST", "/api/v1/sources:prepare", request, f.cookie); status != 400 || bytes.Contains(body, []byte("synthetic-secret")) {
			t.Fatalf("unknown field %s = %d: %s", field, status, body)
		}
	}
	if status, _ := f.request(t, "POST", "/api/v1/sources:prepare", base, nil); status != 401 {
		t.Fatal(status)
	}
	large := map[string]any{"expectedRevision": "0", "purpose": "profile-import", "url": strings.Repeat("a", 1<<20)}
	if status, _ := f.request(t, "POST", "/api/v1/sources:prepare", large, f.cookie); status != 413 {
		t.Fatalf("control budget = %d", status)
	}
	if lookups != 0 {
		t.Fatalf("rejected control request reached DNS %d times", lookups)
	}
}
