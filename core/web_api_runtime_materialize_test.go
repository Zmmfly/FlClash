//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

const webRuntimeTestProfileID WebAPIID = "9007199254740993"
const webRuntimeTestYAML = "proxies:\n  - {name: P, type: socks5, server: 192.0.2.1, port: 1080}\nproxy-groups:\n  - {name: G, type: select, proxies: [P, DIRECT]}\nrules: ['MATCH,G']\n"

func webRuntimeSeedProfile(t *testing.T, s *webRuntimeTestRig, kind, content string) {
	t.Helper()
	_, err := s.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "0", OperationID: "seed-source-profile"}, "test:seed-source", kind, func(tx *WebAPIWriteTx) (any, error) {
		profile := WebAPIProfile{ID: webRuntimeTestProfileID, WebAPIProfileInput: WebAPIProfileInput{Type: kind, Label: "test-source", ExtendType: "standard", SelectedMap: map[string]string{}, UnfoldSet: []string{}, Overrides: &WebAPIProfileOverrides{}}}
		if err := tx.InsertProfile(profile); err != nil {
			return nil, err
		}
		if kind != "custom" {
			_, catalog, err := webContentProfile([]byte(content), "")
			if err != nil {
				return nil, err
			}
			if err = tx.SetProfileCatalog(profile.ID, catalog); err != nil {
				return nil, err
			}
			if err = tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: profile.ID}, MediaType: "application/yaml", Bytes: []byte(content)}); err != nil {
				return nil, err
			}
		}
		settings := DefaultWebAPISettings()
		settings.CurrentProfileID = &profile.ID
		if err := tx.SaveSettings(settings); err != nil {
			return nil, err
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func webRuntimeApplyBody(t *testing.T, operation string, extra map[string]any) string {
	t.Helper()
	body := map[string]any{"expectedRevision": "1", "operationId": operation, "profileId": webRuntimeTestProfileID, "selectedMap": map[string]string{}, "options": webRuntimeCanonicalOptions(DefaultWebAPISettings())}
	for key, value := range extra {
		body[key] = value
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestWebRuntimeApplyCommittedSnapshotPrivateWorkspaceAndReplay(t *testing.T) {
	var count atomic.Int32
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{Apply: func(_ context.Context, p WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
		count.Add(1)
		data, err := os.ReadFile(p.ConfigPath)
		if err != nil {
			t.Error(err)
		}
		if !bytes.Contains(data, []byte("192.0.2.1")) {
			t.Error("committed profile source was not materialized")
		}
		info, err := os.Stat(p.ConfigPath)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Error("snapshot is not private")
		}
		if p.ProfileID == nil || *p.ProfileID != webRuntimeTestProfileID {
			t.Error("profile ID was rounded")
		}
		return WebAPIRuntimeApplyResult{ActualConfig: "candidate", ProfileID: p.ProfileID, Digest: p.Digest}, nil
	}})
	webRuntimeSeedProfile(t, s, "file", webRuntimeTestYAML)
	body := webRuntimeApplyBody(t, "apply-private-source", nil)
	code, receipt := s.request(t, "POST", "/api/v1/runtime:apply", body, s.cookie, "")
	if code != 200 {
		t.Fatalf("apply %d %s", code, receipt)
	}
	state := s.phase(t, "apply-private-source")
	if state.Phase != WebAPIRuntimeApplied || !bytes.Contains(state.Result, []byte(`"actualConfig":"candidate"`)) {
		t.Fatalf("apply result %+v", state)
	}
	if bytes.Contains(receipt, []byte("configPath")) || bytes.Contains(receipt, []byte("flclash-api-runtime")) {
		t.Fatal("private materialization path crossed HTTP")
	}
	code, replay := s.request(t, "POST", "/api/v1/runtime:apply", body, s.cookie, "")
	if code != 200 || !bytes.Equal(receipt, replay) || count.Load() != 1 {
		t.Fatalf("apply replay %d %s count=%d", code, replay, count.Load())
	}
}

func TestWebRuntimeApplyRejectsPathsUncommittedOptionsAndMalformedSource(t *testing.T) {
	var effects atomic.Int32
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{Apply: func(context.Context, WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
		effects.Add(1)
		return WebAPIRuntimeApplyResult{}, nil
	}})
	webRuntimeSeedProfile(t, s, "file", webRuntimeTestYAML)
	cases := []map[string]any{
		{"overlay": "external-ui: /etc/passwd\n"},
		{"overlay": "proxy-providers: {unsafe: {type: file, path: /etc/passwd}}\n"},
		{"overlay": "ntp: {state-dir: /data/auth}\n"},
		{"candidateYaml": "{}"},
		{"options": CoreOptions{TestURL: "https://different.test/", DelayTestTimeout: 8000}},
		{"selectedMap": map[string]string{"G": "P"}},
		{"overlay": "x: &cycle {y: *cycle}\n"},
	}
	for i, extra := range cases {
		code, body := s.request(t, "POST", "/api/v1/runtime:apply", webRuntimeApplyBody(t, "rejected-source-"+string(rune('a'+i)), extra), s.cookie, "")
		if code < 400 || code >= 500 {
			t.Errorf("rejected source %d %s", code, body)
		}
	}
	if effects.Load() != 0 {
		t.Fatal("rejected input reached Core effects")
	}
	if rev, err := s.store.View(context.Background(), func(*WebAPIReadTx) error { return nil }); err != nil || rev != "1" {
		t.Fatal("validation changed canonical revision")
	}
	if code, _ := s.request(t, "POST", "/api/v1/runtime:apply", `{"expectedRevision":"1","operationId":"path-field","config-path":"/data/config.yaml"}`, s.cookie, ""); code != 400 {
		t.Fatal("legacy config path accepted")
	}
}

