//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/tunnel/statistic"
)

type webRuntimeTestRig struct {
	auth    *WebServer
	store   *WebAPIStore
	runtime *WebAPIRuntime
	server  *httptest.Server
	cookie  *http.Cookie
}

func newWebRuntimeTestRig(t *testing.T, b WebAPIRuntimeBackend) *webRuntimeTestRig {
	t.Helper()
	auth := newWebTestServer(t)
	selection := b.SelectProxy != nil
	if b.State == nil {
		b.State = func(context.Context) (WebAPIRuntimeCoreState, error) {
			state := WebAPIRuntimeCoreState{Initialized: true, SafeMode: true, RuntimeGeneration: "0"}
			if selection {
				id := WebAPIID("1")
				state.ProfileID = &id
				state.RuntimeGeneration = "7"
			}
			return state, nil
		}
	}
	var rt *WebAPIRuntime
	store, err := OpenWebAPIStore(WebAPIStoreOptions{DataDir: filepath.Join(t.TempDir(), "managed"), OnCommit: func(c WebAPICommit) {
		if rt != nil {
			rt.OnCommit(c)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if selection {
		webRuntimeSeedSelection(t, store)
	}
	rt, err = NewWebAPIRuntime(store, b)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	resources := NewWebAPIHandler(store)
	api := WebAPIRuntimeSessionHandler(auth, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/runtime") || strings.HasPrefix(r.URL.Path, "/api/v1/profiles/") || r.URL.Path == "/api/v1/profiles:build" {
			rt.ServeHTTP(w, r)
		} else {
			resources.ServeHTTP(w, r)
		}
	}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/auth/"):
			auth.ServeHTTP(w, r)
		case r.URL.Path == "/ws/v1/events":
			rt.ServeEvents(auth, w, r)
		default:
			api.ServeHTTP(w, r)
		}
	}))
	rig := &webRuntimeTestRig{auth: auth, store: store, runtime: rt, server: server}
	t.Cleanup(func() { rt.Close(); server.Close(); store.Close() })
	rig.cookie = rig.login(t)
	return rig
}

