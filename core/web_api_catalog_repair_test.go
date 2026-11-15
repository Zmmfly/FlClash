//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	bolt "github.com/metacubex/bbolt"
)

func catalogRepairFixture(t *testing.T) (*catalogTestRig, WebAPIID, WebAPIID, WebAPIID) {
	t.Helper()
	r := newCatalogTestRig(t, nil)
	owner := r.customProfile("RepairOwner").ID
	proxy := catalogData[WebAPICustomProxy](t, r.call("POST", "/api/v1/custom-proxies:create", catalogMutation(r.revision(), "repair-fixture-proxy", map[string]any{"proxy": catalogProxyInput("ActualNode")}), 200)).Data.ID
	group := catalogData[WebAPIProxyGroup](t, r.call("POST", "/api/v1/proxy-groups:create", catalogMutation(r.revision(), "repair-fixture-group", map[string]any{"profileId": owner, "group": catalogGroupInput("ActualGroup", "ActualNode")}), 200)).Data.ID
	r.call("POST", "/api/v1/rules:create", catalogMutation(r.revision(), "repair-fixture-rule", map[string]any{"rule": map[string]any{"profileId": owner, "ruleAction": "DOMAIN", "content": "unit.invalid", "ruleTarget": "ActualNode"}}), 200)
	return r, owner, proxy, group
}

func catalogMarkRepairUnreadable(r *catalogTestRig, owner WebAPIID) {
	r.t.Helper()
	r.seed("repair-owner-quarantine", func(tx *WebAPIWriteTx) error {
		profile, err := tx.Profile(owner)
		if err != nil {
			return err
		}
		raw := "original unreadable overrides, not empty routing policy"
		profile.Overrides, profile.OverridesRawDocument = nil, &raw
		if err := tx.SaveProfile(profile); err != nil {
			return err
		}
		return tx.SetProfileCatalog(owner, WebAPIProfileCatalog{UnavailableReason: WebAPICatalogUnreadableOverrides})
	})
}

func catalogRepairRawRow(r *catalogTestRig, bucket, key string) []byte {
	r.t.Helper()
	var data []byte
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		data = append([]byte{}, tx.tx.Bucket([]byte(bucket)).Get([]byte(key))...)
		return nil
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return data
}

func catalogRepairAssertActualCatalog(r *catalogTestRig, owner WebAPIID) {
	r.t.Helper()
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		catalog, err := tx.ProfileCatalog(owner)
		if err != nil {
			return err
		}
		if catalog.UnavailableReason != "" || !webAPIContains(catalog.OutboundNames, "ActualGroup") || !webAPIContains(catalog.OutboundNames, "ActualNode") {
			r.t.Fatalf("repair did not rebuild the owner's actual catalog: %+v", catalog)
		}
		members, ready, err := WebAPICatalogGroupMemberNames(tx, owner, "ActualGroup")
		if err != nil || !ready || !webAPIContains(members, "ActualNode") || webAPIContains(members, "DIRECT") {
			r.t.Fatalf("repair substituted routing rather than rebuilding: %v %v %v", members, ready, err)
		}
		return nil
	})
	if err != nil {
		r.t.Fatal(err)
	}
}

