//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Pins the coordination contract in build/api-store-contract.md, not unfinished domain endpoints.
func TestWebAPIBulkJSONAndContentBoundaryContract(t *testing.T) {
	type contentRequest struct {
		Bytes []byte `json:"bytes"`
	}
	payload := bytes.Repeat([]byte{'x'}, WebAPIMaxContentBytes)
	body, err := json.Marshal(contentRequest{Bytes: payload})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/content-limit", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	var decoded contentRequest
	if err = WebAPIDecodeJSONLimit(httptest.NewRecorder(), request, &decoded, WebAPIMaxContentJSONBytes); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.Bytes, payload) {
		t.Fatal("32 MiB decoded content changed")
	}
	r := newWebAPITestRig(t)
	_, err = r.store.Transaction(context.Background(), WebAPIMutation{"0", "store-32mib-content"}, "test:boundary", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		p, err := tx.CreateProfile(WebAPIProfileInput{Type: "file", Label: "Large"})
		if err != nil {
			return nil, err
		}
		return p, tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: p.ID}, MediaType: "application/octet-stream", Bytes: payload})
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.Transaction(context.Background(), WebAPIMutation{"1", "reject-overlimit-content"}, "test:boundary", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		return nil, tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: "1"}, MediaType: "application/octet-stream", Bytes: make([]byte, WebAPIMaxContentBytes+1)})
	})
	webAPITestErrorCode(t, err, "content_too_large")
}

func TestWebAPIGlobalAtomicBulkReplacePreservesRetainedDisabled(t *testing.T) {
	r := newWebAPITestRig(t)
	p := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "global-replace-profile", map[string]any{"profile": webAPITestProfile("Main")}), 200))
	first := webAPITestData[WebAPIRule](t, r.call("POST", "/api/v1/rules:create", webAPITestMutation("1", "global-replace-first", map[string]any{"rule": webAPITestRule(nil, "keep.test")}), 200))
	second := webAPITestData[WebAPIRule](t, r.call("POST", "/api/v1/rules:create", webAPITestMutation("2", "global-replace-second", map[string]any{"rule": webAPITestRule(nil, "remove.test")}), 200))
	r.call("POST", "/api/v1/disabled-rules:replace", webAPITestMutation("3", "global-replace-disabled", map[string]any{"profileId": p.Data.ID, "ruleIds": []WebAPIID{first.Data.ID, second.Data.ID}}), 200)
	kept := webAPITestRule(nil, "kept-updated.test")
	kept["id"] = first.Data.ID
	items := []any{kept}
	for i := 0; i < 200; i++ {
		items = append(items, webAPITestRule(nil, fmt.Sprintf("%04d", i)+strings.Repeat("a", 6000)+".test"))
	}
	result := webAPITestData[[]WebAPIRule](t, r.call("POST", "/api/v1/rules:replace", webAPITestMutation("4", "global-replace-bulk", map[string]any{"profileId": nil, "rules": items}), 200))
	if len(result.Data) != 201 || result.Data[0].ID != first.Data.ID || result.Revision != "5" {
		t.Fatal("global bulk replacement result mismatch")
	}
	disabled := webAPITestData[WebAPIDisabledSelection](t, r.call("GET", "/api/v1/disabled-rules?profileId="+string(p.Data.ID), nil, 200))
	if !reflect.DeepEqual(disabled.Data.RuleIDs, []WebAPIID{first.Data.ID}) {
		t.Fatal(disabled)
	}
	bad := webAPITestRule(&p.Data.ID, "wrong-owner.test")
	bad["id"] = first.Data.ID
	r.call("POST", "/api/v1/rules:replace", webAPITestMutation("5", "global-replace-bad-owner", map[string]any{"profileId": p.Data.ID, "rules": []any{bad}}), 409)
}

func TestWebAPIProfileUpdatePreservesOmittedPresentation(t *testing.T) {
	r := newWebAPITestRig(t)
	input := webAPITestProfile("Presentation")
	input["unfoldSet"] = []string{"Group"}
	p := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "presentation-create", map[string]any{"profile": input}), 200))
	updated := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:update", webAPITestMutation("1", "presentation-update", map[string]any{"id": p.Data.ID, "profile": webAPITestProfile("Presentation")}), 200))
	if !reflect.DeepEqual(updated.Data.UnfoldSet, []string{"Group"}) {
		t.Fatal("omitted browser presentation cleared native backup metadata")
	}
}

