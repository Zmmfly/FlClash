//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	RC "github.com/metacubex/mihomo/rules/common"
	"go.yaml.in/yaml/v3"
)

type webCustomSourceSnapshot struct {
	Target    WebAPIProfile
	Profiles  []WebAPIProfile
	Providers []WebAPIClashProvider
	Proxies   []WebAPICustomProxy
	Groups    []WebAPIProxyGroup
	Contents  map[WebAPIID]WebAPIContent
	States    map[WebAPIID]WebAPIProfileCatalog
}

type webCustomSourceDependency struct {
	Reference string
	Profiles  bool
	Index     int
	URL       string
	Headers   map[string][]string
}

type webCustomSourceParsed struct {
	Custom          WebAPICustomProfileImportDTO
	Dependencies    []webCustomSourceDependency
	DroppedSubRules int
}

func webCustomSourceError() error {
	return webAPIError(400, "invalid_custom_import", "custom import contains an invalid or unsupported entry")
}

func webCustomSourceDocument(data []byte) (map[string]any, *yaml.Node, error) {
	if len(data) > WebAPIMaxContentBytes {
		return nil, nil, webAPIError(413, "content_too_large", "custom import exceeds 32 MiB")
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, nil, webCustomSourceError()
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var node yaml.Node
	if decoder.Decode(&node) != nil {
		return nil, nil, webCustomSourceError()
	}
	if err := WebAPIValidateYAMLWork(&node); err != nil {
		return nil, nil, err
	}
	if len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return nil, nil, webCustomSourceError()
	}
	var extra yaml.Node
	if decoder.Decode(&extra) != io.EOF {
		return nil, nil, webCustomSourceError()
	}
	retagShortIDs(&node)
	var root map[string]any
	if node.Decode(&root) != nil {
		return nil, nil, webCustomSourceError()
	}
	remaining := WebAPIBackupExpandedLimit
	if err := webCustomSourceValueBudget(root, &remaining); err != nil {
		return nil, nil, err
	}
	for _, key := range []string{"proxies", "proxy-groups", "proxy-providers", "rule-providers", "rules", "dns", "ntp", "sniffer"} {
		if _, exists := root[key]; exists {
			return root, node.Content[0], nil
		}
	}
	return nil, nil, webCustomSourceError()
}

func webCustomSourceMap(value any) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	result, ok := value.(map[string]any)
	if !ok {
		return nil, webCustomSourceError()
	}
	return result, nil
}

func webCustomSourceList(value any) ([]any, error) {
	if value == nil {
		return []any{}, nil
	}
	result, ok := value.([]any)
	if !ok {
		return nil, webCustomSourceError()
	}
	return result, nil
}

func webCustomSourceText(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func webCustomSourceDecode(value any, target any) error {
	data, err := json.Marshal(value)
	if err != nil || json.Unmarshal(data, target) != nil {
		return webCustomSourceError()
	}
	return nil
}

func webCustomSourceSectionNode(node *yaml.Node, section string) *yaml.Node {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.AliasNode {
		return webCustomSourceSectionNode(node.Alias, section)
	}
	if node.Kind == yaml.SequenceNode {
		for _, child := range node.Content {
			if found := webCustomSourceSectionNode(child, section); found != nil {
				return found
			}
		}
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == section {
			return node.Content[i+1]
		}
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Tag == "!!merge" {
			if found := webCustomSourceSectionNode(node.Content[i+1], section); found != nil {
				return found
			}
		}
	}
	return nil
}

func webCustomSourceSectionOrder(node *yaml.Node, section string, values map[string]any) []string {
	selected := webCustomSourceSectionNode(node, section)
	result := []string{}
	seen := map[string]bool{}
	var visit func(*yaml.Node)
	visit = func(n *yaml.Node) {
		if n == nil {
			return
		}
		if n.Kind == yaml.AliasNode {
			visit(n.Alias)
			return
		}
		if n.Kind == yaml.SequenceNode {
			for _, child := range n.Content {
				visit(child)
			}
			return
		}
		if n.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Tag == "!!merge" {
				visit(n.Content[i+1])
				continue
			}
			if _, exists := values[key.Value]; exists && !seen[key.Value] {
				seen[key.Value] = true
				result = append(result, key.Value)
			}
		}
	}
	visit(selected)
	remaining := []string{}
	for key := range values {
		if !seen[key] {
			remaining = append(remaining, key)
		}
	}
	sort.Strings(remaining)
	return append(result, remaining...)
}

