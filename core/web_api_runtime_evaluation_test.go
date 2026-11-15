//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func webRuntimeEvaluatedRig(t *testing.T, script string, backend WebAPIRuntimeBackend) (*webEvaluateRig, *webRuntimeTestRig) {
	t.Helper()
	f := newWebEvaluateRig(t, script)
	_, err := f.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "1", OperationID: "runtime-eval-current-profile"}, "test:current", nil, func(tx *WebAPIWriteTx) (any, error) {
		settings := DefaultWebAPISettings()
		id := webEvaluateProfileID
		settings.CurrentProfileID = &id
		return nil, tx.SaveSettings(settings)
	})
	if err != nil {
		t.Fatal(err)
	}
	if backend.State == nil {
		backend.State = func(context.Context) (WebAPIRuntimeCoreState, error) {
			id := webEvaluateProfileID
			return WebAPIRuntimeCoreState{Initialized: true, ProfileID: &id, RuntimeGeneration: "1"}, nil
		}
	}
	runtime, err := NewWebAPIRuntime(f.store, backend)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(WebAPIRuntimeSessionHandler(f.auth, runtime))
	t.Cleanup(func() { runtime.Close(); server.Close() })
	return f, &webRuntimeTestRig{auth: f.auth, store: f.store, runtime: runtime, server: server, cookie: f.cookie}
}

func webRuntimeEvaluate(t *testing.T, f *webEvaluateRig, revision WebAPIRevision) WebAPIEvaluateResult {
	t.Helper()
	request := WebAPIEvaluateRequest{ExpectedRevision: revision, ProfileID: webEvaluateProfileID, ScriptID: webEvaluateScriptID}
	code, body := f.request(t, "/api/v1/profiles:evaluate", request, f.cookie)
	var envelope WebAPIEnvelope[WebAPIEvaluateResult]
	if code != 200 || json.Unmarshal(body, &envelope) != nil || envelope.Data.EvaluationID == "" {
		t.Fatalf("evaluate %d %s", code, body)
	}
	return envelope.Data
}

func TestWebRuntimeEvaluationHTTPBuildApplyConsumesFrozenCacheWithoutReevaluation(t *testing.T) {
	var effects atomic.Int32
	f, s := webRuntimeEvaluatedRig(t, `function main(c){c['x-evaluation-marker']='cached-only';return c}`, WebAPIRuntimeBackend{Apply: func(_ context.Context, p WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
		effects.Add(1)
		data, err := os.ReadFile(p.ConfigPath)
		if err != nil || !bytes.Contains(data, []byte("cached-only")) {
			t.Error("worker did not use frozen evaluated config")
		}
		return WebAPIRuntimeApplyResult{ActualConfig: "candidate", ProfileID: p.ProfileID}, nil
	}})
	evaluation := webRuntimeEvaluate(t, f, "2")
	if err := f.service.engine.Close(); err != nil {
		t.Fatal(err)
	}
	build := map[string]any{"id": webEvaluateProfileID, "candidateYaml": string(evaluation.Config), "evaluationId": evaluation.EvaluationID}
	buildJSON, _ := json.Marshal(build)
	if code, body := s.request(t, "POST", "/api/v1/profiles:build", string(buildJSON), s.cookie, ""); code != 200 || !bytes.Contains(body, []byte("cached-only")) {
		t.Fatalf("cached build %d %s", code, body)
	}
	other := webLogin(t, f.auth)
	if code, _ := s.request(t, "POST", "/api/v1/profiles:build", string(buildJSON), other, ""); code != 409 {
		t.Fatal("wrong session consumed evaluation")
	}
	for _, changed := range []map[string]any{{"evaluationId": strings.Repeat("x", 43)}, {"id": "42"}} {
		copy := map[string]any{}
		for k, v := range build {
			copy[k] = v
		}
		for k, v := range changed {
			copy[k] = v
		}
		raw, _ := json.Marshal(copy)
		if code, _ := s.request(t, "POST", "/api/v1/profiles:build", string(raw), s.cookie, ""); code < 400 {
			t.Fatal("wrong evaluation/profile accepted")
		}
	}
	if effects.Load() != 0 {
		t.Fatal("build/invalid provenance applied Core")
	}
	apply := webRuntimeApplyBody(t, "apply-frozen-evaluation", map[string]any{"expectedRevision": "2", "candidateYaml": string(evaluation.Config), "evaluationId": evaluation.EvaluationID})
	if code, body := s.request(t, "POST", "/api/v1/runtime:apply", apply, s.cookie, ""); code != 200 {
		t.Fatalf("cached apply %d %s", code, body)
	}
	if state := s.phase(t, "apply-frozen-evaluation"); state.Phase != WebAPIRuntimeApplied || effects.Load() != 1 {
		t.Fatalf("frozen apply %+v effects=%d", state, effects.Load())
	}
	if code, _ := s.request(t, "POST", "/api/v1/profiles:build", string(buildJSON), s.cookie, ""); code != 409 {
		t.Fatal("old read revision was fresh-stamped after accepted apply")
	}
}

