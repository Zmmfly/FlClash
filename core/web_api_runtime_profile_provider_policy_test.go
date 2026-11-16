//go:build webpanel && !android

package core

import "testing"

func TestWebRuntimeProfileProviderIgnoresSubscriptionControllerSettings(t *testing.T) {
	bind := func(plain string) (map[string]any, error) {
		root := map[string]any{"proxy-providers": map[string]any{"Sub": map[string]any{"type": "file", "path": "sub.yaml"}}}
		snapshot := webRuntimeSnapshot{Profiles: map[string]webRuntimeSource{"Sub": {Profile: WebAPIProfile{ID: "7"}, Plain: []byte(plain)}}}
		err := (&WebAPIRuntime{}).bindResources(root, snapshot, false)
		return root["proxy-providers"].(map[string]any)["Sub"].(map[string]any), err
	}
	mapping, err := bind("external-controller: 127.0.0.1:9090\nexternal-ui: ui\nproxies:\n  - {name: a, type: socks5, server: 192.0.2.1, port: 1080}\n")
	if err != nil {
		t.Fatal(err)
	}
	if mapping["path"] != "flclash-resource:profile:7" {
		t.Fatal(mapping)
	}
	if _, err = bind("proxies:\n  - {name: a, type: socks5, server: 192.0.2.1, port: 1080, ca-file: /etc/ca.pem}\n"); err == nil {
		t.Fatal("a filesystem path inside a provider proxy was accepted")
	}
}

func TestWebRuntimeInlinedProfileProviderAllowsChainedDialer(t *testing.T) {
	root := map[string]any{
		"proxies":         []any{map[string]any{"name": "exit", "type": "socks5", "server": "192.0.2.2", "port": 1080, "dialer-proxy": "front"}},
		"proxy-providers": map[string]any{"Sub": map[string]any{"type": "file", "path": "sub.yaml", "interval": 0}},
		"proxy-groups":    []any{map[string]any{"name": "front", "type": "select", "use": []any{"Sub"}}},
	}
	snapshot := webRuntimeSnapshot{Profiles: map[string]webRuntimeSource{"Sub": {Profile: WebAPIProfile{ID: "7"}, Plain: []byte("proxies:\n  - {name: a, type: socks5, server: 192.0.2.1, port: 1080}\n")}}}
	check := func() error {
		data, err := webRuntimeMarshalYAML(root)
		if err != nil {
			t.Fatal(err)
		}
		document, err := webRuntimeDocument(data)
		if err != nil {
			t.Fatal(err)
		}
		_, err = prepareDependencyDocument(document, nil)
		return err
	}
	if check() == nil {
		t.Fatal("a dialer through an unknown file provider was accepted")
	}
	if err := webRuntimeInlineProfileProviders(root, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := check(); err != nil {
		t.Fatal(err)
	}
	provider := root["proxy-providers"].(map[string]any)["Sub"].(map[string]any)
	if provider["type"] != "inline" || provider["path"] != nil || len(provider["payload"].([]any)) != 1 {
		t.Fatal(provider)
	}
}
