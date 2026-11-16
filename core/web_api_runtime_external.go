//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	cp "github.com/metacubex/mihomo/constant/provider"
	"github.com/metacubex/mihomo/tunnel"
)

type WebAPIRuntimeProviderBinding struct {
	Kind          string    `json:"kind"`
	ID            string    `json:"id"`
	ProfileID     *WebAPIID `json:"profileId,omitempty"`
	Purpose       string    `json:"purpose,omitempty"`
	SourceVersion string    `json:"sourceVersion,omitempty"`
}

// Runtime metadata is obtained from the running provider, never a native path.
type WebAPIRuntimeExternalProviderMetadata struct {
	Name             string
	Purpose          string
	Type             string
	VehicleType      string
	Count            int
	UpdatedAt        *time.Time
	SubscriptionInfo *WebAPISubscriptionInfo
}

type WebAPIRuntimeExternalProvider struct {
	Name             string                       `json:"name"`
	Type             string                       `json:"type"`
	VehicleType      string                       `json:"vehicleType"`
	Count            int                          `json:"count"`
	UpdatedAt        *time.Time                   `json:"updatedAt"`
	SubscriptionInfo *WebAPISubscriptionInfo      `json:"subscriptionInfo"`
	Binding          WebAPIRuntimeProviderBinding `json:"binding"`
}

type WebAPIRuntimeExternalProviderSnapshot struct {
	RuntimeGeneration WebAPIRevision                  `json:"runtimeGeneration"`
	ActualConfig      string                          `json:"actualConfig"`
	Providers         []WebAPIRuntimeExternalProvider `json:"providers"`
}

type webRuntimeExternalProvenance struct {
	Name        string                          `json:"name"`
	Purpose     string                          `json:"purpose"`
	Binding     WebAPIRuntimeProviderBinding    `json:"binding"`
	OwnerDigest string                          `json:"ownerDigest,omitempty"`
	Evaluation  *webRuntimeEvaluationProvenance `json:"evaluation,omitempty"`
}

func webRuntimeProfileProviderIdentity(source webRuntimeSource) string {
	identity, _ := json.Marshal(struct{ Label, Type, Content string }{webAPIEffectiveProfileLabel(source.Profile), source.Profile.Type, webContentDigest(source.Plain)})
	return webContentDigest(identity)
}

func webRuntimeRuleProviderIdentity(source webRuntimeRuleSource) string {
	identity, _ := json.Marshal(struct{ Label, Behavior, Format, Content string }{source.Provider.Label, source.Provider.Behavior, source.Provider.Format, webContentDigest(source.Bytes)})
	return webContentDigest(identity)
}

