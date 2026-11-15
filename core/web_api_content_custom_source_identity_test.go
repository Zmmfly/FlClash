//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestWebCustomSourceHTTPStructuredIdentityTransaction(t *testing.T) {
	for _, suffix := range []string{"comma,name", `quote"name`, `back\slash`, " edge spaces "} {
		t.Run(suffix, func(t *testing.T) {
			proxy, group, provider, ruleProvider := suffix, "group-"+suffix, "provider-"+suffix, "rule-provider-"+suffix
			root, _ := json.Marshal(map[string]any{"proxies": []any{map[string]any{"name": proxy, "type": "trojan", "server": "synthetic.test", "port": 443, "password": "synthetic"}}, "proxy-groups": []any{map[string]any{"name": group, "type": "select", "proxies": []string{proxy, "DIRECT"}, "use": []string{provider}}}, "proxy-providers": map[string]any{provider: map[string]any{"type": "inline", "payload": []any{}}}, "rule-providers": map[string]any{ruleProvider: map[string]any{"type": "inline", "payload": []any{}}}})
			f := newWebCustomSourceHTTPRig(t, false, string(root))
			job := f.prepare(t, "")
			status, body := f.request(t, "POST", "/api/v1/profiles:import-custom", f.confirm(job), f.cookie)
			if status != 200 {
				t.Fatalf("structured import %d: %s", status, body)
			}
			_, err := f.store.View(context.Background(), func(tx *WebAPIReadTx) error {
				groups, e := tx.ProxyGroups()
				if e != nil {
					return e
				}
				proxies, e := tx.CustomProxies()
				if e != nil {
					return e
				}
				providers, e := tx.ClashProviders()
				if e != nil {
					return e
				}
				profiles, e := tx.Profiles()
				if e != nil {
					return e
				}
				if len(groups) != 1 || groups[0].Definition.Name != group || groups[0].Definition.Proxies[0] != proxy || groups[0].Definition.Use[0] != provider || len(proxies) != 1 || webAPIProxyString(proxies[0].Definition, "name") != proxy || len(providers) != 1 || providers[0].Label != ruleProvider {
					t.Fatal("structured identities changed in final transaction")
				}
				found := false
				for _, p := range profiles {
					if p.Type == "file" && p.Label == provider {
						found = true
					}
				}
				if !found {
					t.Fatal("provider profile label changed")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWebCustomSourceHTTPManagedProviderUsesOriginalSnapshot(t *testing.T) {
	f := newWebCustomSourceHTTPRig(t, false, "proxy-providers: {Original: {type: http, url: SOURCE/proxy}}\nproxy-groups: [{name: G, type: select, use: [Original], default-selected: ProviderNode}]\n")
	request := struct{ Label string }{Label: "Managed provider"}
	receipt, err := f.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: f.revision, OperationID: "seed-managed-provider"}, "synthetic:seed-provider", request, func(tx *WebAPIWriteTx) (any, error) {
		profile, err := tx.CreateProfile(WebAPIProfileInput{Type: "url", Label: request.Label, URL: f.url + "/proxy", ExtendType: "standard", AutoUpdateDurationMillis: 86400000, SelectedMap: map[string]string{}, UnfoldSet: []string{}})
		if err != nil {
			return nil, err
		}
		data := []byte("proxies:\n - {name: ProviderNode, type: trojan, server: synthetic.test, port: 443, password: synthetic}\n")
		_, catalog, err := webContentProfile(data, "")
		if err != nil {
			return nil, err
		}
		if err := tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: profile.ID}, Bytes: data, MediaType: "application/yaml"}); err != nil {
			return nil, err
		}
		if err := tx.SetProfileCatalog(profile.ID, catalog); err != nil {
			return nil, err
		}
		return profile, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.revision = receipt.Revision
	job := f.prepare(t, "")
	if len(job.Dependencies) != 0 || job.Summary.ProxyProviders != 0 {
		t.Fatal("managed source became new remote dependency")
	}
	status, body := f.request(t, "POST", "/api/v1/profiles:import-custom", f.confirm(job), f.cookie)
	if status != 200 {
		t.Fatalf("managed import %d: %s", status, body)
	}
	if f.fetches.Load() != 1 {
		t.Fatal("managed provider was refetched")
	}
	_, err = f.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		groups, err := tx.ProxyGroups()
		if err != nil {
			return err
		}
		if len(groups) != 1 || groups[0].Definition.Use[0] != request.Label {
			t.Fatal("managed provider reference was not rewritten")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebCustomSourceHTTPKeepsExact32MiBSourceCapacity(t *testing.T) {
	prefix := "proxies: []\n#"
	f := newWebCustomSourceHTTPRig(t, false, prefix+strings.Repeat("x", WebAPIMaxContentBytes-len(prefix)))
	job := f.prepare(t, "")
	f.requireUnchanged(t)
	if status, _ := f.request(t, "POST", "/api/v1/profiles:discard-custom-import", map[string]string{"preparedJobId": job.PreparedJobID}, f.cookie); status != 204 {
		t.Fatal(status)
	}
}
