//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const catalogProfileFixture = "proxies:\n  - {name: A, type: socks5, server: a.unit.invalid, port: 443}\n  - {name: B, type: socks5, server: b.unit.invalid, port: 443}\nproxy-groups:\n  - {name: G, type: select, proxies: [A]}\nrule-providers:\n  Rules: {type: inline, behavior: domain, payload: [unit.invalid]}\nrules:\n  - MATCH,G\nsub-rules:\n  child: [MATCH,DIRECT]\n"

func catalogCheckCode(t *testing.T, err error, want string) {
	t.Helper()
	var problem *WebAPIError
	if !errors.As(err, &problem) || problem.Code != want {
		t.Fatalf("error=%v, want code=%s", err, want)
	}
}

func TestWebAPICatalogContractCandidateProfileOverrides(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	profile := catalogData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", catalogMutation("0", "create-source-profile", map[string]any{"profile": map[string]any{"type": "file", "label": "Source", "autoUpdateDurationMillis": 86400000}}), 200)).Data
	r.seed("seed-profile-source", func(tx *WebAPIWriteTx) error {
		raw, catalog, err := webContentProfile([]byte(catalogProfileFixture), "")
		if err != nil || raw == nil {
			return fmt.Errorf("fixture: %w", err)
		}
		if err := tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: profile.ID}, MediaType: "application/yaml", Bytes: []byte(catalogProfileFixture)}); err != nil {
			return err
		}
		return tx.SetProfileCatalog(profile.ID, catalog)
	})
	input := profile.WebAPIProfileInput
	input.Overrides = &WebAPIProfileOverrides{DialerProxies: json.RawMessage(`{"B":"G"}`), DNS: &WebAPIDNS{Enable: true, Nameserver: []string{"1.1.1.1#A"}}, DNSOverrideKeys: []string{"enable", "nameserver"}, NTP: &WebAPINTP{Enable: true, DialerProxy: "B"}, NTPOverrideKeys: []string{"enable", "dialer-proxy"}, Sniffer: &WebAPISniffer{Enable: true, ForceDomain: []string{"rule-set:Rules"}, Sniff: map[string]WebAPISnifferConfig{"HTTP": {Ports: []string{"80", "8080-8880"}}}}, SnifferOverrideKeys: []string{"enable", "force-domain", "sniff.HTTP"}, Tun: &WebAPIProfileTun{MTU: 1500, RouteAddress: []string{"10.0.0.0/8"}, DNSHijack: []string{"any:53"}}, TunOverrideKeys: []string{"mtu", "route-address", "dns-hijack"}}
	before := r.snapshot()
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error { return ValidateWebAPIProfileInput(tx, profile.ID, input) })
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, r.snapshot()) {
		t.Fatal("candidate validation mutated stored input")
	}
	cases := []struct {
		name, code string
		change     func(*WebAPIProfileInput)
	}{
		{"cycle", "outbound_policy_invalid", func(p *WebAPIProfileInput) { p.Overrides.DialerProxies = json.RawMessage(`{"A":"G"}`) }},
		{"missing-source", "outbound_policy_invalid", func(p *WebAPIProfileInput) { p.Overrides.DialerProxies = json.RawMessage(`{"Missing":"DIRECT"}`) }},
		{"missing-target", "outbound_policy_invalid", func(p *WebAPIProfileInput) { p.Overrides.DialerProxies = json.RawMessage(`{"A":"Missing"}`) }},
		{"damaged-field", "unreadable_overrides", func(p *WebAPIProfileInput) { p.Overrides.DialerProxies = json.RawMessage(`{"A":7}`) }},
		{"unknown-key", "invalid_override_key", func(p *WebAPIProfileInput) { p.Overrides.DNSOverrideKeys = []string{"unknown-key"} }},
		{"duplicate-key", "invalid_override_key", func(p *WebAPIProfileInput) { p.Overrides.NTPOverrideKeys = []string{"enable", "enable"} }},
		{"dns-enum", "invalid_dns_override", func(p *WebAPIProfileInput) { p.Overrides.DNS.CacheAlgorithm = "unknown" }},
		{"dns-target", "invalid_reference", func(p *WebAPIProfileInput) { p.Overrides.DNS.Nameserver = []string{"1.1.1.1#Missing"} }},
		{"ntp-target", "invalid_reference", func(p *WebAPIProfileInput) { p.Overrides.NTP.DialerProxy = "Missing" }},
		{"sniffer-provider", "invalid_reference", func(p *WebAPIProfileInput) { p.Overrides.Sniffer.ForceDomain = []string{"rule-set:Missing"} }},
		{"sniffer-port", "invalid_sniffer_override", func(p *WebAPIProfileInput) {
			p.Overrides.Sniffer.Sniff["HTTP"] = WebAPISnifferConfig{Ports: []string{"999999"}}
		}},
		{"tun-mtu", "invalid_tun_override", func(p *WebAPIProfileInput) { p.Overrides.Tun.MTU = 12 }},
		{"tun-cidr", "invalid_tun_override", func(p *WebAPIProfileInput) { p.Overrides.Tun.RouteAddress = []string{"not-a-prefix"} }},
		{"provider-owner", "owner_conflict", func(p *WebAPIProfileInput) {
			p.Overrides.ProxyProviders = map[string]WebAPIProxyProviderOptions{"Source": {}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := webAPIClone(input)
			tc.change(&candidate)
			_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error { return ValidateWebAPIProfileInput(tx, profile.ID, candidate) })
			catalogCheckCode(t, err, tc.code)
			if !bytes.Equal(before, r.snapshot()) {
				t.Fatal("failed validation changed stored input")
			}
		})
	}
	retained := webAPIClone(input)
	retained.ExtendType = "script"
	retained.Overrides.DialerProxies = json.RawMessage(`{"Missing":"Missing"}`)
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error { return ValidateWebAPIProfileInput(tx, profile.ID, retained) })
	if err != nil {
		t.Fatal("script extension should retain, not apply, valid-shaped standard dialer overrides", err)
	}
}

