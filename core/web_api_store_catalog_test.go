//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"

	bolt "github.com/metacubex/bbolt"
)

func TestWebAPICatalogScopedReadsIgnoreUnrelatedLargeUnreadableData(t *testing.T) {
	r := newWebAPITestRig(t)
	if err := r.store.db.Update(func(tx *bolt.Tx) error {
		valid := WebAPIProfile{ID: "1", WebAPIProfileInput: webAPINormalizeProfile(WebAPIProfileInput{Type: "custom", Label: "Owner"})}
		if err := webAPIPut(tx, "profiles", "1", valid); err != nil {
			return err
		}
		raw := strings.Repeat("x", WebAPIMaxContentBytes)
		other := WebAPIProfile{ID: "2", WebAPIProfileInput: WebAPIProfileInput{Type: "file", Label: "Unrelated", OverridesRawDocument: &raw}}
		if err := webAPIPut(tx, "profiles", "2", other); err != nil {
			return err
		}
		for _, p := range []WebAPIProfile{{ID: "3", WebAPIProfileInput: WebAPIProfileInput{Type: "url", URL: "https://provider.example.invalid/sub"}}, {ID: "4", WebAPIProfileInput: WebAPIProfileInput{Type: "file"}}} {
			if err := webAPIPut(tx, "profiles", string(p.ID), p); err != nil {
				return err
			}
		}
		if err := webAPIPut(tx, "proxy-groups", "5", WebAPIProxyGroup{ID: "5", ProfileID: "1", Definition: WebAPIProxyGroupDefinition{Name: "G", Type: "select", Proxies: []string{" node, "}}}); err != nil {
			return err
		}
		if err := webAPIPut(tx, "custom-proxies", "6", WebAPICustomProxy{ID: "6", Definition: WebAPIProxyDefinition{"name": []byte(`" node, "`), "type": []byte(`"socks5"`)}}); err != nil {
			return err
		}
		if err := webAPIPut(tx, "rule-providers", "7", WebAPIClashProvider{ID: "7", Label: "Rules", Behavior: "classical", Format: "yaml"}); err != nil {
			return err
		}
		content, target := "test.example", "DIRECT"
		owner := WebAPIID("1")
		if err := webAPIPut(tx, "rules", "8", WebAPIRule{ID: "8", WebAPIRuleInput: WebAPIRuleInput{ProfileID: &owner, RuleAction: "DOMAIN", Content: &content, RuleTarget: &target}}); err != nil {
			return err
		}
		return webAPIPut(tx, "proxy-dialers", "1:6", WebAPIProxyDialer{ProfileID: "1", ProxyID: "6", Target: "DIRECT"})
	}); err != nil {
		t.Fatal(err)
	}
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		budget := NewWebAPICatalogReadBudget(8192)
		if _, err := tx.CatalogProfile("1", budget); err != nil {
			return err
		}
		profiles, err := tx.CatalogProviderProfilesByNames([]string{"provider.example.invalid", "4"}, budget)
		if err != nil || len(profiles) != 2 {
			t.Fatal(profiles, err)
		}
		groups, err := tx.CatalogProxyGroups("1", budget)
		if err != nil || len(groups) != 1 {
			t.Fatal(groups, err)
		}
		proxies, err := tx.CatalogCustomProxiesByNames([]string{" node, "}, budget)
		if err != nil || len(proxies) != 1 {
			t.Fatal(proxies, err)
		}
		providers, err := tx.CatalogClashProvidersByNames([]string{"Rules"}, budget)
		if err != nil || len(providers) != 1 {
			t.Fatal(providers, err)
		}
		rules, err := tx.CatalogRules("1", budget)
		if err != nil || len(rules) != 1 {
			t.Fatal(rules, err)
		}
		dialers, err := tx.CatalogProxyDialers("1", budget)
		if err != nil || len(dialers) != 1 {
			t.Fatal(dialers, err)
		}
		remaining := budget.remaining
		if _, err := tx.CatalogCustomProxy("6", budget); err != nil || budget.remaining != remaining {
			t.Fatal("same resource charged twice", err)
		}
		if _, err := tx.CatalogProfile("2", budget); err == nil {
			t.Fatal("large selected dependency bypassed scoped budget")
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		if _, err := tx.CatalogProviderProfilesByNames([]string{"provider.example.invalid"}, NewWebAPICatalogReadBudget(8192)); err != nil {
			return err
		}
		runtime.ReadMemStats(&after)
		if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 2<<20 {
			t.Fatalf("unrelated raw override caused %d allocated bytes", allocated)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebAPICatalogReadBudgetDeduplicatesAndBindsSnapshot(t *testing.T) {
	r := newWebAPITestRig(t)
	_, err := r.store.Transaction(context.Background(), WebAPIMutation{"0", "catalog-budget-seed"}, "test:budget", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		p, err := tx.CreateProfile(WebAPIProfileInput{Type: "file", Label: "One"})
		if err != nil {
			return nil, err
		}
		return struct{}{}, tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: p.ID}, MediaType: "text/plain", Bytes: []byte("small")})
	})
	if err != nil {
		t.Fatal(err)
	}
	var budget *WebAPICatalogReadBudget
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		raw := tx.tx.Bucket([]byte("profiles")).Get([]byte("1"))
		budget = NewWebAPICatalogReadBudget(int64(len(raw) + 1))
		if _, err := tx.CatalogProfile("1", budget); err != nil {
			return err
		}
		if budget.remaining != 0 {
			t.Fatal(budget.remaining)
		}
		if _, err := tx.CatalogProfile("1", budget); err != nil {
			return err
		}
		_, err := tx.CatalogContent(WebAPIContentKey{Kind: WebAPIContentProfile, ID: "1"}, budget)
		webAPITestErrorCode(t, err, "catalog_budget_exceeded")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		_, err := tx.CatalogProfile("1", budget)
		webAPITestErrorCode(t, err, "invalid_catalog_budget")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebAPICatalogContentBudgetRejectsBeforeDecodedAllocation(t *testing.T) {
	r := newWebAPITestRig(t)
	_, err := r.store.Transaction(context.Background(), WebAPIMutation{"0", "catalog-large-source"}, "test:budget", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		p, err := tx.CreateProfile(WebAPIProfileInput{Type: "file"})
		if err != nil {
			return nil, err
		}
		return struct{}{}, tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: p.ID}, MediaType: "application/octet-stream", Bytes: bytes.Repeat([]byte{'x'}, WebAPIMaxContentBytes)})
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err := tx.CatalogContent(WebAPIContentKey{Kind: WebAPIContentProfile, ID: "1"}, NewWebAPICatalogReadBudget(1<<20))
		webAPITestErrorCode(t, err, "catalog_budget_exceeded")
		runtime.ReadMemStats(&after)
		if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
			t.Fatalf("rejected source allocated %d bytes", allocated)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebAPICatalogNamedLookupsRejectOnlyRelevantAmbiguity(t *testing.T) {
	r := newWebAPITestRig(t)
	if err := r.store.db.Update(func(tx *bolt.Tx) error {
		for _, p := range []WebAPIProfile{{ID: "1", WebAPIProfileInput: WebAPIProfileInput{Type: "file", Label: "same.example"}}, {ID: "2", WebAPIProfileInput: WebAPIProfileInput{Type: "url", URL: "https://same.example/sub"}}, {ID: "3", WebAPIProfileInput: WebAPIProfileInput{Type: "file", Label: "Unique"}}} {
			if err := webAPIPut(tx, "profiles", string(p.ID), p); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		profiles, err := tx.CatalogProviderProfilesByNames([]string{"Unique"}, NewWebAPICatalogReadBudget(8192))
		if err != nil || len(profiles) != 1 {
			t.Fatal(profiles, err)
		}
		_, err = tx.CatalogProviderProfilesByNames([]string{"same.example"}, NewWebAPICatalogReadBudget(8192))
		webAPITestErrorCode(t, err, "ambiguous_catalog_identity")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebAPICatalogStateReadsUseSameDependencyBudget(t *testing.T) {
	r := newWebAPITestRig(t)
	_, err := r.store.Transaction(context.Background(), WebAPIMutation{"0", "scoped-state-seed"}, "test:state", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		p, err := tx.CreateProfile(WebAPIProfileInput{Type: "file"})
		if err != nil {
			return nil, err
		}
		return struct{}{}, tx.SetProfileCatalog(p.ID, WebAPIProfileCatalog{UnavailableReason: WebAPICatalogContentUnavailable})
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		raw := tx.tx.Bucket([]byte("profile-catalogs")).Get([]byte("1"))
		budget := NewWebAPICatalogReadBudget(int64(len(raw) + 1))
		state, err := tx.CatalogProfileCatalogState("1", budget)
		if err != nil || state.UnavailableReason != WebAPICatalogContentUnavailable || budget.remaining != 0 {
			t.Fatal(state, err, budget.remaining)
		}
		if _, err := tx.CatalogProfileCatalogState("1", budget); err != nil {
			t.Fatal("state double-charged", err)
		}
		_, err = tx.CatalogProfileCatalogState("1", NewWebAPICatalogReadBudget(int64(len(raw))))
		webAPITestErrorCode(t, err, "catalog_budget_exceeded")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebAPICustomRuleSetUsesExactGlobalProviderNotRepairProjection(t *testing.T) {
	r := newWebAPITestRig(t)
	_, err := r.store.Transaction(context.Background(), WebAPIMutation{"0", "new-ruleset-seed"}, "test:resources", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		p, err := tx.CreateProfile(WebAPIProfileInput{Type: "custom", Label: "Custom"})
		if err != nil {
			return nil, err
		}
		if err = tx.SetProfileCatalog(p.ID, WebAPIProfileCatalog{OutboundNames: []string{"DIRECT"}}); err != nil {
			return nil, err
		}
		id, err := tx.AllocateID()
		if err != nil {
			return nil, err
		}
		return struct{}{}, tx.InsertClashProvider(WebAPIClashProvider{ID: id, Label: "NewRuleSet", Behavior: "classical", Format: "yaml"})
	})
	if err != nil {
		t.Fatal(err)
	}
	rule := map[string]any{"profileId": "1", "ruleAction": "RULE_SET", "ruleProvider": "NewRuleSet", "ruleTarget": "DIRECT"}
	r.call("POST", "/api/v1/rules:create", webAPITestMutation("1", "new-ruleset-after-repair", map[string]any{"rule": rule}), 200)
	rule["ruleProvider"] = "newruleset"
	r.call("POST", "/api/v1/rules:create", webAPITestMutation("2", "new-ruleset-case-reject", map[string]any{"rule": rule}), 409)
	rule["ruleProvider"] = " NewRuleSet "
	r.call("POST", "/api/v1/rules:create", webAPITestMutation("2", "new-ruleset-csv-reject", map[string]any{"rule": rule}), 400)
}