var webCustomSourceLabelSuffix = regexp.MustCompile(`\((\d{1,9})\)$`)

func webCustomSourceLabel(name string, taken map[string]bool, next map[string]int) string {
	candidate, stem, index := name, name, 0
	if match := webCustomSourceLabelSuffix.FindStringSubmatchIndex(name); match != nil {
		stem = name[:match[0]]
		index, _ = strconv.Atoi(name[match[2]:match[3]])
	}
	if taken[candidate] {
		if next[stem] > index {
			index = next[stem]
		}
		for {
			index++
			candidate = stem + "(" + strconv.Itoa(index) + ")"
			if !taken[candidate] {
				break
			}
		}
		next[stem] = index
	}
	taken[candidate] = true
	return candidate
}

func webCustomSourceFreeName(name string, taken map[string]bool, next map[string]int) string {
	candidate := name
	if taken[candidate] {
		index := next[name]
		for {
			index++
			candidate = fmt.Sprintf("%s-%02d", name, index)
			if !taken[candidate] {
				break
			}
		}
		next[name] = index
	}
	taken[candidate] = true
	return candidate
}

func webCustomSourceProviders(root map[string]any, node *yaml.Node, snapshot webCustomSourceSnapshot, profiles bool, parsed *webCustomSourceParsed) (map[string]string, map[string]WebAPIProxyProviderOptions, error) {
	section, payloadKey := "rule-providers", "payload"
	taken, byURL, bySource := map[string]bool{}, map[string]string{}, map[string]string{}
	if profiles {
		section, payloadKey = "proxy-providers", "proxies"
		for _, profile := range snapshot.Profiles {
			label := webAPIEffectiveProfileLabel(profile)
			taken[profile.Label], taken[label] = true, true
			if profile.Type == "url" {
				if _, exists := byURL[profile.URL]; !exists {
					byURL[profile.URL] = label
				}
				bySource[profile.URL+"\x00"+profile.AgeSecretKey] = label
			}
		}
	} else {
		for _, provider := range snapshot.Providers {
			taken[provider.Label] = true
			if provider.URL != "" {
				if _, exists := byURL[provider.URL]; !exists {
					byURL[provider.URL] = provider.Label
				}
				bySource[provider.URL+"\x00"] = provider.Label
			}
		}
	}
	values, err := webCustomSourceMap(root[section])
	if err != nil {
		return nil, nil, err
	}
	if len(values) > 1000 {
		return nil, nil, webAPIError(413, "import_complexity_limit", "custom import has too many providers")
	}
	names, options := map[string]string{}, map[string]WebAPIProxyProviderOptions{}
	nextLabels := map[string]int{}
	for _, name := range webCustomSourceSectionOrder(node, section, values) {
		if !webAPICatalogName(name) {
			return nil, nil, webCustomSourceError()
		}
		provider, err := webCustomSourceMap(values[name])
		if err != nil {
			return nil, nil, err
		}
		kind := webCustomSourceText(provider["type"])
		if value, exists := provider["proxy"]; exists && value != "" {
			return nil, nil, webAPIError(400, "unsupported_provider_source", "provider fetch routing cannot be silently discarded")
		}
		if value, exists := provider["override"]; exists {
			mapping, err := webCustomSourceMap(value)
			if err != nil {
				return nil, nil, err
			}
			if _, exists := mapping["dialer-proxy"]; exists {
				return nil, nil, webAPIError(400, "unsupported_provider_dependency", "provider dialer override is not representable by the custom profile model")
			}
		}
		item := WebAPICustomImportedProvider{}
		headers := map[string][]string{}
		switch kind {
		case "http":
			item.URL = webCustomSourceText(provider["url"])
			if profiles {
				item.AgeSecretKey = strings.TrimSpace(webCustomSourceText(provider["age-secret-key"]))
			}
			if raw, exists := provider["header"]; exists {
				if err := webCustomSourceDecode(raw, &headers); err != nil {
					return nil, nil, err
				}
			}
			_, _, headers, err = webSourceIdentity(item.URL, headers)
			if err != nil {
				return nil, nil, err
			}
		case "inline":
			payload, err := webCustomSourceList(provider["payload"])
			if err != nil {
				return nil, nil, err
			}
			content, err := yaml.Marshal(map[string]any{payloadKey: payload})
			if err != nil {
				return nil, nil, webCustomSourceError()
			}
			item.Content = string(content)
		case "file":
			// Native imports file providers as empty app resources; their paths are never read.
			item.Content = payloadKey + ": []\n"
		default:
			return nil, nil, webAPIError(400, "unsupported_provider_source", "custom import requires inline, file placeholder, or HTTP provider declarations")
		}
		label := ""
		if item.URL != "" && len(headers) == 0 {
			if item.AgeSecretKey == "" {
				label = byURL[item.URL]
			} else {
				label = bySource[item.URL+"\x00"+item.AgeSecretKey]
			}
		}
		if label == "" {
			label = webCustomSourceLabel(name, taken, nextLabels)
			if !webAPICatalogName(label) {
				return nil, nil, webCustomSourceError()
			}
			item.Label = label
			index := len(parsed.Custom.RuleProviders)
			if profiles {
				index = len(parsed.Custom.ProxyProviders)
				parsed.Custom.ProxyProviders = append(parsed.Custom.ProxyProviders, item)
			} else {
				parsed.Custom.RuleProviders = append(parsed.Custom.RuleProviders, item)
			}
			if item.URL != "" {
				if len(headers) == 0 {
					bySource[item.URL+"\x00"+item.AgeSecretKey] = label
					if _, exists := byURL[item.URL]; !exists {
						byURL[item.URL] = label
					}
				}
				ref := fmt.Sprintf("%s-%d", section, index)
				parsed.Dependencies = append(parsed.Dependencies, webCustomSourceDependency{Reference: ref, Profiles: profiles, Index: index, URL: item.URL, Headers: headers})
			}
		}
		names[name] = label
		if profiles {
			option := map[string]any{}
			for _, key := range []string{"filter", "exclude-filter"} {
				if value, exists := provider[key]; exists {
					option[key] = webCustomSourceText(value)
				}
			}
			for _, key := range []string{"health-check", "override"} {
				if value, exists := provider[key]; exists {
					mapping, err := webCustomSourceMap(value)
					if err != nil {
						return nil, nil, err
					}
					if key == "health-check" && mapping["enable"] != true {
						continue
					}
					copy := map[string]any{}
					for name, item := range mapping {
						if name != "enable" {
							copy[name] = item
						}
					}
					for _, field := range []string{"url", "expected-status", "additional-prefix", "additional-suffix", "ip-version"} {
						if value, exists := copy[field]; exists {
							copy[field] = webCustomSourceText(value)
						}
					}
					option[key] = copy
				}
			}
			var typed WebAPIProxyProviderOptions
			if err := webCustomSourceDecode(option, &typed); err != nil {
				return nil, nil, err
			}
			if len(option) > 0 {
				options[label] = typed
			}
		}
	}
	return names, options, nil
}

