//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	bolt "github.com/metacubex/bbolt"
	"strings"
	"testing"
)

func webAPISeedQuarantinedRepairProfiles(t *testing.T, r *webAPITestRig, source []byte) {
	t.Helper()
	_, err := r.store.Transaction(context.Background(), WebAPIMutation{"0", "repair-quarantine-seed"}, "test:repair", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		for i, label := range []string{"Repair", "Still quarantined"} {
			raw := "unreadable legacy overrides"
			profile, err := tx.CreateProfile(WebAPIProfileInput{Type: "file", Label: label, OverridesRawDocument: &raw})
			if err != nil {
				return nil, err
			}
			if i == 0 && source != nil {
				if err := tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: profile.ID}, MediaType: "text/yaml", Bytes: source}); err != nil {
					return nil, err
				}
			}
			if err := tx.SetProfileCatalog(profile.ID, WebAPIProfileCatalog{UnavailableReason: WebAPICatalogUnreadableOverrides}); err != nil {
				return nil, err
			}
		}
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebAPIExplicitDialerRepairRebuildsOnlyValidatedAuthority(t *testing.T) {
	r := newWebAPITestRig(t)
	source := []byte("proxies:\n  - {name: A, type: socks5, server: 192.0.2.1, port: 1080}\nproxy-groups: []\nrules: []\n")
	webAPISeedQuarantinedRepairProfiles(t, r, source)
	repaired := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:reset-dialers", webAPITestMutation("1", "repair-explicit-success", map[string]any{"id": "1"}), 200))
	if repaired.Data.OverridesRawDocument != nil || webAPIUnreadable(repaired.Data.WebAPIProfileInput) {
		t.Fatal("explicit repair did not clear corrupt override state")
	}
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		catalog, err := tx.ProfileCatalog("1")
		if err != nil || catalog.UnavailableReason != "" || !webAPIContains(catalog.OutboundNames, "A") {
			t.Fatal("repair did not produce revalidated authority", catalog, err)
		}
		other, err := tx.ProfileCatalogState("2")
		if err != nil || other.UnavailableReason != WebAPICatalogUnreadableOverrides {
			t.Fatal("unrelated quarantine was cleared", other, err)
		}
		_, err = WebAPIRuntimeSetupSnapshot(tx, "1")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r.call("POST", "/api/v1/rules:create", webAPITestMutation("2", "repair-rule-after-revalidation", map[string]any{"rule": webAPITestRule(webAPIPointerID("1"), "example.test")}), 200)
}

func webAPIPointerID(id WebAPIID) *WebAPIID { return &id }