func webRuntimeExternalProvenanceFor(root map[string]any, snapshot webRuntimeSnapshot) []webRuntimeExternalProvenance {
	result := []webRuntimeExternalProvenance{}
	for _, category := range []string{"proxy-providers", "rule-providers"} {
		providers, _ := root[category].(map[string]any)
		for name, raw := range providers {
			mapping, _ := raw.(map[string]any)
			if mapping["type"] == "inline" {
				continue
			}
			record := webRuntimeExternalProvenance{Name: name, Purpose: category}
			if source, found := snapshot.Profiles[name]; category == "proxy-providers" && found {
				record.Binding = WebAPIRuntimeProviderBinding{Kind: "profile", ID: string(source.Profile.ID)}
				record.OwnerDigest = webRuntimeProfileProviderIdentity(source)
			} else if source, found := snapshot.Rules[name]; category == "rule-providers" && found {
				record.Binding = WebAPIRuntimeProviderBinding{Kind: "rule-provider", ID: string(source.Provider.ID)}
				record.OwnerDigest = webRuntimeRuleProviderIdentity(source)
			} else if source, found := snapshot.Remote[category+"/"+name]; found {
				id := source.Descriptor.Source.ProfileID
				record.Binding = WebAPIRuntimeProviderBinding{Kind: "external-provider", ID: string(source.Descriptor.Source.ID), ProfileID: &id, Purpose: category, SourceVersion: source.Descriptor.Source.SourceVersion}
				record.Evaluation = source.Descriptor.Evaluation
			} else {
				continue
			}
			result = append(result, record)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Purpose != result[j].Purpose {
			return result[i].Purpose < result[j].Purpose
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func (rt *WebAPIRuntime) verifyExternalBinding(ctx context.Context, tx *WebAPIReadTx, provenance webRuntimeExternalProvenance) error {
	mismatch := func() error {
		return webAPIError(409, "binding_target_changed", "the applied provider owner changed; apply the current committed source before editing it")
	}
	switch binding := provenance.Binding; binding.Kind {
	case "profile":
		source, err := webRuntimeProfileSource(tx, WebAPIID(binding.ID))
		if err != nil || provenance.Purpose != "proxy-providers" || webRuntimeProfileProviderIdentity(source) != provenance.OwnerDigest {
			return mismatch()
		}
	case "rule-provider":
		provider, err := tx.ClashProvider(WebAPIID(binding.ID))
		if err != nil || provenance.Purpose != "rule-providers" {
			return mismatch()
		}
		content, err := tx.Content(WebAPIContentKey{Kind: WebAPIContentRuleProvider, ID: provider.ID})
		if err != nil || webRuntimeRuleProviderIdentity(webRuntimeRuleSource{Provider: provider, Bytes: content.Bytes}) != provenance.OwnerDigest {
			return mismatch()
		}
	case "external-provider":
		descriptor, err := rt.lookupProviderTx(ctx, tx, WebAPIRuntimeProviderID(binding.ID))
		if err != nil || binding.ProfileID == nil || descriptor.Source.ProfileID != *binding.ProfileID || descriptor.Source.Purpose != binding.Purpose || binding.Purpose != provenance.Purpose || descriptor.Source.SourceVersion != binding.SourceVersion {
			return mismatch()
		}
	default:
		return webAPIError(409, "applied_state_unknown", "applied provider has no typed owner provenance")
	}
	return nil
}

func (rt *WebAPIRuntime) externalProvidersHTTP(w http.ResponseWriter, r *http.Request) {
	if err := webRuntimeNoQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	if !rt.backendMu.TryRLock() {
		WebAPIWriteError(w, webAPIError(503, "runtime_busy", "a runtime effect is still in flight"))
		return
	}
	revision, result, err := func() (WebAPIRevision, WebAPIRuntimeExternalProviderSnapshot, error) {
		defer rt.backendMu.RUnlock()
		_, actual, operationID, err := rt.appliedSnapshot(r.Context())
		result := WebAPIRuntimeExternalProviderSnapshot{RuntimeGeneration: actual.RuntimeGeneration, ActualConfig: actual.ActualConfig, Providers: []WebAPIRuntimeExternalProvider{}}
		if err != nil {
			return "", result, err
		}
		var runtime []WebAPIRuntimeExternalProviderMetadata
		if actual.ActualConfig == "candidate" {
			if rt.backend.ExternalProviders == nil {
				return "", result, webRuntimeUnavailable()
			}
			runtime, err = rt.backend.ExternalProviders(r.Context())
			if err != nil {
				return "", result, err
			}
		}
		revision, err := rt.store.View(r.Context(), func(tx *WebAPIReadTx) error {
			pending, err := tx.runtimeTransitionPending()
			if err != nil {
				return err
			}
			if pending {
				return webAPIError(409, "applied_state_unknown", "wait for the accepted runtime transition before reading applied provider bindings")
			}
			if actual.ActualConfig == "default-fallback" {
				return nil
			}
			record, err := tx.runtimeRecord(operationID)
			if err != nil {
				return err
			}
			var desired webRuntimeDesiredApply
			if err = json.Unmarshal(record.Desired, &desired); err != nil {
				return err
			}
			byName := map[string]webRuntimeExternalProvenance{}
			for _, provenance := range desired.ExternalProviders {
				byName[provenance.Purpose+"/"+provenance.Name] = provenance
			}
			for _, provider := range runtime {
				provenance, found := byName[provider.Purpose+"/"+provider.Name]
				if !found {
					return webAPIError(409, "applied_state_unknown", "running provider does not match the confirmed typed apply provenance")
				}
				if err := rt.verifyExternalBinding(r.Context(), tx, provenance); err != nil {
					return err
				}
				result.Providers = append(result.Providers, WebAPIRuntimeExternalProvider{Name: provider.Name, Type: provider.Type, VehicleType: provider.VehicleType, Count: provider.Count, UpdatedAt: provider.UpdatedAt, SubscriptionInfo: provider.SubscriptionInfo, Binding: provenance.Binding})
			}
			return nil
		})
		return revision, result, err
	}()
	if err != nil {
		WebAPIWriteError(w, webRuntimeFailure(err))
		return
	}
	rt.profileAnswer(w, r, revision, result)
}

func webRuntimeNativeExternalProviders(ctx context.Context) ([]WebAPIRuntimeExternalProviderMetadata, error) {
	result := []WebAPIRuntimeExternalProviderMetadata{}
	appendProvider := func(purpose string, native cp.Provider) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if native.VehicleType() == cp.Compatible || native.VehicleType() == cp.Inline {
			return nil
		}
		provider, err := toExternalProvider(native)
		if err != nil {
			return err
		}
		metadata := WebAPIRuntimeExternalProviderMetadata{Name: provider.Name, Purpose: purpose, Type: provider.Type, VehicleType: provider.VehicleType, Count: provider.Count}
		if !provider.UpdateAt.IsZero() {
			updated := provider.UpdateAt.UTC()
			metadata.UpdatedAt = &updated
		}
		if info := provider.SubscriptionInfo; info != nil {
			metadata.SubscriptionInfo = &WebAPISubscriptionInfo{Upload: WebAPIInt64(strconv.FormatInt(info.Upload, 10)), Download: WebAPIInt64(strconv.FormatInt(info.Download, 10)), Total: WebAPIInt64(strconv.FormatInt(info.Total, 10)), Expire: WebAPIInt64(strconv.FormatInt(info.Expire, 10))}
		}
		result = append(result, metadata)
		return nil
	}
	for _, provider := range tunnel.ProvidersSnapshot() {
		if err := appendProvider("proxy-providers", provider); err != nil {
			return nil, err
		}
	}
	for _, provider := range tunnel.RuleProvidersSnapshot() {
		if err := appendProvider("rule-providers", provider); err != nil {
			return nil, err
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Purpose != result[j].Purpose {
			return result[i].Purpose < result[j].Purpose
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}
