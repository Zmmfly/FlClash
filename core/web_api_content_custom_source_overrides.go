//go:build webpanel && !android

package core

import (
	"strings"
)

func webCustomSourceOverrides(root map[string]any, providers map[string]WebAPIProxyProviderOptions, names, ruleSets map[string]string) (*WebAPIProfileOverrides, error) {
	result := &WebAPIProfileOverrides{ProxyProviders: providers}
	for _, section := range []struct {
		name    string
		allowed []string
		target  any
		keys    *[]string
	}{
		{"dns", webAPIDNSOverrideKeys, &result.DNS, &result.DNSOverrideKeys},
		{"ntp", webAPINTPOverrideKeys, &result.NTP, &result.NTPOverrideKeys},
		{"sniffer", webAPISnifferOverrideKeys, &result.Sniffer, &result.SnifferOverrideKeys},
		{"tun", webAPITunOverrideKeys, &result.Tun, &result.TunOverrideKeys},
	} {
		value, exists := root[section.name]
		if !exists {
			continue
		}
		source, err := webCustomSourceMap(value)
		if err != nil {
			return nil, err
		}
		if section.name == "sniffer" {
			if value, exists := source["sniff"]; exists {
				sniff, err := webCustomSourceMap(value)
				if err != nil {
					return nil, err
				}
				normalized := map[string]any{}
				for name, entry := range sniff {
					name = strings.ToUpper(name)
					if _, exists := normalized[name]; exists {
						return nil, webCustomSourceError()
					}
					definition, err := webCustomSourceMap(entry)
					if err != nil {
						return nil, err
					}
					if value, exists := definition["ports"]; exists {
						ports, err := webCustomSourceList(value)
						if err != nil {
							return nil, err
						}
						text := []string{}
						for _, port := range ports {
							text = append(text, webCustomSourceText(port))
						}
						definition["ports"] = text
					}
					normalized[name] = definition
				}
				source["sniff"] = normalized
			}
		}
		known := map[string]any{}
		for _, key := range section.allowed {
			parts := strings.Split(key, ".")
			value, exists := webAPIOverridePath(source, parts)
			if !exists {
				continue
			}
			if section.name == "dns" && (key == "nameserver-policy" || key == "proxy-server-nameserver-policy") {
				policy, err := webCustomSourceMap(value)
				if err != nil {
					return nil, err
				}
				converted := map[string]string{}
				for name, servers := range policy {
					switch servers := servers.(type) {
					case string:
						converted[name] = servers
					case []any:
						list := []string{}
						for _, server := range servers {
							text, ok := server.(string)
							if !ok {
								return nil, webCustomSourceError()
							}
							list = append(list, text)
						}
						converted[name] = strings.Join(list, ", ")
					default:
						return nil, webCustomSourceError()
					}
				}
				value = converted
			}
			webAPISetOverridePath(known, parts, value)
			*section.keys = append(*section.keys, key)
		}
		if err := webCustomSourceDecode(known, section.target); err != nil {
			return nil, err
		}
	}
	profile := WebAPIProfile{WebAPIProfileInput: WebAPIProfileInput{Type: "custom", Overrides: result}}
	for _, rename := range []struct {
		kind  string
		names map[string]string
	}{{"custom-proxy", names}, {"rule-provider", ruleSets}} {
		if err := webAPIRenameProfileCatalogReferences(&profile, rename.kind, rename.names, nil); err != nil {
			return nil, err
		}
	}
	if err := webAPIValidateProfileOverrides(profile.WebAPIProfileInput); err != nil {
		return nil, err
	}
	return profile.Overrides, nil
}