func TestWebAPIRuleSyntaxAndCatalogTargets(t *testing.T) {
	r := newWebAPITestRig(t)
	p := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "catalog-profile-create", map[string]any{"profile": webAPITestProfile("Catalog")}), 200))
	input := webAPITestRule(&p.Data.ID, "route.test")
	input["ruleTarget"] = "Explicit node"
	r.call("POST", "/api/v1/rules:create", webAPITestMutation("1", "missing-catalog-target", map[string]any{"rule": input}), 409)
	_, err := r.store.Transaction(context.Background(), WebAPIMutation{"1", "populate-real-catalog"}, "test:catalog", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		return struct{}{}, tx.SetProfileCatalog(p.Data.ID, WebAPIProfileCatalog{OutboundNames: []string{"Explicit node"}, RuleProviderNames: []string{"Rules"}})
	})
	if err != nil {
		t.Fatal(err)
	}
	r.call("POST", "/api/v1/rules:create", webAPITestMutation("2", "known-catalog-target", map[string]any{"rule": input}), 200)
	for i, fields := range []map[string]any{
		{"ruleAction": "IP_CIDR", "content": "garbage"},
		{"ruleAction": "DST_PORT", "content": "65536"},
		{"ruleAction": "DSCP", "content": "64"},
		{"ruleAction": "NETWORK", "content": "invalid"},
		{"ruleAction": "DOMAIN_REGEX", "content": "("},
		{"ruleAction": "DOMAIN", "ruleTarget": "MATCH"},
		{"ruleAction": "DOMAIN", "noResolve": true},
	} {
		rule := webAPITestRule(&p.Data.ID, "invalid.test")
		for k, v := range fields {
			rule[k] = v
		}
		r.call("POST", "/api/v1/rules:create", webAPITestMutation("3", fmt.Sprintf("reject-invalid-rule-%d", i), map[string]any{"rule": rule}), 400)
	}
	provider := map[string]any{"profileId": p.Data.ID, "ruleAction": "RULE_SET", "ruleProvider": "Absent", "ruleTarget": "DIRECT"}
	r.call("POST", "/api/v1/rules:create", webAPITestMutation("3", "missing-rule-provider", map[string]any{"rule": provider}), 409)
	provider["ruleProvider"] = "Rules"
	r.call("POST", "/api/v1/rules:create", webAPITestMutation("3", "known-rule-provider", map[string]any{"rule": provider}), 200)
}

func TestWebAPICacheResolutionHTTPIsIndependent(t *testing.T) {
	r := newWebAPITestRig(t)
	r.call("GET", "/api/v1/cache-operations/shared-operation-id", nil, 404)
	r.call("POST", "/api/v1/cache-operations/shared-operation-id:resolve", map[string]any{"intent": "resolve-unknown-result"}, 409)
	r.call("GET", "/api/v1/cache-operations/shared-operation-id", nil, 409)
	response := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "shared-operation-id", map[string]any{"profile": webAPITestProfile("Config still allowed")}), 200))
	receipt := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/operations/shared-operation-id:resolve", map[string]any{"intent": "resolve-unknown-result"}, 200))
	if !reflect.DeepEqual(receipt, response) {
		t.Fatal("resolution did not return original committed envelope")
	}
	r.call("POST", "/api/v1/operations/unknown-operation:resolve", map[string]any{"intent": "force-cancel"}, 400)
}

func TestWebAPIProfileBulkBodyPreserves32MiBRawOverride(t *testing.T) {
	r := newWebAPITestRig(t)
	p := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "large-profile-before", map[string]any{"profile": webAPITestProfile("Large")}), 200))
	input := webAPITestProfile("Large")
	input["overridesRawDocument"] = strings.Repeat("x", WebAPIMaxContentBytes)
	updated := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:update", webAPITestMutation("1", "large-profile-update", map[string]any{"id": p.Data.ID, "profile": input}), 200))
	if updated.Data.OverridesRawDocument == nil || len(*updated.Data.OverridesRawDocument) != WebAPIMaxContentBytes {
		t.Fatal("32 MiB profile override was truncated")
	}
	input["label"] = "Large copy"
	copied := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("2", "large-profile-create", map[string]any{"profile": input}), 200))
	if copied.Data.OverridesRawDocument == nil || len(*copied.Data.OverridesRawDocument) != WebAPIMaxContentBytes {
		t.Fatal("create rejected or truncated full metadata")
	}
	input["label"] = "Too large"
	input["overridesRawDocument"] = strings.Repeat("x", WebAPIMaxContentBytes+1)
	r.call("POST", "/api/v1/profiles:create", webAPITestMutation("3", "large-profile-reject", map[string]any{"profile": input}), 413)
	r.call("GET", "/api/v1/cache/operations/no-old-route", nil, 404)
	r.call("POST", "/api/v1/cache/operations/no-old-route:resolve", map[string]any{"intent": "resolve-unknown-result"}, 404)
}

