//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type webAPITestRig struct {
	t       *testing.T
	store   *WebAPIStore
	opts    WebAPIStoreOptions
	server  *httptest.Server
	commits atomic.Int64
}

func newWebAPITestRig(t *testing.T) *webAPITestRig {
	t.Helper()
	r := &webAPITestRig{t: t}
	r.opts = WebAPIStoreOptions{DataDir: filepath.Join(t.TempDir(), "data"), OnCommit: func(WebAPICommit) { r.commits.Add(1) }}
	var err error
	r.store, err = OpenWebAPIStore(r.opts)
	if err != nil {
		t.Fatal(err)
	}
	r.server = httptest.NewServer(NewWebAPIHandler(r.store))
	t.Cleanup(func() { r.server.Close(); r.store.Close() })
	return r
}

func (r *webAPITestRig) raw(method, path string, body []byte) (int, []byte) {
	r.t.Helper()
	req, err := http.NewRequest(method, r.server.URL+path, bytes.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := r.server.Client().Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		r.t.Fatal(err)
	}
	return response.StatusCode, data
}

func (r *webAPITestRig) call(method, path string, input any, want int) []byte {
	r.t.Helper()
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			r.t.Fatal(err)
		}
	}
	status, out := r.raw(method, path, body)
	if status != want {
		r.t.Fatalf("%s %s status=%d want=%d body=%s", method, path, status, want, out)
	}
	return out
}

func webAPITestData[T any](t *testing.T, body []byte) WebAPIEnvelope[T] {
	t.Helper()
	var result WebAPIEnvelope[T]
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func webAPITestProfile(label string) map[string]any {
	return map[string]any{"type": "file", "label": label, "autoUpdateDurationMillis": 3600000}
}
func webAPITestRule(owner *WebAPIID, content string) map[string]any {
	return map[string]any{"profileId": owner, "ruleAction": "DOMAIN", "content": content, "ruleTarget": "DIRECT"}
}
func webAPITestMutation(rev, op string, fields map[string]any) map[string]any {
	fields["expectedRevision"] = rev
	fields["operationId"] = op
	return fields
}

func TestWebAPIProfilesRulesDisabledHTTP(t *testing.T) {
	r := newWebAPITestRig(t)
	initial := webAPITestData[[]WebAPIProfile](t, r.call("GET", "/api/v1/profiles", nil, 200))
	if initial.Revision != "0" || len(initial.Data) != 0 {
		t.Fatal(initial)
	}
	p := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "create-profile", map[string]any{"profile": webAPITestProfile("Primary")}), 200))
	if p.Data.ID != "1" || p.Revision != "1" {
		t.Fatal(p)
	}
	g := webAPITestData[WebAPIRule](t, r.call("POST", "/api/v1/rules:create", webAPITestMutation("1", "create-global", map[string]any{"rule": webAPITestRule(nil, "global.test")}), 200))
	own := webAPITestData[WebAPIRule](t, r.call("POST", "/api/v1/rules:create", webAPITestMutation("2", "create-owned", map[string]any{"rule": webAPITestRule(&p.Data.ID, "owned.test")}), 200))
	added := webAPITestData[[]WebAPIRule](t, r.call("GET", "/api/v1/rules?scope=added&profileId=1", nil, 200))
	if len(added.Data) != 2 || added.Data[0].ID != own.Data.ID || added.Data[1].ID != g.Data.ID {
		t.Fatal(added)
	}
	d := webAPITestData[WebAPIDisabledSelection](t, r.call("POST", "/api/v1/disabled-rules:replace", webAPITestMutation("3", "disable-global", map[string]any{"profileId": p.Data.ID, "ruleIds": []WebAPIID{g.Data.ID}}), 200))
	if !reflect.DeepEqual(d.Data.RuleIDs, []WebAPIID{g.Data.ID}) {
		t.Fatal(d)
	}
	added = webAPITestData[[]WebAPIRule](t, r.call("GET", "/api/v1/rules?scope=added&profileId=1", nil, 200))
	if len(added.Data) != 1 {
		t.Fatal(added)
	}
	r.call("POST", "/api/v1/rules:delete", webAPITestMutation("4", "delete-global", map[string]any{"ids": []WebAPIID{g.Data.ID}}), 200)
	d = webAPITestData[WebAPIDisabledSelection](t, r.call("GET", "/api/v1/disabled-rules?profileId=1", nil, 200))
	if len(d.Data.RuleIDs) != 0 {
		t.Fatal(d)
	}
	r.call("POST", "/api/v1/profiles:delete", webAPITestMutation("5", "delete-profile", map[string]any{"id": p.Data.ID}), 200)
	all := webAPITestData[[]WebAPIRule](t, r.call("GET", "/api/v1/rules", nil, 200))
	if all.Revision != "6" || len(all.Data) != 0 {
		t.Fatal(all)
	}
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		rules, err := tx.Rules()
		if len(rules) != 0 {
			t.Fatal(rules)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.commits.Load() != 6 {
		t.Fatal(r.commits.Load())
	}
}

