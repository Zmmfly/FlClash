//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestWebImageHTTPOwnedCommittedGroupAndIconSetNeedNoPreviewAndDeletionRevokesCache(t *testing.T) {
	png := webImageTestPNG(t)
	f := newWebUtilityHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) { w.Write(png) })
	profileCommand := WebAPIProfileCreate{WebAPIMutation: WebAPIMutation{ExpectedRevision: "0", OperationID: "icon-owned-profile"}, Profile: WebAPIProfileInput{Type: "custom", Label: "icons"}}
	status, body := f.request(t, f.clients[0], "POST", "/api/v1/profiles:create", profileCommand)
	if status != 200 {
		t.Fatalf("create profile %d: %s", status, body)
	}
	var profile WebAPIEnvelope[WebAPIProfile]
	json.Unmarshal(body, &profile)
	groupURL, setURL := "http://icons.test/committed-group.png", "http://icons.test/committed-set.png"
	groupCommand := webAPIGroupCreateCommand{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "icon-owned-group"}, ProfileID: profile.Data.ID, Group: WebAPIProxyGroupInput{Definition: WebAPIProxyGroupDefinition{Name: "G", Type: "select", Proxies: []string{"DIRECT"}, Icon: &groupURL}, MemberLayout: []string{}}}
	status, body = f.request(t, f.clients[0], "POST", "/api/v1/proxy-groups:create", groupCommand)
	if status != 200 {
		t.Fatalf("create group %d: %s", status, body)
	}
	var group WebAPIEnvelope[WebAPIProxyGroup]
	json.Unmarshal(body, &group)
	setCommand := webAPIIconSetCreateCommand{WebAPIMutation: WebAPIMutation{ExpectedRevision: "2", OperationID: "icon-owned-set"}, IconSet: webAPIIconSetInput{Name: "Fixture", Icons: []WebAPIIconSetIcon{{Name: "icon", URL: setURL}}}}
	status, body = f.request(t, f.clients[0], "POST", "/api/v1/icon-sets:create", setCommand)
	if status != 200 {
		t.Fatalf("create icon set %d: %s", status, body)
	}
	var set WebAPIEnvelope[WebAPIIconSet]
	json.Unmarshal(body, &set)
	for _, url := range []string{groupURL, setURL} {
		if status, body := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:load", WebAPIImageLoad{URL: url}); status != 200 {
			t.Fatalf("committed owned image %d: %s", status, body)
		}
	}
	cacheRevision, err := f.store.CacheView(context.Background(), func(tx *WebAPICacheTx) error {
		rows, err := tx.IconRecords()
		if len(rows) != 0 {
			t.Error("image load manufactured icon history")
		}
		return err
	})
	if err != nil || cacheRevision != "0" {
		t.Fatalf("byte reads changed cache records %s %v", cacheRevision, err)
	}
	deleteGroup := webAPIGroupIDsCommand{WebAPIMutation: WebAPIMutation{ExpectedRevision: "3", OperationID: "delete-owned-icon-group"}, ProfileID: profile.Data.ID, IDs: []WebAPIID{group.Data.ID}}
	if status, body := f.request(t, f.clients[0], "POST", "/api/v1/proxy-groups:delete", deleteGroup); status != 200 {
		t.Fatalf("delete group %d: %s", status, body)
	}
	deleteSet := webAPIIconSetIDsCommand{WebAPIMutation: WebAPIMutation{ExpectedRevision: "4", OperationID: "delete-owned-icon-set"}, IDs: []WebAPIID{set.Data.ID}}
	if status, body := f.request(t, f.clients[0], "POST", "/api/v1/icon-sets:delete", deleteSet); status != 200 {
		t.Fatalf("delete set %d: %s", status, body)
	}
	before := f.totalFetches()
	for _, url := range []string{groupURL, setURL} {
		if status, body := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:load", WebAPIImageLoad{URL: url}); status != 404 {
			t.Fatalf("orphan bytes remained readable %d: %s", status, body)
		}
	}
	if f.totalFetches() != before {
		t.Fatal("deleted icon resource caused another network fetch")
	}
}
