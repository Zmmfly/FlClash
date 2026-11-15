//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/metacubex/age"
	"github.com/metacubex/age/armor"
	"github.com/metacubex/mihomo/config"
	"go.yaml.in/yaml/v3"
)

func webContentDecrypt(data []byte, secret string) ([]byte, error) {
	armored := bytes.HasPrefix(data, []byte(armor.Header))
	binary := bytes.HasPrefix(data, []byte("age-encryption.org/v1"))
	if !armored && !binary {
		return data, nil
	}
	if secret == "" {
		return nil, webAPIError(400, "age_secret_key_required", "an age identity is required")
	}
	if len(secret) > 4096 {
		return nil, webAPIError(400, "invalid_age_identity", "invalid age identity")
	}
	identities, err := age.ParseIdentities(strings.NewReader(secret))
	if err != nil {
		return nil, webAPIError(400, "invalid_age_identity", "invalid age identity")
	}
	for _, identity := range identities {
		switch identity.(type) {
		case *age.X25519Identity, *age.HybridIdentity:
		default:
			return nil, webAPIError(400, "invalid_age_identity", "unsupported age identity")
		}
	}
	var source io.Reader = bytes.NewReader(data)
	if armored {
		source = armor.NewReader(source)
	}
	r, err := age.Decrypt(source, identities...)
	if err != nil {
		return nil, webAPIError(400, "age_decryption_failed", "age identity did not decrypt this content")
	}
	return webContentReadLimit(r, WebAPIMaxContentBytes)
}

func webContentProfile(data []byte, secret string) (*config.RawConfig, WebAPIProfileCatalog, error) {
	var catalog WebAPIProfileCatalog
	if len(data) > WebAPIMaxContentBytes {
		return nil, catalog, webAPIError(413, "content_too_large", "profile exceeds the content limit")
	}
	plain, err := webContentDecompress(data)
	if err != nil {
		return nil, catalog, err
	}
	plain, err = webContentDecrypt(plain, secret)
	if err != nil {
		return nil, catalog, err
	}
	if !utf8.Valid(plain) || bytes.IndexByte(plain, 0) >= 0 {
		return nil, catalog, webAPIError(400, "invalid_profile_content", "profile must be UTF-8 YAML")
	}
	var document yaml.Node
	d := yaml.NewDecoder(bytes.NewReader(plain))
	if d.Decode(&document) != nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, catalog, webAPIError(400, "invalid_profile_content", "profile must be one YAML mapping")
	}
	var second yaml.Node
	if d.Decode(&second) != io.EOF {
		return nil, catalog, webAPIError(400, "invalid_profile_content", "profile contains multiple YAML documents")
	}
	if err := WebAPIValidateYAMLWork(&document); err != nil {
		return nil, catalog, err
	}
	recognized := false
	for i := 0; i+1 < len(document.Content[0].Content); i += 2 {
		key := document.Content[0].Content[i].Value
		if webAPIContains([]string{"proxies", "proxy-groups", "proxy-providers", "rule-providers", "rules", "sub-rules", "dns", "ntp", "sniffer", "hosts", "mixed-port", "port", "socks-port", "redir-port", "tproxy-port", "mode", "tun", "listeners", "tunnels", "external-controller", "log-level", "ipv6", "allow-lan"}, key) {
			recognized = true
			break
		}
	}
	if !recognized {
		return nil, catalog, webAPIError(400, "invalid_profile_content", "document has no recognized profile section")
	}
	retagShortIDs(&document)
	raw := config.DefaultRawConfig()
	if document.Decode(raw) != nil {
		return nil, catalog, webAPIError(400, "invalid_profile_content", "profile YAML has an invalid typed configuration")
	}
	if err = webContentValidateStateful(raw); err != nil {
		return nil, catalog, err
	}
	if err = ValidateProxyDependencies(raw); err != nil {
		return nil, catalog, webAPIError(400, "outbound_policy_invalid", "profile outbound graph is invalid")
	}
	for _, proxy := range raw.Proxy {
		name, _ := proxy["name"].(string)
		if !webAPICatalogName(name) {
			return nil, catalog, webAPIError(400, "invalid_profile_content", "profile outbound has an invalid name")
		}
		catalog.OutboundNames = append(catalog.OutboundNames, name)
	}
	icons := map[string]bool{}
	for _, group := range raw.ProxyGroup {
		name, _ := group["name"].(string)
		if !webAPICatalogName(name) {
			return nil, catalog, webAPIError(400, "invalid_profile_content", "profile group has an invalid name")
		}
		catalog.OutboundNames = append(catalog.OutboundNames, name)
		if icon, ok := group["icon"].(string); ok && !icons[icon] {
			if _, err := webContentURL(icon); err == nil {
				icons[icon] = true
				catalog.IconURLs = append(catalog.IconURLs, icon)
			}
		}
	}
	for name := range raw.RuleProvider {
		catalog.RuleProviderNames = append(catalog.RuleProviderNames, name)
	}
	for name := range raw.SubRules {
		catalog.SubRuleNames = append(catalog.SubRuleNames, name)
	}
	sort.Strings(catalog.OutboundNames)
	sort.Strings(catalog.RuleProviderNames)
	sort.Strings(catalog.SubRuleNames)
	sort.Strings(catalog.IconURLs)
	return raw, catalog, nil
}

func webContentShareProfile(links []string) ([]byte, error) {
	if len(links) == 0 || len(links) > 10000 {
		return nil, webAPIError(400, "invalid_share_links", "share links must be a bounded nonempty list")
	}
	total := 0
	for _, link := range links {
		total += len(link)
		if total > WebAPIMaxContentBytes {
			return nil, webAPIError(413, "content_too_large", "share links exceed the content limit")
		}
		scheme, _, ok := strings.Cut(link, ":")
		if !ok || !webAPIContains([]string{"hysteria", "hysteria2", "hy2", "hysteria2+realm", "hy2+realm", "tuic", "trojan", "vless", "vmess", "ss", "ssr", "socks", "socks5", "socks5h", "anytls", "mierus"}, scheme) {
			return nil, webAPIError(400, "invalid_share_links", "unsupported share link scheme")
		}
	}
	decoded := handleDecodeShareLinks(LinesParams{Lines: links})
	proxies := []map[string]any{}
	taken := map[string]bool{"DIRECT": true, "REJECT": true, "REJECT-DROP": true, "PASS": true, "PASS-RULE": true, "COMPATIBLE": true, "GLOBAL": true}
	for _, entries := range decoded {
		if len(entries) == 0 {
			return nil, webAPIError(400, "invalid_share_links", "a share link did not contain a readable proxy")
		}
		for _, proxy := range entries {
			name, _ := proxy["name"].(string)
			if !webAPICatalogName(name) {
				name = "Proxy"
			}
			proxy["name"] = webContentUniqueName(name, taken)
			proxies = append(proxies, proxy)
		}
	}
	return yaml.Marshal(map[string]any{"proxies": proxies})
}

func webContentCloneValue[T any](v T) (T, error) {
	var result T
	data, err := json.Marshal(v)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(data, &result)
	return result, err
}

func webContentUniqueName(name string, taken map[string]bool) string {
	candidate := name
	for n := 2; taken[candidate]; n++ {
		candidate = name + " (" + strconv.Itoa(n) + ")"
	}
	taken[candidate] = true
	return candidate
}
