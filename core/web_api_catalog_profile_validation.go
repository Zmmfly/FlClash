//go:build webpanel && !android

package core

import (
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/metacubex/mihomo/config"
)

var webAPIDNSOverrideKeys = strings.Fields("enable listen listen-routing-mark use-hosts use-system-hosts ipv6 ipv6-timeout respect-rules prefer-h3 cache-algorithm cache-max-size enhanced-mode fake-ip-range fake-ip-range6 fake-ip-filter fake-ip-filter-mode fake-ip-ttl default-nameserver nameserver-policy nameserver fallback fallback-lazy-query proxy-server-nameserver proxy-server-nameserver-policy direct-nameserver direct-nameserver-follow-policy fallback-filter.geoip fallback-filter.geoip-code fallback-filter.geosite fallback-filter.ipcidr fallback-filter.domain")
var webAPINTPOverrideKeys = strings.Fields("enable server port interval dialer-proxy write-to-system")
var webAPISnifferOverrideKeys = strings.Fields("enable override-destination force-dns-mapping parse-pure-ip force-domain skip-domain skip-src-address skip-dst-address sniff.HTTP sniff.TLS sniff.QUIC")
var webAPITunOverrideKeys = strings.Fields("mtu congestion-controller strict-route route-address route-exclude-address dns-hijack udp-timeout disable-icmp-forwarding gso include-interface exclude-interface include-uid exclude-uid exclude-src-port exclude-dst-port")

func webAPIValidateOverrideKeys(values, allowed []string) error {
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] || !webAPIContains(allowed, value) {
			return webAPIError(400, "invalid_override_key", "unknown or duplicate override key")
		}
		seen[value] = true
	}
	return nil
}