func webCustomSourceComparable(value any) any {
	switch item := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for key, value := range item {
			result[key] = webCustomSourceComparable(value)
		}
		return result
	case []any:
		result := make([]any, len(item))
		for i, value := range item {
			result[i] = webCustomSourceComparable(value)
		}
		return result
	case int, int64, uint64, float64, json.Number:
		return fmt.Sprint(item)
	default:
		return value
	}
}

func webCustomSourceProxyKey(proxy map[string]any) string {
	copy := map[string]any{}
	for key, value := range proxy {
		if key != "name" && key != "dialer-proxy" {
			copy[key] = webCustomSourceComparable(value)
		}
	}
	data, _ := json.Marshal(copy)
	return string(data)
}

func webCustomSourceIdentity(proxy map[string]any) string {
	keys := map[string][]string{"ss": {"password"}, "ssr": {"password"}, "vmess": {"uuid"}, "vless": {"uuid"}, "tuic": {"uuid", "password"}, "trojan": {"password"}, "anytls": {"password"}, "hysteria2": {"password"}, "hysteria": {"auth-str"}, "snell": {"psk"}, "sudoku": {"key"}, "wireguard": {"private-key"}, "masque": {"private-key"}, "mieru": {"username", "password"}, "socks5": {"username", "password"}, "http": {"username", "password"}, "ssh": {"username", "password"}, "shadowquic": {"username", "password"}, "trusttunnel": {"username", "password"}, "gost-relay": {"username", "password"}, "openvpn": {"username", "password"}}[webCustomSourceText(proxy["type"])]
	if len(keys) == 0 || webCustomSourceText(proxy["server"]) == "" {
		return ""
	}
	parts := []any{webCustomSourceText(proxy["name"]), webCustomSourceText(proxy["type"]), webCustomSourceText(proxy["server"]), webCustomSourceText(proxy["port"])}
	for _, key := range keys {
		if proxy[key] == nil {
			parts = append(parts, nil)
		} else {
			parts = append(parts, webCustomSourceText(proxy[key]))
		}
	}
	data, _ := json.Marshal(parts)
	return string(data)
}