func TestWebAPICatalogContractComplexRuleGrammarAndReferences(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	profile := r.customProfile("Complex")
	provider := catalogData[WebAPIClashProvider](t, r.call("POST", "/api/v1/clash-providers:create", catalogMutation(r.revision(), "complex-rule-provider", map[string]any{"provider": map[string]any{"label": "Rules", "url": ""}}), 200)).Data
	_ = provider
	direct := "DIRECT"
	good := []struct{ action, content string }{{"AND", "((RULE-SET,Rules),(NETWORK,tcp))"}, {"OR", "((DOMAIN,unit.invalid),(NOT,((DST-PORT,80-90))))"}, {"NOT", "((PROCESS-NAME-REGEX,a,b))"}, {"AND", "((GEOSITE,cn),(GEOIP,lan,no-resolve))"}}
	for _, tc := range good {
		content := tc.content
		input := WebAPIRuleInput{ProfileID: &profile.ID, RuleAction: tc.action, Content: &content, RuleTarget: &direct}
		_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error { return ValidateWebAPIComplexRule(tx, input) })
		if err != nil {
			t.Fatalf("%s %s: %v", tc.action, tc.content, err)
		}
	}
	bad := []struct{ action, content, code string }{{"AND", "((RULE-SET,Missing),(NETWORK,tcp))", "invalid_reference"}, {"AND", "((DOMAIN,unit.invalid)", "invalid_rule"}, {"NOT", "((DOMAIN,a),(DOMAIN,b))", "invalid_rule"}, {"OR", "((SUB-RULE,(DOMAIN,a),child))", "invalid_rule"}, {"AND", "((NETWORK,icmp),(DOMAIN,a))", "invalid_rule"}, {"AND", "((DST-PORT,90000),(DOMAIN,a))", "invalid_rule"}, {"AND", "((RULE-SET,Rules,unexpected),(DOMAIN,a))", "invalid_rule"}}
	for _, tc := range bad {
		content := tc.content
		input := WebAPIRuleInput{ProfileID: &profile.ID, RuleAction: tc.action, Content: &content, RuleTarget: &direct}
		_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error { return ValidateWebAPIComplexRule(tx, input) })
		catalogCheckCode(t, err, tc.code)
	}
	content, child := "(DOMAIN,unit.invalid)", "child"
	input := WebAPIRuleInput{ProfileID: &profile.ID, RuleAction: "SUB_RULE", Content: &content, SubRule: &child}
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error { return ValidateWebAPIComplexRule(tx, input) })
	catalogCheckCode(t, err, "invalid_reference")
}