func TestWebAPIReceiptsReplayRestartAndMeta(t *testing.T) {
	r := newWebAPITestRig(t)
	meta := webAPITestData[WebAPIStoreMeta](t, r.call("GET", "/api/v1/meta", nil, 200))
	if len(meta.Data.StoreID) != 36 || meta.Data.SchemaVersion != 1 || meta.Data.Revision != "0" {
		t.Fatal(meta)
	}
	input := webAPITestMutation("0", "durable-operation", map[string]any{"profile": webAPITestProfile("Saved")})
	first := r.call("POST", "/api/v1/profiles:create", input, 200)
	if replay := r.call("POST", "/api/v1/profiles:create", input, 200); !bytes.Equal(replay, first) {
		t.Fatalf("replay changed: %s / %s", first, replay)
	}
	if receipt := r.call("GET", "/api/v1/operations/durable-operation", nil, 200); !bytes.Equal(receipt, first) {
		t.Fatal("receipt differs")
	}
	changed := webAPITestMutation("0", "durable-operation", map[string]any{"profile": webAPITestProfile("Changed")})
	r.call("POST", "/api/v1/profiles:create", changed, 409)
	if r.commits.Load() != 1 {
		t.Fatal("replay notified twice")
	}
	r.server.Close()
	if err := r.store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	r.store, err = OpenWebAPIStore(r.opts)
	if err != nil {
		t.Fatal(err)
	}
	r.server = httptest.NewServer(NewWebAPIHandler(r.store))
	if receipt := r.call("GET", "/api/v1/operations/durable-operation", nil, 200); !bytes.Equal(receipt, first) {
		t.Fatal("receipt lost at restart")
	}
	if replay := r.call("POST", "/api/v1/profiles:create", input, 200); !bytes.Equal(replay, first) {
		t.Fatal("restart replay differs")
	}
	after := webAPITestData[WebAPIStoreMeta](t, r.call("GET", "/api/v1/meta", nil, 200))
	if after.Data.StoreID != meta.Data.StoreID || after.Data.Revision != "1" {
		t.Fatal(after)
	}
}

