//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type webCustomSourceHTTPRig struct {
	*webContentHTTPFixture
	custom    *WebAPICustomSourceService
	owner     WebAPIID
	revision  WebAPIRevision
	url       string
	fetches   atomic.Int32
	mu        sync.Mutex
	clock     time.Time
	bodies    map[string]string
	headers   map[string]string
	entered   chan struct{}
	completed chan struct{}
}

func newWebCustomSourceHTTPRig(t *testing.T, private bool, root string) *webCustomSourceHTTPRig {
	t.Helper()
	f := &webCustomSourceHTTPRig{clock: time.Now(), bodies: map[string]string{"/root": root, "/proxy": "proxies:\n - {name: ProviderNode, type: trojan, server: synthetic.test, port: 443, password: synthetic}\n", "/rules": "payload: [+.example.test]\n"}, headers: map[string]string{}}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.fetches.Add(1)
		f.mu.Lock()
		body, ok := f.bodies[r.URL.Path]
		f.headers[r.URL.Path] = r.Header.Get("Authorization")
		entered, completed := f.entered, f.completed
		f.mu.Unlock()
		if body == "block" {
			close(entered)
			<-r.Context().Done()
			close(completed)
			return
		}
		if strings.HasPrefix(body, "redirect:") {
			http.Redirect(w, r, strings.TrimPrefix(body, "redirect:"), http.StatusFound)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/yaml")
		io.WriteString(w, body)
	}))
	t.Cleanup(origin.Close)
	target, _ := url.Parse(origin.URL)
	ip := "8.8.8.8"
	if private {
		ip = "10.2.3.4"
	}
	options := WebAPISourceFetchOptions{Now: func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.clock }, LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr(ip)}, nil
	}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != ip+":"+target.Port() {
			t.Errorf("unapproved unpinned dial %q", address)
			return nil, context.Canceled
		}
		return (&net.Dialer{}).DialContext(ctx, network, target.Host)
	}}
	f.url = "http://source.test:" + target.Port()
	f.mu.Lock()
	for path, body := range f.bodies {
		f.bodies[path] = strings.ReplaceAll(body, "SOURCE", f.url)
	}
	f.mu.Unlock()
	store, err := OpenWebAPIStore(WebAPIStoreOptions{DataDir: filepath.Join(t.TempDir(), "managed")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	auth := newWebTestServer(t)
	mux := http.NewServeMux()
	RegisterWebAPIResourceRoutes(mux, store)
	content := RegisterWebAPIContentRoutes(mux, store, WebAPIContentOptions{SourceOptions: options, Now: options.Now})
	f.custom = NewWebAPICustomSourceService(store, WebAPICustomSourceOptions{SourceOptions: options, Now: options.Now})
	t.Cleanup(f.custom.Close)
	content.CustomSources = f.custom
	f.custom.RegisterRoutes(mux)
	sources := RegisterWebAPISourceRoutes(mux, store, WebAPISourceOptions{WebAPISourceFetchOptions: options, ResolveCustomImportJobSource: f.custom.PendingSource})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			if hijacker, ok := w.(http.Hijacker); ok {
				connection, _, err := hijacker.Hijack()
				if err == nil {
					connection.Close()
				}
			}
			return
		}
		mux.ServeHTTP(w, r.WithContext(ctx))
	}))
	t.Cleanup(server.Close)
	f.webContentHTTPFixture = &webContentHTTPFixture{store: store, content: content, sources: sources, server: server, auth: auth, cookie: webLogin(t, auth)}
	request := WebAPIProfileCreate{WebAPIMutation: WebAPIMutation{ExpectedRevision: "0", OperationID: "seed-custom-owner"}, Profile: WebAPIProfileInput{Type: "custom", Label: "Custom", ExtendType: "standard", AutoUpdateDurationMillis: 86400000, SelectedMap: map[string]string{}, UnfoldSet: []string{}}}
	status, body := f.request(t, "POST", "/api/v1/profiles:create", request, f.cookie)
	if status != 200 {
		t.Fatalf("seed %d: %s", status, body)
	}
	var created WebAPIEnvelope[WebAPIProfile]
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	f.owner, f.revision = created.Data.ID, created.Revision
	return f
}

