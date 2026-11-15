//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWebRuntimeExternalProviderBindingsUseAppliedProvenanceAndExactIDNamespaces(t *testing.T) {
	const managedProfile WebAPIID = "9007199254740994"
	const managedRule WebAPIID = "9007199254740995"
	rootYAML := "proxy-providers:\n  ManagedP: {type: file}\nrule-providers:\n  ManagedR: {type: file, behavior: domain, format: text}\nproxy-groups:\n  - {name: G, type: select, proxies: [DIRECT], use: [ManagedP]}\nrules: ['RULE-SET,ManagedR,DIRECT']\n"
	observedTime := time.Unix(1700000000, 0).UTC()
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{
		Apply: func(_ context.Context, p WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
			return WebAPIRuntimeApplyResult{ActualConfig: "candidate", ProfileID: p.ProfileID}, nil
		},
		ExternalProviders: func(context.Context) ([]WebAPIRuntimeExternalProviderMetadata, error) {
			return []WebAPIRuntimeExternalProviderMetadata{{Name: "ManagedP", Purpose: "proxy-providers", Type: "Proxy", VehicleType: "File", Count: 5, UpdatedAt: &observedTime}, {Name: "ManagedR", Purpose: "rule-providers", Type: "Rule", VehicleType: "File", Count: 2}}, nil
		},
	})
	webRuntimeSeedProfile(t, s, "file", rootYAML)
	_, err := s.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "1", OperationID: "seed-managed-provider-owners"}, "test:managed-owners", rootYAML, func(tx *WebAPIWriteTx) (any, error) {
		profile := WebAPIProfile{ID: managedProfile, WebAPIProfileInput: WebAPIProfileInput{Type: "file", Label: "ManagedP", ExtendType: "standard", SelectedMap: map[string]string{}, UnfoldSet: []string{}, Overrides: &WebAPIProfileOverrides{}}}
		if err := tx.InsertProfile(profile); err != nil {
			return nil, err
		}
		_, catalog, err := webContentProfile([]byte(webRuntimeTestYAML), "")
		if err != nil {
			return nil, err
		}
		if err := tx.SetProfileCatalog(profile.ID, catalog); err != nil {
			return nil, err
		}
		if err := tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: profile.ID}, MediaType: "application/yaml", Bytes: []byte(webRuntimeTestYAML)}); err != nil {
			return nil, err
		}
		if err := tx.InsertClashProvider(WebAPIClashProvider{ID: managedRule, Label: "ManagedR", Behavior: "domain", Format: "text"}); err != nil {
			return nil, err
		}
		return nil, tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentRuleProvider, ID: managedRule}, MediaType: "text/plain", Bytes: []byte("example.test\n")})
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := s.request(t, "GET", "/api/v1/runtime/external-providers", "", s.cookie, ""); code != 409 {
		t.Fatal("unapplied candidate was mislabeled as actual provider bindings")
	}
	request := webRuntimeApplyBody(t, "apply-managed-provider-owners", map[string]any{"expectedRevision": "2", "resourceBindings": WebAPIRuntimeResourceBindings{ProfileProviders: map[string]WebAPIID{"ManagedP": managedProfile}, RuleProviders: map[string]WebAPIID{"ManagedR": managedRule}}})
	if code, body := s.request(t, "POST", "/api/v1/runtime:apply", request, s.cookie, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if state := s.phase(t, "apply-managed-provider-owners"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	code, body := s.request(t, "GET", "/api/v1/runtime/external-providers", "", s.cookie, "")
	var result WebAPIEnvelope[WebAPIRuntimeExternalProviderSnapshot]
	if code != 200 || json.Unmarshal(body, &result) != nil || result.Revision != "3" || len(result.Data.Providers) != 2 || result.Data.ActualConfig != "candidate" {
		t.Fatalf("applied provider snapshot %d %s", code, body)
	}
	bindings := map[string]WebAPIRuntimeProviderBinding{}
	for _, provider := range result.Data.Providers {
		bindings[provider.Name] = provider.Binding
	}
	if bindings["ManagedP"].Kind != "profile" || bindings["ManagedP"].ID != string(managedProfile) || bindings["ManagedP"].ProfileID != nil || bindings["ManagedR"].Kind != "rule-provider" || bindings["ManagedR"].ID != string(managedRule) || bindings["ManagedR"].SourceVersion != "" {
		t.Fatal("managed owner namespaces were guessed or rounded")
	}
	if bytes.Contains(body, []byte("path")) || bytes.Contains(body, []byte("/tmp/")) || bytes.Contains(body, []byte("http://")) {
		t.Fatalf("provider metadata exposed privileged paths/URLs: %s", body)
	}
	_, err = s.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "3", OperationID: "edit-managed-owner-after-apply"}, "test:edit-managed-owner", managedProfile, func(tx *WebAPIWriteTx) (any, error) {
		return nil, tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: managedProfile}, MediaType: "application/yaml", Bytes: []byte(webRuntimeTestYAML + "# edited but not applied\n")})
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, body := s.request(t, "GET", "/api/v1/runtime/external-providers", "", s.cookie, ""); code != 409 || !bytes.Contains(body, []byte("binding_target_changed")) {
		t.Fatalf("applied provider fresh-stamped a changed owner %d %s", code, body)
	}
}

