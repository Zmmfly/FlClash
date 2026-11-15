//go:build webpanel && !android

package core

import "encoding/json"

type WebAPIProfileOverrides struct {
	DialerProxies       json.RawMessage                       `json:"dialer-proxies,omitempty"`
	DNS                 *WebAPIDNS                            `json:"dns,omitempty"`
	DNSOverrideKeys     []string                              `json:"dns-override-keys,omitempty"`
	NTP                 *WebAPINTP                            `json:"ntp,omitempty"`
	NTPOverrideKeys     []string                              `json:"ntp-override-keys,omitempty"`
	Sniffer             *WebAPISniffer                        `json:"sniffer,omitempty"`
	SnifferOverrideKeys []string                              `json:"sniffer-override-keys,omitempty"`
	Tun                 *WebAPIProfileTun                     `json:"tun,omitempty"`
	TunOverrideKeys     []string                              `json:"tun-override-keys,omitempty"`
	ProxyProviders      map[string]WebAPIProxyProviderOptions `json:"proxy-providers,omitempty"`
}

type WebAPIDNS struct {
	Enable                       bool                 `json:"enable"`
	Listen                       string               `json:"listen"`
	ListenRoutingMark            int32                `json:"listen-routing-mark"`
	PreferH3                     bool                 `json:"prefer-h3"`
	UseHosts                     bool                 `json:"use-hosts"`
	UseSystemHosts               bool                 `json:"use-system-hosts"`
	RespectRules                 bool                 `json:"respect-rules"`
	IPv6                         bool                 `json:"ipv6"`
	IPv6Timeout                  int32                `json:"ipv6-timeout"`
	CacheAlgorithm               string               `json:"cache-algorithm"`
	CacheMaxSize                 int32                `json:"cache-max-size"`
	DefaultNameserver            []string             `json:"default-nameserver"`
	EnhancedMode                 string               `json:"enhanced-mode"`
	FakeIPRange                  string               `json:"fake-ip-range"`
	FakeIPRange6                 string               `json:"fake-ip-range6"`
	FakeIPFilter                 []string             `json:"fake-ip-filter"`
	FakeIPFilterMode             string               `json:"fake-ip-filter-mode"`
	FakeIPTTL                    int32                `json:"fake-ip-ttl"`
	NameserverPolicy             map[string]string    `json:"nameserver-policy"`
	Nameserver                   []string             `json:"nameserver"`
	Fallback                     []string             `json:"fallback"`
	FallbackLazyQuery            bool                 `json:"fallback-lazy-query"`
	ProxyServerNameserver        []string             `json:"proxy-server-nameserver"`
	ProxyServerNameserverPolicy  map[string]string    `json:"proxy-server-nameserver-policy"`
	DirectNameserver             []string             `json:"direct-nameserver"`
	DirectNameserverFollowPolicy bool                 `json:"direct-nameserver-follow-policy"`
	FallbackFilter               WebAPIFallbackFilter `json:"fallback-filter"`
}

type WebAPIFallbackFilter struct {
	GeoIP     bool     `json:"geoip"`
	GeoIPCode string   `json:"geoip-code"`
	Geosite   []string `json:"geosite"`
	IPCIDR    []string `json:"ipcidr"`
	Domain    []string `json:"domain"`
}

type WebAPINTP struct {
	Enable        bool   `json:"enable"`
	Server        string `json:"server"`
	Port          uint16 `json:"port"`
	Interval      int32  `json:"interval"`
	DialerProxy   string `json:"dialer-proxy"`
	WriteToSystem bool   `json:"write-to-system"`
}

type WebAPISniffer struct {
	Enable              bool                           `json:"enable"`
	OverrideDestination bool                           `json:"override-destination"`
	ForceDNSMapping     bool                           `json:"force-dns-mapping"`
	ParsePureIP         bool                           `json:"parse-pure-ip"`
	ForceDomain         []string                       `json:"force-domain"`
	SkipDomain          []string                       `json:"skip-domain"`
	SkipSrcAddress      []string                       `json:"skip-src-address"`
	SkipDstAddress      []string                       `json:"skip-dst-address"`
	Sniff               map[string]WebAPISnifferConfig `json:"sniff"`
}