func TestWebRuntimeApplyFallbackIsNotReportedAsLastGood(t *testing.T) {
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{Apply: func(context.Context, WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
		return WebAPIRuntimeApplyResult{ActualConfig: "default-fallback"}, webAPIError(422, "config_rejected", "the default fallback is running")
	}})
	webRuntimeSeedProfile(t, s, "file", webRuntimeTestYAML)
	if code, body := s.request(t, "POST", "/api/v1/runtime:apply", webRuntimeApplyBody(t, "fallback-observed", nil), s.cookie, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	state := s.phase(t, "fallback-observed")
	if state.Phase != WebAPIRuntimeFailed || state.Error.Code != "config_rejected" || !bytes.Contains(state.Result, []byte(`"actualConfig":"default-fallback"`)) {
		t.Fatalf("fallback hidden as last-good: %+v", state)
	}
}

func TestWebRuntimeDerivedProfilesAreOneReadAndNeverServerPaths(t *testing.T) {
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	webRuntimeSeedProfile(t, s, "file", webRuntimeTestYAML)
	for _, kind := range []string{"base", "outbounds", "config", "setup-state"} {
		code, body := s.request(t, "GET", "/api/v1/profiles/"+string(webRuntimeTestProfileID)+"/"+kind, "", s.cookie, "")
		if code != 200 || !bytes.Contains(body, []byte(`"revision":"1"`)) {
			t.Errorf("derived %s=%d %s", kind, code, body)
		}
		if bytes.Contains(body, []byte("flclash-api-runtime")) {
			t.Fatal("derived result exposed materialized filesystem path")
		}
	}
	overlay := "mode: rule\nrules: []\n"
	request, _ := json.Marshal(WebAPIRuntimeBuildRequest{ID: webRuntimeTestProfileID, Overlay: &overlay})
	code, body := s.request(t, "POST", "/api/v1/profiles:build", string(request), s.cookie, "")
	if code != 200 || !bytes.Contains(body, []byte(`"yaml":`)) {
		t.Fatalf("typed build %d %s", code, body)
	}
	if bytes.Contains(body, []byte("configPath")) || bytes.Contains(body, []byte("/tmp/")) {
		t.Fatal("build returned a server temp path")
	}
}