func webCustomSourceProxies(proxies, groups []any, snapshot webCustomSourceSnapshot, parsed *webCustomSourceParsed) (map[string]string, []string, error) {
	held, identities := map[string]string{}, map[string]bool{}
	existing, otherGroups := map[string]bool{}, map[string]bool{}
	for _, item := range snapshot.Proxies {
		var proxy map[string]any
		if err := webCustomSourceDecode(item.Definition, &proxy); err != nil {
			return nil, nil, err
		}
		name := webCustomSourceText(proxy["name"])
		existing[name] = true
		key := webCustomSourceProxyKey(proxy)
		if _, exists := held[key]; !exists {
			held[key] = name
		}
		if identity := webCustomSourceIdentity(proxy); identity != "" {
			identities[identity] = true
		}
	}
	for _, group := range snapshot.Groups {
		if group.ProfileID != snapshot.Target.ID {
			otherGroups[group.Definition.Name] = true
		}
	}
	proxyMaps, groupMaps := []map[string]any{}, []map[string]any{}
	for _, item := range proxies {
		value, err := webCustomSourceMap(item)
		if err != nil {
			return nil, nil, err
		}
		proxyMaps = append(proxyMaps, value)
	}
	for _, item := range groups {
		value, err := webCustomSourceMap(item)
		if err != nil {
			return nil, nil, err
		}
		groupMaps = append(groupMaps, value)
	}
	nameOf := func(proxy map[string]any) string {
		name := webCustomSourceText(proxy["name"])
		if name == "" {
			name = webCustomSourceText(proxy["server"])
			if name == "" {
				name = webCustomSourceText(proxy["type"])
			}
		}
		return name
	}
	kept, taken, used := map[string]bool{}, map[string]bool{}, map[string]bool{}
	nextNames := map[string]int{}
	for _, proxy := range proxyMaps {
		name := nameOf(proxy)
		if !existing[name] && !otherGroups[name] {
			kept[name] = true
		}
	}
	for _, group := range groupMaps {
		name := webCustomSourceText(group["name"])
		if !existing[name] {
			kept[name] = true
		}
	}
	for _, name := range []string{"DIRECT", "REJECT", "REJECT-DROP", "PASS", "PASS-RULE", "COMPATIBLE", "GLOBAL"} {
		taken[name] = true
	}
	for name := range existing {
		taken[name] = true
	}
	for name := range otherGroups {
		taken[name] = true
	}
	for name := range kept {
		taken[name] = true
	}
	free := func(name string) string {
		if kept[name] && !used[name] {
			used[name] = true
			return name
		}
		return webCustomSourceFreeName(name, taken, nextNames)
	}
	names, repeats := map[string]string{}, map[string]string{}
	proxyNames, groupNames := []string{}, []string{}
	for _, proxy := range proxyMaps {
		original, name := webCustomSourceText(proxy["name"]), nameOf(proxy)
		if !webAPICatalogName(name) {
			return nil, nil, webCustomSourceError()
		}
		copy := map[string]any{}
		for key, value := range proxy {
			if key != "dialer-proxy" {
				copy[key] = value
			}
		}
		copy["name"] = name
		key := webCustomSourceProxyKey(copy)
		imported := held[key]
		if imported == "" && identities[webCustomSourceIdentity(copy)] {
			imported = name
		}
		if imported == "" {
			imported = repeats[name+"\x00"+key]
		}
		if imported == "" {
			if otherGroups[name] || webAPIContains([]string{"DIRECT", "REJECT", "REJECT-DROP", "PASS", "PASS-RULE", "COMPATIBLE", "GLOBAL"}, name) {
				imported = webCustomSourceFreeName(name, taken, nextNames)
			} else {
				imported = free(name)
			}
			copy["name"] = imported
			var definition WebAPIProxyDefinition
			if err := webCustomSourceDecode(copy, &definition); err != nil {
				return nil, nil, err
			}
			if err := webAPIValidateProxy(definition, nil); err != nil {
				return nil, nil, err
			}
			parsed.Custom.Proxies = append(parsed.Custom.Proxies, definition)
			repeats[name+"\x00"+key] = imported
		}
		if _, exists := names[original]; !exists {
			names[original] = imported
		}
		proxyNames = append(proxyNames, imported)
	}
	for _, group := range groupMaps {
		name := webCustomSourceText(group["name"])
		if !webAPICatalogName(name) {
			return nil, nil, webCustomSourceError()
		}
		imported := free(name)
		if _, exists := names[name]; !exists {
			names[name] = imported
		}
		groupNames = append(groupNames, imported)
	}
	for index, proxy := range proxyMaps {
		if value, exists := proxy["dialer-proxy"]; exists {
			target, ok := value.(string)
			if !ok {
				return nil, nil, webCustomSourceError()
			}
			if target != "" {
				if renamed, exists := names[target]; exists {
					target = renamed
				}
				if _, exists := parsed.Custom.Dialers[proxyNames[index]]; !exists {
					parsed.Custom.Dialers[proxyNames[index]] = target
				}
			}
		}
	}
	return names, groupNames, nil
}