func webAPIValidateProfileOverrides(input WebAPIProfileInput) error {
	if webAPIUnreadable(input) {
		return webAPIError(409, "unreadable_overrides", "unreadable override fields require explicit repair or quarantine import")
	}
	if input.Overrides == nil {
		return nil
	}
	o := input.Overrides
	for _, pair := range []struct{ values, allowed []string }{{o.DNSOverrideKeys, webAPIDNSOverrideKeys}, {o.NTPOverrideKeys, webAPINTPOverrideKeys}, {o.SnifferOverrideKeys, webAPISnifferOverrideKeys}, {o.TunOverrideKeys, webAPITunOverrideKeys}} {
		if err := webAPIValidateOverrideKeys(pair.values, pair.allowed); err != nil {
			return err
		}
	}
	if len(o.DNSOverrideKeys) > 0 && o.DNS == nil || len(o.NTPOverrideKeys) > 0 && o.NTP == nil || len(o.SnifferOverrideKeys) > 0 && o.Sniffer == nil || len(o.TunOverrideKeys) > 0 && o.Tun == nil {
		return webAPIError(400, "missing_override_section", "selected override keys require their typed section")
	}
	if o.DNS != nil {
		d := o.DNS
		if d.IPv6Timeout < 0 || d.CacheMaxSize < 0 || d.FakeIPTTL < 0 {
			return webAPIError(400, "invalid_dns_override", "DNS numeric values cannot be negative")
		}
		if d.CacheAlgorithm != "" && !webAPIContains([]string{"lru", "arc"}, d.CacheAlgorithm) || d.EnhancedMode != "" && !webAPIContains([]string{"normal", "fake-ip", "redir-host", "hosts"}, d.EnhancedMode) || d.FakeIPFilterMode != "" && !webAPIContains([]string{"blacklist", "whitelist", "rule"}, d.FakeIPFilterMode) {
			return webAPIError(400, "invalid_dns_override", "unknown DNS enum value")
		}
		for _, value := range []string{d.FakeIPRange, d.FakeIPRange6} {
			if value != "" {
				if _, err := netip.ParsePrefix(value); err != nil {
					return webAPIError(400, "invalid_dns_override", "invalid fake-IP prefix")
				}
			}
		}
		for _, value := range d.FallbackFilter.IPCIDR {
			if _, err := netip.ParsePrefix(value); err != nil {
				return webAPIError(400, "invalid_dns_override", "invalid fallback IP prefix")
			}
		}
		if d.Listen != "" {
			if _, port, err := net.SplitHostPort(d.Listen); err != nil {
				return webAPIError(400, "invalid_dns_override", "invalid DNS listen address")
			} else if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
				return webAPIError(400, "invalid_dns_override", "invalid DNS listen port")
			}
		}
	}
	if o.NTP != nil {
		n := o.NTP
		if n.Interval < 0 || webAPIContains(o.NTPOverrideKeys, "port") && n.Port == 0 {
			return webAPIError(400, "invalid_ntp_override", "invalid NTP interval or port")
		}
		if webAPIContains(o.NTPOverrideKeys, "server") && (n.Server == "" || strings.ContainsAny(n.Server, "\x00\r\n")) {
			return webAPIError(400, "invalid_ntp_override", "NTP server is required")
		}
	}
	if o.Sniffer != nil {
		for kind, settings := range o.Sniffer.Sniff {
			if !webAPIContains([]string{"HTTP", "TLS", "QUIC"}, kind) {
				return webAPIError(400, "invalid_sniffer_override", "unknown sniffer protocol")
			}
			for _, ports := range settings.Ports {
				if !webAPIRanges(ports, 65535) {
					return webAPIError(400, "invalid_sniffer_override", "invalid sniffer port range")
				}
			}
		}
	}
	if o.Tun != nil {
		t := o.Tun
		if t.MTU < 0 || t.UDPTimeout < 0 || webAPIContains(o.TunOverrideKeys, "mtu") && (t.MTU < 1280 || t.MTU > 65535) || t.CongestionController != "" && !webAPIContains([]string{"cubic", "reno", "bbr", "bbr3"}, t.CongestionController) {
			return webAPIError(400, "invalid_tun_override", "invalid TUN numeric value or congestion controller")
		}
		for _, values := range [][]string{t.RouteAddress, t.RouteExcludeAddress} {
			for _, value := range values {
				if _, err := netip.ParsePrefix(value); err != nil {
					return webAPIError(400, "invalid_tun_override", "invalid TUN route prefix")
				}
			}
		}
		for _, address := range t.DNSHijack {
			value := strings.TrimPrefix(strings.TrimPrefix(address, "tcp://"), "udp://")
			if _, port, err := net.SplitHostPort(value); err != nil {
				return webAPIError(400, "invalid_tun_override", "invalid DNS hijack address")
			} else if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
				return webAPIError(400, "invalid_tun_override", "invalid DNS hijack port")
			}
		}
	}
	if input.Type != "custom" && len(o.ProxyProviders) > 0 {
		return webAPIError(409, "owner_conflict", "app proxy-provider options belong to a custom profile")
	}
	for name, options := range o.ProxyProviders {
		if !webAPICatalogName(name) {
			return webAPIError(400, "invalid_provider_override", "invalid proxy provider name")
		}
		if _, err := dependencyRegex(webAPIString(options.Filter)); err != nil {
			return webAPIError(400, "invalid_provider_override", "invalid provider filter")
		}
		if _, err := dependencyRegex(webAPIString(options.ExcludeFilter)); err != nil {
			return webAPIError(400, "invalid_provider_override", "invalid provider exclude filter")
		}
		for _, value := range []*int32{options.HealthCheck.Interval, options.HealthCheck.Timeout} {
			if value != nil && *value < 0 {
				return webAPIError(400, "invalid_provider_override", "provider numeric values cannot be negative")
			}
		}
		if options.HealthCheck.URL != nil && *options.HealthCheck.URL != "" && !webAPICatalogURL(*options.HealthCheck.URL, false) {
			return webAPIError(400, "invalid_provider_override", "invalid provider health-check URL")
		}
		if options.HealthCheck.ExpectedStatus != nil && *options.HealthCheck.ExpectedStatus != "" && *options.HealthCheck.ExpectedStatus != "*" && !webAPIRanges(*options.HealthCheck.ExpectedStatus, 65535) {
			return webAPIError(400, "invalid_provider_override", "invalid provider expected status")
		}
		if options.Override.IPVersion != nil && !webAPIContains([]string{"dual", "ipv4", "ipv6", "ipv4-prefer", "ipv6-prefer"}, *options.Override.IPVersion) {
			return webAPIError(400, "invalid_provider_override", "unknown provider IP version")
		}
	}
	return nil
}

