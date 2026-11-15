//go:build webpanel && !android

package core

import (
	"github.com/metacubex/mihomo/config"
)

// Content owns root and inline-provider inventories only. External/provider
// snapshots are not expanded here and must not be reconciled as absent.
func webContentStatefulInputs(id WebAPIID, raw *config.RawConfig) ([]WebAPIStatefulInput, error) {
	if raw == nil {
		return nil, webAPIError(500, "stateful_inventory_missing", "validated profile inventory is unavailable")
	}
	inputs := []WebAPIStatefulInput{{Scope: WebAPIStatefulScope{Kind: WebAPIStatefulRoot}, Proxies: raw.Proxy}}
	for name, provider := range raw.ProxyProvider {
		if provider["type"] != "inline" {
			continue
		}
		input := WebAPIStatefulInput{Scope: WebAPIStatefulScope{Kind: WebAPIStatefulProfileProvider, ProviderName: name, SourceProfileID: id, Inline: true}}
		switch payload := provider["payload"].(type) {
		case nil:
		case []map[string]any:
			input.Proxies = payload
		case []any:
			for _, value := range payload {
				definition, ok := value.(map[string]any)
				if !ok {
					return nil, webAPIError(400, "invalid_profile_content", "inline proxy definitions must be mappings")
				}
				input.Proxies = append(input.Proxies, definition)
			}
		default:
			return nil, webAPIError(400, "invalid_profile_content", "inline proxy payload must be a list")
		}
		inputs = append(inputs, input)
	}
	return inputs, nil
}

func webContentValidateStateful(raw *config.RawConfig) error {
	inputs, err := webContentStatefulInputs("1", raw)
	if err != nil {
		return err
	}
	for _, input := range inputs {
		for _, definition := range input.Proxies {
			if _, err := WebAPIValidateStatefulDefinition(definition); err != nil {
				return err
			}
		}
	}
	return nil
}

func webContentStatefulPlan(tx *WebAPIReadTx, id WebAPIID, raw *config.RawConfig) ([]WebAPIStatefulInput, WebAPIStatefulPlan, error) {
	inputs, err := webContentStatefulInputs(id, raw)
	if err != nil {
		return nil, WebAPIStatefulPlan{}, err
	}
	assets := map[string]WebAPIStatefulPlanetAsset{}
	for i := range inputs {
		needed := map[string]bool{}
		for _, definition := range inputs[i].Proxies {
			value, exists := definition["planet-asset"]
			if !exists {
				continue
			}
			reference, err := webStatefulParsePlanetReference(value)
			if err != nil {
				return nil, WebAPIStatefulPlan{}, err
			}
			asset, cached := assets[reference.SHA256]
			if !cached {
				if tx == nil {
					return nil, WebAPIStatefulPlan{}, webAPIError(409, "planet_asset_unavailable", "planet assets require an existing owner profile")
				}
				content, err := tx.Content(WebAPIContentKey{Kind: WebAPIContentProfileAsset, ID: id, Name: reference.Purpose + ":" + reference.SHA256})
				if err != nil {
					return nil, WebAPIStatefulPlan{}, webAPIError(409, "planet_asset_unavailable", "referenced planet asset is not committed for this profile")
				}
				asset, err = webContentPlanetAsset(content)
				if err != nil {
					return nil, WebAPIStatefulPlan{}, err
				}
				assets[reference.SHA256] = asset
			}
			if !needed[reference.SHA256] {
				inputs[i].PlanetAssets = append(inputs[i].PlanetAssets, asset)
				needed[reference.SHA256] = true
			}
		}
	}
	plan, err := WebAPIPlanStatefulResources(id, inputs)
	if err != nil {
		return nil, WebAPIStatefulPlan{}, err
	}
	return inputs, plan, nil
}

func webContentReconcileStateful(tx *WebAPIWriteTx, id WebAPIID, raw *config.RawConfig) error {
	inputs, plan, err := webContentStatefulPlan(tx.WebAPIReadTx, id, raw)
	if err != nil {
		return err
	}
	current := map[WebAPIStatefulScope]bool{}
	for _, input := range inputs {
		current[input.Scope] = true
		resources := []WebAPIStatefulResource{}
		for _, resource := range plan.Resources {
			if resource.Scope == input.Scope {
				resources = append(resources, resource)
			}
		}
		if err := tx.ReconcileStatefulScopeResources(id, input.Scope, resources); err != nil {
			return err
		}
	}
	profile, err := tx.Profile(id)
	if err != nil {
		return err
	}
	if previous, err := tx.Content(WebAPIContentKey{Kind: WebAPIContentProfile, ID: id}); err == nil {
		if oldRaw, _, err := webContentProfile(previous.Bytes, profile.AgeSecretKey); err == nil {
			if oldInputs, err := webContentStatefulInputs(id, oldRaw); err == nil {
				removed := []WebAPIStatefulScope{}
				for _, old := range oldInputs {
					if old.Scope.Kind != WebAPIStatefulRoot && !current[old.Scope] {
						removed = append(removed, old.Scope)
					}
				}
				if err := tx.DetachStatefulScopes(id, removed); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