func TestWebRuntimeReconcileDerivesCommittedSettingsNotClientPatch(t *testing.T) {
	var called atomic.Int32
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{Reconcile: func(_ context.Context, id *WebAPIID, p UpdateParams) (WebAPIRuntimeApplyResult, error) {
		called.Add(1)
		if p.Options == nil || p.Options.TestURL != DefaultWebAPISettings().Runtime.PatchClashConfig.TestURL {
			t.Error("patch did not come from settings")
		}
		return WebAPIRuntimeApplyResult{ActualConfig: "patched"}, nil
	}})
	if code, body := s.request(t, "POST", "/api/v1/runtime:reconcile", `{"expectedRevision":"0","operationId":"derive-patch"}`, s.cookie, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if state := s.phase(t, "derive-patch"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	if code, _ := s.request(t, "POST", "/api/v1/runtime:reconcile", `{"expectedRevision":"1","operationId":"injected-patch","patch":{"tun":{"enable":true}}}`, s.cookie, ""); code != 400 {
		t.Fatal("client whole patch accepted")
	}
	if called.Load() != 1 {
		t.Fatal("patch injection reached backend")
	}
}

func TestWebRuntimeSafeModeBlocksAllEffectsWithoutOpeningListeners(t *testing.T) {
	var called atomic.Int32
	b := WebAPIRuntimeBackend{SafeMode: true, State: func(context.Context) (WebAPIRuntimeCoreState, error) {
		return WebAPIRuntimeCoreState{Initialized: true, SafeMode: true}, nil
	}, SetListener: func(context.Context, bool) error { called.Add(1); return nil }, InstallGeo: func(context.Context, string, []byte) error { called.Add(1); return nil }, Delay: func(context.Context, WebAPIRuntimeDelayRequest) (*Delay, error) { called.Add(1); return nil, nil }, OutboundIP: func(context.Context, WebAPIRuntimeOutboundIPRequest) (*OutboundIpResult, error) {
		called.Add(1)
		return nil, nil
	}, Apply: func(context.Context, WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
		called.Add(1)
		return WebAPIRuntimeApplyResult{}, nil
	}}
	s := newWebRuntimeTestRig(t, b)
	for path, body := range map[string]string{
		"listener:set-enabled": `{"expectedRevision":"0","operationId":"safe-listener","enabled":true}`,
		"geo:update":           `{"expectedRevision":"0","operationId":"safe-geo-update","type":"MMDB"}`,
		"proxies:delay":        `{"proxyName":"P","testUrl":"https://example.test","timeoutMillis":1000}`,
		"probes/outbound-ip":   `{"proxyName":"P","urls":["https://ipinfo.io/json"],"timeoutMillis":1000}`,
	} {
		if code, body := s.request(t, "POST", "/api/v1/runtime/"+path, body, s.cookie, ""); code != 409 || !bytes.Contains(body, []byte("safe_mode")) {
			t.Errorf("safe effect %s=%d %s", path, code, body)
		}
	}
	if called.Load() != 0 {
		t.Fatal("server safety policy was bypassed")
	}
	before := isRunning.Load()
	native := DefaultWebAPIRuntimeBackend(true)
	if err := native.SetListener(context.Background(), true); err == nil || isRunning.Load() != before {
		t.Fatal("safe backend touched the real listener state")
	}
}

func TestWebRuntimeCookieSessionIdentitySurvivesJSONRoundTrip(t *testing.T) {
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	var identity string
	h := WebAPIRuntimeSessionHandler(s.auth, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity = WebAPIRequestSessionID(r.Context())
		webJSON(w, map[string]string{"identity": identity})
	}))
	r := httptest.NewRequest("GET", "http://panel.test/api/v1/runtime/state", nil)
	r.AddCookie(s.cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if len(identity) != 64 || strings.ContainsAny(identity, "\ufffd\x00") {
		t.Fatal("session identity is not stable hex")
	}
	var result map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result["identity"] != identity {
		t.Fatal("JSON changed the actual cookie session identity")
	}
}
