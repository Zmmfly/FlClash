//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestWebAPIUnavailableCatalogIsNotEmptyRoutingAuthority(t *testing.T) {
	for _, reason := range []string{WebAPICatalogUnreadableOverrides, WebAPICatalogDependencyQuarantined, WebAPICatalogContentUnavailable} {
		t.Run(reason, func(t *testing.T) {
			r := newWebAPITestRig(t)
			p := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "unavailable-profile-create", map[string]any{"profile": webAPITestProfile("Unavailable")}), 200))
			_, err := r.store.Transaction(context.Background(), WebAPIMutation{"1", "unavailable-catalog-store"}, "test:catalog", reason, func(tx *WebAPIWriteTx) (any, error) {
				return struct{}{}, tx.SetProfileCatalog(p.Data.ID, WebAPIProfileCatalog{UnavailableReason: reason})
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
				state, err := tx.ProfileCatalogState(p.Data.ID)
				if err != nil || state.UnavailableReason != reason {
					t.Fatal(state, err)
				}
				state, err = tx.ProfileCatalog(p.Data.ID)
				webAPITestErrorCode(t, err, "catalog_unavailable")
				if state.UnavailableReason != reason {
					t.Fatal("unavailability evidence was lost")
				}
				webAPITestErrorCode(t, tx.RequireProfileCatalogAvailable(p.Data.ID), "catalog_unavailable")
				_, err = WebAPIRuntimeSetupSnapshot(tx, p.Data.ID)
				webAPITestErrorCode(t, err, "catalog_unavailable")
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			for i, owner := range []*WebAPIID{nil, &p.Data.ID} {
				body := r.call("POST", "/api/v1/rules:create", webAPITestMutation("2", fmt.Sprintf("unavailable-rule-%d", i), map[string]any{"rule": webAPITestRule(owner, "example.test")}), 409)
				var problem struct {
					Error WebAPIError `json:"error"`
				}
				if err := json.Unmarshal(body, &problem); err != nil || problem.Error.Code != "catalog_unavailable" || problem.Error.Details["reason"] != reason {
					t.Fatal(string(body), err)
				}
			}
			input := webAPITestProfile("Unavailable")
			input["currentGroupName"] = "DIRECT"
			body := r.call("POST", "/api/v1/profiles:update", webAPITestMutation("2", "unavailable-selection", map[string]any{"id": p.Data.ID, "profile": input}), 409)
			if len(body) == 0 {
				t.Fatal("missing selection failure")
			}
			_, err = r.store.Transaction(context.Background(), WebAPIMutation{"2", "unavailable-catalog-repair"}, "test:catalog", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
				return struct{}{}, tx.SetProfileCatalog(p.Data.ID, WebAPIProfileCatalog{OutboundNames: []string{"DIRECT"}})
			})
			if err != nil {
				t.Fatal(err)
			}
			r.call("POST", "/api/v1/rules:create", webAPITestMutation("3", "available-rule-create", map[string]any{"rule": webAPITestRule(&p.Data.ID, "example.test")}), 200)
		})
	}
}

func TestWebAPIUnavailableCatalogReasonAndEntryStateAreStrict(t *testing.T) {
	r := newWebAPITestRig(t)
	p := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "catalog-state-profile", map[string]any{"profile": webAPITestProfile("State")}), 200))
	for i, catalog := range []WebAPIProfileCatalog{{UnavailableReason: "anything"}, {UnavailableReason: WebAPICatalogContentUnavailable, OutboundNames: []string{"DIRECT"}}, {UnavailableReason: WebAPICatalogDependencyQuarantined, GroupMembers: map[string][]string{"G": {"DIRECT"}}}} {
		_, err := r.store.Transaction(context.Background(), WebAPIMutation{"1", fmt.Sprintf("invalid-catalog-state-%d", i)}, "test:state", catalog, func(tx *WebAPIWriteTx) (any, error) { return struct{}{}, tx.SetProfileCatalog(p.Data.ID, catalog) })
		var problem *WebAPIError
		if !errors.As(err, &problem) || problem.Code != "invalid_catalog_state" {
			t.Fatal(err)
		}
	}
	meta := webAPITestData[WebAPIStoreMeta](t, r.call("GET", "/api/v1/meta", nil, 200))
	if meta.Revision != "1" {
		t.Fatal(meta)
	}
}

func TestWebAPICustomUnavailableCatalogBlocksRuntimeSnapshot(t *testing.T) {
	r := newWebAPITestRig(t)
	_, err := r.store.Transaction(context.Background(), WebAPIMutation{"0", "custom-quarantine-create"}, "test:state", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		p, err := tx.CreateProfile(WebAPIProfileInput{Type: "custom", Label: "Quarantined"})
		if err != nil {
			return nil, err
		}
		return struct{}{}, tx.SetProfileCatalog(p.ID, WebAPIProfileCatalog{UnavailableReason: WebAPICatalogDependencyQuarantined})
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		_, err := WebAPIRuntimeSetupSnapshot(tx, "1")
		webAPITestErrorCode(t, err, "catalog_unavailable")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