func TestWebAPIURLProfilePurposeBoundBasicMetadataAndRedactedErrors(t *testing.T) {
	r := newWebAPITestRig(t)
	input := webAPITestProfile("Basic")
	input["type"] = "url"
	input["url"] = "https://synthetic-user:synthetic-secret@example.invalid/profile"
	profile := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "basic-profile-metadata", map[string]any{"profile": input}), 200))
	if profile.Data.URL != input["url"] {
		t.Fatal("metadata dropped purpose-bound Basic source")
	}
	input["label"] = "Invalid"
	input["url"] = "file://synthetic-user:synthetic-secret@example.invalid/profile"
	body := r.call("POST", "/api/v1/profiles:create", webAPITestMutation("1", "basic-invalid-scheme", map[string]any{"profile": input}), 400)
	if bytes.Contains(body, []byte("synthetic-user")) || bytes.Contains(body, []byte("synthetic-secret")) {
		t.Fatal("source credentials leaked in validation error")
	}
}

func TestWebAPIComplexRulesUseSharedValidationHTTP(t *testing.T) {
	r := newWebAPITestRig(t)
	input := map[string]any{"profileId": nil, "ruleAction": "AND", "content": "((DOMAIN,one.test),(DOMAIN,two.test))", "ruleTarget": "DIRECT"}
	result := webAPITestData[WebAPIRule](t, r.call("POST", "/api/v1/rules:create", webAPITestMutation("0", "complex-rule-valid", map[string]any{"rule": input}), 200))
	if result.Data.RuleAction != "AND" {
		t.Fatal(result)
	}
	input["content"] = "((DOMAIN,one.test),(DST-PORT,bad))"
	r.call("POST", "/api/v1/rules:create", webAPITestMutation("1", "complex-rule-invalid", map[string]any{"rule": input}), 400)
	input["content"] = "((RULE-SET,missing),(DOMAIN,two.test))"
	r.call("POST", "/api/v1/rules:create", webAPITestMutation("1", "complex-rule-missing-provider", map[string]any{"rule": input}), 409)
}

func TestWebAPIProfileOverridesUseCandidateGraphHTTP(t *testing.T) {
	r := newWebAPITestRig(t)
	p := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "graph-profile-create", map[string]any{"profile": webAPITestProfile("Graph")}), 200))
	source := []byte("proxies:\n  - {name: A, type: socks5, server: 192.0.2.1, port: 1080}\n  - {name: B, type: socks5, server: 192.0.2.2, port: 1080}\nproxy-groups: []\nrules: []\n")
	_, err := r.store.Transaction(context.Background(), WebAPIMutation{"1", "graph-profile-content"}, "test:content", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		return struct{}{}, tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: p.Data.ID}, MediaType: "text/yaml", Bytes: source})
	})
	if err != nil {
		t.Fatal(err)
	}
	input := webAPITestProfile("Graph")
	input["overrides"] = map[string]any{"dialer-proxies": map[string]string{"A": "B"}}
	r.call("POST", "/api/v1/profiles:update", webAPITestMutation("2", "graph-overrides-valid", map[string]any{"id": p.Data.ID, "profile": input}), 200)
	input["overrides"] = map[string]any{"dialer-proxies": map[string]string{"A": "B", "B": "A"}}
	body := r.call("POST", "/api/v1/profiles:update", webAPITestMutation("3", "graph-overrides-cycle", map[string]any{"id": p.Data.ID, "profile": input}), 409)
	if !bytes.Contains(body, []byte("outbound_policy_invalid")) {
		t.Fatal(string(body))
	}
	input["overrides"] = map[string]any{"dns-override-keys": []string{"unknown"}, "dns": map[string]any{}}
	r.call("POST", "/api/v1/profiles:update", webAPITestMutation("3", "graph-overrides-unknown-key", map[string]any{"id": p.Data.ID, "profile": input}), 400)
}