func TestWebAPITwoClientsCASAndConcurrentReplay(t *testing.T) {
	r := newWebAPITestRig(t)
	clients := []*http.Client{{Transport: &http.Transport{}}, {Transport: &http.Transport{}}}
	for _, c := range clients {
		defer c.CloseIdleConnections()
	}
	start := make(chan struct{})
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for i, c := range clients {
		wg.Add(1)
		go func(i int, c *http.Client) {
			defer wg.Done()
			<-start
			body, _ := json.Marshal(webAPITestMutation("0", fmt.Sprintf("session-%d-create", i), map[string]any{"profile": webAPITestProfile(fmt.Sprintf("Session %d", i))}))
			req, _ := http.NewRequest("POST", r.server.URL+"/api/v1/profiles:create", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := c.Do(req)
			if err != nil {
				results <- 0
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			results <- resp.StatusCode
		}(i, c)
	}
	close(start)
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for code := range results {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal(counts)
	}
	profiles := webAPITestData[[]WebAPIProfile](t, r.call("GET", "/api/v1/profiles", nil, 200))
	if len(profiles.Data) != 1 || profiles.Revision != "1" {
		t.Fatal(profiles)
	}
	input := webAPITestMutation("1", "concurrent-replay", map[string]any{"profile": webAPITestProfile("Replay")})
	body, _ := json.Marshal(input)
	outputs := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", r.server.URL+"/api/v1/profiles:create", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				outputs <- err.Error()
				return
			}
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			outputs <- fmt.Sprintf("%d:%s", resp.StatusCode, data)
		}()
	}
	wg.Wait()
	close(outputs)
	unique := map[string]bool{}
	for output := range outputs {
		unique[output] = true
	}
	if len(unique) != 1 {
		t.Fatal(unique)
	}
	if r.commits.Load() != 2 {
		t.Fatalf("new commits=%d", r.commits.Load())
	}
}

type webAPIDropWriter struct{ http.ResponseWriter }

func (w webAPIDropWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestWebAPILostHTTPResponseRecoveredByReceipt(t *testing.T) {
	r := newWebAPITestRig(t)
	r.server.Close()
	handler := NewWebAPIHandler(r.store)
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == "POST" {
			handler.ServeHTTP(webAPIDropWriter{w}, request)
		} else {
			handler.ServeHTTP(w, request)
		}
	}))
	input := webAPITestMutation("0", "lost-response-op", map[string]any{"profile": webAPITestProfile("Committed")})
	if dropped := r.call("POST", "/api/v1/profiles:create", input, 200); len(dropped) != 0 {
		t.Fatal("response not dropped")
	}
	receipt := webAPITestData[WebAPIProfile](t, r.call("GET", "/api/v1/operations/lost-response-op", nil, 200))
	if receipt.Revision != "1" || receipt.Data.Label != "Committed" {
		t.Fatal(receipt)
	}
	r.server.Close()
	r.server = httptest.NewServer(handler)
	replay := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", input, 200))
	if replay.Data.ID != receipt.Data.ID || r.commits.Load() != 1 {
		t.Fatal(replay)
	}
}