func (s *webRuntimeTestRig) login(t *testing.T) *http.Cookie {
	t.Helper()
	response, err := http.Post(s.server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"admin","password":"correct horse"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	if response.StatusCode != 200 || len(response.Cookies()) != 1 {
		t.Fatalf("login status %d", response.StatusCode)
	}
	return response.Cookies()[0]
}

func (s *webRuntimeTestRig) request(t *testing.T, method, path, body string, cookie *http.Cookie, origin string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	r, err := http.NewRequest(method, s.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := s.server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data
}

func (s *webRuntimeTestRig) phase(t *testing.T, id string) WebAPIRuntimeOperation {
	t.Helper()
	return s.phaseWithin(t, id, 3*time.Second)
}

func (s *webRuntimeTestRig) phaseWithin(t *testing.T, id string, timeout time.Duration) WebAPIRuntimeOperation {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		code, body := s.request(t, "GET", "/api/v1/runtime/operations/"+id, "", s.cookie, "")
		if code != 200 {
			t.Fatalf("phase %d: %s", code, body)
		}
		var envelope WebAPIEnvelope[WebAPIRuntimeOperation]
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatal(err)
		}
		phase := envelope.Data.Phase
		if phase != WebAPIRuntimePending && phase != WebAPIRuntimeApplying {
			return envelope.Data
		}
		if time.Now().After(deadline) {
			t.Fatalf("phase did not settle: %s", body)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWebRuntimeTypedQueriesDoNotMutate(t *testing.T) {
	var subscriptions atomic.Int32
	b := WebAPIRuntimeBackend{
		Memory: func(context.Context) (MemoryStats, error) { return MemoryStats{HeapInuse: 123}, nil },
		Proxies: func(context.Context) (ProxiesData, error) {
			return ProxiesData{Proxies: map[string]any{"G": map[string]any{"name": "G", "type": "Selector"}}, All: []string{"G"}}, nil
		},
		ProxyDefinition: func(context.Context, string) (map[string]any, error) {
			return map[string]any{"name": "P", "type": "socks5", "server": "192.0.2.1", "port": 1080}, nil
		},
		Delay: func(_ context.Context, p WebAPIRuntimeDelayRequest) (*Delay, error) {
			return &Delay{Name: p.ProxyName, Url: p.TestURL, Value: 12}, nil
		},
		Connections:     func(context.Context) (*statistic.Snapshot, error) { return &statistic.Snapshot{}, nil },
		ConnectionCount: func(context.Context) (int, error) { return 0, nil },
		Traffic:         func(context.Context, bool, bool) (Traffic, error) { return Traffic{Up: 4, Down: 6}, nil },
		GeoResources: func(context.Context) ([]WebAPIRuntimeGeoResource, error) {
			return []WebAPIRuntimeGeoResource{{Type: "MMDB"}}, nil
		},
		Route: func(context.Context) (RouteState, error) { return RouteState{CoreEpoch: 1, RouteVersion: 2}, nil },
		CheckRoutes: func(context.Context, CheckRoutesParams) (*RouteCheck, error) {
			return &RouteCheck{Held: []bool{true}}, nil
		},
		OutboundIP: func(context.Context, WebAPIRuntimeOutboundIPRequest) (*OutboundIpResult, error) {
			return &OutboundIpResult{Body: `{"ip":"203.0.113.10"}`}, nil
		},
		Services: func(context.Context, WebAPIRuntimeServicesRequest) ([]ServiceCheckItem, error) {
			return []ServiceCheckItem{{Name: "github", Status: "available"}}, nil
		},
		Subscribe: func(bool, bool) { subscriptions.Add(1) },
	}
	s := newWebRuntimeTestRig(t, b)
	for _, path := range []string{"state", "memory", "proxies", "proxies/definition?name=P", "connections", "connections/count", "traffic?total=true&onlyProxy=false", "listener", "logs", "geo", "route"} {
		code, body := s.request(t, "GET", "/api/v1/runtime/"+path, "", s.cookie, "")
		if code != 200 || !bytes.Contains(body, []byte(`"revision":"0"`)) {
			t.Errorf("GET %s = %d %s", path, code, body)
		}
	}
	queries := map[string]string{
		"proxies:delay":      `{"proxyName":"P","testUrl":"https://example.test/","timeoutMillis":1000}`,
		"proxies:validate":   `{"proxies":[{"name":"P","type":"socks5","server":"192.0.2.1","port":1080}]}`,
		"proxies:convert":    `{"content":"ss://YWVzLTEyOC1nY206cGFzc3dvcmQ@192.0.2.1:1234#P"}`,
		"share-links:encode": `{"proxies":[]}`,
		"share-links:decode": `{"lines":[]}`,
		"filters:validate":   `{"filters":[".*"]}`,
		"route:check":        `{"routes":[[{"host":"example.test","exit":"DIRECT"}]]}`,
		"probes/outbound-ip": `{"proxyName":"DIRECT","urls":["https://ipinfo.io/json"],"timeoutMillis":1000}`,
		"probes/services":    `{"proxyName":"DIRECT","names":["github"],"timeoutMillis":1000}`,
	}
	for path, body := range queries {
		code, result := s.request(t, "POST", "/api/v1/runtime/"+path, body, s.cookie, "")
		if code != 200 || !bytes.Contains(result, []byte(`"revision":"0"`)) {
			t.Errorf("POST %s = %d %s", path, code, result)
		}
	}
	if subscriptions.Load() != 0 {
		t.Fatal("query toggled global subscriptions")
	}
	if records, err := s.store.runtimeRecords(context.Background()); err != nil || len(records) != 0 {
		t.Fatalf("query created runtime intent: %v %v", records, err)
	}
}

func TestWebRuntimeStrictHTTPAndAuthMatrix(t *testing.T) {
	var applied atomic.Int32
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{SelectProxy: func(context.Context, string, string) (*ChangeProxyResult, error) {
		applied.Add(1)
		return &ChangeProxyResult{Changed: true}, nil
	}})
	paths := []string{"state", "proxies", "proxies:select", "proxies:validate", "proxies:convert", "connections:close", "traffic:reset", "dns:clear-cache", "listener:set-enabled", "geo:update", "probes/outbound-ip", "mitm/certificate", "operations/unknown-id"}
	for _, p := range paths {
		for _, method := range []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH", "HEAD"} {
			if code, _ := s.request(t, method, "/api/v1/runtime/"+p, "{}", nil, ""); code != 401 {
				t.Fatalf("unauth %s %s=%d", method, p, code)
			}
			if code, _ := s.request(t, method, "/api/v1/runtime/"+p, "{}", s.cookie, "http://evil.test"); code != 401 {
				t.Fatalf("origin %s %s=%d", method, p, code)
			}
		}
	}
	valid := `{"expectedRevision":"0","operationId":"strict-op","groupName":"G","proxyName":"P"}`
	invalid := []string{
		strings.Replace(valid, `"groupName"`, `"GroupName"`, 1),
		strings.Replace(valid, `"groupName":"G"`, `"groupName":"G","groupName":"G"`, 1),
		strings.Replace(valid, `"groupName":"G"`, `"groupName":null`, 1),
		strings.Replace(valid, `"expectedRevision":"0"`, `"expectedRevision":0`, 1),
		strings.Replace(valid, `"operationId":"strict-op"`, `"operationId":"a"`, 1),
		valid + `{}`, `null`, `[]`, strings.TrimSuffix(valid, "}") + `,"path":"/etc/passwd"}`,
		`{"id":"native","method":"listener.start","arguments":{}}`,
	}
	for _, body := range invalid {
		code, result := s.request(t, "POST", "/api/v1/runtime/proxies:select", body, s.cookie, "")
		if code != 400 {
			t.Errorf("strict %s = %d %s", body, code, result)
		}
	}
	if code, _ := s.request(t, "GET", "/api/v1/runtime/proxies:select", "", s.cookie, ""); code != 405 {
		t.Fatal("mutation accepted GET")
	}
	if code, _ := s.request(t, "POST", "/api/v1/runtime/proxies:select?unknown=1", valid, s.cookie, ""); code != 400 {
		t.Fatal("unknown query accepted")
	}
	for _, path := range []string{"traffic?total=1", "proxies/definition?name=P&name=Q", "route?watch=true", "state?path=/data"} {
		if code, _ := s.request(t, "GET", "/api/v1/runtime/"+path, "", s.cookie, ""); code != 400 {
			t.Errorf("bad query %s=%d", path, code)
		}
	}
	if code, _ := s.request(t, "POST", "/api/v1/runtime/proxies:select", strings.Repeat(" ", webAPIMaxJSONBytes+1), s.cookie, ""); code != 413 {
		t.Fatal("oversize action was accepted")
	}
	for _, body := range []string{`{"proxies":[{"type":"ssh","private-key":"/etc/secret","name":"P"}]}`, `{"proxies":[{"type":"tailscale","name":"P","state-dir":"/tmp"}]}`, `{"proxies":[{"type":"socks5","name":"P","certificate":"/data/auth"}]}`} {
		if code, _ := s.request(t, "POST", "/api/v1/runtime/proxies:validate", body, s.cookie, ""); code != 422 {
			t.Errorf("path bypass %d %s", code, body)
		}
	}
	if code, _ := s.request(t, "POST", "/api/v1/runtime/probes/outbound-ip", `{"urls":["http://127.0.0.1/secrets"],"timeoutMillis":1000}`, s.cookie, ""); code != 400 {
		t.Fatal("probe became an arbitrary HTTP relay")
	}
	if applied.Load() != 0 {
		t.Fatal("rejected HTTP mutated runtime")
	}
}

func TestWebRuntimeCASReceiptsTwoSessionsAndLostResponse(t *testing.T) {
	var count atomic.Int32
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{SelectProxy: func(context.Context, string, string) (*ChangeProxyResult, error) {
		count.Add(1)
		return &ChangeProxyResult{Changed: true}, nil
	}})
	second := s.login(t)
	bodies := []string{`{"expectedRevision":"1","operationId":"session-one","groupName":"G","proxyName":"A","runtimeGeneration":"7"}`, `{"expectedRevision":"1","operationId":"session-two","groupName":"G","proxyName":"B","runtimeGeneration":"7"}`}
	cookies := []*http.Cookie{s.cookie, second}
	codes := make([]int, 2)
	results := make([][]byte, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], results[i] = s.request(t, "POST", "/api/v1/runtime/proxies:select", bodies[i], cookies[i], "")
		}(i)
	}
	wg.Wait()
	winner := 0
	if codes[0] != 200 {
		winner = 1
	}
	if codes[winner] != 200 || codes[1-winner] != 409 {
		t.Fatalf("CAS race %v", codes)
	}
	id := []string{"session-one", "session-two"}[winner]
	state := s.phase(t, id)
	if state.Phase != WebAPIRuntimeApplied || count.Load() != 1 {
		t.Fatalf("apply %+v count %d", state, count.Load())
	}
	code, receipt := s.request(t, "GET", "/api/v1/operations/"+id, "", second, "")
	if code != 200 || !bytes.Equal(receipt, results[winner]) {
		t.Fatalf("lost-response receipt %d %s", code, receipt)
	}
	code, replay := s.request(t, "POST", "/api/v1/runtime/proxies:select", bodies[winner], cookies[winner], "")
	if code != 200 || !bytes.Equal(replay, receipt) || count.Load() != 1 {
		t.Fatalf("replay repeated external effect %d %s count %d", code, replay, count.Load())
	}
	if !bytes.Contains(receipt, []byte(`"phase":"pending"`)) {
		t.Fatal("immutable receipt was rewritten to masquerade as atomic Core apply")
	}
	if code, _ := s.request(t, "POST", "/api/v1/runtime/proxies:select", strings.Replace(bodies[winner], `"groupName":"G"`, `"groupName":"changed"`, 1), second, ""); code != 409 {
		t.Fatal("operation body changed")
	}
	if rev, err := s.store.View(context.Background(), func(*WebAPIReadTx) error { return nil }); err != nil || rev != "2" {
		t.Fatalf("status update changed config revision: %s %v", rev, err)
	}
}

