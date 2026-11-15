//go:build webpanel && !android

package core

import (
	"context"
	"testing"
)

func TestWebAPIProfileLabelsPreserveStructuredNamesWithoutRelaxingRuleCSV(t *testing.T) {
	r := newWebAPITestRig(t)
	label := " provider, "
	p := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "structured-profile-label", map[string]any{"profile": webAPITestProfile(label)}), 200))
	if p.Data.Label != label {
		t.Fatal("profile label was normalized")
	}
	_, err := r.store.Transaction(context.Background(), WebAPIMutation{"1", "structured-label-references"}, "test:refs", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		consumer, err := tx.CreateProfile(WebAPIProfileInput{Type: "custom", Label: "Consumer"})
		if err != nil {
			return nil, err
		}
		id, err := tx.AllocateID()
		if err != nil {
			return nil, err
		}
		if err = tx.InsertProxyGroup(WebAPIProxyGroup{ID: id, ProfileID: consumer.ID, Definition: WebAPIProxyGroupDefinition{Name: "Group", Type: "select", Use: []string{label}, UsePosition: map[string]int32{label: 0}}}); err != nil {
			return nil, err
		}
		consumer.Overrides.ProxyProviders = map[string]WebAPIProxyProviderOptions{label: {}}
		return struct{}{}, tx.SaveProfile(consumer)
	})
	if err != nil {
		t.Fatal(err)
	}
	next := " renamed, "
	renamed := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:rename", webAPITestMutation("2", "structured-label-rename", map[string]any{"id": p.Data.ID, "label": next}), 200))
	if renamed.Data.Label != next {
		t.Fatal(renamed)
	}
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		groups, err := tx.ProxyGroups()
		if err != nil || groups[0].Definition.Use[0] != next {
			t.Fatal(groups, err)
		}
		consumer, err := tx.Profile("2")
		if _, ok := consumer.Overrides.ProxyProviders[next]; err != nil || !ok {
			t.Fatal(consumer, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rule := webAPITestRule(&p.Data.ID, "example.test")
	rule["ruleTarget"] = next
	r.call("POST", "/api/v1/rules:create", webAPITestMutation("3", "structured-label-csv-block", map[string]any{"rule": rule}), 400)
	empty := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:rename", webAPITestMutation("3", "structured-label-empty", map[string]any{"id": p.Data.ID, "label": ""}), 200))
	if empty.Data.Label != "" {
		t.Fatal("empty-label semantics lost")
	}
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		groups, err := tx.ProxyGroups()
		if err != nil || groups[0].Definition.Use[0] != string(p.Data.ID) {
			t.Fatal("empty label did not move provider identity to fallback", groups, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
