//go:build webpanel && !android

package core

import "testing"

func webStatefulTestIdentities(bound WebAPIStatefulBindings) map[WebAPIStatefulScope]string {
	out := map[WebAPIStatefulScope]string{}
	for _, binding := range bound.Bindings {
		out[binding.Resource.Scope] = binding.Identity
	}
	return out
}

func TestWebAPIStatefulContractScopedInventoryDoesNotDetachInlineOrOtherProviders(t *testing.T) {
	f := newWebStatefulFixture(t)
	var source WebAPIProfile
	f.mutate(func(tx *WebAPIWriteTx) (any, error) {
		var err error
		input := f.profile.WebAPIProfileInput
		input.Label = "source-profile"
		source, err = tx.CreateProfile(input)
		return source, err
	})
	root := WebAPIStatefulScope{Kind: WebAPIStatefulRoot}
	inline := WebAPIStatefulScope{Kind: WebAPIStatefulProfileProvider, ProviderName: "inline", SourceProfileID: f.profile.ID, Inline: true}
	provider := WebAPIStatefulScope{Kind: WebAPIStatefulProfileProvider, ProviderName: "external", SourceProfileID: source.ID}
	definition := webStatefulTestDefinition("easytier")
	inputs := []WebAPIStatefulInput{{Scope: root, Proxies: []map[string]any{definition}}, {Scope: inline, Proxies: []map[string]any{definition}}, {Scope: provider, Proxies: []map[string]any{definition}}}
	plan, err := WebAPIPlanStatefulResources(f.profile.ID, inputs)
	if err != nil {
		t.Fatal(err)
	}
	first := webStatefulTestIdentities(f.bind(plan))
	f.mutate(func(tx *WebAPIWriteTx) (any, error) {
		return nil, tx.ReconcileStatefulScopeResources(f.profile.ID, root, nil)
	})
	second := webStatefulTestIdentities(f.bind(plan))
	if first[root] == second[root] || first[inline] != second[inline] || first[provider] != second[provider] {
		t.Fatal("root-only inventory rotated an unexamined provider or retained a removed root")
	}
	f.mutate(func(tx *WebAPIWriteTx) (any, error) {
		return nil, tx.ReconcileStatefulScopeResources(source.ID, root, nil)
	})
	third := webStatefulTestIdentities(f.bind(plan))
	if second[root] != third[root] || second[inline] != third[inline] || second[provider] == third[provider] {
		t.Fatal("source root inventory did not isolate its real consumers from inline providers")
	}
	f.mutate(func(tx *WebAPIWriteTx) (any, error) {
		return nil, tx.DetachStatefulScopes(f.profile.ID, []WebAPIStatefulScope{inline})
	})
	fourth := webStatefulTestIdentities(f.bind(plan))
	if third[root] != fourth[root] || third[inline] == fourth[inline] || third[provider] != fourth[provider] {
		t.Fatal("closed provider detach changed another resource scope")
	}
}

func TestWebAPIStatefulContractClosedScopeValidation(t *testing.T) {
	definition := webStatefulTestDefinition("tailscale")
	for _, scope := range []WebAPIStatefulScope{
		{Kind: "filesystem", ProviderName: "anything"},
		{Kind: WebAPIStatefulRoot, ProviderName: "provider"},
		{Kind: WebAPIStatefulRoot, Inline: true},
		{Kind: WebAPIStatefulProfileProvider, ProviderName: "inline", SourceProfileID: "2", Inline: true},
		{Kind: WebAPIStatefulRuntimeProvider, ProviderName: "remote", OriginDomain: "client-chosen-path"},
	} {
		_, err := WebAPIPlanStatefulResources("1", []WebAPIStatefulInput{{Scope: scope, Proxies: []map[string]any{definition}}})
		webAPITestErrorCode(t, err, "invalid_stateful_scope")
	}
	f := newWebStatefulFixture(t)
	plan := webStatefulTestPlan(t, f.profile.ID, definition)
	f.bind(plan)
	mutation := f.mutation()
	_, err := f.store.Transaction(f.ctx, mutation, "synthetic-reconcile", mutation, func(tx *WebAPIWriteTx) (any, error) {
		return nil, tx.ReconcileStatefulScopeResources(f.profile.ID, WebAPIStatefulScope{Kind: WebAPIStatefulProfileProvider, ProviderName: "another", SourceProfileID: f.profile.ID}, plan.Resources)
	})
	webAPITestErrorCode(t, err, "invalid_stateful_scope")
}

func TestWebAPIStatefulContractCustomResourceRenameAndReplacement(t *testing.T) {
	f := newWebStatefulFixture(t)
	definition := webStatefulTestDefinition("tailscale")
	input := WebAPIStatefulInput{Scope: WebAPIStatefulScope{Kind: WebAPIStatefulRoot}, Proxies: []map[string]any{definition}, ResourceIDs: map[string]WebAPIID{"synthetic-node": "100"}}
	firstPlan, err := WebAPIPlanStatefulResources(f.profile.ID, []WebAPIStatefulInput{input})
	if err != nil {
		t.Fatal(err)
	}
	first := f.bind(firstPlan)
	definition["name"] = "renamed"
	input.ResourceIDs = map[string]WebAPIID{"renamed": "100"}
	secondPlan, err := WebAPIPlanStatefulResources(f.profile.ID, []WebAPIStatefulInput{input})
	if err != nil {
		t.Fatal(err)
	}
	second := f.bind(secondPlan)
	if first.Bindings[0].Identity != second.Bindings[0].Identity {
		t.Fatal("renaming the same server resource rotated its identity")
	}
	input.ResourceIDs["renamed"] = "101"
	replacementPlan, err := WebAPIPlanStatefulResources(f.profile.ID, []WebAPIStatefulInput{input})
	if err != nil {
		t.Fatal(err)
	}
	replacement := f.bind(replacementPlan)
	if second.Bindings[0].Identity == replacement.Bindings[0].Identity {
		t.Fatal("new server resource reused the deleted resource's identity")
	}
}