func TestWebRuntimeUnknownEffectIsNeverRepeated(t *testing.T) {
	var count atomic.Int32
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{ResetTraffic: func(context.Context) error { count.Add(1); return context.DeadlineExceeded }})
	body := `{"expectedRevision":"0","operationId":"unknown-effect"}`
	if code, _ := s.request(t, "POST", "/api/v1/runtime/traffic:reset", body, s.cookie, ""); code != 200 {
		t.Fatal(code)
	}
	state := s.phase(t, "unknown-effect")
	if state.Phase != WebAPIRuntimeUnknown || state.Error == nil {
		t.Fatalf("uncertain side effect was not exposed: %+v", state)
	}
	if code, _ := s.request(t, "POST", "/api/v1/runtime/traffic:reset", body, s.cookie, ""); code != 200 {
		t.Fatal(code)
	}
	if count.Load() != 1 {
		t.Fatal("unknown effect was automatically repeated")
	}
}

func TestWebRuntimeInterruptedPendingBecomesUnknown(t *testing.T) {
	store, err := OpenWebAPIStore(WebAPIStoreOptions{DataDir: filepath.Join(t.TempDir(), "data")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := WebAPIMutation{ExpectedRevision: "0", OperationID: "pending-restart"}
	if _, err = store.Transaction(context.Background(), m, "runtime/test", m, func(tx *WebAPIWriteTx) (any, error) {
		return tx.RecordRuntimeIntent(m.OperationID, webRuntimeResetTraffic, m)
	}); err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	rt, err := NewWebAPIRuntime(store, WebAPIRuntimeBackend{ResetTraffic: func(context.Context) error { count.Add(1); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	var state WebAPIRuntimeOperation
	if _, err = store.View(context.Background(), func(tx *WebAPIReadTx) error {
		var err error
		state, err = tx.RuntimeOperation(m.OperationID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if state.Phase != WebAPIRuntimeUnknown || count.Load() != 0 {
		t.Fatalf("restart replayed a durable intent: %+v", state)
	}
}

func TestWebRuntimeBulkConversionPreserves32MiBCapability(t *testing.T) {
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	content := strings.Repeat(" ", (32<<20)-60) + "ss://YWVzLTEyOC1nY206cGFzc3dvcmQ@192.0.2.1:1234#P"
	body, err := json.Marshal(WebAPIRuntimeConvertRequest{Content: content})
	if err != nil {
		t.Fatal(err)
	}
	code, result := s.request(t, "POST", "/api/v1/runtime/proxies:convert", string(body), s.cookie, "")
	if code == 413 {
		t.Fatalf("32MiB content was reduced to a small-command cap: %s", result)
	}
	if code != 200 && code != 422 {
		t.Fatalf("unexpected bounded conversion result %d: %s", code, result)
	}
}

func TestWebRuntimeMITMUnavailableIsExplicit(t *testing.T) {
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	for _, p := range []string{"mitm/certificate", "mitm/captures?connectionId=00000000-0000-0000-0000-000000000000", "mitm/capture?id=9223372036854775807", "mitm/body?id=9223372036854775807&response=true"} {
		code, body := s.request(t, "GET", "/api/v1/runtime/"+p, "", s.cookie, "")
		if code != 501 || !bytes.Contains(body, []byte("not_implemented")) {
			t.Errorf("MITM absence %s = %d %s", p, code, body)
		}
	}
	for p, body := range map[string]string{"mitm/certificate:generate": `{"expectedRevision":"0","operationId":"mitm-not-built"}`, "mitm/certificate:issue": `{"host":"example.test"}`, "mitm:validate": `{"hostname":[],"url-rewrite":[],"header-rewrite":[],"body-rewrite":[],"map-local":[],"script":[]}`, "mitm/scripts:run": `{"expectedRevision":"0","operationId":"mitm-script-no","scriptName":"script"}`} {
		code, result := s.request(t, "POST", "/api/v1/runtime/"+p, body, s.cookie, "")
		if code != 501 {
			t.Errorf("MITM absent %s = %d %s", p, code, result)
		}
	}
	if records, err := s.store.runtimeRecords(context.Background()); err != nil || len(records) != 0 {
		t.Fatalf("unsupported MITM created a receipt: %v %v", records, err)
	}
}
