//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"go.yaml.in/yaml/v3"
)

func webCustomSourceReadSnapshot(ctx context.Context, tx *WebAPIReadTx, id WebAPIID, data []byte) (webCustomSourceSnapshot, webCustomSourceParsed, error) {
	snapshot := webCustomSourceSnapshot{Contents: map[WebAPIID]WebAPIContent{}, States: map[WebAPIID]WebAPIProfileCatalog{}}
	var parsed webCustomSourceParsed
	budget := NewWebAPICatalogReadBudget(WebAPIBackupExpandedLimit)
	for _, bucket := range []string{"profiles", "rule-providers", "custom-proxies", "proxy-groups"} {
		count := 0
		rows := tx.tx.Bucket([]byte(bucket))
		if rows == nil {
			return snapshot, parsed, webAPIError(500, "custom_import_snapshot_unavailable", "custom import metadata is unavailable")
		}
		err := rows.ForEach(func(key, value []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			count++
			if count > 100000 {
				return webAPIError(413, "import_complexity_limit", "custom import metadata exceeds the row limit")
			}
			return budget.charge(tx.tx, bucket, key, value)
		})
		if err != nil {
			return snapshot, parsed, err
		}
	}
	var err error
	if snapshot.Target, err = tx.Profile(id); err != nil {
		return snapshot, parsed, err
	}
	if snapshot.Target.Type != "custom" || webAPIUnreadable(snapshot.Target.WebAPIProfileInput) {
		return snapshot, parsed, webAPIError(409, "invalid_profile_state", "custom import requires a readable custom profile")
	}
	if snapshot.Profiles, err = tx.Profiles(); err != nil {
		return snapshot, parsed, err
	}
	if snapshot.Providers, err = tx.ClashProviders(); err != nil {
		return snapshot, parsed, err
	}
	if snapshot.Proxies, err = tx.CustomProxies(); err != nil {
		return snapshot, parsed, err
	}
	if snapshot.Groups, err = tx.ProxyGroups(); err != nil {
		return snapshot, parsed, err
	}
	parsed, err = webCustomSourceParse(data, snapshot)
	if err != nil {
		return snapshot, parsed, err
	}
	names := map[string]bool{}
	for _, group := range parsed.Custom.ProxyGroups {
		for _, name := range group.Definition.Use {
			names[name] = true
		}
	}
	if parsed.Custom.Overrides != nil {
		for name := range parsed.Custom.Overrides.ProxyProviders {
			names[name] = true
		}
	}
	seen := map[string]bool{}
	for _, profile := range snapshot.Profiles {
		name := webAPIEffectiveProfileLabel(profile)
		if profile.Type == "custom" || !names[name] {
			continue
		}
		if seen[name] {
			return snapshot, parsed, webAPIError(409, "ambiguous_catalog_identity", "managed provider identity is ambiguous")
		}
		seen[name] = true
		content, e := tx.CatalogContent(WebAPIContentKey{Kind: WebAPIContentProfile, ID: profile.ID}, budget)
		if e == nil {
			snapshot.Contents[profile.ID] = content
		} else {
			var problem *WebAPIError
			if !errors.As(e, &problem) || problem.Status != 404 {
				return snapshot, parsed, e
			}
		}
		state, e := tx.CatalogProfileCatalogState(profile.ID, budget)
		if e == nil {
			snapshot.States[profile.ID] = state
		} else {
			var problem *WebAPIError
			if !errors.As(e, &problem) || problem.Status != 404 {
				return snapshot, parsed, e
			}
		}
	}
	return snapshot, parsed, nil
}

func webCustomSourcePrepareProvider(source WebAPICustomImportedProvider, data []byte, profiles bool) (webContentCustomProvider, error) {
	item := webContentCustomProvider{source: source, content: data}
	if len(data) > WebAPIMaxContentBytes {
		return item, webAPIError(413, "content_too_large", "provider source exceeds 32 MiB")
	}
	if profiles {
		_, catalog, err := webContentProfile(data, source.AgeSecretKey)
		if err != nil {
			return item, err
		}
		item.catalog = &catalog
	} else {
		if source.AgeSecretKey != "" {
			return item, webCustomSourceError()
		}
		// Rule-set decoding also receives the shared pre-Decode alias budget.
		if len(data) > 0 && !bytes.HasPrefix(data, zstdMagic) {
			if err := webCustomSourceRuleYAMLBudget(data); err != nil {
				return item, err
			}
		}
		info, _, err := webContentRuleSet(data, true)
		if err != nil {
			return item, err
		}
		item.ruleInfo = info
	}
	return item, nil
}