func TestWebAPIStrictHTTPInput(t *testing.T) {
	r := newWebAPITestRig(t)
	valid := `"expectedRevision":"0","operationId":"invalid-input-op","profile":{"type":"file","label":"Test"}`
	cases := []struct {
		name, body string
		status     int
	}{
		{"unknown", `{` + valid + `,"table":"profiles"}`, 400},
		{"case_alias", `{` + valid + `,"ExpectedRevision":"0"}`, 400},
		{"duplicate", `{` + valid + `,"operationId":"duplicate-op"}`, 400},
		{"escaped_duplicate", `{` + valid + `,"\u006fperationId":"duplicate-op"}`, 400},
		{"nested_duplicate", `{"expectedRevision":"0","operationId":"invalid-input-op","profile":{"type":"file","label":"A","label":"B"}}`, 400},
		{"trailing", `{` + valid + `} {}`, 400},
		{"null_profile", `{"expectedRevision":"0","operationId":"invalid-input-op","profile":null}`, 400},
		{"number_revision", `{"expectedRevision":0,"operationId":"invalid-input-op","profile":{"type":"file"}}`, 400},
		{"negative_revision", `{"expectedRevision":"-1","operationId":"invalid-input-op","profile":{"type":"file"}}`, 400},
		{"overflow_revision", `{"expectedRevision":"9223372036854775808","operationId":"invalid-input-op","profile":{"type":"file"}}`, 400},
		{"duration_overflow", `{"expectedRevision":"0","operationId":"invalid-input-op","profile":{"type":"file","autoUpdateDurationMillis":9223372036854775808}}`, 400},
		{"duration_unsafe", `{"expectedRevision":"0","operationId":"invalid-input-op","profile":{"type":"file","autoUpdateDurationMillis":9007199254740992}}`, 400},
		{"client_identity", `{"expectedRevision":"0","operationId":"invalid-input-op","profile":{"id":"123","type":"file"}}`, 400},
		{"unknown_override", `{"expectedRevision":"0","operationId":"invalid-input-op","profile":{"type":"file","overrides":{"dialer-proxy":"A"}}}`, 400},
		{"depth", strings.Repeat("[", 66) + strings.Repeat("]", 66), 400},
		{"utf8", string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}), 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := r.raw("POST", "/api/v1/profiles:create", []byte(c.body))
			if status != c.status {
				t.Fatalf("got %d: %s", status, body)
			}
		})
	}
	if status, body := r.raw("POST", "/api/v1/profiles:delete", []byte(strings.Repeat(" ", webAPIMaxJSONBytes+1))); status != 413 {
		t.Fatalf("small control body cap: %d %s", status, body)
	}
	for _, id := range []string{"0", "-1", "01", "+1", "1e3", "9223372036854775808", " 1"} {
		input := fmt.Sprintf(`{"expectedRevision":"0","operationId":"bad-id-format","id":%q}`, id)
		status, body := r.raw("POST", "/api/v1/profiles:delete", []byte(input))
		if status != 400 {
			t.Fatalf("id %q status %d: %s", id, status, body)
		}
	}
	for _, path := range []string{"/api/v1/profiles?table=profiles", "/api/v1/profiles?id=1&id=2", "/api/v1/rules?where=1", "/api/v1/rules?scope=unknown", "/api/v1/disabled-rules?profileId=1e3"} {
		r.call("GET", path, nil, 400)
	}
	r.call("PATCH", "/api/v1/profiles", nil, 405)
	r.call("POST", "/api/v1/fs:write", map[string]any{}, 404)
	meta := webAPITestData[WebAPIStoreMeta](t, r.call("GET", "/api/v1/meta", nil, 200))
	if meta.Revision != "0" || r.commits.Load() != 0 {
		t.Fatal(meta)
	}
}