func TestWebAPICatalogContractRestoreQuarantineDoesNotBypassKnownConstraints(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	profile := r.customProfile("Unreadable")
	r.seed("seed-quarantine-profile", func(tx *WebAPIWriteTx) error {
		p, err := tx.Profile(profile.ID)
		if err != nil {
			return err
		}
		raw := "{broken override\x00text"
		p.Overrides, p.OverridesRawDocument = nil, &raw
		return tx.SaveProfile(p)
	})
	before := r.snapshot()
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		if err := ValidateWebAPICatalogForRestore(tx); err != nil {
			return err
		}
		ids, err := WebAPICatalogQuarantinedProfiles(tx)
		if !reflect.DeepEqual(ids, []WebAPIID{profile.ID}) {
			t.Fatal(ids)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.View(context.Background(), ValidateWebAPICatalog)
	catalogCheckCode(t, err, "unreadable_overrides")
	if !bytes.Equal(before, r.snapshot()) {
		t.Fatal("quarantine validator normalized raw document")
	}
	r.seed("seed-known-invalid-complex-rule", func(tx *WebAPIWriteTx) error {
		id, err := tx.AllocateID()
		if err != nil {
			return err
		}
		target, content := "DIRECT", "((NETWORK,icmp))"
		return tx.InsertRule(WebAPIRule{ID: id, WebAPIRuleInput: WebAPIRuleInput{ProfileID: &profile.ID, RuleAction: "AND", Content: &content, RuleTarget: &target}})
	})
	_, err = r.store.View(context.Background(), ValidateWebAPICatalogForRestore)
	catalogCheckCode(t, err, "invalid_rule")
}

func TestWebAPICatalogContractGroupMembersUsePreparedProviderPayload(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	custom := r.customProfile("Custom")
	provider := catalogData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", catalogMutation(r.revision(), "provider-source-create", map[string]any{"profile": map[string]any{"type": "file", "label": "Subscription", "autoUpdateDurationMillis": 86400000}}), 200)).Data
	group := map[string]any{"definition": map[string]any{"name": "Pool", "type": "select", "use": []string{"Subscription"}, "filter": "^A$"}}
	r.call("POST", "/api/v1/proxy-groups:create", catalogMutation(r.revision(), "provider-group-create", map[string]any{"profileId": custom.ID, "group": group}), 200)
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		names, ready, err := WebAPICatalogGroupMemberNames(tx, custom.ID, "Pool")
		if len(names) != 0 || ready {
			t.Fatal("unprepared provider manufactured members", names, ready)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r.seed("seed-prepared-provider", func(tx *WebAPIWriteTx) error {
		return tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: provider.ID}, MediaType: "application/yaml", Bytes: []byte(catalogProfileFixture)})
	})
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		names, ready, err := WebAPICatalogGroupMemberNames(tx, custom.ID, "Pool")
		if !reflect.DeepEqual(names, []string{"A"}) || !ready {
			t.Fatal(names, ready)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebAPICatalogContractProxyOptionsAndNativeEmptyFieldParity(t *testing.T) {
	valid := catalogProxyInput("Node")
	data, _ := json.Marshal(valid["definition"])
	var definition WebAPIProxyDefinition
	_ = json.Unmarshal(data, &definition)
	if err := webAPIValidateProxy(definition, []string{"allowedIps"}); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"unexpected", "port", "server"} {
		candidate := webAPIClone(definition)
		if field == "port" {
			candidate[field] = json.RawMessage(`70000`)
		} else if field == "server" {
			candidate[field] = json.RawMessage(`[]`)
		} else {
			candidate[field] = json.RawMessage(`true`)
		}
		if err := webAPIValidateProxy(candidate, nil); err == nil {
			t.Fatal("invalid option was accepted", field)
		}
	}
	if err := webAPIValidateProxy(definition, []string{"notAnOption"}); err == nil {
		t.Fatal("unknown empty option accepted")
	}
	if len(webAPIProxyEmptyOptions) != 85 || !strings.Contains(strings.Join(webAPIProxyEmptyOptions, " "), "allowedIps") {
		t.Fatal(webAPIProxyEmptyOptions)
	}
}