func webCustomSourceGraph(snapshot webCustomSourceSnapshot, custom WebAPICustomProfileImportDTO, profiles, providers []webContentCustomProvider) error {
	profile := webAPIClone(snapshot.Target)
	profile.Overrides = custom.Overrides
	rows := webAPICatalogCandidateRows{proxies: append([]WebAPICustomProxy{}, snapshot.Proxies...), providerProfiles: append([]WebAPIProfile{}, snapshot.Profiles...), ruleProviders: append([]WebAPIClashProvider{}, snapshot.Providers...), providerContents: map[WebAPIID]WebAPIContent{}, providerStates: snapshot.States}
	for id, content := range snapshot.Contents {
		rows.providerContents[id] = content
	}
	for i, item := range profiles {
		id := WebAPIID("pending-profile-" + strconv.Itoa(i))
		rows.providerProfiles = append(rows.providerProfiles, WebAPIProfile{ID: id, WebAPIProfileInput: WebAPIProfileInput{Type: "file", Label: item.source.Label, AgeSecretKey: item.source.AgeSecretKey}})
		rows.providerContents[id] = WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: id}, Bytes: item.content}
	}
	for i, item := range providers {
		rows.ruleProviders = append(rows.ruleProviders, WebAPIClashProvider{ID: WebAPIID("pending-rule-provider-" + strconv.Itoa(i)), Label: item.source.Label, Behavior: item.ruleInfo.Behavior, Format: item.ruleInfo.Format})
	}
	for i, proxy := range custom.Proxies {
		rows.proxies = append(rows.proxies, WebAPICustomProxy{ID: WebAPIID("pending-proxy-" + strconv.Itoa(i)), Definition: proxy})
	}
	for i, group := range custom.ProxyGroups {
		rows.groups = append(rows.groups, WebAPIProxyGroup{ID: WebAPIID("pending-group-" + strconv.Itoa(i)), ProfileID: profile.ID, Definition: group.Definition, MemberLayout: group.MemberLayout})
	}
	for _, input := range custom.Rules {
		input.ProfileID = &profile.ID
		rows.rules = append(rows.rules, WebAPIRule{WebAPIRuleInput: input})
	}
	byName := map[string]WebAPIID{}
	for _, proxy := range rows.proxies {
		name := webAPIProxyString(proxy.Definition, "name")
		if _, exists := byName[name]; exists {
			return webAPIError(409, "ambiguous_catalog_identity", "proxy identity is ambiguous")
		}
		byName[name] = proxy.ID
	}
	for source, target := range custom.Dialers {
		id, ok := byName[source]
		if !ok {
			return webAPIError(409, "invalid_reference", "dialer source is absent")
		}
		rows.dialers = append(rows.dialers, WebAPIProxyDialer{ProfileID: profile.ID, ProxyID: id, Target: target})
	}
	raw, catalog, err := webAPICatalogRawCandidateRows(profile, rows, false)
	if err != nil {
		return err
	}
	raw, err = webAPIMergeProfileOverrides(raw, profile.WebAPIProfileInput)
	if err != nil {
		return err
	}
	graph, err := buildDependencyGraph(raw)
	if err != nil {
		return webAPICatalogPolicyError(err, profile.ID)
	}
	if err := ValidateProxyDependencies(raw); err != nil {
		return webAPICatalogPolicyError(err, profile.ID)
	}
	return webAPIValidateCatalogProfileReferences(webAPICandidateReferenceProfile(profile, raw), raw, catalog, graph)
}

func webCustomSourceSnapshotDigest(snapshot webCustomSourceSnapshot) (string, int, error) {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", 0, err
	}
	return webContentDigest(data), len(data), nil
}

func webCustomSourceRuleYAMLBudget(data []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var node yaml.Node
	if decoder.Decode(&node) != nil {
		return webCustomSourceError()
	}
	if err := WebAPIValidateYAMLWork(&node); err != nil {
		return err
	}
	var extra yaml.Node
	if decoder.Decode(&extra) != io.EOF {
		return webCustomSourceError()
	}
	return nil
}