func TestWebAPIExplicitDialerRepairCannotBlessMissingContent(t *testing.T) {
	r := newWebAPITestRig(t)
	webAPISeedQuarantinedRepairProfiles(t, r, nil)
	r.call("POST", "/api/v1/profiles:reset-dialers", webAPITestMutation("1", "repair-missing-content", map[string]any{"id": "1"}), 200)
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		state, err := tx.ProfileCatalogState("1")
		if err != nil || state.UnavailableReason != WebAPICatalogContentUnavailable {
			t.Fatal(state, err)
		}
		_, err = tx.ProfileCatalog("1")
		webAPITestErrorCode(t, err, "catalog_unavailable")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebAPIExplicitDialerRepairRejectsInvalidSourceGraphsAtomically(t *testing.T) {
	cases := map[string]string{
		"cycle":   "proxies:\n  - {name: A, type: socks5, server: 192.0.2.1, port: 1080, dialer-proxy: B}\n  - {name: B, type: socks5, server: 192.0.2.2, port: 1080, dialer-proxy: A}\nproxy-groups: []\nrules: []\n",
		"missing": "proxies:\n  - {name: A, type: socks5, server: 192.0.2.1, port: 1080, dialer-proxy: Missing}\nproxy-groups: []\nrules: []\n",
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			r := newWebAPITestRig(t)
			webAPISeedQuarantinedRepairProfiles(t, r, []byte(source))
			body := r.call("POST", "/api/v1/profiles:reset-dialers", webAPITestMutation("1", "repair-invalid-graph", map[string]any{"id": "1"}), 409)
			var result struct {
				Error WebAPIError `json:"error"`
			}
			if err := json.Unmarshal(body, &result); err != nil || result.Error.Code != "catalog_unavailable" {
				t.Fatal(string(body), err)
			}
			_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
				profile, err := tx.Profile("1")
				if err != nil || profile.OverridesRawDocument == nil {
					t.Fatal("failed repair partially committed", profile, err)
				}
				state, err := tx.ProfileCatalogState("1")
				if err != nil || state.UnavailableReason != WebAPICatalogUnreadableOverrides {
					t.Fatal(state, err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			r.call("GET", "/api/v1/operations/repair-invalid-graph", nil, 404)
		})
	}
}

func TestWebAPIExplicitRepairIgnoresUnrelatedMalformedAndLargeMetadata(t *testing.T) {
	r := newWebAPITestRig(t)
	source := []byte("proxies: [{name: A, type: socks5, server: 192.0.2.1, port: 1080}]\nproxy-groups: []\nrules: []\n")
	webAPISeedQuarantinedRepairProfiles(t, r, source)
	if err := r.store.db.Update(func(tx *bolt.Tx) error {
		return webAPIPut(tx, "profiles", "2", map[string]any{"id": "2", "type": "file", "label": "Unrelated corrupt", "autoUpdateDurationMillis": "invalid-number", "overridesRawDocument": strings.Repeat("x", 32<<20)})
	}); err != nil {
		t.Fatal(err)
	}
	before := []byte(nil)
	if err := r.store.db.View(func(tx *bolt.Tx) error {
		before = append(before, tx.Bucket([]byte("profiles")).Get([]byte("2"))...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.call("POST", "/api/v1/profiles:reset-dialers", webAPITestMutation("1", "scoped-repair-with-corrupt-peer", map[string]any{"id": "1"}), 200)
	if err := r.store.db.View(func(tx *bolt.Tx) error {
		if string(before) != string(tx.Bucket([]byte("profiles")).Get([]byte("2"))) {
			t.Fatal("repair changed unrelated malformed metadata")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		catalog, err := tx.ProfileCatalog("1")
		if err != nil || !webAPIContains(catalog.OutboundNames, "A") {
			t.Fatal(catalog, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.Transaction(context.Background(), WebAPIMutation{"2", "ordinary-cannot-skip-integrity"}, "not-a-repair", WebAPIProfileResetDialers{WebAPIMutation: WebAPIMutation{"2", "ordinary-cannot-skip-integrity"}, ID: "1"}, func(tx *WebAPIWriteTx) (any, error) { return tx.ResetProfileDialers("1") })
	if err == nil {
		t.Fatal("ordinary command acquired repair-only integrity authority")
	}
	_, err = r.store.Receipt(context.Background(), "ordinary-cannot-skip-integrity")
	webAPITestErrorCode(t, err, "not_found")
}

func TestWebAPIExplicitRepairStillRejectsOwnScriptReference(t *testing.T) {
	r := newWebAPITestRig(t)
	webAPISeedQuarantinedRepairProfiles(t, r, []byte("proxies: []\nproxy-groups: []\nrules: []\n"))
	if err := r.store.db.Update(func(tx *bolt.Tx) error {
		var profile WebAPIProfile
		if err := webAPIGet(tx, "profiles", "1", &profile); err != nil {
			return err
		}
		profile.ScriptID = webAPIPointerID("999")
		return webAPIPut(tx, "profiles", "1", profile)
	}); err != nil {
		t.Fatal(err)
	}
	body := r.call("POST", "/api/v1/profiles:reset-dialers", webAPITestMutation("1", "own-script-reference-repair", map[string]any{"id": "1"}), 409)
	var result struct {
		Error WebAPIError `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Error.Code != "invalid_reference" {
		t.Fatal(string(body), err)
	}
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		profile, err := tx.Profile("1")
		if err != nil || profile.OverridesRawDocument == nil {
			t.Fatal("failed scoped validation partially repaired", profile, err)
		}
		catalog, err := tx.ProfileCatalogState("1")
		if err != nil || catalog.UnavailableReason != WebAPICatalogUnreadableOverrides {
			t.Fatal(catalog, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