func TestWebAPICatalogContractProfileAndComplexRuleMutationHTTP(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	profile := catalogData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", catalogMutation("0", "http-profile-source-create", map[string]any{"profile": map[string]any{"type": "file", "label": "Source", "autoUpdateDurationMillis": 86400000}}), 200)).Data
	r.seed("http-profile-content-seed", func(tx *WebAPIWriteTx) error {
		_, catalog, err := webContentProfile([]byte(catalogProfileFixture), "")
		if err != nil {
			return err
		}
		if err := tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: profile.ID}, MediaType: "application/yaml", Bytes: []byte(catalogProfileFixture)}); err != nil {
			return err
		}
		return tx.SetProfileCatalog(profile.ID, catalog)
	})
	makeInput := func(dialers map[string]string) map[string]any {
		return map[string]any{"type": "file", "label": "Source", "autoUpdateDurationMillis": 86400000, "extendType": "standard", "overrides": map[string]any{"dialer-proxies": dialers, "dns": map[string]any{"enable": true, "nameserver": []string{"1.1.1.1#A"}}, "dns-override-keys": []string{"enable", "nameserver"}}}
	}
	good := catalogMutation(r.revision(), "http-profile-valid-candidate", map[string]any{"id": profile.ID, "profile": makeInput(map[string]string{"B": "G"})})
	body := r.call("POST", "/api/v1/profiles:update", good, 200)
	if got := catalogData[WebAPIProfile](t, body); got.Data.Overrides == nil || !bytes.Contains(got.Data.Overrides.DialerProxies, []byte("G")) {
		t.Fatal(got)
	}
	before, revision := r.snapshot(), r.revision()
	bad := catalogMutation(revision, "http-profile-cycle-candidate", map[string]any{"id": profile.ID, "profile": makeInput(map[string]string{"A": "G"})})
	r.call("POST", "/api/v1/profiles:update", bad, 409)
	if r.revision() != revision || !bytes.Equal(before, r.snapshot()) {
		t.Fatal("candidate profile write was not atomic")
	}
	content, target, child := "((RULE-SET,Rules),(NETWORK,tcp))", "G", "child"
	rule := map[string]any{"profileId": profile.ID, "ruleAction": "AND", "content": content, "ruleTarget": target}
	created := catalogData[WebAPIRule](t, r.call("POST", "/api/v1/rules:create", catalogMutation(r.revision(), "http-complex-rule-create", map[string]any{"rule": rule}), 200)).Data
	revision = r.revision()
	before = r.snapshot()
	rule["content"] = "((NETWORK,icmp))"
	r.call("POST", "/api/v1/rules:update", catalogMutation(revision, "http-complex-invalid-update", map[string]any{"id": created.ID, "rule": rule}), 400)
	if r.revision() != revision || !bytes.Equal(before, r.snapshot()) {
		t.Fatal("complex rule failure changed rows")
	}
	r.call("POST", "/api/v1/rules:create", catalogMutation(r.revision(), "http-subrule-create", map[string]any{"rule": map[string]any{"profileId": profile.ID, "ruleAction": "SUB_RULE", "content": "(DOMAIN,unit.invalid)", "subRule": child}}), 200)
}

func TestWebAPICatalogContractRestorePropagatesQuarantineAndStillChecksFK(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	r.seed("quarantine-provider-fixture", func(tx *WebAPIWriteTx) error {
		raw := "unreadable original provider overrides"
		if err := tx.InsertProfile(WebAPIProfile{ID: "100", WebAPIProfileInput: WebAPIProfileInput{Type: "file", Label: "BrokenProvider", OverridesRawDocument: &raw}}); err != nil {
			return err
		}
		if err := tx.InsertProfile(WebAPIProfile{ID: "101", WebAPIProfileInput: WebAPIProfileInput{Type: "custom", Label: "Dependent", SelectedMap: map[string]string{"Pool": "UnknownProviderMember"}}}); err != nil {
			return err
		}
		if err := tx.InsertProxyGroup(WebAPIProxyGroup{ID: "102", ProfileID: "101", Definition: WebAPIProxyGroupDefinition{Name: "Pool", Type: "select", Use: []string{"BrokenProvider"}}}); err != nil {
			return err
		}
		if err := tx.InsertProfile(WebAPIProfile{ID: "103", WebAPIProfileInput: WebAPIProfileInput{Type: "custom", Label: "Independent"}}); err != nil {
			return err
		}
		return tx.InsertProxyGroup(WebAPIProxyGroup{ID: "104", ProfileID: "103", Definition: WebAPIProxyGroupDefinition{Name: "Safe", Type: "select", Proxies: []string{"DIRECT"}}})
	})
	before := r.snapshot()
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		if err := ValidateWebAPICatalogForRestore(tx); err != nil {
			return err
		}
		ids, err := WebAPICatalogQuarantinedProfiles(tx)
		if !reflect.DeepEqual(ids, []WebAPIID{"100", "101"}) {
			t.Fatal(ids)
		}
		names, ready, e := WebAPICatalogGroupMemberNames(tx, "103", "Safe")
		if e != nil || !ready || !reflect.DeepEqual(names, []string{"DIRECT"}) {
			t.Fatal(names, ready, e)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, r.snapshot()) {
		t.Fatal("restore preview rewrote quarantine state")
	}
	_, err = r.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: r.revision(), OperationID: "quarantine-known-fk-error"}, "test:restore-invalid-fk", "invalid-fk", func(tx *WebAPIWriteTx) (any, error) {
		return nil, tx.SaveProxyDialer(WebAPIProxyDialer{ProfileID: "101", ProxyID: "999999", Target: "DIRECT"})
	})
	catalogCheckCode(t, err, "invalid_reference")
	if !bytes.Equal(before, r.snapshot()) {
		t.Fatal("quarantine bypassed a known foreign key")
	}
}

