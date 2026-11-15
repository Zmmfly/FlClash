//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	C "github.com/metacubex/mihomo/constant"
	"go.yaml.in/yaml/v3"
)

func webRuntimeStatefulTestManager(t *testing.T, s *webRuntimeTestRig) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state-root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	old := C.Path.HomeDir()
	C.SetHomeDir(root)
	t.Cleanup(func() { C.SetHomeDir(old) })
	manager, err := NewWebAPIStatefulManager(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	s.runtime.workspace.options.StatefulManager = manager
	return root
}

func TestWebRuntimeStatefulPreviewIsPureApplyBindsThenUsesPersistentScopedState(t *testing.T) {
	profile := "proxies:\n  - {name: root-state, type: tailscale, auth-key: synthetic-auth, ephemeral: false}\nproxy-providers:\n  inline:\n    type: inline\n    payload:\n      - {name: inline-state, type: easytier, network-name: synthetic-network, network-secret: synthetic-secret, peers: ['tcp://192.0.2.1:11010']}\nproxy-groups:\n  - {name: G, type: select, proxies: [root-state], use: [inline]}\nrules: ['MATCH,G']\n"
	var mu sync.Mutex
	paths := []map[string]string{}
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{Apply: func(_ context.Context, prepared WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
		data, err := os.ReadFile(prepared.ConfigPath)
		if err != nil {
			t.Error(err)
		}
		var root map[string]any
		if err = yaml.Unmarshal(data, &root); err != nil {
			t.Error(err)
		}
		proxy := root["proxies"].([]any)[0].(map[string]any)
		provider := root["proxy-providers"].(map[string]any)["inline"].(map[string]any)
		inline := provider["payload"].([]any)[0].(map[string]any)
		observed := map[string]string{"root": proxy["state-dir"].(string), "inline": inline["state-dir"].(string)}
		if observed["root"] == observed["inline"] {
			t.Error("root and inline provider shared private state")
		}
		for _, path := range observed {
			info, err := os.Stat(path)
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
				t.Error("worker did not materialize a private state directory")
			}
		}
		mu.Lock()
		paths = append(paths, observed)
		mu.Unlock()
		return WebAPIRuntimeApplyResult{ActualConfig: "candidate", ProfileID: prepared.ProfileID, Digest: prepared.Digest}, nil
	}})
	stateRoot := webRuntimeStatefulTestManager(t, s)
	webRuntimeSeedProfile(t, s, "file", profile)
	preview := `{"id":"` + string(webRuntimeTestProfileID) + `"}`
	if code, body := s.request(t, "POST", "/api/v1/profiles:build", preview, s.cookie, ""); code != 200 || strings.Contains(string(body), "state-dir") {
		t.Fatalf("pure stateful build %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(stateRoot, ".api-proxy-state")); !os.IsNotExist(err) {
		t.Fatal("build allocated persistent protocol state")
	}
	if s.runtime.workspace.root != nil {
		t.Fatal("preview allocated a private runtime workspace")
	}
	if code, body := s.request(t, "POST", "/api/v1/runtime:apply", webRuntimeApplyBody(t, "stateful-stale-cas", map[string]any{"expectedRevision": "0"}), s.cookie, ""); code != 409 {
		t.Fatalf("stale apply %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(stateRoot, ".api-proxy-state")); !os.IsNotExist(err) {
		t.Fatal("rejected CAS allocated protocol state")
	}
	code, receipt := s.request(t, "POST", "/api/v1/runtime:apply", webRuntimeApplyBody(t, "stateful-committed-apply", nil), s.cookie, "")
	if code != 200 {
		t.Fatalf("stateful apply %d %s", code, receipt)
	}
	if state := s.phase(t, "stateful-committed-apply"); state.Phase != WebAPIRuntimeApplied {
		t.Fatalf("stateful worker %+v", state)
	}
	bindings, err := s.store.StatefulRuntimeBindings(context.Background(), "stateful-committed-apply")
	if err != nil || len(bindings.Bindings) != 2 {
		t.Fatalf("same-CAS binding missing %+v %v", bindings, err)
	}
	for _, binding := range bindings.Bindings {
		if binding.Resource.Scope.Kind == WebAPIStatefulProfileProvider && !binding.Resource.Scope.Inline {
			t.Fatal("inline provider lost its explicit self-source scope")
		}
	}
	if strings.Contains(string(receipt), "incarnation") || strings.Contains(string(receipt), "state-dir") || strings.Contains(string(receipt), stateRoot) {
		t.Fatal("stateful receipt leaked private resources")
	}
	var record webRuntimeRecord
	if _, err = s.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		var err error
		record, err = tx.runtimeRecord("stateful-committed-apply")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var accepted webRuntimeDesiredApply
	if err = json.Unmarshal(record.Desired, &accepted); err != nil {
		t.Fatal(err)
	}
	oldInput, err := os.ReadFile(accepted.Prepared.ConfigPath)
	if err != nil || strings.Contains(string(oldInput), "state-dir") {
		t.Fatal("worker rewrote its immutable pre-CAS snapshot in place")
	}
	if code, body := s.request(t, "POST", "/api/v1/runtime:apply", webRuntimeApplyBody(t, "stateful-repeated-apply", map[string]any{"expectedRevision": "2"}), s.cookie, ""); code != 200 {
		t.Fatalf("reapply %d %s", code, body)
	}
	if state := s.phase(t, "stateful-repeated-apply"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 2 || paths[0]["root"] != paths[1]["root"] || paths[0]["inline"] != paths[1]["inline"] {
		t.Fatal("same resource reapply did not retain its stable private state")
	}
}

func TestWebRuntimeStatefulExternalProviderUsesStableOriginNotOpaqueSourceHash(t *testing.T) {
	profile := "proxy-providers:\n  remote: {type: http, url: 'https://provider.test/outbounds'}\nproxy-groups:\n  - {name: G, type: select, proxies: [DIRECT], use: [remote]}\nrules: ['MATCH,G']\n"
	content := []byte("proxies:\n  - {name: device, type: zerotier, network: '0123456789abcdef'}\n")
	var mu sync.Mutex
	paths := []string{}
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{Apply: func(_ context.Context, prepared WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
		data, err := os.ReadFile(prepared.ConfigPath)
		if err != nil {
			t.Error(err)
		}
		var root map[string]any
		if err = yaml.Unmarshal(data, &root); err != nil {
			t.Error(err)
		}
		provider := root["proxy-providers"].(map[string]any)["remote"].(map[string]any)
		data, err = os.ReadFile(provider["path"].(string))
		if err != nil {
			t.Error(err)
		}
		root = map[string]any{}
		if err = yaml.Unmarshal(data, &root); err != nil {
			t.Error(err)
		}
		if root["proxy-providers"] != nil {
			t.Error("provider payload was polluted with the root configuration")
		}
		proxy := root["proxies"].([]any)[0].(map[string]any)
		mu.Lock()
		paths = append(paths, proxy["state-dir"].(string))
		mu.Unlock()
		return WebAPIRuntimeApplyResult{ActualConfig: "candidate", ProfileID: prepared.ProfileID, Digest: prepared.Digest}, nil
	}})
	webRuntimeStatefulTestManager(t, s)
	webRuntimeSeedProfile(t, s, "file", profile)
	first := webRuntimeTestSource(t, s)
	if code, body := s.request(t, "POST", "/api/v1/runtime/provider-sources/"+string(first.ID)+":sideload", webRuntimeProviderWriteBody(t, first, "1", "stateful-first-provider-bytes", content), s.cookie, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if state := s.phase(t, "stateful-first-provider-bytes"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	if code, body := s.request(t, "POST", "/api/v1/runtime:apply", webRuntimeApplyBody(t, "stateful-first-provider-apply", map[string]any{"expectedRevision": "2"}), s.cookie, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if state := s.phase(t, "stateful-first-provider-apply"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	_, err := s.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "3", OperationID: "stateful-source-comment-edit"}, "test:source-change", profile, func(tx *WebAPIWriteTx) (any, error) {
		return nil, tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: webRuntimeTestProfileID}, MediaType: "application/yaml", Bytes: []byte(profile + "# unrelated source edit changes the opaque ID\n")})
	})
	if err != nil {
		t.Fatal(err)
	}
	second := webRuntimeTestSource(t, s)
	if first.ID == second.ID {
		t.Fatal("fixture failed to change the source hash identity")
	}
	if code, body := s.request(t, "POST", "/api/v1/runtime/provider-sources/"+string(second.ID)+":sideload", webRuntimeProviderWriteBody(t, second, "4", "stateful-second-provider-bytes", content), s.cookie, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if state := s.phase(t, "stateful-second-provider-bytes"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	if code, body := s.request(t, "POST", "/api/v1/runtime:apply", webRuntimeApplyBody(t, "stateful-second-provider-apply", map[string]any{"expectedRevision": "5"}), s.cookie, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if state := s.phase(t, "stateful-second-provider-apply"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 2 || paths[0] != paths[1] {
		t.Fatal("opaque source hash churn replaced persistent provider device identity")
	}
}
