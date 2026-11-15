//go:build webpanel && !android

package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWebContentStatefulWriteReconcilesOnlyRootAndInlineScopes(t *testing.T) {
	f := newWebStatefulFixture(t)
	service := NewWebAPIContentService(f.store, WebAPIContentOptions{})
	body := []byte("proxies:\n - {name: root-node, type: tailscale, auth-key: synthetic}\nproxy-providers:\n  inline-source:\n    type: inline\n    payload:\n      - {name: inline-node, type: tailscale, auth-key: synthetic-inline}\n")
	if _, err := service.Write(f.ctx, WebAPIContentWrite{WebAPIMutation: f.mutation(), Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: f.profile.ID}, Bytes: body}); err != nil {
		t.Fatal(err)
	}
	root := webStatefulTestDefinition("tailscale")
	root["name"] = "root-node"
	root["auth-key"] = "synthetic"
	inline := webStatefulTestDefinition("tailscale")
	inline["name"] = "inline-node"
	inline["auth-key"] = "synthetic-inline"
	external := webStatefulTestDefinition("tailscale")
	external["name"] = "external-node"
	plan, err := WebAPIPlanStatefulResources(f.profile.ID, []WebAPIStatefulInput{
		{Scope: WebAPIStatefulScope{Kind: WebAPIStatefulRoot}, Proxies: []map[string]any{root}},
		{Scope: WebAPIStatefulScope{Kind: WebAPIStatefulProfileProvider, ProviderName: "inline-source", SourceProfileID: f.profile.ID, Inline: true}, Proxies: []map[string]any{inline}},
		{Scope: WebAPIStatefulScope{Kind: WebAPIStatefulRuntimeProvider, ProviderName: "external-source", OriginDomain: strings.Repeat("a", 64)}, Proxies: []map[string]any{external}},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := f.bind(plan)
	if _, err = service.Write(f.ctx, WebAPIContentWrite{WebAPIMutation: f.mutation(), Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: f.profile.ID}, Bytes: []byte("proxies: []\n")}); err != nil {
		t.Fatal(err)
	}
	second := f.bind(plan)
	identities := map[string]string{}
	for _, binding := range first.Bindings {
		identities[binding.Resource.Name] = binding.Identity
	}
	for _, binding := range second.Bindings {
		if binding.Resource.Name == "external-node" && identities[binding.Resource.Name] != binding.Identity {
			t.Fatal("content-only inventory detached an unexpanded external provider")
		}
		if binding.Resource.Name != "external-node" && identities[binding.Resource.Name] == binding.Identity {
			t.Fatalf("removed %s scope reused old state identity", binding.Resource.Name)
		}
	}
}

func TestWebBackupStatefulMergeDetachResetAndFailureShareConfigurationTransaction(t *testing.T) {
	f := newWebStatefulFixture(t)
	service := NewWebAPIContentService(f.store, WebAPIContentOptions{})
	write := func(id WebAPIID) {
		t.Helper()
		if _, err := service.Write(f.ctx, WebAPIContentWrite{WebAPIMutation: f.mutation(), Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: id}, Bytes: []byte("proxies:\n - {name: synthetic-node, type: tailscale, auth-key: synthetic-enrollment-key}\n")}); err != nil {
			t.Fatal(err)
		}
	}
	write(f.profile.ID)
	backup := NewWebAPIBackupService(f.store, WebAPIBackupOptions{})
	_, archive, err := backup.Export(f.ctx, "flclash-v1", false)
	if err != nil {
		t.Fatal(err)
	}
	var other WebAPIProfile
	f.mutate(func(tx *WebAPIWriteTx) (any, error) {
		var err error
		other, err = tx.CreateProfile(WebAPIProfileInput{Type: "file", Label: "unaffected", AutoUpdateDurationMillis: 3600000})
		return other, err
	})
	write(other.ID)
	plan := webStatefulTestPlan(t, f.profile.ID, webStatefulTestDefinition("tailscale"))
	otherPlan := webStatefulTestPlan(t, other.ID, webStatefulTestDefinition("tailscale"))
	first, unaffected := f.bind(plan), f.bind(otherPlan)
	failed := NewWebAPIBackupService(f.store, WebAPIBackupOptions{Fault: func(stage string) error {
		if stage == "after-content" {
			return errors.New("synthetic rollback")
		}
		return nil
	}})
	request := WebAPIBackupRestore{WebAPIMutation: f.mutation(), Intent: "restore-application-data", Strategy: "merge", ArchiveBase64: archive}
	if _, err = failed.Restore(f.ctx, request); err == nil {
		t.Fatal("restore fault did not abort")
	}
	afterFailure := f.bind(plan)
	if first.Bindings[0].Identity != afterFailure.Bindings[0].Identity {
		t.Fatal("rolled-back restore detached committed state")
	}
	request.WebAPIMutation = f.mutation()
	if _, err = backup.Restore(f.ctx, request); err != nil {
		t.Fatal(err)
	}
	after, stillUnaffected := f.bind(plan), f.bind(otherPlan)
	if first.Bindings[0].Identity == after.Bindings[0].Identity {
		t.Fatal("restored profile reused stale state")
	}
	if unaffected.Bindings[0].Identity != stillUnaffected.Bindings[0].Identity {
		t.Fatal("merge detached an untouched profile")
	}
	reset := WebAPIReset{WebAPIMutation: f.mutation(), Intent: "reset-application-data"}
	if _, err = backup.Reset(f.ctx, reset); err != nil {
		t.Fatal(err)
	}
	request.WebAPIMutation = f.mutation()
	request.Strategy = "override"
	if _, err = backup.Restore(f.ctx, request); err != nil {
		t.Fatal(err)
	}
	afterReset := f.bind(plan)
	if after.Bindings[0].Identity == afterReset.Bindings[0].Identity || after.ProfileIncarnation == afterReset.ProfileIncarnation {
		t.Fatal("reset/restore reused a detached profile incarnation")
	}
	_, err = f.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		_, err := tx.Profile(other.ID)
		if err == nil {
			t.Error("override retained unrelated profile")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
