//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const webEvaluateProfileID WebAPIID = "9007199254740993"
const webEvaluateScriptID WebAPIID = "9007199254740995"
const webEvaluateSource = "proxies: [{name: P, type: socks5, server: 192.0.2.1, port: 1080}]\nproxy-groups: [{name: G, type: select, proxies: [P, DIRECT]}]\nrules: ['MATCH,G']\n"

type webEvaluateRig struct {
	store   *WebAPIStore
	service *WebAPIEvaluator
	auth    *WebServer
	server  *httptest.Server
	cookie  *http.Cookie
}

func newWebEvaluateRig(t *testing.T, script string) *webEvaluateRig {
	t.Helper()
	store, err := OpenWebAPIStore(WebAPIStoreOptions{DataDir: filepath.Join(t.TempDir(), "managed")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_, err = store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "0", OperationID: "seed-evaluation"}, "test:seed-evaluation", nil, func(tx *WebAPIWriteTx) (any, error) {
		if err := tx.InsertScript(WebAPIScript{ID: webEvaluateScriptID, Label: "Extension", LastUpdateTime: "2026-11-15T00:00:00Z"}); err != nil {
			return nil, err
		}
		scriptID := webEvaluateScriptID
		profile := WebAPIProfile{ID: webEvaluateProfileID, WebAPIProfileInput: WebAPIProfileInput{Type: "file", Label: "  exact original label  ", ExtendType: "script", ScriptID: &scriptID, SelectedMap: map[string]string{}, UnfoldSet: []string{}, Overrides: &WebAPIProfileOverrides{}}}
		if err := tx.InsertProfile(profile); err != nil {
			return nil, err
		}
		_, catalog, err := webContentProfile([]byte(webEvaluateSource), "")
		if err != nil {
			return nil, err
		}
		if err := tx.SetProfileCatalog(profile.ID, catalog); err != nil {
			return nil, err
		}
		for _, content := range []WebAPIContent{
			{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: webEvaluateProfileID}, Bytes: []byte(webEvaluateSource), MediaType: "application/yaml"},
			{Key: WebAPIContentKey{Kind: WebAPIContentScript, ID: webEvaluateScriptID}, Bytes: []byte(script), MediaType: "text/javascript"},
		} {
			if err := tx.PutContent(content); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	service, err := RegisterWebAPIEvaluateRoutes(mux, store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close() })
	auth := newWebTestServer(t)
	server := httptest.NewServer(WebAPIRuntimeSessionHandler(auth, mux))
	t.Cleanup(server.Close)
	return &webEvaluateRig{store: store, service: service, auth: auth, server: server, cookie: webLogin(t, auth)}
}

func (f *webEvaluateRig) request(t *testing.T, path string, body any, cookie *http.Cookie) (int, []byte) {
	t.Helper()
	var data []byte
	if text, ok := body.(string); ok {
		data = []byte(text)
	} else {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, f.server.URL+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", f.server.URL)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	response, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	output, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, output
}

func webEvaluateRequest() WebAPIEvaluateRequest {
	return WebAPIEvaluateRequest{ExpectedRevision: "1", ProfileID: webEvaluateProfileID, ScriptID: webEvaluateScriptID}
}

func (f *webEvaluateRig) sessionContext(cookie *http.Cookie) context.Context {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(cookie)
	key, _ := f.auth.session(r)
	return WithWebAPIRequestAuthorization(context.Background(), hex.EncodeToString(key[:]), func(context.Context) (func(), error) { return func() {}, nil })
}

func TestWebEvaluateHTTPCommittedSourceAndSnapshot(t *testing.T) {
	// build/api-evaluate-contract.md: actual HTTP, committed source, exact wire IDs and no mutation.
	script := `const main=async(c,name)=>{await Promise.resolve();c.mode='global';c.profileName=name;console.log('synthetic');return c}`
	f := newWebEvaluateRig(t, script)
	status, body := f.request(t, "/api/v1/profiles:evaluate", webEvaluateRequest(), f.cookie)
	if status != 200 {
		t.Fatalf("evaluate %d %s", status, body)
	}
	var result WebAPIEnvelope[WebAPIEvaluateResult]
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Revision != "1" || result.Data.Revision != "1" || result.Data.ProfileID != webEvaluateProfileID || result.Data.ScriptID != webEvaluateScriptID || result.Data.Preview || len(result.Data.EvaluationID) != 43 {
		t.Fatalf("invalid provenance %+v", result.Data.WebAPIEvaluationSnapshot)
	}
	var config map[string]any
	if err := json.Unmarshal(result.Data.Config, &config); err != nil {
		t.Fatal(err)
	}
	if config["mode"] != "global" || config["profileName"] != "exact original label" || config["rules"] == nil || config["rule"] != nil {
		t.Fatalf("wrong transformation %s", result.Data.Config)
	}
	if result.Data.Counts != (WebAPIEvaluationCounts{Proxies: 1, Groups: 1, Rules: 1}) || result.Data.SHA256 != webEvaluateHash(result.Data.Config) || result.Data.ScriptDigest != webEvaluateHash([]byte(script)) || result.Data.SourceDigest != webEvaluateHash([]byte(webEvaluateSource)) {
		t.Fatal("result did not retain source, script, output digests and counts")
	}
	ctx := f.sessionContext(f.cookie)
	resolved, err := ResolveWebAPIEvaluation(ctx, f.store, result.Data.EvaluationID, webEvaluateProfileID, "1")
	if err != nil || !bytes.Equal(resolved.Config, result.Data.Config) {
		t.Fatalf("resolve %v", err)
	}
	resolved.Config[0] = '['
	resolved, err = ResolveWebAPIEvaluation(ctx, f.store, result.Data.EvaluationID, webEvaluateProfileID, "1")
	if err != nil || resolved.Config[0] != '{' {
		t.Fatal("mutable cached result")
	}
	for _, check := range []struct {
		ctx      context.Context
		id       WebAPIID
		revision WebAPIRevision
	}{
		{f.sessionContext(webLogin(t, f.auth)), webEvaluateProfileID, "1"},
		{ctx, "42", "1"}, {ctx, webEvaluateProfileID, "2"},
	} {
		if _, err := ResolveWebAPIEvaluation(check.ctx, f.store, result.Data.EvaluationID, check.id, check.revision); err == nil {
			t.Fatal("cross-session/profile/revision resolve succeeded")
		}
	}
	if revision, err := f.store.View(ctx, func(tx *WebAPIReadTx) error {
		content, err := tx.Content(WebAPIContentKey{Kind: WebAPIContentScript, ID: webEvaluateScriptID})
		if err == nil && string(content.Bytes) != script {
			t.Error("evaluation modified content")
		}
		return err
	}); err != nil || revision != "1" {
		t.Fatal("evaluation modified the store")
	}
}

func TestWebEvaluateHTTPRejectsAuthorityAndPolicyBypasses(t *testing.T) {
	f := newWebEvaluateRig(t, `function main(c){return c}`)
	if code, _ := f.request(t, "/api/v1/profiles:evaluate", webEvaluateRequest(), nil); code != 401 {
		t.Fatal("unauthenticated evaluation admitted")
	}
	for _, body := range []string{
		`{"expectedRevision":"1","profileId":9007199254740993,"scriptId":"9007199254740995"}`,
		`{"expectedRevision":"1","profileId":"9007199254740993","scriptId":"9007199254740995","scriptText":"function main(){}"}`,
		`{"expectedRevision":"1","profileId":"9007199254740993","scriptId":"9007199254740995","path":"/synthetic"}`,
		`{"expectedRevision":"1","profileId":"9007199254740993","profileId":"42","scriptId":"9007199254740995"}`,
		`{"profileId":"9007199254740993","scriptId":"9007199254740995"}`,
		`{"expectedRevision":"0","profileId":"9007199254740993","scriptId":"9007199254740995"}`,
		`{"expectedRevision":"1","profileId":"9007199254740993","scriptId":"42"}`,
	} {
		if code, output := f.request(t, "/api/v1/profiles:evaluate", body, f.cookie); code < 400 || code >= 500 {
			t.Fatalf("bypass %d %s", code, output)
		}
	}
	for _, script := range []string{
		`function main(c){c['external-ui']='/synthetic/private';return c}`,
		`function main(c){c['proxy-providers']={bad:{type:'file',path:'/synthetic/private'}};return c}`,
		`function main(c){c.proxies[0]['dialer-proxy']='G';return c}`,
		`function main(c){c.proxies[0]['dialer-proxy']='REMOTE';c['proxy-groups'].push({name:'REMOTE',type:'select',use:['remote']});c['proxy-providers']={remote:{type:'http',url:'https://synthetic.invalid/provider'}};return c}`,
		`function main(){return []}`, `function main(){return 'not config'}`,
		`function main(c){c['proxy-groups']='malformed';return c}`,
	} {
		request := WebAPIScriptPreviewRequest{WebAPIEvaluateRequest: webEvaluateRequest(), ScriptText: script, Config: json.RawMessage(`{"proxies":[{"name":"P","type":"socks5","server":"192.0.2.1","port":1080}],"proxy-groups":[{"name":"G","type":"select","proxies":["P"]}],"rules":["MATCH,G"]}`)}
		if code, body := f.request(t, "/api/v1/scripts:preview", request, f.cookie); code < 400 || code >= 500 {
			t.Fatalf("policy bypass %d %s", code, body)
		}
	}
}

func TestWebEvaluateHTTPPreviewIsNotRuntimeAuthority(t *testing.T) {
	f := newWebEvaluateRig(t, `function main(c){throw new Error('committed must not run for draft preview')}`)
	request := WebAPIScriptPreviewRequest{WebAPIEvaluateRequest: webEvaluateRequest(), ScriptText: `function main(c){c.mode='global';return c}`, Config: json.RawMessage(`{"mode":"rule","rules":["MATCH,DIRECT"]}`)}
	status, body := f.request(t, "/api/v1/scripts:preview", request, f.cookie)
	if status != 200 {
		t.Fatalf("preview %d %s", status, body)
	}
	var result WebAPIEnvelope[WebAPIEvaluateResult]
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Data.Preview || result.Data.EvaluationID != "" || result.Data.Revision != "1" || !bytes.Contains(result.Data.Config, []byte(`"mode":"global"`)) {
		t.Fatal("draft became apply authority")
	}
	if len(f.service.cache) != 0 {
		t.Fatal("preview entered runtime cache")
	}
}

func TestWebEvaluateCacheBoundsTTLAndCancellation(t *testing.T) {
	f := newWebEvaluateRig(t, `function main(c){return c}`)
	ctx := f.sessionContext(f.cookie)
	first := ""
	for i := 0; i < webEvaluateCacheEntries+1; i++ {
		id, err := f.service.remember(ctx, WebAPIEvaluationSnapshot{ProfileID: webEvaluateProfileID, Revision: "1", Config: json.RawMessage(fmt.Sprintf(`{"n":%d}`, i))})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = id
		}
	}
	if len(f.service.cache) != webEvaluateCacheEntries || f.service.bytes > webEvaluateCacheBytes {
		t.Fatal("cache entry budget escaped")
	}
	if _, err := ResolveWebAPIEvaluation(ctx, f.store, first, webEvaluateProfileID, "1"); err == nil {
		t.Fatal("oldest entry was not evicted")
	}
	now := time.Now().Add(webEvaluateCacheTTL + time.Second)
	f.service.now = func() time.Time { return now }
	f.service.mu.Lock()
	f.service.expireLocked()
	f.service.mu.Unlock()
	if len(f.service.cache) != 0 || f.service.bytes != 0 {
		t.Fatal("expired cache retained")
	}
	f.service.now = time.Now
	for i := 0; i < 3; i++ {
		if _, err := f.service.remember(ctx, WebAPIEvaluationSnapshot{ProfileID: webEvaluateProfileID, Revision: "1", Config: json.RawMessage(strings.Repeat(" ", 24<<20))}); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.service.cache) != 2 || f.service.bytes > webEvaluateCacheBytes {
		t.Fatal("cache byte budget escaped")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := f.service.remember(cancelled, WebAPIEvaluationSnapshot{Config: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("cancelled result cached")
	}
}