func webCustomSourceRenamedList(value any, names map[string]string) ([]string, error) {
	list, err := webCustomSourceList(value)
	if err != nil {
		return nil, err
	}
	result, seen := []string{}, map[string]bool{}
	for _, item := range list {
		name := webCustomSourceText(item)
		if renamed, exists := names[name]; exists {
			name = renamed
		}
		if !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
	}
	return result, nil
}

func webCustomSourceParse(data []byte, snapshot webCustomSourceSnapshot) (webCustomSourceParsed, error) {
	parsed := webCustomSourceParsed{Custom: WebAPICustomProfileImportDTO{ProxyProviders: []WebAPICustomImportedProvider{}, RuleProviders: []WebAPICustomImportedProvider{}, Proxies: []WebAPIProxyDefinition{}, Dialers: map[string]string{}, ProxyGroups: []WebAPIProxyGroupInput{}, Rules: []WebAPIRuleInput{}}}
	root, node, err := webCustomSourceDocument(data)
	if err != nil {
		return parsed, err
	}
	providerNames, options, err := webCustomSourceProviders(root, node, snapshot, true, &parsed)
	if err != nil {
		return parsed, err
	}
	ruleNames, _, err := webCustomSourceProviders(root, node, snapshot, false, &parsed)
	if err != nil {
		return parsed, err
	}
	proxies, err := webCustomSourceList(root["proxies"])
	if err != nil {
		return parsed, err
	}
	groups, err := webCustomSourceList(root["proxy-groups"])
	if err != nil {
		return parsed, err
	}
	if len(proxies)+len(groups) > 100000 {
		return parsed, webAPIError(413, "import_complexity_limit", "custom import has too many outbounds")
	}
	names, importedGroups, err := webCustomSourceProxies(proxies, groups, snapshot, &parsed)
	if err != nil {
		return parsed, err
	}
	for index, value := range groups {
		group, err := webCustomSourceMap(value)
		if err != nil {
			return parsed, err
		}
		group["name"] = importedGroups[index]
		for key, replacements := range map[string]map[string]string{"proxies": names, "use": providerNames} {
			if value, exists := group[key]; exists {
				group[key], err = webCustomSourceRenamedList(value, replacements)
				if err != nil {
					return parsed, err
				}
			}
		}
		for _, key := range []string{"name", "url", "filter", "exclude-filter", "exclude-type", "expected-status", "hash-key", "default-selected", "empty-fallback", "icon"} {
			if value, exists := group[key]; exists {
				group[key] = webCustomSourceText(value)
			}
		}
		for _, key := range []string{"default-selected", "empty-fallback"} {
			if name, ok := group[key].(string); ok {
				if next, exists := names[name]; exists {
					group[key] = next
				}
			}
		}
		positions, err := webCustomSourceMap(group["use-position"])
		if err != nil {
			return parsed, err
		}
		delete(group, "use-position")
		var definition WebAPIProxyGroupDefinition
		if err := webCustomSourceDecode(group, &definition); err != nil {
			return parsed, err
		}
		for name, value := range positions {
			position, e := strconv.ParseInt(webCustomSourceText(value), 10, 32)
			if e != nil {
				continue
			}
			if next, exists := providerNames[name]; exists {
				name = next
			}
			if webAPIContains(definition.Use, name) && position < int64(len(definition.Proxies)) {
				if position < 0 {
					position = 0
				}
				if definition.UsePosition == nil {
					definition.UsePosition = map[string]int32{}
				}
				definition.UsePosition[name] = int32(position)
			}
		}
		if err := webAPIValidateGroup(definition); err != nil {
			return parsed, err
		}
		layout := []string{}
		if value, exists := group["memberLayout"]; exists {
			if err := webCustomSourceDecode(value, &layout); err != nil {
				return parsed, err
			}
		}
		parsed.Custom.ProxyGroups = append(parsed.Custom.ProxyGroups, WebAPIProxyGroupInput{Definition: definition, MemberLayout: layout})
	}
	rules, err := webCustomSourceList(root["rules"])
	if err != nil {
		return parsed, err
	}
	for _, value := range rules {
		line, ok := value.(string)
		if !ok || len(line) > 65536 || strings.ContainsAny(line, "\x00\r\n") {
			return parsed, webCustomSourceError()
		}
		kind, payload, target, parameters := RC.ParseRulePayload(line, true)
		if kind == "MATCH" && strings.Count(line, ",") != 1 {
			return parsed, webCustomSourceError()
		}
		action := strings.ReplaceAll(kind, "-", "_")
		if !webAPIContains(webAPIRuleActions, action) {
			return parsed, webCustomSourceError()
		}
		if action == "SUB_RULE" {
			parsed.DroppedSubRules++
			continue
		}
		if next, exists := names[target]; exists {
			target = next
		}
		rule := WebAPIRuleInput{RuleAction: action, RuleTarget: &target}
		if action == "RULE_SET" {
			if next, exists := ruleNames[payload]; exists {
				payload = next
			}
			rule.RuleProvider = &payload
		} else if action != "MATCH" {
			renamed, e := webAPIRenameRuleSetText(payload, ruleNames)
			if e != nil {
				return parsed, webCustomSourceError()
			}
			rule.Content = &renamed
		}
		for _, parameter := range parameters {
			switch parameter {
			case "src":
				if rule.Src {
					return parsed, webCustomSourceError()
				}
				rule.Src = true
			case "no-resolve":
				if rule.NoResolve {
					return parsed, webCustomSourceError()
				}
				rule.NoResolve = true
			default:
				return parsed, webCustomSourceError()
			}
		}
		if err := webAPIValidateStoredRuleSyntax(rule); err != nil {
			return parsed, err
		}
		parsed.Custom.Rules = append(parsed.Custom.Rules, rule)
	}
	parsed.Custom.Overrides, err = webCustomSourceOverrides(root, options, names, ruleNames)
	if err != nil {
		return parsed, err
	}
	if len(parsed.Custom.ProxyProviders)+len(parsed.Custom.RuleProviders) > 1000 || len(parsed.Custom.Proxies)+len(parsed.Custom.ProxyGroups)+len(parsed.Custom.Rules) > 100000 {
		return parsed, webAPIError(413, "import_complexity_limit", "custom import has too many resources")
	}
	return parsed, nil
}