func TestWebAPIComplexRuleVariantsAndSubRuleCatalogHTTP(t *testing.T) {
	r := newWebAPITestRig(t)
	p := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "complex-variants-profile", map[string]any{"profile": webAPITestProfile("Complex")}), 200))
	_, err := r.store.Transaction(context.Background(), WebAPIMutation{"1", "complex-variants-catalog"}, "test:catalog", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		return struct{}{}, tx.SetProfileCatalog(p.Data.ID, WebAPIProfileCatalog{SubRuleNames: []string{"nested"}})
	})
	if err != nil {
		t.Fatal(err)
	}
	rev := "2"
	for i, item := range []struct{ action, content string }{{"OR", "((DOMAIN,a.test),(NETWORK,tcp))"}, {"NOT", "((DOMAIN,a.test))"}, {"SUB_RULE", "(DOMAIN,a.test)"}} {
		rule := map[string]any{"profileId": p.Data.ID, "ruleAction": item.action, "content": item.content}
		if item.action == "SUB_RULE" {
			rule["subRule"] = "nested"
		} else {
			rule["ruleTarget"] = "DIRECT"
		}
		result := webAPITestData[WebAPIRule](t, r.call("POST", "/api/v1/rules:create", webAPITestMutation(rev, fmt.Sprintf("complex-variant-%d", i), map[string]any{"rule": rule}), 200))
		rev = string(result.Revision)
	}
	bad := map[string]any{"profileId": p.Data.ID, "ruleAction": "SUB_RULE", "content": "(DOMAIN,a.test)", "subRule": "absent"}
	r.call("POST", "/api/v1/rules:create", webAPITestMutation(rev, "complex-unknown-subrule", map[string]any{"rule": bad}), 409)
}

func TestWebAPICatalogExactIdentitiesAreNotRuleCSVNames(t *testing.T) {
	r := newWebAPITestRig(t)
	p := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "exact-identity-profile", map[string]any{"profile": webAPITestProfile("Exact")}), 200))
	target := " node, "
	group := " group, "
	quoted := `quoted"\name`
	catalog := WebAPIProfileCatalog{OutboundNames: []string{"A", target, group, quoted}, GroupMembers: map[string][]string{group: {"A", target, quoted}}}
	source, err := json.Marshal(map[string]any{"proxies": []any{map[string]any{"name": "A", "type": "socks5", "server": "192.0.2.1", "port": 1080}, map[string]any{"name": target, "type": "socks5", "server": "192.0.2.2", "port": 1080}, map[string]any{"name": quoted, "type": "socks5", "server": "192.0.2.3", "port": 1080}}, "proxy-groups": []any{map[string]any{"name": group, "type": "select", "proxies": []string{"A", target, quoted}}}, "rules": []string{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.Transaction(context.Background(), WebAPIMutation{"1", "exact-identity-source"}, "test:source", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		if err := tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: p.Data.ID}, MediaType: "application/json", Bytes: source}); err != nil {
			return nil, err
		}
		return struct{}{}, tx.SetProfileCatalog(p.Data.ID, catalog)
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		got, err := tx.ProfileCatalog(p.Data.ID)
		if err == nil && !reflect.DeepEqual(got, catalog) {
			t.Fatal("authority catalog normalized or dropped exact names")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	input := webAPITestProfile("Exact")
	input["currentGroupName"] = group
	input["selectedMap"] = map[string]string{group: target}
	input["unfoldSet"] = []string{group}
	input["overrides"] = map[string]any{"dialer-proxies": map[string]string{"A": target}}
	updated := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:update", webAPITestMutation("2", "exact-identity-dialer", map[string]any{"id": p.Data.ID, "profile": input}), 200))
	if updated.Data.SelectedMap[group] != target {
		t.Fatal("exact selected member changed")
	}
	rule := webAPITestRule(&p.Data.ID, "example.test")
	rule["ruleTarget"] = target
	r.call("POST", "/api/v1/rules:create", webAPITestMutation("3", "exact-identity-csv-reject", map[string]any{"rule": rule}), 400)
	rule["ruleTarget"] = quoted
	r.call("POST", "/api/v1/rules:create", webAPITestMutation("3", "exact-identity-csv-quote", map[string]any{"rule": rule}), 200)
}