func TestWebRuntimeEvaluationDynamicProviderPreparedFromEvaluatedOutputAndNewReadRequired(t *testing.T) {
	var fetched, applied atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched.Add(1)
		_, _ = w.Write([]byte("proxies: [{name: Remote, type: socks5, server: 192.0.2.9, port: 1080}]\n"))
	}))
	defer remote.Close()
	_, port, _ := net.SplitHostPort(remote.Listener.Addr().String())
	script := `function main(c){c['proxy-providers']={D:{type:'http',url:'http://evaluated-provider.test:` + port + `/provider'}};c['proxy-groups']=[{name:'G',type:'select',proxies:['DIRECT'],use:['D']}];return c}`
	f, s := webRuntimeEvaluatedRig(t, script, WebAPIRuntimeBackend{Apply: func(_ context.Context, p WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
		applied.Add(1)
		return WebAPIRuntimeApplyResult{ActualConfig: "candidate", ProfileID: p.ProfileID}, nil
	}, ExternalProviders: func(context.Context) ([]WebAPIRuntimeExternalProviderMetadata, error) {
		return []WebAPIRuntimeExternalProviderMetadata{{Name: "D", Purpose: "proxy-providers", Type: "Proxy", VehicleType: "File", Count: 1}}, nil
	}})
	s.runtime.workspace.options.SourceFetchOptions = WebAPISourceFetchOptions{ResolveRuntimeSourceIdentity: s.runtime.ResolveRuntimeSourceIdentity, LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, remote.Listener.Addr().String())
	}}
	e2 := webRuntimeEvaluate(t, f, "2")
	path := "/api/v1/runtime/provider-sources?profileId=" + string(webEvaluateProfileID) + "&evaluationId=" + e2.EvaluationID
	code, body := s.request(t, "GET", path, "", s.cookie, "")
	var sources WebAPIEnvelope[[]WebAPIRuntimeProviderSource]
	if code != 200 || json.Unmarshal(body, &sources) != nil || len(sources.Data) != 1 || sources.Data[0].Name != "D" {
		t.Fatalf("dynamic provider was derived from original source instead of evaluation %d %s", code, body)
	}
	source := sources.Data[0]
	refresh, _ := json.Marshal(WebAPIRuntimeProviderRefresh{WebAPIMutation: WebAPIMutation{ExpectedRevision: "2", OperationID: "refresh-evaluated-provider"}, ID: source.ID, Purpose: source.Purpose})
	if code, body = s.request(t, "POST", "/api/v1/runtime/provider-sources:refresh", string(refresh), s.cookie, ""); code != 200 {
		t.Fatalf("prepare dynamic %d %s", code, body)
	}
	if state := s.phase(t, "refresh-evaluated-provider"); state.Phase != WebAPIRuntimeApplied || fetched.Load() != 1 || applied.Load() != 0 || !bytes.Contains(state.Result, []byte(`"requiresApply":true`)) {
		t.Fatalf("prepare dynamic %+v fetch=%d apply=%d", state, fetched.Load(), applied.Load())
	}
	oldBuild, _ := json.Marshal(map[string]any{"id": webEvaluateProfileID, "candidateYaml": string(e2.Config), "evaluationId": e2.EvaluationID})
	if code, _ := s.request(t, "POST", "/api/v1/profiles:build", string(oldBuild), s.cookie, ""); code != 409 {
		t.Fatal("owned refresh revision bump fresh-stamped old evaluation")
	}
	e3 := webRuntimeEvaluate(t, f, "3") // explicit new read/evaluation, never a hidden runtime re-eval.
	if err := f.service.engine.Close(); err != nil {
		t.Fatal(err)
	}
	newBuild, _ := json.Marshal(map[string]any{"id": webEvaluateProfileID, "candidateYaml": string(e3.Config), "evaluationId": e3.EvaluationID})
	if code, body = s.request(t, "POST", "/api/v1/profiles:build", string(newBuild), s.cookie, ""); code != 200 {
		t.Fatalf("new frozen read cannot reuse prepared matching provider %d %s", code, body)
	}
	apply := webRuntimeApplyBody(t, "apply-evaluated-provider", map[string]any{"expectedRevision": "3", "candidateYaml": string(e3.Config), "evaluationId": e3.EvaluationID})
	if code, body = s.request(t, "POST", "/api/v1/runtime:apply", apply, s.cookie, ""); code != 200 {
		t.Fatalf("apply dynamic %d %s", code, body)
	}
	if state := s.phase(t, "apply-evaluated-provider"); state.Phase != WebAPIRuntimeApplied || applied.Load() != 1 || fetched.Load() != 1 {
		t.Fatalf("dynamic final %+v", state)
	}
	if code, body = s.request(t, "GET", "/api/v1/runtime/external-providers", "", s.cookie, ""); code != 200 || !bytes.Contains(body, []byte(string(source.ID))) {
		t.Fatalf("applied dynamic inventory lost its committed provenance %d %s", code, body)
	}
	if code, body = s.request(t, "GET", "/api/v1/runtime/provider-sources/"+string(source.ID)+"/content", "", s.cookie, ""); code != 200 {
		t.Fatalf("applied dynamic source became unreadable after owned apply revision %d %s", code, body)
	}
	if code, _ := s.request(t, "POST", "/api/v1/profiles:build", string(newBuild), webLogin(t, f.auth), ""); code != 409 {
		t.Fatal("installed source authority leaked an evaluation candidate to another session")
	}
	_, err := s.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "4", OperationID: "change-evaluated-label-only"}, "test:label-only", nil, func(tx *WebAPIWriteTx) (any, error) {
		profile, err := tx.Profile(webEvaluateProfileID)
		if err != nil {
			return nil, err
		}
		profile.Label = "different normalized guest input"
		return nil, tx.SaveProfile(profile)
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := s.request(t, "GET", "/api/v1/runtime/provider-sources/"+string(source.ID)+"/content", "", s.cookie, ""); code != 409 {
		t.Fatal("label-only change retained applied evaluated provider authority")
	}
	if code, _ := s.request(t, "GET", "/api/v1/runtime/external-providers", "", s.cookie, ""); code != 409 {
		t.Fatal("label-only change retained old applied provider inventory binding")
	}
	lateRefresh, _ := json.Marshal(WebAPIRuntimeProviderRefresh{WebAPIMutation: WebAPIMutation{ExpectedRevision: "5", OperationID: "refresh-after-evaluated-label-change"}, ID: source.ID, Purpose: source.Purpose})
	if code, _ := s.request(t, "POST", "/api/v1/runtime/provider-sources:refresh", string(lateRefresh), s.cookie, ""); code != 409 || fetched.Load() != 1 || applied.Load() != 1 {
		t.Fatal("old label provenance triggered a new fetch/effect")
	}
}