func TestWebRuntimeExternalProviderOpaqueBindingDoesNotBecomeInt64OrRawPathLookup(t *testing.T) {
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{
		Apply: func(_ context.Context, p WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
			return WebAPIRuntimeApplyResult{ActualConfig: "candidate", ProfileID: p.ProfileID}, nil
		},
		ExternalProviders: func(context.Context) ([]WebAPIRuntimeExternalProviderMetadata, error) {
			return []WebAPIRuntimeExternalProviderMetadata{{Name: "remote", Purpose: "rule-providers", Type: "Rule", VehicleType: "File", Count: 1}}, nil
		},
	})
	webRuntimeSeedProfile(t, s, "file", webRuntimeRuleSourceYAML)
	source := webRuntimeTestSource(t, s)
	if code, body := s.request(t, "POST", "/api/v1/runtime/provider-sources/"+string(source.ID)+":write", webRuntimeProviderWriteBody(t, source, "1", "write-opaque-provider-binding", []byte("example.test\n")), s.cookie, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if state := s.phase(t, "write-opaque-provider-binding"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	if code, body := s.request(t, "POST", "/api/v1/runtime:apply", webRuntimeApplyBody(t, "apply-opaque-provider-binding", map[string]any{"expectedRevision": "2"}), s.cookie, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if state := s.phase(t, "apply-opaque-provider-binding"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	code, body := s.request(t, "GET", "/api/v1/runtime/external-providers", "", s.cookie, "")
	var result WebAPIEnvelope[WebAPIRuntimeExternalProviderSnapshot]
	if code != 200 || json.Unmarshal(body, &result) != nil || len(result.Data.Providers) != 1 {
		t.Fatalf("%d %s", code, body)
	}
	binding := result.Data.Providers[0].Binding
	if binding.Kind != "external-provider" || binding.ID != string(source.ID) || len(binding.ID) < 20 || binding.ProfileID == nil || *binding.ProfileID != webRuntimeTestProfileID || binding.Purpose != source.Purpose || binding.SourceVersion != source.SourceVersion {
		t.Fatalf("opaque provider identity was narrowed or lost provenance: %+v", binding)
	}
	for _, query := range []string{"?path=/etc/passwd", "?name=remote", "?id=" + string(source.ID)} {
		if code, _ := s.request(t, "GET", "/api/v1/runtime/external-providers"+query, "", s.cookie, ""); code != 400 {
			t.Fatal("external provider inventory accepted raw lookup parameters")
		}
	}
	if strings.Contains(string(body), "provider.test") || strings.Contains(string(body), "api-runtime-") {
		t.Fatal("opaque provider metadata leaked transport authority or server workspace")
	}
}