func (f *webCustomSourceHTTPRig) prepare(t *testing.T, authorization string) WebAPICustomSourcePreparation {
	t.Helper()
	status, body := f.request(t, "POST", "/api/v1/profiles:prepare-custom-import", WebAPICustomSourcePrepare{ExpectedRevision: f.revision, ProfileID: f.owner, URL: f.url + "/root", AuthorizationID: authorization}, f.cookie)
	if status != 200 {
		t.Fatalf("prepare %d: %s", status, body)
	}
	if bytes.Contains(body, []byte(f.url)) || bytes.Contains(body, []byte("password")) || bytes.Contains(body, []byte("headers")) {
		t.Fatal("prepare exposed a raw source response")
	}
	var envelope WebAPIEnvelope[WebAPICustomSourcePreparation]
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Revision != f.revision || envelope.Data.ProfileID != f.owner || envelope.Data.PreparedJobID != envelope.Data.OperationID || !webAPIOperationID(envelope.Data.OperationID) {
		t.Fatal("preparation lost original identity")
	}
	return envelope.Data
}

func (f *webCustomSourceHTTPRig) confirm(job WebAPICustomSourcePreparation, grants ...string) map[string]any {
	request := map[string]any{"expectedRevision": f.revision, "operationId": job.OperationID, "id": f.owner, "preparedJobId": job.PreparedJobID, "requestDigest": job.RequestDigest}
	if len(grants) > 0 {
		request["authorizationIds"] = grants
	}
	return request
}

func (f *webCustomSourceHTTPRig) authorize(t *testing.T, request WebAPISourcePrepare, operation string) string {
	t.Helper()
	status, body := f.request(t, "POST", "/api/v1/sources:prepare", request, f.cookie)
	if status != 200 {
		t.Fatalf("source prepare %d: %s", status, body)
	}
	var prepared WebAPIEnvelope[WebAPISourcePreparation]
	json.Unmarshal(body, &prepared)
	if !prepared.Data.RequiresPrivateNetworkConfirmation {
		t.Fatal("private source not labeled")
	}
	status, body = f.request(t, "POST", "/api/v1/sources:authorize", WebAPISourceAuthorize{ExpectedRevision: f.revision, OperationID: operation, PreparationID: prepared.Data.PreparationID, URLDigest: prepared.Data.URLDigest, Intent: "authorize-server-private-source"}, f.cookie)
	if status != 200 {
		t.Fatalf("source authorize %d: %s", status, body)
	}
	var grant WebAPIEnvelope[WebAPISourceAuthorizationResult]
	json.Unmarshal(body, &grant)
	return grant.Data.AuthorizationID
}

