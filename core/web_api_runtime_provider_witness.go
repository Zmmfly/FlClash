//go:build webpanel && !android

package core

import "sort"

type webRuntimeProviderWitness struct {
	ID            WebAPIRuntimeProviderID `json:"id"`
	SourceVersion string                  `json:"sourceVersion"`
	OperationID   string                  `json:"sourceOperationId"`
	SHA256        string                  `json:"sha256"`
}

func webRuntimeProviderWitnesses(root map[string]any, snapshot webRuntimeSnapshot) []webRuntimeProviderWitness {
	result := []webRuntimeProviderWitness{}
	for _, category := range []string{"proxy-providers", "rule-providers"} {
		providers, _ := root[category].(map[string]any)
		for name, raw := range providers {
			mapping, _ := raw.(map[string]any)
			if mapping["type"] == "inline" {
				continue
			}
			if _, bound := snapshot.Profiles[name]; category == "proxy-providers" && bound {
				continue
			}
			if _, bound := snapshot.Rules[name]; category == "rule-providers" && bound {
				continue
			}
			if cached, found := snapshot.Remote[category+"/"+name]; found {
				result = append(result, webRuntimeProviderWitness{ID: cached.Descriptor.Source.ID, SourceVersion: cached.Descriptor.Source.SourceVersion, OperationID: cached.OperationID, SHA256: webContentDigest(cached.Bytes)})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func webRuntimeValidateProviderWitnesses(tx *WebAPIReadTx, witnesses []webRuntimeProviderWitness) error {
	for _, witness := range witnesses {
		current, err := tx.runtimeProvider(witness.ID)
		if err != nil || current.Phase != "ready" || current.OperationID != witness.OperationID || current.Descriptor.Source.SourceVersion != witness.SourceVersion || webContentDigest(current.Bytes) != witness.SHA256 {
			return webAPIError(409, "provider_source_changed", "prepared provider content changed before apply acceptance; reload the original source snapshot")
		}
	}
	return nil
}