func TestWebAPICatalogContractRestoreRefreshBuildsKnownOrdinaryCatalogs(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	ordinary := catalogData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", catalogMutation("0", "restore-ordinary-create", map[string]any{"profile": map[string]any{"type": "file", "label": "Ordinary", "autoUpdateDurationMillis": 86400000}}), 200)).Data
	r.seed("restore-ordinary-source", func(tx *WebAPIWriteTx) error {
		return tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: ordinary.ID}, MediaType: "application/yaml", Bytes: []byte(catalogProfileFixture)})
	})
	r.seed("restore-refresh-known-catalog", func(tx *WebAPIWriteTx) error { return RefreshWebAPICatalogForRestore(tx) })
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		catalog, err := tx.ProfileCatalog(ordinary.ID)
		if err != nil {
			return err
		}
		if !webAPIContains(catalog.OutboundNames, "A") || !webAPIContains(catalog.OutboundNames, "G") || !webAPIContains(catalog.RuleProviderNames, "Rules") || !webAPIContains(catalog.SubRuleNames, "child") {
			t.Fatal(catalog)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r.seed("restore-stage-unreadable-catalog", func(tx *WebAPIWriteTx) error {
		raw := "broken raw document"
		return tx.InsertProfile(WebAPIProfile{ID: "999", WebAPIProfileInput: WebAPIProfileInput{Type: "custom", Label: "Unreadable", ExtendType: "standard", OverridesRawDocument: &raw}})
	})
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		items, err := webAPIPrepareRestoreCatalogs(tx)
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.id == "999" && item.unavailableReason != "unreadable_overrides" {
				t.Fatal(item)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebAPICatalogContractRestorePersistsQuarantineReasonAndExplicitRepair(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	profile := r.customProfile("Quarantine")
	raw := "unreadable original override document"
	r.seed("quarantine-reason-source", func(tx *WebAPIWriteTx) error {
		p, err := tx.Profile(profile.ID)
		if err != nil {
			return err
		}
		p.Overrides = nil
		p.OverridesRawDocument = &raw
		return tx.SaveProfile(p)
	})
	r.seed("quarantine-reason-refresh", RefreshWebAPICatalogForRestore)
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		catalog, err := tx.ProfileCatalogState(profile.ID)
		if err != nil {
			return err
		}
		if catalog.UnavailableReason != WebAPICatalogUnreadableOverrides || len(catalog.OutboundNames)+len(catalog.GroupMembers) != 0 {
			t.Fatal(catalog)
		}
		_, err = tx.ProfileCatalog(profile.ID)
		catalogCheckCode(t, err, "catalog_unavailable")
		p, err := tx.Profile(profile.ID)
		if p.OverridesRawDocument == nil || *p.OverridesRawDocument != raw {
			t.Fatal("quarantine lost raw document", p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r.call("POST", "/api/v1/rules:create", catalogMutation(r.revision(), "quarantine-blocks-rule-create", map[string]any{"rule": map[string]any{"profileId": profile.ID, "ruleAction": "DOMAIN", "content": "unit.invalid", "ruleTarget": "DIRECT"}}), 409)
	r.seed("quarantine-explicit-repair", func(tx *WebAPIWriteTx) error {
		if _, err := tx.ResetProfileDialers(profile.ID); err != nil {
			return err
		}
		return RefreshWebAPICatalogForRestore(tx)
	})
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		catalog, err := tx.ProfileCatalog(profile.ID)
		if err != nil {
			return err
		}
		if catalog.UnavailableReason != "" || !webAPIContains(catalog.OutboundNames, "DIRECT") {
			t.Fatal(catalog)
		}
		p, err := tx.Profile(profile.ID)
		if p.OverridesRawDocument != nil {
			t.Fatal("explicit repair did not reset raw state")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
