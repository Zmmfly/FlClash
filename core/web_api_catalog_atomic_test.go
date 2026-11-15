//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func (r *catalogTestRig) seed(operation string, apply func(*WebAPIWriteTx) error) {
	r.t.Helper()
	_, err := r.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: r.revision(), OperationID: operation}, "test:seed", operation, func(tx *WebAPIWriteTx) (any, error) { return nil, apply(tx) })
	if err != nil {
		r.t.Fatal(err)
	}
}

func (r *catalogTestRig) snapshot() []byte {
	r.t.Helper()
	var values []any
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		profiles, err := tx.Profiles()
		if err != nil {
			return err
		}
		values = append(values, profiles)
		groups, err := tx.ProxyGroups()
		if err != nil {
			return err
		}
		values = append(values, groups)
		proxies, err := tx.CustomProxies()
		if err != nil {
			return err
		}
		values = append(values, proxies)
		rules, err := tx.Rules()
		if err != nil {
			return err
		}
		values = append(values, rules)
		dialers, err := tx.ProxyDialers()
		if err != nil {
			return err
		}
		values = append(values, dialers)
		providers, err := tx.ClashProviders()
		if err != nil {
			return err
		}
		values = append(values, providers)
		scripts, err := tx.Scripts()
		if err != nil {
			return err
		}
		values = append(values, scripts)
		modules, err := tx.MitmModules()
		if err != nil {
			return err
		}
		values = append(values, modules)
		icons, err := tx.IconSets()
		if err != nil {
			return err
		}
		values = append(values, icons)
		contents, err := tx.Contents()
		if err != nil {
			return err
		}
		values = append(values, contents)
		return nil
	})
	if err != nil {
		r.t.Fatal(err)
	}
	body, err := json.Marshal(values)
	if err != nil {
		r.t.Fatal(err)
	}
	return body
}

