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
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type catalogTestRig struct {
	t       *testing.T
	store   *WebAPIStore
	server  *httptest.Server
	commits atomic.Int64
	now     atomic.Int64
	drop    atomic.Bool
	session atomic.Value
}

type catalogDiscardResponse struct{ http.ResponseWriter }

func (w catalogDiscardResponse) Write(body []byte) (int, error) { return len(body), nil }

func newCatalogTestRig(t *testing.T, sourceOptions *WebAPISourceFetchOptions) *catalogTestRig {
	t.Helper()
	r := &catalogTestRig{t: t}
	r.session.Store("")
	r.now.Store(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	var err error
	r.store, err = OpenWebAPIStore(WebAPIStoreOptions{DataDir: filepath.Join(t.TempDir(), "data"), OnCommit: func(WebAPICommit) { r.commits.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Unix(0, r.now.Load()).UTC() }
	mux := http.NewServeMux()
	RegisterWebAPIResourceRoutes(mux, r.store)
	fetchOptions := WebAPISourceFetchOptions{}
	if sourceOptions != nil {
		fetchOptions = *sourceOptions
	}
	if fetchOptions.Now == nil {
		fetchOptions.Now = now
	}
	RegisterWebAPICatalogRoutesWithOptions(mux, r.store, WebAPICatalogOptions{SourceFetchOptions: fetchOptions, Now: now})
	RegisterWebAPISourceRoutes(mux, r.store, WebAPISourceOptions{WebAPISourceFetchOptions: fetchOptions})
	registerWebAPICacheRoutesWithClock(mux, r.store, now)
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if session, _ := r.session.Load().(string); session != "" {
			req = req.WithContext(WithWebAPIRequestAuthorization(req.Context(), session, func(context.Context) (func(), error) { return func() {}, nil }))
		}
		if r.drop.Swap(false) {
			mux.ServeHTTP(catalogDiscardResponse{w}, req)
			return
		}
		mux.ServeHTTP(w, req)
	}))
	t.Cleanup(func() { r.server.Close(); _ = r.store.Close() })
	return r
}

