//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
)

type webRuntimeStatefulTarget struct {
	Scope       WebAPIStatefulScope `json:"scope"`
	Inline      bool                `json:"inline,omitempty"`
	Path        string              `json:"path,omitempty"`
	ResourceIDs map[string]WebAPIID `json:"resourceIds,omitempty"`
}

type webRuntimeStatefulPreparation struct {
	Plan         WebAPIStatefulPlan          `json:"plan"`
	Targets      []webRuntimeStatefulTarget  `json:"targets"`
	PlanetAssets []WebAPIStatefulPlanetAsset `json:"planetAssets,omitempty"`
}

func webRuntimeDefinitionList(raw any) ([]map[string]any, error) {
	if raw == nil {
		return []map[string]any{}, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, webAPIError(400, "invalid_proxies", "proxy definitions must be a list")
	}
	definitions := make([]map[string]any, 0, len(values))
	for _, value := range values {
		mapping, ok := value.(map[string]any)
		if !ok {
			return nil, webAPIError(400, "invalid_proxies", "proxy definitions must be objects")
		}
		definitions = append(definitions, mapping)
	}
	return definitions, nil
}

func webRuntimeStatefulReferencedAssets(definitions []map[string]any, owner WebAPIID, assets []WebAPIStatefulPlanetAsset) ([]WebAPIStatefulPlanetAsset, error) {
	needed := map[string]bool{}
	for _, definition := range definitions {
		if raw, exists := definition["planet-asset"]; exists {
			reference, err := webStatefulParsePlanetReference(raw)
			if err != nil {
				return nil, err
			}
			needed[reference.SHA256] = true
		}
	}
	result := []WebAPIStatefulPlanetAsset{}
	for _, asset := range assets {
		if asset.ProfileID == owner && needed[asset.Reference.SHA256] {
			result = append(result, asset)
		}
	}
	return result, nil
}