func TestWebAPICatalogScopedRepairIgnoresUnrelatedDamagedRowsHTTP(t *testing.T) {
	r, owner, _, _ := catalogRepairFixture(t)
	other := r.customProfile("Unrelated").ID
	var otherGroup, otherRule, otherProxy WebAPIID
	r.seed("repair-unrelated-fixtures", func(tx *WebAPIWriteTx) error {
		var err error
		otherGroup, err = tx.AllocateID()
		if err != nil {
			return err
		}
		if err := tx.InsertProxyGroup(WebAPIProxyGroup{ID: otherGroup, ProfileID: other, Definition: WebAPIProxyGroupDefinition{Name: "OtherGroup", Type: "select", Proxies: []string{"DIRECT"}}}); err != nil {
			return err
		}
		otherRule, err = tx.AllocateID()
		if err != nil {
			return err
		}
		target, content := "DIRECT", "other.invalid"
		if err := tx.InsertRule(WebAPIRule{ID: otherRule, WebAPIRuleInput: WebAPIRuleInput{ProfileID: &other, RuleAction: "DOMAIN", Content: &content, RuleTarget: &target}}); err != nil {
			return err
		}
		otherProxy, err = tx.AllocateID()
		if err != nil {
			return err
		}
		input := catalogProxyInput("UnrelatedProxy")
		data, _ := json.Marshal(input["definition"])
		var definition WebAPIProxyDefinition
		_ = json.Unmarshal(data, &definition)
		if err := tx.InsertCustomProxy(WebAPICustomProxy{ID: otherProxy, Definition: definition}); err != nil {
			return err
		}
		if err := tx.SetProfileCatalog(other, WebAPIProfileCatalog{UnavailableReason: WebAPICatalogUnreadableOverrides}); err != nil {
			return err
		}

		return nil
	})
	catalogMarkRepairUnreadable(r, owner)
	if err := r.store.db.Update(func(tx *bolt.Tx) error {
		for _, change := range []struct {
			bucket string
			id     WebAPIID
			field  string
			value  any
		}{{"profiles", other, "autoUpdateDurationMillis", "broken unrelated numeric field"}, {"proxy-groups", otherGroup, "definition", "broken unrelated group"}, {"rules", otherRule, "noResolve", "broken unrelated flag"}, {"custom-proxies", otherProxy, "emptyOptions", 7}} {
			var object map[string]any
			if err := json.Unmarshal(tx.Bucket([]byte(change.bucket)).Get([]byte(change.id)), &object); err != nil {
				return err
			}
			object[change.field] = change.value
			encoded, _ := json.Marshal(object)
			if err := tx.Bucket([]byte(change.bucket)).Put([]byte(change.id), encoded); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, key := range []struct {
		bucket string
		id     WebAPIID
	}{{"profiles", other}, {"proxy-groups", otherGroup}, {"rules", otherRule}, {"custom-proxies", otherProxy}, {"profile-catalogs", other}} {
		before[key.bucket+":"+string(key.id)] = catalogRepairRawRow(r, key.bucket, string(key.id))
	}
	command := catalogMutation(r.revision(), "repair-owner-scoped-http", map[string]any{"id": owner})
	body := r.call("POST", "/api/v1/profiles:reset-dialers", command, 200)
	profile := catalogData[WebAPIProfile](t, body).Data
	if profile.OverridesRawDocument != nil {
		t.Fatal("explicit repair retained unreadable state")
	}
	catalogRepairAssertActualCatalog(r, owner)
	for _, key := range []struct {
		bucket string
		id     WebAPIID
	}{{"profiles", other}, {"proxy-groups", otherGroup}, {"rules", otherRule}, {"custom-proxies", otherProxy}, {"profile-catalogs", other}} {
		if !bytes.Equal(before[key.bucket+":"+string(key.id)], catalogRepairRawRow(r, key.bucket, string(key.id))) {
			t.Fatalf("repair changed unrelated %s:%s", key.bucket, key.id)
		}
	}
	if replay := r.call("POST", "/api/v1/profiles:reset-dialers", command, 200); !bytes.Equal(body, replay) {
		t.Fatal("repair replay changed the committed result")
	}
}

func TestWebAPICatalogScopedRepairOwnBadReferenceRollsBack(t *testing.T) {
	r, owner, _, group := catalogRepairFixture(t)
	catalogMarkRepairUnreadable(r, owner)
	r.seed("repair-invalid-own-reference", func(tx *WebAPIWriteTx) error {
		item, err := tx.ProxyGroup(group)
		if err != nil {
			return err
		}
		item.Definition.Proxies = []string{"AbsentNode"}
		return tx.SaveProxyGroup(item)
	})
	beforeProfile, beforeCatalog := catalogRepairRawRow(r, "profiles", string(owner)), catalogRepairRawRow(r, "profile-catalogs", string(owner))
	revision := r.revision()
	r.call("POST", "/api/v1/profiles:reset-dialers", catalogMutation(revision, "repair-invalid-own-candidate", map[string]any{"id": owner}), 409)
	if r.revision() != revision || !bytes.Equal(beforeProfile, catalogRepairRawRow(r, "profiles", string(owner))) || !bytes.Equal(beforeCatalog, catalogRepairRawRow(r, "profile-catalogs", string(owner))) {
		t.Fatal("failed repair erased raw policy or its unavailable marker")
	}
}

func TestWebAPICatalogScopedRepairHonorsActualDependencyMarker(t *testing.T) {
	r, owner, _, group := catalogRepairFixture(t)
	var provider WebAPIID
	r.seed("repair-provider-fixture", func(tx *WebAPIWriteTx) error {
		var err error
		provider, err = tx.AllocateID()
		if err != nil {
			return err
		}
		if err := tx.InsertProfile(WebAPIProfile{ID: provider, WebAPIProfileInput: WebAPIProfileInput{Type: "file", Label: "UsedProvider", ExtendType: "standard"}}); err != nil {
			return err
		}
		if err := tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: provider}, MediaType: "application/yaml", Bytes: []byte("proxies:\n  - {name: ProvidedNode, type: direct}\n")}); err != nil {
			return err
		}
		item, err := tx.ProxyGroup(group)
		if err != nil {
			return err
		}
		item.Definition.Use = []string{"UsedProvider"}
		if err := tx.SaveProxyGroup(item); err != nil {
			return err
		}
		return tx.SetProfileCatalog(provider, WebAPIProfileCatalog{UnavailableReason: WebAPICatalogDependencyQuarantined})
	})
	catalogMarkRepairUnreadable(r, owner)
	beforeOwner, beforeDependency := catalogRepairRawRow(r, "profile-catalogs", string(owner)), catalogRepairRawRow(r, "profile-catalogs", string(provider))
	r.call("POST", "/api/v1/profiles:reset-dialers", catalogMutation(r.revision(), "repair-dependency-unavailable", map[string]any{"id": owner}), 409)
	if !bytes.Equal(beforeOwner, catalogRepairRawRow(r, "profile-catalogs", string(owner))) || !bytes.Equal(beforeDependency, catalogRepairRawRow(r, "profile-catalogs", string(provider))) {
		t.Fatal("repair erased an actual dependency quarantine")
	}
}