type WebAPISnifferConfig struct {
	Ports               []string `json:"ports"`
	OverrideDestination *bool    `json:"override-destination"`
}

type WebAPIProfileTun struct {
	MTU                   int32    `json:"mtu"`
	CongestionController  string   `json:"congestion-controller"`
	StrictRoute           bool     `json:"strict-route"`
	RouteAddress          []string `json:"route-address"`
	RouteExcludeAddress   []string `json:"route-exclude-address"`
	DNSHijack             []string `json:"dns-hijack"`
	UDPTimeout            int32    `json:"udp-timeout"`
	DisableICMPForwarding bool     `json:"disable-icmp-forwarding"`
	GSO                   bool     `json:"gso"`
	IncludeInterface      []string `json:"include-interface"`
	ExcludeInterface      []string `json:"exclude-interface"`
	IncludeUID            []uint32 `json:"include-uid"`
	ExcludeUID            []uint32 `json:"exclude-uid"`
	ExcludeSrcPort        []uint16 `json:"exclude-src-port"`
	ExcludeDstPort        []uint16 `json:"exclude-dst-port"`
}

type WebAPIProviderHealthCheck struct {
	URL            *string `json:"url"`
	Interval       *int32  `json:"interval"`
	Timeout        *int32  `json:"timeout"`
	Lazy           *bool   `json:"lazy"`
	ExpectedStatus *string `json:"expected-status"`
}

type WebAPIProviderOverride struct {
	AdditionalPrefix *string `json:"additional-prefix"`
	AdditionalSuffix *string `json:"additional-suffix"`
	UDP              *bool   `json:"udp"`
	SkipCertVerify   *bool   `json:"skip-cert-verify"`
	IPVersion        *string `json:"ip-version"`
}

type WebAPIProxyProviderOptions struct {
	HealthCheck   WebAPIProviderHealthCheck `json:"health-check"`
	Filter        *string                   `json:"filter"`
	ExcludeFilter *string                   `json:"exclude-filter"`
	Override      WebAPIProviderOverride    `json:"override"`
	MemberLayout  []string                  `json:"memberLayout"`
}

type WebAPIProxyGroupDefinition struct {
	Name                string           `json:"name"`
	Type                string           `json:"type"`
	Proxies             []string         `json:"proxies"`
	Use                 []string         `json:"use"`
	UsePosition         map[string]int32 `json:"use-position"`
	Interval            *int32           `json:"interval"`
	Lazy                *bool            `json:"lazy"`
	DisableUDP          *bool            `json:"disable-udp"`
	URL                 *string          `json:"url"`
	Timeout             *int32           `json:"timeout"`
	MaxFailedTimes      *int32           `json:"max-failed-times"`
	Filter              *string          `json:"filter"`
	ExcludeFilter       *string          `json:"exclude-filter"`
	ExcludeType         *string          `json:"exclude-type"`
	ExpectedStatus      *string          `json:"expected-status"`
	Tolerance           *int32           `json:"tolerance"`
	Strategy            *string          `json:"strategy"`
	HashKey             *string          `json:"hash-key"`
	DefaultSelected     *string          `json:"default-selected"`
	EmptyFallback       *string          `json:"empty-fallback"`
	IncludeAll          *bool            `json:"include-all"`
	IncludeAllProxies   *bool            `json:"include-all-proxies"`
	IncludeAllProviders *bool            `json:"include-all-providers"`
	Hidden              *bool            `json:"hidden"`
	Icon                *string          `json:"icon"`
}

type WebAPIProxyGroup struct {
	ID           WebAPIID                   `json:"id"`
	ProfileID    WebAPIID                   `json:"profileId"`
	Definition   WebAPIProxyGroupDefinition `json:"definition"`
	MemberLayout []string                   `json:"memberLayout"`
	Order        *string                    `json:"order"`
}

type WebAPIProxyGroupInput struct {
	Definition   WebAPIProxyGroupDefinition `json:"definition"`
	MemberLayout []string                   `json:"memberLayout"`
}

// A protocol definition is a domain document, never a table/column update.
// Its future command must validate it with the protocol parser before storage.
type WebAPIProxyDefinition map[string]json.RawMessage

type WebAPICustomProxy struct {
	ID           WebAPIID              `json:"id"`
	Definition   WebAPIProxyDefinition `json:"definition"`
	Order        *string               `json:"order"`
	EmptyOptions []string              `json:"emptyOptions"`
}