func (f *webCustomSourceHTTPRig) requireUnchanged(t *testing.T) {
	t.Helper()
	revision, err := f.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		profiles, err := tx.Profiles()
		if err != nil {
			return err
		}
		if len(profiles) != 1 {
			t.Fatalf("preparation created profiles: %d", len(profiles))
		}
		groups, err := tx.ProxyGroups()
		if err != nil {
			return err
		}
		proxies, err := tx.CustomProxies()
		if err != nil {
			return err
		}
		rules, err := tx.OwnedRules(&f.owner)
		if err != nil {
			return err
		}
		if len(groups)+len(proxies)+len(rules) != 0 {
			t.Fatal("preparation wrote custom graph")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if revision != f.revision {
		t.Fatalf("revision changed: %s", revision)
	}
}

func TestWebCustomSourceHTTPPublicPreparedAtomicReceipt(t *testing.T) {
	// build/api-custom-source-contract.md: typed preparation has no business write or generic body relay.
	f := newWebCustomSourceHTTPRig(t, false, "proxies:\n - {name: Node, type: trojan, server: synthetic.test, port: 443, password: synthetic}\nproxy-groups:\n - {name: Pick, type: select, proxies: [Node, DIRECT]}\nrules: [MATCH,Pick]\n")
	f.mu.Lock()
	f.bodies["/root"] = strings.Replace(f.bodies["/root"], "rules: [MATCH,Pick]", "rules: ['MATCH,Pick']", 1)
	f.mu.Unlock()
	job := f.prepare(t, "")
	f.requireUnchanged(t)
	request := f.confirm(job)
	status, first := f.request(t, "POST", "/api/v1/profiles:import-custom", request, f.cookie)
	if status != 200 {
		t.Fatalf("confirm %d: %s", status, first)
	}
	if f.fetches.Load() != 1 {
		t.Fatal("confirmation re-fetched immutable root")
	}
	status, replay := f.request(t, "POST", "/api/v1/profiles:import-custom", request, f.cookie)
	if status != 200 || !bytes.Equal(first, replay) {
		t.Fatalf("receipt replay %d: %s", status, replay)
	}
	var result WebAPIEnvelope[WebAPICustomProfileImportResult]
	json.Unmarshal(first, &result)
	if result.Data.Profile.ID != f.owner || len(result.Data.AddedProxyIDs) != 1 || len(result.Data.GroupIDs) != 1 || len(result.Data.RuleIDs) != 1 {
		t.Fatalf("wrong atomic result: %s", first)
	}
	f.custom.mu.Lock()
	defer f.custom.mu.Unlock()
	if len(f.custom.jobs) != 0 || f.custom.bytes != 0 {
		t.Fatal("successful job retained staging")
	}
}

func TestWebCustomSourceHTTPPrivateRootAndEachDependency(t *testing.T) {
	root := `proxy-providers:
  p: {type: http, url: SOURCE/proxy, header: {Authorization: [Bearer synthetic]}}
rule-providers:
  r: {type: http, url: SOURCE/rules}
proxy-groups:
  - {name: Pick, type: select, use: [p]}
rules: ['RULE-SET,r,Pick', 'MATCH,DIRECT']
`
	f := newWebCustomSourceHTTPRig(t, true, root)
	status, _ := f.request(t, "POST", "/api/v1/profiles:prepare-custom-import", WebAPICustomSourcePrepare{ExpectedRevision: f.revision, ProfileID: f.owner, URL: f.url + "/root"}, f.cookie)
	if status != 403 || f.fetches.Load() != 0 {
		t.Fatalf("implicit private root access %d/%d", status, f.fetches.Load())
	}
	rootGrant := f.authorize(t, WebAPISourcePrepare{ExpectedRevision: f.revision, Purpose: webCustomSourceRootPurpose, Key: &WebAPISourceResource{Kind: "profile", ID: string(f.owner)}, URL: f.url + "/root"}, "grant-custom-root")
	job := f.prepare(t, rootGrant)
	f.requireUnchanged(t)
	if len(job.Dependencies) != 2 || f.fetches.Load() != 1 {
		t.Fatal("dependencies were fetched without separate grants")
	}
	grants := []string{}
	for i, dependency := range job.Dependencies {
		grants = append(grants, f.authorize(t, WebAPISourcePrepare{ExpectedRevision: f.revision, Purpose: webCustomSourceDependencyPurpose, ImportJobID: job.PreparedJobID, SourceReference: dependency.SourceReference}, "grant-custom-child-"+string(rune('a'+i))))
	}
	status, body := f.request(t, "POST", "/api/v1/profiles:import-custom", f.confirm(job, grants...), f.cookie)
	if status != 200 {
		t.Fatalf("private confirm %d: %s", status, body)
	}
	if f.fetches.Load() != 3 {
		t.Fatalf("unexpected dependency fetches: %d", f.fetches.Load())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.headers["/proxy"] != "Bearer synthetic" {
		t.Fatal("derived headers were not fetched through shared identity")
	}
}

func TestWebCustomSourceHTTPWrongSessionRevisionDigestBodyAndTTL(t *testing.T) {
	for _, variant := range []string{"session", "revision", "digest", "body", "id", "ttl", "scope"} {
		t.Run(variant, func(t *testing.T) {
			f := newWebCustomSourceHTTPRig(t, false, "proxies: []\nproxy-groups: [{name: G, type: select, proxies: [DIRECT]}]\n")
			job := f.prepare(t, "")
			request := f.confirm(job)
			cookie := f.cookie
			switch variant {
			case "session":
				cookie = webLogin(t, f.auth)
			case "revision":
				request["expectedRevision"] = "0"
			case "digest":
				request["requestDigest"] = strings.Repeat("0", 64)
			case "body":
				request["custom"] = map[string]any{"dialers": map[string]string{"A": "DIRECT"}}
			case "id":
				request["id"] = "999"
			case "ttl":
				f.mu.Lock()
				f.clock = f.clock.Add(6 * time.Minute)
				f.mu.Unlock()
			case "scope":
				request["url"] = "http://another.test/"
			}
			status, body := f.request(t, "POST", "/api/v1/profiles:import-custom", request, cookie)
			if status != 400 && status != 409 {
				t.Fatalf("mismatch accepted %d: %s", status, body)
			}
			f.requireUnchanged(t)
		})
	}
}

func TestWebCustomSourceHTTPInvalidParserAndCancelZeroWrites(t *testing.T) {
	f := newWebCustomSourceHTTPRig(t, false, "<html>200 but not YAML</html>")
	status, _ := f.request(t, "POST", "/api/v1/profiles:prepare-custom-import", WebAPICustomSourcePrepare{ExpectedRevision: f.revision, ProfileID: f.owner, URL: f.url + "/root"}, f.cookie)
	if status != 400 {
		t.Fatalf("non-YAML accepted %d", status)
	}
	f.requireUnchanged(t)
	f.mu.Lock()
	f.bodies["/root"] = "proxies: []\n"
	f.mu.Unlock()
	job := f.prepare(t, "")
	if status, _ := f.request(t, "POST", "/api/v1/profiles:discard-custom-import", map[string]string{"preparedJobId": job.PreparedJobID}, f.cookie); status != 204 {
		t.Fatal(status)
	}
	if status, _ := f.request(t, "POST", "/api/v1/profiles:import-custom", f.confirm(job), f.cookie); status != 409 {
		t.Fatal("discarded job confirmed")
	}
	f.requireUnchanged(t)
	f.prepare(t, "")
	f.custom.Close()
	f.custom.mu.Lock()
	defer f.custom.mu.Unlock()
	if len(f.custom.jobs) != 0 || f.custom.bytes != 0 || len(f.custom.timers) != 0 {
		t.Fatal("Close retained staging")
	}
}

func TestWebCustomSourceHTTPCommitLossUsesOriginalOperation(t *testing.T) {
	f := newWebCustomSourceHTTPRig(t, false, "proxies: []\nproxy-groups: [{name: G, type: select, proxies: [DIRECT]}]\n")
	job := f.prepare(t, "")
	body, _ := json.Marshal(f.confirm(job))
	request, _ := http.NewRequest("POST", f.server.URL+"/api/v1/profiles:import-custom", bytes.NewReader(body))
	request.AddCookie(f.cookie)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Test-Drop-Response", "1")
	response, err := f.server.Client().Do(request)
	if err == nil {
		response.Body.Close()
		t.Fatal("response was not lost")
	}
	status, receipt := f.request(t, "GET", "/api/v1/operations/"+job.OperationID, nil, f.cookie)
	if status != 200 {
		t.Fatalf("original receipt %d: %s", status, receipt)
	}
	status, resolved := f.request(t, "POST", "/api/v1/operations/"+job.OperationID+":resolve", map[string]string{"intent": "resolve-unknown-result"}, f.cookie)
	if status != 200 || !bytes.Equal(receipt, resolved) {
		t.Fatalf("original resolve %d: %s", status, resolved)
	}
	if f.fetches.Load() != 1 {
		t.Fatal("receipt recovery reimported source")
	}
}