func TestWebAPICatalogScopedRepairIgnoresLargeUnrelatedProfiles(t *testing.T) {
	r, owner, _, group := catalogRepairFixture(t)
	r.seed("repair-used-provider-ready", func(tx *WebAPIWriteTx) error {
		id, err := tx.AllocateID()
		if err != nil {
			return err
		}
		if err := tx.InsertProfile(WebAPIProfile{ID: id, WebAPIProfileInput: WebAPIProfileInput{Type: "file", Label: "UsedProvider", ExtendType: "standard"}}); err != nil {
			return err
		}
		if err := tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: id}, MediaType: "application/yaml", Bytes: []byte("proxies:\n  - {name: ProvidedNode, type: direct}\n")}); err != nil {
			return err
		}
		item, err := tx.ProxyGroup(group)
		if err != nil {
			return err
		}
		item.Definition.Use = []string{"UsedProvider"}
		return tx.SaveProxyGroup(item)
	})
	large := strings.Repeat("x", 27<<20)
	r.seed("repair-unrelated-large-metadata", func(tx *WebAPIWriteTx) error {
		for i := 0; i < 5; i++ {
			id, err := tx.AllocateID()
			if err != nil {
				return err
			}
			if err := tx.InsertProfile(WebAPIProfile{ID: id, WebAPIProfileInput: WebAPIProfileInput{Type: "file", Label: fmt.Sprintf("UnrelatedLarge%d", i), ExtendType: "standard", OverridesRawDocument: &large}}); err != nil {
				return err
			}
		}
		return nil
	})
	catalogMarkRepairUnreadable(r, owner)
	r.call("POST", "/api/v1/profiles:reset-dialers", catalogMutation(r.revision(), "repair-over-global-byte-threshold", map[string]any{"id": owner}), 200)
	catalogRepairAssertActualCatalog(r, owner)
}

func TestWebAPICatalogScopedCandidateBudgetOnlyChargesActualRows(t *testing.T) {
	r, owner, _, _ := catalogRepairFixture(t)
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		profile, err := tx.Profile(owner)
		if err != nil {
			return err
		}
		_, _, err = webAPICatalogRawCandidateWithBudget(tx, profile, false, NewWebAPICatalogReadBudget(1))
		return err
	})
	catalogCheckCode(t, err, "catalog_budget_exceeded")
}