func (r *catalogTestRig) raw(method, path string, body []byte) (int, []byte) {
	r.t.Helper()
	request, err := http.NewRequest(method, r.server.URL+path, bytes.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := r.server.Client().Do(request)
	if err != nil {
		r.t.Fatal(err)
	}
	defer response.Body.Close()
	out, err := io.ReadAll(response.Body)
	if err != nil {
		r.t.Fatal(err)
	}
	return response.StatusCode, out
}

func (r *catalogTestRig) call(method, path string, value any, want int) []byte {
	r.t.Helper()
	var body []byte
	if value != nil {
		var err error
		body, err = json.Marshal(value)
		if err != nil {
			r.t.Fatal(err)
		}
	}
	status, out := r.raw(method, path, body)
	if status != want {
		r.t.Fatalf("%s %s: status=%d want=%d response=%s", method, path, status, want, out)
	}
	return out
}

func catalogData[T any](t *testing.T, body []byte) WebAPIEnvelope[T] {
	t.Helper()
	var value WebAPIEnvelope[T]
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func catalogMutation(revision WebAPIRevision, operation string, fields map[string]any) map[string]any {
	fields["expectedRevision"], fields["operationId"] = revision, operation
	return fields
}

func (r *catalogTestRig) revision() WebAPIRevision {
	r.t.Helper()
	revision, err := r.store.View(context.Background(), func(*WebAPIReadTx) error { return nil })
	if err != nil {
		r.t.Fatal(err)
	}
	return revision
}

func (r *catalogTestRig) customProfile(label string) WebAPIProfile {
	r.t.Helper()
	body := r.call("POST", "/api/v1/profiles:create", catalogMutation(r.revision(), "profile-"+label, map[string]any{"profile": map[string]any{"type": "custom", "label": label, "autoUpdateDurationMillis": 86400000}}), 200)
	return catalogData[WebAPIProfile](r.t, body).Data
}

func catalogProxyInput(name string) map[string]any {
	return map[string]any{"definition": map[string]any{"name": name, "type": "socks5", "server": "proxy.unit.invalid", "port": 443}, "emptyOptions": []string{}}
}
func catalogGroupInput(name string, members ...string) map[string]any {
	return map[string]any{"definition": map[string]any{"name": name, "type": "select", "proxies": members}, "memberLayout": []string{}}
}

func TestWebAPICatalogContractEveryResourceHTTP(t *testing.T) {
	cases := []struct {
		path, singular, plural string
		owner                  bool
		input                  func(string) map[string]any
	}{
		{"proxy-groups", "group", "groups", true, func(name string) map[string]any { return catalogGroupInput(name, "DIRECT") }},
		{"custom-proxies", "proxy", "proxies", false, catalogProxyInput},
		{"clash-providers", "provider", "providers", false, func(name string) map[string]any { return map[string]any{"label": name, "url": ""} }},
		{"scripts", "script", "scripts", false, func(name string) map[string]any { return map[string]any{"label": name} }},
		{"mitm-modules", "module", "modules", false, func(name string) map[string]any {
			return map[string]any{"name": name, "enabled": true, "arguments": map[string]string{}, "system": "iOS >= 18"}
		}},
		{"icon-sets", "iconSet", "iconSets", false, func(name string) map[string]any {
			return map[string]any{"name": name, "url": "", "icons": []map[string]any{{"name": "star", "url": "https://icons.unit.invalid/star.svg"}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			r := newCatalogTestRig(t, nil)
			var owner WebAPIID
			query := "/api/v1/" + tc.path
			if tc.owner {
				owner = r.customProfile("Owner").ID
				query += "?profileId=" + string(owner)
			}
			fields := func(v map[string]any) map[string]any {
				if tc.owner {
					v["profileId"] = owner
				}
				return v
			}
			initial := catalogData[[]map[string]any](t, r.call("GET", query, nil, 200))
			if initial.Data == nil || len(initial.Data) != 0 {
				t.Fatal(initial)
			}
			create := catalogMutation(r.revision(), "create-first", fields(map[string]any{tc.singular: tc.input("First")}))
			firstBody := r.call("POST", "/api/v1/"+tc.path+":create", create, 200)
			first := catalogData[map[string]any](t, firstBody)
			id, ok := first.Data["id"].(string)
			if !ok || id == "" {
				t.Fatal(first)
			}
			listed := catalogData[[]map[string]any](t, r.call("GET", query, nil, 200))
			if len(listed.Data) != 1 || listed.Data[0]["id"] != id {
				t.Fatal(listed)
			}
			beforeInvalid := r.revision()
			bad := tc.input("Invalid")
			bad["id"] = "1"
			r.call("POST", "/api/v1/"+tc.path+":create", catalogMutation(beforeInvalid, "bad-create-id", fields(map[string]any{tc.singular: bad})), 400)
			if tc.path != "mitm-modules" && tc.path != "icon-sets" {
				r.call("POST", "/api/v1/"+tc.path+":create", catalogMutation(beforeInvalid, "duplicate-first", fields(map[string]any{tc.singular: tc.input("First")})), 409)
			}
			r.call("POST", "/api/v1/"+tc.path+":update", catalogMutation(beforeInvalid, "unknown-update", fields(map[string]any{"id": "9223372036854775807", tc.singular: tc.input("Unknown")})), 404)
			r.call("POST", "/api/v1/"+tc.path+":delete", catalogMutation(beforeInvalid, "unknown-delete", fields(map[string]any{"ids": []string{"9223372036854775807"}})), 404)
			r.call("POST", "/api/v1/"+tc.path+":delete", catalogMutation(beforeInvalid, "duplicate-delete", fields(map[string]any{"ids": []string{id, id}})), 409)
			r.call("POST", "/api/v1/"+tc.path+":replace", catalogMutation(beforeInvalid, "missing-replace", fields(map[string]any{})), 400)
			if r.revision() != beforeInvalid {
				t.Fatal("failed requests committed")
			}
			second := catalogData[map[string]any](t, r.call("POST", "/api/v1/"+tc.path+":create", catalogMutation(r.revision(), "create-second", fields(map[string]any{tc.singular: tc.input("Second")})), 200)).Data
			secondID := second["id"].(string)
			ordered := catalogData[[]map[string]any](t, r.call("POST", "/api/v1/"+tc.path+":reorder", catalogMutation(r.revision(), "reorder-both", fields(map[string]any{"ids": []string{secondID, id}})), 200))
			if len(ordered.Data) != 2 || ordered.Data[0]["id"] != secondID {
				t.Fatal(ordered)
			}
			kept := tc.input("Kept")
			kept["id"] = secondID
			replaced := catalogData[[]map[string]any](t, r.call("POST", "/api/v1/"+tc.path+":replace", catalogMutation(r.revision(), "replace-owner", fields(map[string]any{tc.plural: []map[string]any{kept, tc.input("New")}})), 200))
			if len(replaced.Data) != 2 || replaced.Data[0]["id"] != secondID || replaced.Data[1]["id"] == id {
				t.Fatal(replaced)
			}
			commits, revision := r.commits.Load(), r.revision()
			if replay := r.call("POST", "/api/v1/"+tc.path+":create", create, 200); !bytes.Equal(replay, firstBody) {
				t.Fatalf("replay changed: %s != %s", replay, firstBody)
			}
			if r.revision() != revision || r.commits.Load() != commits {
				t.Fatal("replay committed twice")
			}
			changed := catalogMutation(WebAPIRevision(create["expectedRevision"].(WebAPIRevision)), "create-first", fields(map[string]any{tc.singular: tc.input("Changed")}))
			r.call("POST", "/api/v1/"+tc.path+":create", changed, 409)
			casRevision := r.revision()
			var wait sync.WaitGroup
			statuses := make(chan int, 2)
			for i := 0; i < 2; i++ {
				wait.Add(1)
				go func(i int) {
					defer wait.Done()
					request := catalogMutation(casRevision, fmt.Sprintf("cas-writer-%d", i), fields(map[string]any{tc.singular: tc.input(fmt.Sprintf("Writer%d", i))}))
					body, _ := json.Marshal(request)
					status, _ := r.raw("POST", "/api/v1/"+tc.path+":create", body)
					statuses <- status
				}(i)
			}
			wait.Wait()
			close(statuses)
			counts := map[int]int{}
			for status := range statuses {
				counts[status]++
			}
			if counts[200] != 1 || counts[409] != 1 {
				t.Fatal(counts)
			}
			lost := catalogMutation(r.revision(), "lost-response", fields(map[string]any{tc.singular: tc.input("Lost")}))
			r.drop.Store(true)
			if body := r.call("POST", "/api/v1/"+tc.path+":create", lost, 200); len(body) != 0 {
				t.Fatal("response was not dropped")
			}
			commits = r.commits.Load()
			recovered := r.call("POST", "/api/v1/"+tc.path+":create", lost, 200)
			if len(recovered) == 0 || r.commits.Load() != commits {
				t.Fatal("unknown outcome replay created a second row")
			}
			operation := r.call("GET", "/api/v1/operations/lost-response", nil, 200)
			if !bytes.Equal(operation, recovered) {
				t.Fatal("receipt recovery differs")
			}
			all := catalogData[[]map[string]any](t, r.call("GET", query, nil, 200)).Data
			ids := make([]string, len(all))
			for i, item := range all {
				ids[i] = item["id"].(string)
			}
			deleted := catalogData[WebAPIDeleted](t, r.call("POST", "/api/v1/"+tc.path+":delete", catalogMutation(r.revision(), "delete-owner", fields(map[string]any{"ids": ids})), 200))
			if len(deleted.Data.DeletedIDs) != len(ids) {
				t.Fatal(deleted)
			}
			if got := catalogData[[]map[string]any](t, r.call("GET", query, nil, 200)); got.Data == nil || len(got.Data) != 0 {
				t.Fatal(got)
			}
		})
	}
}

func TestWebAPICatalogContractGroupCrossOwnerAndDialerHTTP(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	a, b := r.customProfile("OwnerA"), r.customProfile("OwnerB")
	proxy := catalogData[WebAPICustomProxy](t, r.call("POST", "/api/v1/custom-proxies:create", catalogMutation(r.revision(), "create-dialer-source", map[string]any{"proxy": catalogProxyInput("Node")}), 200)).Data
	group := catalogData[WebAPIProxyGroup](t, r.call("POST", "/api/v1/proxy-groups:create", catalogMutation(r.revision(), "create-owned-group", map[string]any{"profileId": a.ID, "group": catalogGroupInput("Owned", "Node")}), 200)).Data
	before := r.revision()
	r.call("POST", "/api/v1/proxy-groups:update", catalogMutation(before, "cross-owner-update", map[string]any{"profileId": b.ID, "id": group.ID, "group": catalogGroupInput("Cross", "DIRECT")}), 409)
	kept := catalogGroupInput("Cross", "DIRECT")
	kept["id"] = group.ID
	r.call("POST", "/api/v1/proxy-groups:replace", catalogMutation(before, "cross-owner-replace", map[string]any{"profileId": b.ID, "groups": []any{kept}}), 409)
	r.call("POST", "/api/v1/proxy-dialers:replace", catalogMutation(before, "unknown-dialer-source", map[string]any{"profileId": a.ID, "dialers": []any{map[string]any{"proxyId": "999999", "target": "DIRECT"}}}), 404)
	r.call("POST", "/api/v1/proxy-dialers:replace", catalogMutation(before, "duplicate-dialers", map[string]any{"profileId": a.ID, "dialers": []any{map[string]any{"proxyId": proxy.ID, "target": "DIRECT"}, map[string]any{"proxyId": proxy.ID, "target": "DIRECT"}}}), 409)
	r.call("POST", "/api/v1/proxy-dialers:replace", catalogMutation(before, "missing-dialer-target", map[string]any{"profileId": a.ID, "dialers": []any{map[string]any{"proxyId": proxy.ID, "target": "Absent"}}}), 409)
	r.call("POST", "/api/v1/proxy-dialers:replace", catalogMutation(before, "cyclic-dialer-target", map[string]any{"profileId": a.ID, "dialers": []any{map[string]any{"proxyId": proxy.ID, "target": "Owned"}}}), 409)
	if r.revision() != before {
		t.Fatal("failed dialer request committed")
	}
	command := catalogMutation(before, "save-dialer-target", map[string]any{"profileId": a.ID, "dialers": []any{map[string]any{"proxyId": proxy.ID, "target": "DIRECT"}}})
	body := r.call("POST", "/api/v1/proxy-dialers:replace", command, 200)
	got := catalogData[webAPIDialerSelection](t, r.call("GET", "/api/v1/proxy-dialers?profileId="+string(a.ID), nil, 200))
	if len(got.Data.Dialers) != 1 || got.Data.Dialers[0].ProxyID != proxy.ID || got.Data.Dialers[0].Target != "DIRECT" {
		t.Fatal(got)
	}
	if replay := r.call("POST", "/api/v1/proxy-dialers:replace", command, 200); !bytes.Equal(replay, body) {
		t.Fatal("dialer replay changed")
	}
	proxyInput := catalogProxyInput("Node")
	proxyInput["id"] = proxy.ID
	r.call("POST", "/api/v1/custom-proxies:replace", catalogMutation(r.revision(), "keep-dialer-source", map[string]any{"proxies": []any{proxyInput}}), 200)
	keptDialers := catalogData[webAPIDialerSelection](t, r.call("GET", "/api/v1/proxy-dialers?profileId="+string(a.ID), nil, 200)).Data
	if !reflect.DeepEqual(keptDialers, got.Data) {
		t.Fatal("kept proxy lost its dialer", keptDialers)
	}
	rev := r.revision()
	r.call("POST", "/api/v1/custom-proxies:replace", catalogMutation(rev, "remove-referenced-source", map[string]any{"proxies": []any{}}), 409)
	if r.revision() != rev {
		t.Fatal("rejected replacement committed")
	}
	remaining := catalogData[webAPIDialerSelection](t, r.call("GET", "/api/v1/proxy-dialers?profileId="+string(a.ID), nil, 200)).Data
	if !reflect.DeepEqual(remaining, got.Data) {
		t.Fatal("rollback lost source dialers")
	}
}

func catalogNextRevision(value WebAPIRevision) WebAPIRevision {
	n, _ := strconv.ParseInt(string(value), 10, 64)
	return WebAPIRevision(strconv.FormatInt(n+1, 10))
}

func TestWebAPICatalogContractDialerCASAndUnknownOutcomeHTTP(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	profile := r.customProfile("DialerOwner")
	proxy := catalogData[WebAPICustomProxy](t, r.call("POST", "/api/v1/custom-proxies:create", catalogMutation(r.revision(), "dialer-cas-proxy", map[string]any{"proxy": catalogProxyInput("Node")}), 200)).Data
	r.call("POST", "/api/v1/proxy-groups:create", catalogMutation(r.revision(), "dialer-cas-group", map[string]any{"profileId": profile.ID, "group": catalogGroupInput("G", "Node")}), 200)
	revision := r.revision()
	var wait sync.WaitGroup
	codes := make(chan int, 2)
	for i, target := range []string{"DIRECT", ""} {
		wait.Add(1)
		go func(i int, target string) {
			defer wait.Done()
			codes <- r.callIfAny("POST", "/api/v1/proxy-dialers:replace", catalogMutation(revision, fmt.Sprintf("dialer-cas-%d", i), map[string]any{"profileId": profile.ID, "dialers": []any{map[string]any{"proxyId": proxy.ID, "target": target}}}))
		}(i, target)
	}
	wait.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal(counts)
	}
	before, revision := r.snapshot(), r.revision()
	r.call("POST", "/api/v1/proxy-dialers:replace", catalogMutation(revision, "dialer-after-write-rollback", map[string]any{"profileId": profile.ID, "dialers": []any{map[string]any{"proxyId": proxy.ID, "target": "G"}}}), 409)
	if r.revision() != revision || !bytes.Equal(before, r.snapshot()) {
		t.Fatal("cyclic dialer candidate was not rolled back")
	}
	command := catalogMutation(revision, "dialer-lost-response", map[string]any{"profileId": profile.ID, "dialers": []any{}})
	r.drop.Store(true)
	if body := r.call("POST", "/api/v1/proxy-dialers:replace", command, 200); len(body) != 0 {
		t.Fatal("response was not lost")
	}
	commits := r.commits.Load()
	recovered := r.call("POST", "/api/v1/proxy-dialers:replace", command, 200)
	if r.commits.Load() != commits || !bytes.Equal(recovered, r.call("GET", "/api/v1/operations/dialer-lost-response", nil, 200)) {
		t.Fatal("dialer unknown result was applied twice")
	}
}