func TestWebAPICatalogContractRenameAtomicReferencesAndRollbackHTTP(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	profile := r.customProfile("Custom")
	createProxy := func(name string) WebAPICustomProxy {
		return catalogData[WebAPICustomProxy](t, r.call("POST", "/api/v1/custom-proxies:create", catalogMutation(r.revision(), "proxy-create-"+name, map[string]any{"proxy": catalogProxyInput(name)}), 200)).Data
	}
	node, other := createProxy("Node"), createProxy("Other")
	provider := catalogData[WebAPIClashProvider](t, r.call("POST", "/api/v1/clash-providers:create", catalogMutation(r.revision(), "create-rule-provider", map[string]any{"provider": map[string]any{"label": "Rules", "url": ""}}), 200)).Data
	createGroup := func(name string, members ...string) WebAPIProxyGroup {
		return catalogData[WebAPIProxyGroup](t, r.call("POST", "/api/v1/proxy-groups:create", catalogMutation(r.revision(), "group-create-"+name, map[string]any{"profileId": profile.ID, "group": catalogGroupInput(name, members...)}), 200)).Data
	}
	group, parent := createGroup("G", "Node", "Other"), createGroup("Parent", "G", "Node")
	r.seed("seed-reference-sites", func(tx *WebAPIWriteTx) error {
		p, err := tx.Profile(profile.ID)
		if err != nil {
			return err
		}
		current, match := "G", "Node"
		p.CurrentGroupName, p.MatchTarget = &current, &match
		p.SelectedMap, p.UnfoldSet = map[string]string{"G": "Node", "Parent": "G"}, []string{"G", "Parent"}
		p.Overrides = &WebAPIProfileOverrides{DialerProxies: json.RawMessage(`{"Node":"DIRECT","Other":"Node"}`), DNS: &WebAPIDNS{Nameserver: []string{"1.1.1.1#G"}, NameserverPolicy: map[string]string{"rule-set:Rules": "8.8.8.8#Node, 1.1.1.1#G"}, FakeIPFilter: []string{"RULE-SET,Rules,fake-ip", "AND,((RULE-SET,Rules),(NETWORK,UDP)),real-ip"}}, NTP: &WebAPINTP{DialerProxy: "G"}, Sniffer: &WebAPISniffer{ForceDomain: []string{"rule-set:Rules"}}}
		if err := tx.SaveProfile(p); err != nil {
			return err
		}
		g, err := tx.ProxyGroup(parent.ID)
		if err != nil {
			return err
		}
		selected := "G"
		g.Definition.DefaultSelected = &selected
		if err := tx.SaveProxyGroup(g); err != nil {
			return err
		}
		for i, target := range []string{"G", "Node"} {
			id, err := tx.AllocateID()
			if err != nil {
				return err
			}
			action, content := "DOMAIN", fmt.Sprintf("unit%d.invalid", i)
			if err := tx.InsertRule(WebAPIRule{ID: id, WebAPIRuleInput: WebAPIRuleInput{ProfileID: &profile.ID, RuleAction: action, Content: &content, RuleTarget: &target}}); err != nil {
				return err
			}
		}
		id, err := tx.AllocateID()
		if err != nil {
			return err
		}
		content, target := "((RULE-SET,Rules),(NETWORK,tcp))", "DIRECT"
		if err := tx.InsertRule(WebAPIRule{ID: id, WebAPIRuleInput: WebAPIRuleInput{ProfileID: &profile.ID, RuleAction: "AND", Content: &content, RuleTarget: &target}}); err != nil {
			return err
		}
		if err := tx.SaveProxyDialer(WebAPIProxyDialer{ProfileID: profile.ID, ProxyID: other.ID, Target: "Node"}); err != nil {
			return err
		}
		return tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentRuleProvider, ID: provider.ID}, MediaType: "text/plain", Bytes: []byte("DOMAIN,unit.invalid\n")})
	})
	before, revision := r.snapshot(), r.revision()
	bad := catalogGroupInput("G2", "Parent")
	r.call("POST", "/api/v1/proxy-groups:update", catalogMutation(revision, "rename-cycle-rollback", map[string]any{"profileId": profile.ID, "id": group.ID, "group": bad}), 409)
	if !bytes.Equal(before, r.snapshot()) || r.revision() != revision {
		t.Fatal("failed candidate left partial rename writes")
	}
	if _, err := r.store.Receipt(context.Background(), "rename-cycle-rollback"); err == nil {
		t.Fatal("failed rename stored a receipt")
	}
	command := catalogMutation(revision, "rename-group-atomic", map[string]any{"profileId": profile.ID, "id": group.ID, "group": catalogGroupInput("G2", "Node", "Other")})
	body := r.call("POST", "/api/v1/proxy-groups:update", command, 200)
	if r.revision() != catalogNextRevision(revision) {
		t.Fatal("rename required more than one transaction")
	}
	if !bytes.Equal(body, r.call("POST", "/api/v1/proxy-groups:update", command, 200)) {
		t.Fatal("group rename replay changed")
	}
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		p, err := tx.Profile(profile.ID)
		if err != nil {
			return err
		}
		if p.CurrentGroupName == nil || *p.CurrentGroupName != "G2" || p.SelectedMap["Parent"] != "G2" || p.SelectedMap["G2"] != "Node" || p.UnfoldSet[0] != "G2" || p.Overrides.NTP.DialerProxy != "G2" || p.Overrides.DNS.Nameserver[0] != "1.1.1.1#G2" {
			t.Fatalf("profile references did not follow group rename: %+v", p)
		}
		g, err := tx.ProxyGroup(parent.ID)
		if err != nil {
			return err
		}
		if g.Definition.Proxies[0] != "G2" || *g.Definition.DefaultSelected != "G2" {
			t.Fatal(g)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r.call("POST", "/api/v1/custom-proxies:update", catalogMutation(r.revision(), "rename-proxy-atomic", map[string]any{"id": node.ID, "proxy": catalogProxyInput("Node2")}), 200)
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		p, err := tx.Profile(profile.ID)
		if err != nil {
			return err
		}
		dialers, valid := webAPIDialers(p.Overrides)
		if !valid || dialers["Node2"] != "DIRECT" || dialers["Other"] != "Node2" || p.SelectedMap["G2"] != "Node2" || p.MatchTarget == nil || *p.MatchTarget != "Node2" || !strings.Contains(p.Overrides.DNS.NameserverPolicy["rule-set:Rules"], "#Node2") {
			t.Fatal(p, dialers)
		}
		items, err := tx.ProxyDialers()
		if err != nil {
			return err
		}
		if len(items) != 1 || items[0].Target != "Node2" {
			t.Fatal(items)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r.call("POST", "/api/v1/clash-providers:update", catalogMutation(r.revision(), "rename-rule-provider", map[string]any{"id": provider.ID, "provider": map[string]any{"label": "Rules2", "url": "https://rules.unit.invalid/list"}}), 200)
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		p, err := tx.Profile(profile.ID)
		if err != nil {
			return err
		}
		if _, exists := p.Overrides.DNS.NameserverPolicy["rule-set:Rules2"]; !exists || p.Overrides.Sniffer.ForceDomain[0] != "rule-set:Rules2" || p.Overrides.DNS.FakeIPFilter[0] != "RULE-SET,Rules2,fake-ip" {
			t.Fatal(p.Overrides)
		}
		rules, err := tx.Rules()
		if err != nil {
			return err
		}
		for _, rule := range rules {
			if rule.RuleAction == "AND" && *rule.Content != "((RULE-SET,Rules2),(NETWORK,tcp))" {
				t.Fatal(rule)
			}
		}
		content, err := tx.Content(WebAPIContentKey{Kind: WebAPIContentRuleProvider, ID: provider.ID})
		if err != nil {
			return err
		}
		if string(content.Bytes) != "DOMAIN,unit.invalid\n" {
			t.Fatal("metadata edit replaced source")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, before = r.revision(), r.snapshot()
	failure := r.call("POST", "/api/v1/clash-providers:delete", catalogMutation(revision, "delete-referenced-provider", map[string]any{"ids": []WebAPIID{provider.ID}}), 409)
	if !bytes.Contains(failure, []byte(`"references"`)) || !bytes.Contains(failure, []byte(`"resource":"rules"`)) {
		t.Fatalf("missing explicit reference details: %s", failure)
	}
	if r.revision() != revision || !bytes.Equal(before, r.snapshot()) {
		t.Fatal("rejected provider deletion lost metadata or bytes")
	}
}

func TestWebAPICatalogContractUnreadableOverridesStayWhole(t *testing.T) {
	for _, raw := range []string{`{"dialer-proxies":{"Node":"DIRECT","bad":7}}`, `not-json-at-all`} {
		t.Run(raw, func(t *testing.T) {
			r := newCatalogTestRig(t, nil)
			profile := r.customProfile("Custom")
			proxy := catalogData[WebAPICustomProxy](t, r.call("POST", "/api/v1/custom-proxies:create", catalogMutation(r.revision(), "create-unreadable-proxy", map[string]any{"proxy": catalogProxyInput("Node")}), 200)).Data
			r.seed("seed-unreadable-overrides", func(tx *WebAPIWriteTx) error {
				p, err := tx.Profile(profile.ID)
				if err != nil {
					return err
				}
				if json.Valid([]byte(raw)) {
					p.Overrides = &WebAPIProfileOverrides{DialerProxies: json.RawMessage(`{"Node":"DIRECT","bad":7}`)}
				} else {
					p.Overrides = nil
					p.OverridesRawDocument = &raw
				}
				return tx.SaveProfile(p)
			})
			before, revision := r.snapshot(), r.revision()
			r.call("POST", "/api/v1/custom-proxies:update", catalogMutation(revision, "rename-unreadable-proxy", map[string]any{"id": proxy.ID, "proxy": catalogProxyInput("Node2")}), 409)
			if r.revision() != revision || !bytes.Equal(before, r.snapshot()) {
				t.Fatal("rename salvaged a damaged override field/document")
			}
		})
	}
}

func TestWebAPICatalogContractContentNamespaceAndDeleteRollback(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	const id WebAPIID = "9007199254740993"
	r.seed("seed-same-id-namespaces", func(tx *WebAPIWriteTx) error {
		if err := tx.InsertScript(WebAPIScript{ID: id, Label: "Script", LastUpdateTime: "2026-10-01T00:00:00Z"}); err != nil {
			return err
		}
		if err := tx.InsertClashProvider(WebAPIClashProvider{ID: id, Label: "Provider", Behavior: "classical", Format: "yaml"}); err != nil {
			return err
		}
		if err := tx.InsertMitmModule(WebAPIMitmModule{ID: id, Name: "Module", Arguments: map[string]string{}, Resources: map[string]string{}, LastUpdateTime: "2026-10-01T00:00:00Z"}); err != nil {
			return err
		}
		for _, kind := range []WebAPIContentKind{WebAPIContentScript, WebAPIContentRuleProvider, WebAPIContentMitmModule} {
			if err := tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: kind, ID: id}, MediaType: "text/plain", Bytes: []byte(kind)}); err != nil {
				return err
			}
		}
		return nil
	})
	r.call("POST", "/api/v1/clash-providers:update", catalogMutation(r.revision(), "metadata-keeps-source", map[string]any{"id": id, "provider": map[string]any{"label": "Provider2", "url": "https://unit.invalid/rules"}}), 200)
	r.call("POST", "/api/v1/clash-providers:delete", catalogMutation(r.revision(), "delete-one-namespace", map[string]any{"ids": []WebAPIID{id}}), 200)
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		for _, kind := range []WebAPIContentKind{WebAPIContentScript, WebAPIContentMitmModule} {
			content, err := tx.Content(WebAPIContentKey{Kind: kind, ID: id})
			if err != nil {
				return err
			}
			if string(content.Bytes) != string(kind) {
				t.Fatal(content)
			}
		}
		if _, err := tx.Content(WebAPIContentKey{Kind: WebAPIContentRuleProvider, ID: id}); err == nil {
			t.Fatal("deleted provider left its source")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before, revision := r.snapshot(), r.revision()
	_, err = r.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: revision, OperationID: "rollback-after-delete"}, "test:rollback", "rollback", func(tx *WebAPIWriteTx) (any, error) {
		if _, err := webAPICatalogDelete(tx, webAPIScriptResource(), []WebAPIID{id}); err != nil {
			return nil, err
		}
		return nil, errors.New("injected failure")
	})
	if err == nil || r.revision() != revision || !bytes.Equal(before, r.snapshot()) {
		t.Fatal("delete/owned-blob rollback was not atomic", err)
	}
	r.call("POST", "/api/v1/mitm-modules:update", catalogMutation(r.revision(), "module-argument-content-guard", map[string]any{"id": id, "module": map[string]any{"name": "Module", "enabled": true, "arguments": map[string]string{"new": "scripts"}}}), 409)
}