func TestWebAPIReferenceOwnershipDuplicatesAndAtomicReplace(t *testing.T) {
	r := newWebAPITestRig(t)
	p := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "create-profile-a", map[string]any{"profile": webAPITestProfile("A")}), 200))
	q := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("1", "create-profile-b", map[string]any{"profile": webAPITestProfile("B")}), 200))
	missing := WebAPIID("999")
	r.call("POST", "/api/v1/rules:create", webAPITestMutation("2", "bad-rule-owner", map[string]any{"rule": webAPITestRule(&missing, "x.test")}), 409)
	rule := webAPITestData[WebAPIRule](t, r.call("POST", "/api/v1/rules:create", webAPITestMutation("2", "create-rule-one", map[string]any{"rule": webAPITestRule(&p.Data.ID, "one.test")}), 200))
	r.call("POST", "/api/v1/rules:create", webAPITestMutation("3", "duplicate-rule", map[string]any{"rule": webAPITestRule(&p.Data.ID, "one.test")}), 409)
	r.call("POST", "/api/v1/rules:update", webAPITestMutation("3", "rule-change-owner", map[string]any{"id": rule.Data.ID, "rule": webAPITestRule(&q.Data.ID, "one.test")}), 409)
	r.call("POST", "/api/v1/disabled-rules:replace", webAPITestMutation("3", "disable-owned-rule", map[string]any{"profileId": p.Data.ID, "ruleIds": []WebAPIID{rule.Data.ID}}), 409)
	r.call("POST", "/api/v1/disabled-rules:replace", webAPITestMutation("3", "disable-missing-rule", map[string]any{"profileId": p.Data.ID, "ruleIds": []WebAPIID{missing}}), 409)
	r.call("POST", "/api/v1/profiles:reorder", webAPITestMutation("3", "partial-profile-order", map[string]any{"ids": []WebAPIID{p.Data.ID}}), 409)
	r.call("POST", "/api/v1/profiles:reorder", webAPITestMutation("3", "duplicate-profile-order", map[string]any{"ids": []WebAPIID{p.Data.ID, p.Data.ID}}), 409)
	r.call("POST", "/api/v1/rules:delete", webAPITestMutation("3", "delete-one-missing", map[string]any{"ids": []WebAPIID{rule.Data.ID, missing}}), 404)
	r.call("POST", "/api/v1/rules:reorder", webAPITestMutation("3", "partial-rule-order", map[string]any{"profileId": p.Data.ID, "ids": []WebAPIID{}}), 409)
	cross := webAPITestRule(&q.Data.ID, "bad.test")
	cross["id"] = rule.Data.ID
	r.call("POST", "/api/v1/rules:replace", webAPITestMutation("3", "replace-cross-owner", map[string]any{"profileId": q.Data.ID, "rules": []any{cross}}), 409)
	first := webAPITestRule(&p.Data.ID, "updated.test")
	first["id"] = rule.Data.ID
	second := webAPITestRule(&p.Data.ID, "two.test")
	replaced := webAPITestData[[]WebAPIRule](t, r.call("POST", "/api/v1/rules:replace", webAPITestMutation("3", "replace-owned-rules", map[string]any{"profileId": p.Data.ID, "rules": []any{first, second}}), 200))
	if len(replaced.Data) != 2 || replaced.Data[0].ID != rule.Data.ID || replaced.Revision != "4" {
		t.Fatal(replaced)
	}
	input := webAPITestMutation("4", "duplicate-replacement", map[string]any{"profileId": p.Data.ID, "rules": []any{second, second}})
	r.call("POST", "/api/v1/rules:replace", input, 409)
	persisted := webAPITestData[[]WebAPIRule](t, r.call("GET", "/api/v1/rules?scope=profile&profileId="+string(p.Data.ID), nil, 200))
	if !reflect.DeepEqual(persisted.Data, replaced.Data) || persisted.Revision != "4" {
		t.Fatal(persisted)
	}
}

func TestWebAPIUnreadableOverridesRepairHTTP(t *testing.T) {
	for _, raw := range []bool{false, true} {
		t.Run(fmt.Sprint(raw), func(t *testing.T) {
			r := newWebAPITestRig(t)
			input := webAPITestProfile("Broken")
			if raw {
				input["overridesRawDocument"] = `{not-json`
			} else {
				input["overrides"] = map[string]any{"dialer-proxies": []string{"bad"}, "ntp": map[string]any{"server": "preserved.test"}}
			}
			p := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "create-broken-profile", map[string]any{"profile": input}), 200))
			input["label"] = "Edited"
			r.call("POST", "/api/v1/profiles:update", webAPITestMutation("1", "edit-broken-profile", map[string]any{"id": p.Data.ID, "profile": input}), 409)
			r.call("POST", "/api/v1/profiles:rename", webAPITestMutation("1", "rename-broken-profile", map[string]any{"id": p.Data.ID, "label": "Edited"}), 409)
			before := webAPITestData[[]WebAPIProfile](t, r.call("GET", "/api/v1/profiles", nil, 200))
			if !reflect.DeepEqual(before.Data[0], p.Data) {
				t.Fatal("corrupt overrides changed")
			}
			repaired := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:reset-dialers", webAPITestMutation("1", "reset-broken-dialers", map[string]any{"id": p.Data.ID}), 200))
			if repaired.Data.OverridesRawDocument != nil || webAPIUnreadable(repaired.Data.WebAPIProfileInput) {
				t.Fatal(repaired)
			}
			if !raw && repaired.Data.Overrides.NTP.Server != "preserved.test" {
				t.Fatal("reset dropped unrelated override")
			}
		})
	}
}