func webAPIString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func webAPIOverrideObject(value any) map[string]any {
	data, _ := json.Marshal(value)
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}

func webAPIOverridePath(object map[string]any, parts []string) (any, bool) {
	var value any = object
	for _, part := range parts {
		next, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok = next[part]
		if !ok {
			return nil, false
		}
	}
	return value, true
}

func webAPISetOverridePath(object map[string]any, parts []string, value any) {
	for _, part := range parts[:len(parts)-1] {
		child, ok := object[part].(map[string]any)
		if !ok {
			child = map[string]any{}
			object[part] = child
		}
		object = child
	}
	object[parts[len(parts)-1]] = value
}

func webAPIMergeProfileOverrides(raw *config.RawConfig, input WebAPIProfileInput) (*config.RawConfig, error) {
	if input.Overrides == nil {
		return raw, nil
	}
	o := input.Overrides
	root := webAPIOverrideObject(raw)
	for _, section := range []struct {
		name   string
		source any
		keys   []string
	}{{"dns", o.DNS, o.DNSOverrideKeys}, {"ntp", o.NTP, o.NTPOverrideKeys}, {"sniffer", o.Sniffer, o.SnifferOverrideKeys}, {"tun", o.Tun, o.TunOverrideKeys}} {
		source := webAPIOverrideObject(section.source)
		for _, key := range section.keys {
			parts := strings.Split(key, ".")
			value, ok := webAPIOverridePath(source, parts)
			if !ok {
				return nil, webAPIError(400, "missing_override_value", "selected override has no typed value")
			}
			webAPISetOverridePath(root, append([]string{section.name}, parts...), value)
		}
	}
	data, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	candidate := config.DefaultRawConfig()
	if err := json.Unmarshal(data, candidate); err != nil {
		return nil, webAPIError(400, "invalid_overrides", "override candidate does not match the Core schema")
	}
	return candidate, nil
}

func webAPICandidateDialers(raw *config.RawConfig, dialers map[string]string) error {
	byName := map[string][]map[string]any{}
	for _, proxy := range raw.Proxy {
		name, _ := proxy["name"].(string)
		byName[name] = append(byName[name], proxy)
	}
	for source, target := range dialers {
		values := byName[source]
		if len(values) == 0 {
			return dependencyPolicy("missing_source", source, "")
		}
		if len(values) != 1 {
			return dependencyPolicy("duplicate_outbound", source, source)
		}
		kind, _ := values[0]["type"].(string)
		if target != "" && !dependencySupportsDialer(kind) {
			return dependencyPolicy("unsupported_source", source, target)
		}
		values[0]["dialer-proxy"] = target
	}
	return nil
}

func webAPICandidateReferenceProfile(profile WebAPIProfile, raw *config.RawConfig) WebAPIProfile {
	profile.Overrides = &WebAPIProfileOverrides{}
	if raw.DNS.Enable {
		d := raw.DNS
		profile.Overrides.DNS = &WebAPIDNS{DefaultNameserver: d.DefaultNameserver, Nameserver: d.NameServer, Fallback: d.Fallback, ProxyServerNameserver: d.ProxyServerNameserver, DirectNameserver: d.DirectNameServer, FakeIPFilter: d.FakeIPFilter, NameserverPolicy: map[string]string{}, ProxyServerNameserverPolicy: map[string]string{}}
		for _, policy := range []struct {
			source map[string]any
			target map[string]string
		}{{webAPIOverrideObject(d.NameServerPolicy), profile.Overrides.DNS.NameserverPolicy}, {webAPIOverrideObject(d.ProxyServerNameserverPolicy), profile.Overrides.DNS.ProxyServerNameserverPolicy}} {
			for key, value := range policy.source {
				switch servers := value.(type) {
				case string:
					policy.target[key] = servers
				case []any:
					var values []string
					for _, server := range servers {
						if text, ok := server.(string); ok {
							values = append(values, text)
						}
					}
					policy.target[key] = strings.Join(values, ", ")
				}
			}
		}
	}
	if raw.NTP.Enable {
		profile.Overrides.NTP = &WebAPINTP{DialerProxy: raw.NTP.DialerProxy}
	}
	if raw.Sniffer.Enable {
		s := raw.Sniffer
		profile.Overrides.Sniffer = &WebAPISniffer{ForceDomain: s.ForceDomain, SkipDomain: s.SkipDomain, SkipSrcAddress: s.SkipSrcAddress, SkipDstAddress: s.SkipDstAddress}
	}
	return profile
}

