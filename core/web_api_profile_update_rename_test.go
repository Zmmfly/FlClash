//go:build webpanel && !android

package core

import (
	"context"
	"testing"
)

func TestWebAPIProfileUpdateRenamesWithReferences(t *testing.T) {
	r := newWebAPITestRig(t)
	p := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "update-rename-create", map[string]any{"profile": webAPITestProfile("Old")}), 200))
	_, err := r.store.Transaction(context.Background(), WebAPIMutation{"1", "update-rename-refs"}, "test:refs", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		consumer, err := tx.CreateProfile(WebAPIProfileInput{Type: "custom", Label: "Consumer"})
		if err != nil {
			return nil, err
		}
		id, err := tx.AllocateID()
		if err != nil {
			return nil, err
		}
		return struct{}{}, tx.InsertProxyGroup(WebAPIProxyGroup{ID: id, ProfileID: consumer.ID, Definition: WebAPIProxyGroupDefinition{Name: "Group", Type: "select", Use: []string{"Old"}}})
	})
	if err != nil {
		t.Fatal(err)
	}
	r.call("POST", "/api/v1/profiles:update", webAPITestMutation("2", "update-rename-duplicate", map[string]any{"id": p.Data.ID, "profile": webAPITestProfile("Consumer")}), 409)
	updated := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:update", webAPITestMutation("2", "update-rename", map[string]any{"id": p.Data.ID, "profile": webAPITestProfile("New")}), 200))
	if updated.Data.Label != "New" {
		t.Fatal(updated)
	}
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		groups, err := tx.ProxyGroups()
		if err != nil || groups[0].Definition.Use[0] != "New" {
			t.Fatal("rename through update left a stale provider reference", groups, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