func webRuntimePrepareStateful(root map[string]any, snapshot webRuntimeSnapshot, profileID *WebAPIID) (*webRuntimeStatefulPreparation, error) {
	if profileID == nil {
		return nil, nil
	}
	prepared := &webRuntimeStatefulPreparation{Targets: []webRuntimeStatefulTarget{}, PlanetAssets: []WebAPIStatefulPlanetAsset{}}
	inputs := []WebAPIStatefulInput{}
	assetsSeen := map[string]bool{}
	appendInput := func(target webRuntimeStatefulTarget, definitions []map[string]any, owner WebAPIID) error {
		stateful := false
		for _, definition := range definitions {
			matched, err := WebAPIValidateStatefulDefinition(definition)
			if err != nil {
				return err
			}
			stateful = stateful || matched
		}
		if !stateful {
			return nil
		}
		assets, err := webRuntimeStatefulReferencedAssets(definitions, owner, snapshot.PlanetAssets[owner])
		if err != nil {
			return err
		}
		inputs = append(inputs, WebAPIStatefulInput{Scope: target.Scope, Proxies: definitions, ResourceIDs: target.ResourceIDs, PlanetAssets: assets})
		prepared.Targets = append(prepared.Targets, target)
		for _, asset := range assets {
			key := string(asset.ProfileID) + "/" + asset.Reference.SHA256
			if !assetsSeen[key] {
				assetsSeen[key] = true
				prepared.PlanetAssets = append(prepared.PlanetAssets, asset)
			}
		}
		return nil
	}
	definitions, err := webRuntimeDefinitionList(root["proxies"])
	if err != nil {
		return nil, err
	}
	if err = appendInput(webRuntimeStatefulTarget{Scope: WebAPIStatefulScope{Kind: WebAPIStatefulRoot}, ResourceIDs: snapshot.ResourceIDs}, definitions, *profileID); err != nil {
		return nil, err
	}
	providers, _ := root["proxy-providers"].(map[string]any)
	for name, raw := range providers {
		mapping, ok := raw.(map[string]any)
		if !ok {
			return nil, webAPIError(400, "invalid_provider", "proxy provider declaration must be a map")
		}
		target := webRuntimeStatefulTarget{Scope: WebAPIStatefulScope{ProviderName: name}}
		owner := *profileID
		var content []byte
		if mapping["type"] == "inline" {
			target.Scope.Kind, target.Scope.SourceProfileID, target.Scope.Inline, target.Inline = WebAPIStatefulProfileProvider, *profileID, true, true
			definitions, err = webRuntimeDefinitionList(mapping["payload"])
		} else if source, found := snapshot.Profiles[name]; found {
			target.Scope.Kind, target.Scope.SourceProfileID = WebAPIStatefulProfileProvider, source.Profile.ID
			owner, content = source.Profile.ID, source.Plain
		} else if source, found := snapshot.Remote["proxy-providers/"+name]; found {
			target.Scope.Kind = WebAPIStatefulRuntimeProvider
			target.Scope.OriginDomain, err = WebAPIStatefulOriginDomain(source.Descriptor.URL, source.Descriptor.Headers)
			content = source.Bytes
		} else {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !target.Inline {
			target.Path, _ = mapping["path"].(string)
			document, parseErr := webRuntimeDocument(content)
			if parseErr != nil {
				return nil, parseErr
			}
			var payload map[string]any
			if err = document.Decode(&payload); err != nil {
				return nil, err
			}
			definitions, err = webRuntimeDefinitionList(payload["proxies"])
			if err != nil {
				return nil, err
			}
		}
		if err = appendInput(target, definitions, owner); err != nil {
			return nil, err
		}
	}
	if len(inputs) == 0 {
		return nil, nil
	}
	prepared.Plan, err = WebAPIPlanStatefulResources(*profileID, inputs)
	return prepared, err
}

func webRuntimeSnapshotPlanetAssets(tx *WebAPIReadTx, snapshot *webRuntimeSnapshot, p WebAPIRuntimeApplyRequest, budget *webRuntimeSnapshotBudget) error {
	if p.ProfileID == nil {
		return nil
	}
	snapshot.PlanetAssets = map[WebAPIID][]WebAPIStatefulPlanetAsset{}
	seen := map[string]bool{}
	collect := func(owner WebAPIID, definitions []map[string]any) error {
		for _, definition := range definitions {
			raw, exists := definition["planet-asset"]
			if !exists {
				continue
			}
			if _, err := WebAPIValidateStatefulDefinition(definition); err != nil {
				return err
			}
			reference, err := webStatefulParsePlanetReference(raw)
			if err != nil {
				return err
			}
			key := string(owner) + "/" + reference.SHA256
			if seen[key] {
				continue
			}
			content, err := budget.content(tx, WebAPIContentKey{Kind: WebAPIContentProfileAsset, ID: owner, Name: "zerotier-planet:" + reference.SHA256})
			if err != nil {
				return webAPIError(409, "planet_asset_unavailable", "the typed planet asset is not committed in this profile snapshot")
			}
			if webContentDigest(content.Bytes) != reference.SHA256 {
				return webAPIError(409, "planet_asset_digest_mismatch", "committed planet bytes do not match their typed reference")
			}
			if _, err = WebAPIValidateStatefulPlanet(content.Bytes); err != nil {
				return err
			}
			seen[key] = true
			snapshot.PlanetAssets[owner] = append(snapshot.PlanetAssets[owner], WebAPIStatefulPlanetAsset{ProfileID: owner, Reference: reference, Bytes: content.Bytes})
		}
		return nil
	}
	document, err := webRuntimeCandidate(*snapshot, p)
	if err != nil {
		return err
	}
	var root map[string]any
	if err = document.Decode(&root); err != nil {
		return err
	}
	definitions, err := webRuntimeDefinitionList(root["proxies"])
	if err != nil {
		return err
	}
	if err = collect(*p.ProfileID, definitions); err != nil {
		return err
	}
	providers, _ := root["proxy-providers"].(map[string]any)
	for name, raw := range providers {
		mapping, ok := raw.(map[string]any)
		if !ok {
			return webAPIError(400, "invalid_provider", "proxy provider declaration must be a map")
		}
		owner := *p.ProfileID
		var data []byte
		if mapping["type"] == "inline" {
			definitions, err = webRuntimeDefinitionList(mapping["payload"])
		} else if profile, found := snapshot.Profiles[name]; found {
			owner, data = profile.Profile.ID, profile.Plain
		} else if remote, found := snapshot.Remote["proxy-providers/"+name]; found {
			data = remote.Bytes
		} else {
			continue
		}
		if err != nil {
			return err
		}
		if mapping["type"] != "inline" {
			document, err = webRuntimeDocument(data)
			if err != nil {
				return err
			}
			var source map[string]any
			if err = document.Decode(&source); err != nil {
				return err
			}
			definitions, err = webRuntimeDefinitionList(source["proxies"])
			if err != nil {
				return err
			}
		}
		if err = collect(owner, definitions); err != nil {
			return err
		}
	}
	return nil
}

func (w *webRuntimeWorkspace) read(path string) ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.root == nil || filepath.Dir(path) != w.root.Name() {
		return nil, webAPIError(409, "materialization_failed", "runtime input is not a private workspace resource")
	}
	name := filepath.Base(path)
	info, err := w.root.Lstat(name)
	if err != nil || !webAPIPrivateFile(info) || info.Size() > WebAPIMaxContentBytes {
		return nil, webAPIError(409, "materialization_failed", "runtime input is not a regular bounded private resource")
	}
	file, err := w.root.Open(name)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, WebAPIMaxContentBytes+1))
	closeErr := file.Close()
	if err = errors.Join(err, closeErr); err != nil || int64(len(data)) != info.Size() {
		return nil, webAPIError(409, "materialization_failed", "runtime input changed while being read")
	}
	return data, nil
}

func webRuntimeStatefulKey(scope WebAPIStatefulScope, name string) string {
	key, _ := json.Marshal(struct {
		Scope WebAPIStatefulScope
		Name  string
	}{scope, name})
	return string(key)
}

