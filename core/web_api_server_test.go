//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type webAPIIntegration struct {
	server *WebServer
	http   *httptest.Server
	client *http.Client
}

func newWebAPIIntegration(t *testing.T) *webAPIIntegration {
	t.Helper()
	root := t.TempDir()
	s, err := NewWebServer(WebServerOptions{
		DataDir: filepath.Join(root, "managed"), APIStoreDir: filepath.Join(root, "private"),
		Username: "api-fixture", Password: "synthetic-api-fixture", SafeMode: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &webAPIIntegration{s, ts, &http.Client{Jar: jar, Timeout: 10 * time.Second}}
	t.Cleanup(func() { ts.Close(); s.Close() })
	response := f.request(t, http.MethodPost, "/api/auth/login", map[string]any{"username": "api-fixture", "password": "synthetic-api-fixture"}, true)
	if response.StatusCode != 200 {
		t.Fatalf("fixture login status %d", response.StatusCode)
	}
	response.Body.Close()
	return f
}

func (f *webAPIIntegration) request(t *testing.T, method, path string, value any, origin bool) *http.Response {
	t.Helper()
	var body io.Reader
	if value != nil {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	r, err := http.NewRequest(method, f.http.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if value != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if origin {
		r.Header.Set("Origin", f.http.URL)
	}
	response, err := f.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func webAPIIntegrationJSON(t *testing.T, response *http.Response, status int) map[string]json.RawMessage {
	t.Helper()
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("HTTP status %d, wanted %d: %s", response.StatusCode, status, data)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("invalid API envelope: %v", err)
	}
	return result
}

func TestWebIntegratedAPIRulesPrivateAuthorityAndLegacyDenial(t *testing.T) {
	f := newWebAPIIntegration(t)
	meta := webAPIIntegrationJSON(t, f.request(t, "GET", "/api/v1/meta", nil, false), 200)
	var revision string
	if err := json.Unmarshal(meta["revision"], &revision); err != nil || revision != "0" {
		t.Fatalf("unexpected initial revision %q", revision)
	}
	created := webAPIIntegrationJSON(t, f.request(t, "POST", "/api/v1/rules:create", map[string]any{
		"expectedRevision": revision, "operationId": "integration-rule-create",
		"rule": map[string]any{"profileId": nil, "ruleAction": "DOMAIN_SUFFIX", "content": "example.test", "ruleTarget": "DIRECT", "noResolve": false, "src": false},
	}, true), 200)
	var rule WebAPIRule
	if err := json.Unmarshal(created["data"], &rule); err != nil || rule.ID == "" || rule.Content == nil || *rule.Content != "example.test" {
		t.Fatalf("invalid canonical rule result: %v", err)
	}
	listed := webAPIIntegrationJSON(t, f.request(t, "GET", "/api/v1/rules?scope=global", nil, false), 200)
	var rules []WebAPIRule
	if err := json.Unmarshal(listed["data"], &rules); err != nil || len(rules) != 1 || rules[0].ID != rule.ID {
		t.Fatalf("server rule authority mismatch: %v", err)
	}
	for _, path := range []string{"/api/fs/snapshot", "/api/fs/read?path=/data/database.sqlite", "/api/fs/stat?path=/data/shared_preferences.json", "/ws/core"} {
		response := f.request(t, "GET", path, nil, true)
		response.Body.Close()
		if response.StatusCode != 404 {
			t.Errorf("legacy route %s remains reachable: %d", path, response.StatusCode)
		}
	}
	for _, path := range []string{"/api/fs/write?path=/data/database.sqlite", "/api/http-proxy", "/api/v1/query", "/api/v1/execute"} {
		response := f.request(t, "POST", path, map[string]any{"sql": "SELECT * FROM profiles", "path": "/data/database.sqlite"}, true)
		response.Body.Close()
		if response.StatusCode != 404 {
			t.Errorf("generic route %s remains reachable: %d", path, response.StatusCode)
		}
	}
	if webWithin(f.server.opts.DataDir, f.server.api.store.root.Name()) {
		t.Fatal("private database is within the managed resource root")
	}
	if _, err := os.Stat(filepath.Join(f.server.opts.DataDir, "database.sqlite")); !os.IsNotExist(err) {
		t.Fatal("browser database mirror was materialized")
	}
	if _, err := os.Stat(filepath.Join(f.server.api.store.root.Name(), webAPIStoreFile)); err != nil {
		t.Fatal("private API database was not persisted")
	}
}

func TestWebIntegratedAPIMutationRequiresOriginAndStrictBusinessDTO(t *testing.T) {
	f := newWebAPIIntegration(t)
	webAPIIntegrationJSON(t, f.request(t, "GET", "/api/v1/meta", nil, false), 200)
	body := map[string]any{"expectedRevision": "0", "operationId": "integration-no-origin", "rule": map[string]any{"ruleAction": "DOMAIN_SUFFIX", "content": "example.test", "ruleTarget": "DIRECT", "sql": "DELETE FROM profiles"}}
	webAPIIntegrationJSON(t, f.request(t, "POST", "/api/v1/rules:create", body, false), 403)
	body["operationId"] = "integration-no-sql"
	webAPIIntegrationJSON(t, f.request(t, "POST", "/api/v1/rules:create", body, true), 400)
	listed := webAPIIntegrationJSON(t, f.request(t, "GET", "/api/v1/rules?scope=global", nil, false), 200)
	if string(listed["revision"]) != `"0"` || string(listed["data"]) != "[]" {
		t.Fatal("rejected request changed business authority")
	}
}

func TestWebIntegratedAPIQueuedMutationCannotCommitAfterLogout(t *testing.T) {
	f := newWebAPIIntegration(t)
	webAPIIntegrationJSON(t, f.request(t, "GET", "/api/v1/meta", nil, false), 200)
	entered, release := make(chan struct{}), make(chan struct{})
	mux := http.NewServeMux()
	RegisterWebAPIResourceRoutes(mux, f.server.api.store)
	f.server.api.handler = WebAPIRuntimeSessionHandler(f.server, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/rules:create" {
			close(entered)
			<-release
		}
		mux.ServeHTTP(w, r)
	}))
	result := make(chan *http.Response, 1)
	failures := make(chan error, 1)
	go func() {
		payload := `{"expectedRevision":"0","operationId":"revoked-in-flight","rule":{"ruleAction":"DOMAIN_SUFFIX","content":"example.test","ruleTarget":"DIRECT"}}`
		r, _ := http.NewRequest("POST", f.http.URL+"/api/v1/rules:create", strings.NewReader(payload))
		r.Header.Set("Origin", f.http.URL)
		r.Header.Set("Content-Type", "application/json")
		response, err := f.client.Do(r)
		if err != nil {
			failures <- err
			return
		}
		result <- response
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("request did not reach the authenticated transaction boundary")
	}
	logout := f.request(t, "POST", "/api/auth/logout", nil, true)
	logout.Body.Close()
	if logout.StatusCode != 204 {
		close(release)
		t.Fatalf("logout status %d", logout.StatusCode)
	}
	close(release)
	select {
	case response := <-result:
		webAPIIntegrationJSON(t, response, 401)
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("revoked mutation did not finish")
	}
	revision, err := f.server.api.store.View(t.Context(), func(tx *WebAPIReadTx) error {
		rules, err := tx.Rules()
		if err == nil && len(rules) != 0 {
			t.Error("revoked authenticated request persisted a rule")
		}
		return err
	})
	if err != nil || revision != "0" {
		t.Fatalf("revoked request changed revision: %s %v", revision, err)
	}
}