type WebAPIProxyDialer struct {
	ProfileID WebAPIID `json:"profileId"`
	ProxyID   WebAPIID `json:"proxyId"`
	Target    string   `json:"target"`
}

type WebAPIScript struct {
	ID             WebAPIID `json:"id"`
	Label          string   `json:"label"`
	LastUpdateTime string   `json:"lastUpdateTime"`
	URL            *string  `json:"url"`
	Order          *int32   `json:"order"`
}

type WebAPIClashProvider struct {
	ID       WebAPIID `json:"id"`
	Label    string   `json:"label"`
	URL      string   `json:"url"`
	Behavior string   `json:"behavior"`
	Format   string   `json:"format"`
	Order    *int32   `json:"order"`
}

type WebAPIMitmModule struct {
	ID             WebAPIID          `json:"id"`
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	URL            *string           `json:"url"`
	Enabled        bool              `json:"enabled"`
	Order          *int32            `json:"order"`
	Arguments      map[string]string `json:"arguments"`
	Resources      map[string]string `json:"resources"`
	System         *string           `json:"system"`
	Scripts        int32             `json:"scripts"`
	LastUpdateTime string            `json:"lastUpdateTime"`
}

type WebAPIIconSetIcon struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type WebAPIIconSet struct {
	ID             WebAPIID            `json:"id"`
	Name           string              `json:"name"`
	URL            string              `json:"url"`
	Icons          []WebAPIIconSetIcon `json:"icons"`
	LastUpdateTime *string             `json:"lastUpdateTime"`
	Order          *int32              `json:"order"`
}

type WebAPIIconRecord struct {
	URL          string      `json:"url"`
	LastAccessed WebAPIInt64 `json:"lastAccessed"`
}

type WebAPIIPQuality struct {
	IP           string  `json:"ip"`
	Source       string  `json:"source"`
	Type         string  `json:"type"`
	Organization *string `json:"organization"`
	ASN          *uint32 `json:"asn"`
	IsProxy      bool    `json:"isProxy"`
	IsVPN        bool    `json:"isVpn"`
	IsTor        bool    `json:"isTor"`
	IsAbuser     bool    `json:"isAbuser"`
}

type WebAPIIPQualityRecord struct {
	IP        string          `json:"ip"`
	Quality   WebAPIIPQuality `json:"quality"`
	CheckedAt string          `json:"checkedAt"`
}

type WebAPIContentKind string

const (
	WebAPIContentProfile      WebAPIContentKind = "profile"
	WebAPIContentProfileAsset WebAPIContentKind = "profile-asset"
	WebAPIContentScript       WebAPIContentKind = "script"
	WebAPIContentRuleProvider WebAPIContentKind = "rule-provider"
	WebAPIContentMitmModule   WebAPIContentKind = "mitm-module"
	WebAPIContentMitmResource WebAPIContentKind = "mitm-resource"
	WebAPIMaxContentBytes                       = 32 << 20
)

type WebAPIContentKey struct {
	Kind WebAPIContentKind `json:"kind"`
	ID   WebAPIID          `json:"id"`
	Name string            `json:"name,omitempty"`
}

type WebAPIContent struct {
	ModifiedAt string           `json:"modifiedAt,omitempty"`
	Key        WebAPIContentKey `json:"key"`
	MediaType  string           `json:"mediaType"`
	SHA256     string           `json:"sha256"`
	Bytes      []byte           `json:"bytes"`
}

type WebAPICustomProxyInput struct {
	Definition   WebAPIProxyDefinition `json:"definition"`
	EmptyOptions []string              `json:"emptyOptions"`
}
type WebAPIScriptInput struct {
	Label string  `json:"label"`
	URL   *string `json:"url"`
}
type WebAPIClashProviderInput struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}
type WebAPIMitmModuleInput struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	URL         *string           `json:"url"`
	Enabled     bool              `json:"enabled"`
	Arguments   map[string]string `json:"arguments"`
	System      *string           `json:"system"`
}
type WebAPIIconSetInput struct {
	Name  string              `json:"name"`
	URL   string              `json:"url"`
	Icons []WebAPIIconSetIcon `json:"icons"`
}