func (rt *WebAPIRuntime) materializeStatefulApply(ctx context.Context, operationID string, desired webRuntimeDesiredApply) (WebAPIRuntimePreparedApply, error) {
	prepared := desired.Prepared
	if desired.Stateful == nil {
		return prepared, nil
	}
	if err := ctx.Err(); err != nil {
		return prepared, err
	}
	bound, err := rt.store.StatefulRuntimeBindings(ctx, operationID)
	if err != nil {
		return prepared, err
	}
	resources := make([]WebAPIStatefulResource, 0, len(bound.Bindings))
	for _, binding := range bound.Bindings {
		resources = append(resources, binding.Resource)
	}
	if bound.ProfileID != desired.Stateful.Plan.ProfileID || !reflect.DeepEqual(resources, desired.Stateful.Plan.Resources) {
		return prepared, webAPIError(409, "stateful_intent_mismatch", "committed state binding no longer matches the accepted apply plan")
	}
	data, err := rt.workspace.read(prepared.ConfigPath)
	if err != nil {
		return prepared, err
	}
	document, err := webRuntimeDocument(data)
	if err != nil {
		return prepared, err
	}
	var root map[string]any
	if err = document.Decode(&root); err != nil {
		return prepared, err
	}
	type targetContent struct {
		target      webRuntimeStatefulTarget
		root        map[string]any
		definitions []map[string]any
	}
	targets := []targetContent{}
	providers, _ := root["proxy-providers"].(map[string]any)
	for _, target := range desired.Stateful.Targets {
		item := targetContent{target: target, root: root}
		if target.Scope.Kind == WebAPIStatefulRoot {
			item.definitions, err = webRuntimeDefinitionList(root["proxies"])
		} else {
			provider, ok := providers[target.Scope.ProviderName].(map[string]any)
			if !ok {
				return prepared, webAPIError(409, "stateful_intent_mismatch", "accepted provider is not present in the immutable apply input")
			}
			if target.Inline {
				item.definitions, err = webRuntimeDefinitionList(provider["payload"])
			} else {
				if provider["path"] != target.Path {
					return prepared, webAPIError(409, "stateful_intent_mismatch", "accepted provider path changed")
				}
				data, err = rt.workspace.read(target.Path)
				if err != nil {
					return prepared, err
				}
				document, err = webRuntimeDocument(data)
				if err != nil {
					return prepared, err
				}
				item.root = map[string]any{}
				if err = document.Decode(&item.root); err == nil {
					item.definitions, err = webRuntimeDefinitionList(item.root["proxies"])
				}
			}
		}
		if err != nil {
			return prepared, err
		}
		targets = append(targets, item)
	}
	manager := rt.workspace.options.StatefulManager
	if manager == nil {
		return prepared, webAPIError(503, "stateful_storage_unavailable", "server-owned persistent state storage is not configured")
	}
	materialized, err := manager.Materialize(bound)
	if err != nil {
		return prepared, err
	}
	byName := map[string]WebAPIStatefulMaterialized{}
	for _, resource := range materialized {
		byName[webRuntimeStatefulKey(resource.Resource.Scope, resource.Resource.Name)] = resource
	}
	assets := map[string][]byte{}
	for _, asset := range desired.Stateful.PlanetAssets {
		assets[asset.Reference.SHA256] = asset.Bytes
	}
	used := 0
	for _, target := range targets {
		for _, definition := range target.definitions {
			name, _ := definition["name"].(string)
			resource, found := byName[webRuntimeStatefulKey(target.target.Scope, name)]
			if !found {
				continue
			}
			if definition["type"] != resource.Resource.Protocol {
				return prepared, webAPIError(409, "stateful_intent_mismatch", "accepted stateful protocol changed")
			}
			definition["state-dir"] = resource.StateDir
			delete(definition, "planet-asset")
			if resource.Resource.PlanetSHA256 != "" {
				asset, exists := assets[resource.Resource.PlanetSHA256]
				if !exists || webContentDigest(asset) != resource.Resource.PlanetSHA256 {
					return prepared, webAPIError(409, "planet_asset_unavailable", "accepted planet bytes are unavailable")
				}
				definition["planet"], err = rt.workspace.put(asset, ".planet")
				if err != nil {
					return prepared, err
				}
			}
			used++
		}
		if target.target.Scope.Kind != WebAPIStatefulRoot && !target.target.Inline {
			data, err = webRuntimeMarshalYAML(target.root)
			if err != nil {
				return prepared, err
			}
			path, err := rt.workspace.put(data, ".provider")
			if err != nil {
				return prepared, err
			}
			providers[target.target.Scope.ProviderName].(map[string]any)["path"] = path
		}
	}
	if used != len(materialized) {
		return prepared, webAPIError(409, "stateful_intent_mismatch", "accepted stateful resources were not found in their exact scopes")
	}
	if err := ctx.Err(); err != nil {
		return prepared, err
	}
	data, err = webRuntimeMarshalYAML(root)
	if err != nil {
		return prepared, err
	}
	prepared.ConfigPath, err = rt.workspace.put(data, ".yaml")
	return prepared, err
}