type webAPICatalogProfileCandidate struct {
	profile WebAPIProfile
	raw     *config.RawConfig
	catalog WebAPIProfileCatalog
	graph   *dependencyGraph
}

func webAPIReadProfileCandidate(tx *WebAPIReadTx, id WebAPIID, input WebAPIProfileInput, budget *WebAPICatalogReadBudget) (webAPICatalogProfileCandidate, error) {
	candidate := webAPICatalogProfileCandidate{}
	if err := webAPIValidateProfileOverrides(input); err != nil {
		return candidate, err
	}
	input = webAPINormalizeProfile(input)
	profile := WebAPIProfile{ID: id, WebAPIProfileInput: webAPIClone(input)}
	candidate.profile = profile
	var raw *config.RawConfig
	var catalog WebAPIProfileCatalog
	var err error
	if profile.Type == "custom" {
		raw, catalog, err = webAPICatalogRawCandidateWithBudget(tx, profile, false, budget)
		if err != nil {
			return candidate, err
		}
		raw.DNS.Enable = true
	} else {
		if _, e := tx.CatalogProfile(id, budget); e != nil {
			var problem *WebAPIError
			if !errors.As(e, &problem) || problem.Status != 404 {
				return candidate, e
			}
		}
		content, e := tx.CatalogContent(WebAPIContentKey{Kind: WebAPIContentProfile, ID: id}, budget)
		if e != nil {
			var problem *WebAPIError
			if !errors.As(e, &problem) || problem.Status != 404 {
				return candidate, e
			}
			o := input.Overrides
			dialers, _ := webAPIDialers(o)
			if len(dialers) > 0 || o != nil && len(o.DNSOverrideKeys)+len(o.NTPOverrideKeys)+len(o.SnifferOverrideKeys)+len(o.TunOverrideKeys) > 0 {
				return candidate, webAPIError(409, "catalog_unavailable", "profile source must be prepared before graph overrides can be validated")
			}
			candidate.catalog = WebAPIProfileCatalog{OutboundNames: []string{}, RuleProviderNames: []string{}, SubRuleNames: []string{}, GroupMembers: map[string][]string{}, UnavailableReason: WebAPICatalogContentUnavailable}
			return candidate, nil
		}
		raw, catalog, err = webContentProfile(content.Bytes, input.AgeSecretKey)
		if err != nil {
			return candidate, webAPIError(409, "catalog_unavailable", "stored profile source cannot be interpreted")
		}
	}
	if input.ExtendType != "script" || input.Type == "custom" {
		raw, err = webAPIMergeProfileOverrides(raw, input)
		if err != nil {
			return candidate, err
		}
		if input.Type != "custom" {
			dialers, _ := webAPIDialers(input.Overrides)
			if err := webAPICandidateDialers(raw, dialers); err != nil {
				return candidate, webAPICatalogPolicyError(err, id)
			}
		}
	}
	graph, err := buildDependencyGraph(raw)
	if err != nil {
		return candidate, webAPICatalogPolicyError(err, id)
	}
	if err := webAPIValidateCatalogProfileReferences(webAPICandidateReferenceProfile(profile, raw), raw, catalog, graph); err != nil {
		return candidate, err
	}
	candidate.raw, candidate.catalog, candidate.graph = raw, catalog, graph
	return candidate, nil
}

func ValidateWebAPIProfileInput(tx *WebAPIReadTx, id WebAPIID, input WebAPIProfileInput) error {
	_, err := webAPIReadProfileCandidate(tx, id, input, NewWebAPICatalogReadBudget(webAPICatalogScopedReadBytes))
	return err
}

func webAPICatalogPolicyError(err error, id WebAPIID) error {
	var policy *DependencyPolicyError
	failure := webAPIError(409, "outbound_policy_invalid", "candidate outbound graph is invalid")
	if errors.As(err, &policy) {
		failure.Details = map[string]any{"reason": policy.Reason, "source": policy.Source, "target": policy.Target, "path": policy.Path, "profileId": id}
	}
	return failure
}
