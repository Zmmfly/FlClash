//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"net"
	"net/url"
	"sort"
	"strings"
)

func webStatefulName(name string) bool {
	return name != "" && len(name) <= 1024 && !strings.ContainsAny(name, "\x00\r\n")
}

func webStatefulScope(scope WebAPIStatefulScope) error {
	switch scope.Kind {
	case WebAPIStatefulRoot:
		if scope.ProviderName == "" && scope.SourceProfileID == "" && scope.OriginDomain == "" && !scope.Inline {
			return nil
		}
	case WebAPIStatefulProfileProvider:
		if _, err := webAPIInteger(string(scope.SourceProfileID), true); err == nil && webStatefulName(scope.ProviderName) && scope.OriginDomain == "" {
			return nil
		}
	case WebAPIStatefulRuntimeProvider:
		if webStatefulName(scope.ProviderName) && scope.SourceProfileID == "" && webStatefulHex(scope.OriginDomain) && !scope.Inline {
			return nil
		}
	}
	return webAPIError(400, "invalid_stateful_scope", "stateful resources require an authoritative closed provider scope")
}

func webStatefulJSONDigest(value any) string {
	raw, _ := json.Marshal(value)
	return webStatefulDigest(raw)
}

func webStatefulCanonicalURL(text string) (string, error) {
	u, err := url.Parse(text)
	if err != nil || u.Opaque != "" || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Fragment != "" || u.User != nil {
		return "", webAPIError(400, "invalid_stateful_origin", "stateful origin requires an HTTP URL without userinfo or fragments")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if u.Scheme == "http" && port == "80" || u.Scheme == "https" && port == "443" {
		port = ""
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

func WebAPIStatefulOriginDomain(source string, headers map[string][]string) (string, error) {
	origin, err := webStatefulCanonicalURL(source)
	if err != nil {
		return "", err
	}
	auth := map[string][]string{}
	for name, values := range headers {
		key := strings.ToLower(name)
		if key == "authorization" || key == "proxy-authorization" || key == "cookie" {
			if _, duplicate := auth[key]; duplicate {
				return "", webAPIError(400, "invalid_stateful_origin", "duplicate origin authentication headers")
			}
			auth[key] = append([]string(nil), values...)
		}
	}
	return webStatefulJSONDigest(struct {
		Origin string
		Auth   map[string][]string
	}{origin, auth}), nil
}

func webStatefulRawPaths(definition map[string]any) error {
	for key := range definition {
		normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(key))
		switch normalized {
		case "statedir", "configfile", "path", "planet":
			return webAPIError(422, "stateful_path_forbidden", "stateful filesystem paths are server-owned and cannot be supplied")
		case "planetasset":
			if key != "planet-asset" {
				return webAPIError(400, "invalid_planet_reference", "the typed planet field must be planet-asset")
			}
		}
	}
	return nil
}

func WebAPIValidateStatefulDefinition(definition map[string]any) (bool, error) {
	kind, _ := definition["type"].(string)
	for key := range definition {
		if strings.EqualFold(strings.NewReplacer("-", "", "_", "").Replace(key), "statedir") {
			return kind == "tailscale" || kind == "easytier" || kind == "zerotier", webAPIError(422, "stateful_path_forbidden", "state-dir is server-owned even when empty")
		}
	}
	if kind != "tailscale" && kind != "easytier" && kind != "zerotier" {
		if _, exists := definition["planet-asset"]; exists {
			return false, webAPIError(400, "invalid_planet_reference", "planet-asset is only valid for ZeroTier")
		}
		return false, nil
	}
	if err := webStatefulRawPaths(definition); err != nil {
		return true, err
	}
	name, _ := definition["name"].(string)
	if !webStatefulName(name) {
		return true, webAPIError(400, "invalid_stateful_resource", "stateful resource must have an exact outbound name")
	}
	if raw, exists := definition["planet-asset"]; exists {
		if kind != "zerotier" {
			return true, webAPIError(400, "invalid_planet_reference", "planet-asset is only valid for ZeroTier")
		}
		if _, err := webStatefulParsePlanetReference(raw); err != nil {
			return true, err
		}
	}
	if _, err := webStatefulSecurityDomain(definition, "default"); err != nil {
		return true, err
	}
	return true, webStatefulValidateOptions(definition)
}

func webStatefulSecurityDomain(definition map[string]any, planetDomain string) (string, error) {
	kind, _ := definition["type"].(string)
	domain := map[string]any{"protocol": kind}
	var textFields, boolFields []string
	switch kind {
	case "tailscale":
		textFields = []string{"auth-key"}
		boolFields = []string{"ephemeral"}
		control, ok := definition["control-url"].(string)
		if definition["control-url"] != nil && !ok {
			return "", webAPIError(400, "invalid_stateful_resource", "control-url must be a string")
		}
		if control == "" {
			control = "https://controlplane.tailscale.com/"
		}
		origin, err := webStatefulCanonicalURL(control)
		if err != nil {
			return "", err
		}
		domain["control"] = origin
	case "easytier":
		textFields = []string{"network-name", "network-secret", "local-private-key", "local-public-key"}
		boolFields = []string{"secure-mode"}
		domain["peer-public-keys"] = webStatefulEasyTierPeerKeys(definition)
	case "zerotier":
		textFields = []string{"network", "identity-secret"}
		domain["planet"] = planetDomain
	default:
		return "", webAPIError(400, "invalid_stateful_resource", "unknown stateful protocol")
	}
	for _, field := range textFields {
		value := ""
		if raw, exists := definition[field]; exists {
			var valid bool
			value, valid = raw.(string)
			if !valid {
				return "", webAPIError(400, "invalid_stateful_resource", "stateful identity fields must be strings")
			}
		}
		if kind == "zerotier" && field == "network" {
			value = strings.ToLower(value)
		}
		domain[field] = value
	}
	for _, field := range boolFields {
		value := false
		if kind == "easytier" && field == "secure-mode" {
			value = domain["local-private-key"] != "" || domain["local-public-key"] != "" || len(webStatefulEasyTierPeerKeys(definition)) > 0
		}
		if raw, exists := definition[field]; exists {
			var valid bool
			value, valid = raw.(bool)
			if !valid {
				return "", webAPIError(400, "invalid_stateful_resource", "stateful identity flags must be booleans")
			}
		}
		domain[field] = value
	}
	return webStatefulJSONDigest(domain), nil
}

func WebAPIPlanStatefulResources(profileID WebAPIID, inputs []WebAPIStatefulInput) (WebAPIStatefulPlan, error) {
	plan := WebAPIStatefulPlan{ProfileID: profileID, Resources: []WebAPIStatefulResource{}}
	if _, err := webAPIInteger(string(profileID), true); err != nil {
		return plan, webAPIError(400, "invalid_stateful_scope", "stateful plan requires a committed profile identity")
	}
	if len(inputs) > WebAPIStatefulMaxResources {
		return plan, webAPIError(413, "stateful_budget_exceeded", "stateful scope budget exceeded")
	}
	scopes := map[string]bool{}
	byOwner := map[WebAPIID]map[string]WebAPIStatefulPlanetAsset{}
	for _, input := range inputs {
		if err := webStatefulScope(input.Scope); err != nil {
			return plan, err
		}
		if input.Scope.Inline && input.Scope.SourceProfileID != profileID {
			return plan, webAPIError(400, "invalid_stateful_scope", "inline providers must belong to their parent profile")
		}
		scopeKey := webStatefulJSONDigest(input.Scope)
		if scopes[scopeKey] {
			return plan, webAPIError(400, "duplicate_stateful_scope", "stateful scopes must be unique")
		}
		scopes[scopeKey] = true
		owner := webStatefulAssetOwner(profileID, input.Scope)
		if byOwner[owner] == nil {
			byOwner[owner] = map[string]WebAPIStatefulPlanetAsset{}
		}
		for _, asset := range input.PlanetAssets {
			if asset.ProfileID != owner {
				return plan, webAPIError(409, "planet_asset_owner_mismatch", "planet asset belongs to a different profile scope")
			}
			if previous, exists := byOwner[owner][asset.Reference.SHA256]; exists {
				if previous.Reference != asset.Reference || !bytes.Equal(previous.Bytes, asset.Bytes) {
					return plan, webAPIError(409, "planet_asset_digest_mismatch", "one planet reference names different bytes")
				}
				continue
			}
			byOwner[owner][asset.Reference.SHA256] = asset
		}
	}
	validated := map[WebAPIID]map[string]WebAPIStatefulPlanetInfo{}
	for owner, assets := range byOwner {
		all := make([]WebAPIStatefulPlanetAsset, 0, len(assets))
		for _, asset := range assets {
			all = append(all, asset)
		}
		var err error
		validated[owner], err = WebAPIValidateStatefulPlanetAssets(all)
		if err != nil {
			return plan, err
		}
	}
	for _, input := range inputs {
		assets := validated[webStatefulAssetOwner(profileID, input.Scope)]
		names := map[string]bool{}
		for _, definition := range input.Proxies {
			stateful, err := WebAPIValidateStatefulDefinition(definition)
			if err != nil {
				return plan, err
			}
			if !stateful {
				continue
			}
			name := definition["name"].(string)
			if names[name] {
				return plan, webAPIError(400, "duplicate_stateful_resource", "stateful outbound names must be unique within their scope")
			}
			names[name] = true
			if len(plan.Resources) >= WebAPIStatefulMaxResources {
				return plan, webAPIError(413, "stateful_budget_exceeded", "stateful resource budget exceeded")
			}
			planetDomain, planetDigest := "default", ""
			if raw, exists := definition["planet-asset"]; exists {
				reference, _ := webStatefulParsePlanetReference(raw)
				info, exists := assets[reference.SHA256]
				if !exists {
					return plan, webAPIError(409, "planet_asset_unavailable", "referenced purpose-bound planet asset is not committed for this profile")
				}
				planetDomain, planetDigest = info.TrustDomain, info.SHA256
			}
			domain, err := webStatefulSecurityDomain(definition, planetDomain)
			if err != nil {
				return plan, err
			}
			resource := WebAPIStatefulResource{Scope: input.Scope, Name: name, ResourceID: input.ResourceIDs[name], Protocol: definition["type"].(string), SecurityDomain: domain, PlanetSHA256: planetDigest}
			if resource.ResourceID != "" {
				if _, err := webAPIInteger(string(resource.ResourceID), true); err != nil {
					return plan, webAPIError(400, "invalid_stateful_resource", "resource identity must be a server-owned decimal identity")
				}
			}
			plan.Resources = append(plan.Resources, resource)
		}
	}
	sort.Slice(plan.Resources, func(i, j int) bool {
		return webStatefulResourceKey(plan.Resources[i]) < webStatefulResourceKey(plan.Resources[j])
	})
	return plan, webStatefulValidatePlan(plan)
}

func webStatefulAssetOwner(profileID WebAPIID, scope WebAPIStatefulScope) WebAPIID {
	if scope.Kind == WebAPIStatefulProfileProvider {
		return scope.SourceProfileID
	}
	return profileID
}

func webStatefulResourceKey(resource WebAPIStatefulResource) string {
	name := resource.Name
	if resource.ResourceID != "" {
		name = ""
	}
	return webStatefulJSONDigest(struct {
		Scope    WebAPIStatefulScope
		Name     string
		ID       WebAPIID
		Protocol string
	}{resource.Scope, name, resource.ResourceID, resource.Protocol})
}

func webStatefulValidatePlan(plan WebAPIStatefulPlan) error {
	if _, err := webAPIInteger(string(plan.ProfileID), true); err != nil {
		return webAPIError(400, "invalid_stateful_scope", "stateful plan requires a profile identity")
	}
	if len(plan.Resources) > WebAPIStatefulMaxResources {
		return webAPIError(413, "stateful_budget_exceeded", "stateful resource budget exceeded")
	}
	seen := map[string]bool{}
	for _, resource := range plan.Resources {
		if err := webStatefulScope(resource.Scope); err != nil {
			return err
		}
		if resource.Scope.Inline && resource.Scope.SourceProfileID != plan.ProfileID {
			return webAPIError(400, "invalid_stateful_scope", "inline providers must belong to their parent profile")
		}
		if !webStatefulName(resource.Name) || !webStatefulHex(resource.SecurityDomain) || resource.Protocol != "tailscale" && resource.Protocol != "easytier" && resource.Protocol != "zerotier" || resource.PlanetSHA256 != "" && (resource.Protocol != "zerotier" || !webStatefulHex(resource.PlanetSHA256)) {
			return webAPIError(400, "invalid_stateful_resource", "stateful resource identity is invalid")
		}
		if resource.ResourceID != "" {
			if _, err := webAPIInteger(string(resource.ResourceID), true); err != nil {
				return webAPIError(400, "invalid_stateful_resource", "stateful resource ID is invalid")
			}
		}
		key := webStatefulResourceKey(resource)
		if seen[key] {
			return webAPIError(400, "duplicate_stateful_resource", "stateful resource identities must be unique")
		}
		seen[key] = true
	}
	return nil
}
