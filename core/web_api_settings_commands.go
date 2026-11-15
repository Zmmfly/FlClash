//go:build webpanel && !android

package core

type WebAPISettingsValue[T any] struct {
	Value *T `json:"value" settings:"nullable"`
}

type WebAPISettingsRuntimePatch struct {
	PatchClashConfig *WebAPISettingsClashPatch `json:"patchClashConfig,omitempty"`
	AppSetting       *WebAPISettingsAppPatch   `json:"appSetting,omitempty"`
}

type WebAPISettingsAppPatch struct {
	OnlyStatisticsProxy        *bool     `json:"onlyStatisticsProxy,omitempty"`
	ShowNotificationStopAction *bool     `json:"showNotificationStopAction,omitempty"`
	AutoLaunch                 *bool     `json:"autoLaunch,omitempty"`
	SilentLaunch               *bool     `json:"silentLaunch,omitempty"`
	AutoRun                    *bool     `json:"autoRun,omitempty"`
	OpenLogs                   *bool     `json:"openLogs,omitempty"`
	CloseConnections           *bool     `json:"closeConnections,omitempty"`
	RequestFeed                *string   `json:"requestFeed,omitempty"`
	DelayTestTimeout           *int32    `json:"delayTestTimeout,omitempty"`
	AutoCheckUpdate            *bool     `json:"autoCheckUpdate,omitempty"`
	RestoreStrategy            *string   `json:"restoreStrategy,omitempty"`
	CheckCertificate           *bool     `json:"checkCertificate,omitempty"`
	UserAgents                 *[]string `json:"userAgents,omitempty"`
}

type WebAPISettingsNetworkPatch struct {
	NetworkProps *WebAPISettingsNetworkPropsPatch `json:"networkProps,omitempty"`
	VPNProps     *WebAPISettingsVPNPatch          `json:"vpnProps,omitempty"`
	ExcludeSSIDs *[]string                        `json:"excludeSSIDs,omitempty"`
}

type WebAPISettingsNetworkPropsPatch struct {
	SystemProxy        *bool                              `json:"systemProxy,omitempty"`
	BypassDomain       *[]string                          `json:"bypassDomain,omitempty"`
	BypassPrivateRoute *bool                              `json:"bypassPrivateRoute,omitempty"`
	AutoSetSystemDNS   *bool                              `json:"autoSetSystemDns,omitempty"`
	AppendSystemDNS    *bool                              `json:"appendSystemDns,omitempty"`
	Authentication     *WebAPISettingsAuthenticationPatch `json:"authentication,omitempty"`
}

type WebAPISettingsAuthenticationPatch struct {
	Enable   *bool   `json:"enable,omitempty"`
	Username *string `json:"username,omitempty"`
	Password *string `json:"password,omitempty"`
}

type WebAPISettingsVPNPatch struct {
	Enable             *bool                             `json:"enable,omitempty"`
	SystemProxy        *bool                             `json:"systemProxy,omitempty"`
	IPv6               *bool                             `json:"ipv6,omitempty"`
	AllowBypass        *bool                             `json:"allowBypass,omitempty"`
	DNSHijacking       *bool                             `json:"dnsHijacking,omitempty"`
	AccessControlProps *WebAPISettingsAccessControlPatch `json:"accessControlProps,omitempty"`
}

type WebAPISettingsAccessControlPatch struct {
	Enable                 *bool     `json:"enable,omitempty"`
	Mode                   *string   `json:"mode,omitempty"`
	AcceptList             *[]string `json:"acceptList,omitempty"`
	RejectList             *[]string `json:"rejectList,omitempty"`
	Sort                   *string   `json:"sort,omitempty"`
	IsFilterSystemApp      *bool     `json:"isFilterSystemApp,omitempty"`
	IsFilterNonInternetApp *bool     `json:"isFilterNonInternetApp,omitempty"`
}

type WebAPISettingsDAVPatch struct {
	URI      *string `json:"uri,omitempty"`
	User     *string `json:"user,omitempty"`
	Password *string `json:"password,omitempty"`
	FileName *string `json:"fileName,omitempty"`
}

type WebAPISettingsClashPatch struct {
	MixedPort           *uint16                        `json:"mixed-port,omitempty"`
	SocksPort           *uint16                        `json:"socks-port,omitempty"`
	Port                *uint16                        `json:"port,omitempty"`
	RedirPort           *uint16                        `json:"redir-port,omitempty"`
	TProxyPort          *uint16                        `json:"tproxy-port,omitempty"`
	Mode                *string                        `json:"mode,omitempty"`
	AllowLAN            *bool                          `json:"allow-lan,omitempty"`
	LogLevel            *string                        `json:"log-level,omitempty"`
	IPv6                *bool                          `json:"ipv6,omitempty"`
	FindProcessMode     *string                        `json:"find-process-mode,omitempty"`
	InterfaceNameMode   *string                        `json:"interface-name-mode,omitempty"`
	InterfaceName       *string                        `json:"interface-name,omitempty"`
	KeepAliveInterval   *int32                         `json:"keep-alive-interval,omitempty"`
	KeepAliveIdle       *int32                         `json:"keep-alive-idle,omitempty"`
	DisableKeepAlive    *bool                          `json:"disable-keep-alive,omitempty"`
	RoutingMark         *int32                         `json:"routing-mark,omitempty"`
	UnifiedDelay        *bool                          `json:"unified-delay,omitempty"`
	TCPConcurrent       *bool                          `json:"tcp-concurrent,omitempty"`
	Tun                 *WebAPISettingsTunPatch        `json:"tun,omitempty"`
	DNS                 *WebAPISettingsDNSPatch        `json:"dns,omitempty"`
	DNSOverrideKeys     *[]string                      `json:"dns-override-keys,omitempty"`
	TunOverride         *WebAPISettingsProfileTunPatch `json:"tun-override,omitempty"`
	TunOverrideKeys     *[]string                      `json:"tun-override-keys,omitempty"`
	GeoXURL             *WebAPISettingsGeoXURLPatch    `json:"geox-url,omitempty"`
	GeodataLoader       *string                        `json:"geodata-loader,omitempty"`
	GlobalUA            *WebAPISettingsValue[string]   `json:"global-ua,omitempty"`
	ExternalController  *string                        `json:"external-controller,omitempty"`
	Secret              *string                        `json:"secret,omitempty"`
	Hosts               *map[string]string             `json:"hosts,omitempty"`
	GeoAutoUpdate       *bool                          `json:"geo-auto-update,omitempty"`
	GeoUpdateInterval   *int32                         `json:"geo-update-interval,omitempty"`
	TestURL             *string                        `json:"test-url,omitempty"`
	GeneralOverrideKeys *[]string                      `json:"general-override-keys,omitempty"`
	Mitm                *WebAPISettingsMitmPatch       `json:"mitm,omitempty"`
}

type WebAPISettingsGeoXURLPatch struct {
	MMDB    *string `json:"mmdb,omitempty"`
	ASN     *string `json:"asn,omitempty"`
	GEOIP   *string `json:"geoip,omitempty"`
	GEOSITE *string `json:"geosite,omitempty"`
}

type WebAPISettingsTunPatch struct {
	Enable               *bool     `json:"enable,omitempty"`
	Device               *string   `json:"device,omitempty"`
	Stack                *string   `json:"stack,omitempty"`
	RouteAddress         *[]string `json:"route-address,omitempty"`
	MTU                  *int32    `json:"mtu,omitempty"`
	CongestionController *string   `json:"congestion-controller,omitempty"`
	RouteExcludeAddress  *[]string `json:"route-exclude-address,omitempty"`
}

type WebAPISettingsMitmPatch struct {
	Enable               *bool                        `json:"enable,omitempty"`
	Hostname             *[]string                    `json:"hostname,omitempty"`
	SkipServerCertVerify *bool                        `json:"skip-server-cert-verify,omitempty"`
	AutoQUICBlock        *bool                        `json:"auto-quic-block,omitempty"`
	H2                   *bool                        `json:"h2,omitempty"`
	DecryptUntrusted     *bool                        `json:"decrypt-untrusted,omitempty"`
	TrustedFingerprint   *WebAPISettingsValue[string] `json:"trusted-fingerprint,omitempty"`
	Capture              *bool                        `json:"capture,omitempty"`
	CaptureEntries       *WebAPISettingsValue[int32]  `json:"capture-entries,omitempty"`
}

type WebAPISettingsDNSPatch struct {
	Enable                       *bool                              `json:"enable,omitempty"`
	Listen                       *string                            `json:"listen,omitempty"`
	ListenRoutingMark            *int32                             `json:"listen-routing-mark,omitempty"`
	PreferH3                     *bool                              `json:"prefer-h3,omitempty"`
	UseHosts                     *bool                              `json:"use-hosts,omitempty"`
	UseSystemHosts               *bool                              `json:"use-system-hosts,omitempty"`
	RespectRules                 *bool                              `json:"respect-rules,omitempty"`
	IPv6                         *bool                              `json:"ipv6,omitempty"`
	IPv6Timeout                  *int32                             `json:"ipv6-timeout,omitempty"`
	CacheAlgorithm               *string                            `json:"cache-algorithm,omitempty"`
	CacheMaxSize                 *int32                             `json:"cache-max-size,omitempty"`
	DefaultNameserver            *[]string                          `json:"default-nameserver,omitempty"`
	EnhancedMode                 *string                            `json:"enhanced-mode,omitempty"`
	FakeIPRange                  *string                            `json:"fake-ip-range,omitempty"`
	FakeIPRange6                 *string                            `json:"fake-ip-range6,omitempty"`
	FakeIPFilter                 *[]string                          `json:"fake-ip-filter,omitempty"`
	FakeIPFilterMode             *string                            `json:"fake-ip-filter-mode,omitempty"`
	FakeIPTTL                    *int32                             `json:"fake-ip-ttl,omitempty"`
	NameserverPolicy             *map[string]string                 `json:"nameserver-policy,omitempty"`
	Nameserver                   *[]string                          `json:"nameserver,omitempty"`
	Fallback                     *[]string                          `json:"fallback,omitempty"`
	FallbackLazyQuery            *bool                              `json:"fallback-lazy-query,omitempty"`
	ProxyServerNameserver        *[]string                          `json:"proxy-server-nameserver,omitempty"`
	ProxyServerNameserverPolicy  *map[string]string                 `json:"proxy-server-nameserver-policy,omitempty"`
	DirectNameserver             *[]string                          `json:"direct-nameserver,omitempty"`
	DirectNameserverFollowPolicy *bool                              `json:"direct-nameserver-follow-policy,omitempty"`
	FallbackFilter               *WebAPISettingsFallbackFilterPatch `json:"fallback-filter,omitempty"`
}

type WebAPISettingsFallbackFilterPatch struct {
	GeoIP     *bool     `json:"geoip,omitempty"`
	GeoIPCode *string   `json:"geoip-code,omitempty"`
	Geosite   *[]string `json:"geosite,omitempty"`
	IPCIDR    *[]string `json:"ipcidr,omitempty"`
	Domain    *[]string `json:"domain,omitempty"`
}

type WebAPISettingsProfileTunPatch struct {
	MTU                   *int32    `json:"mtu,omitempty"`
	CongestionController  *string   `json:"congestion-controller,omitempty"`
	StrictRoute           *bool     `json:"strict-route,omitempty"`
	RouteAddress          *[]string `json:"route-address,omitempty"`
	RouteExcludeAddress   *[]string `json:"route-exclude-address,omitempty"`
	DNSHijack             *[]string `json:"dns-hijack,omitempty"`
	UDPTimeout            *int32    `json:"udp-timeout,omitempty"`
	DisableICMPForwarding *bool     `json:"disable-icmp-forwarding,omitempty"`
	GSO                   *bool     `json:"gso,omitempty"`
	IncludeInterface      *[]string `json:"include-interface,omitempty"`
	ExcludeInterface      *[]string `json:"exclude-interface,omitempty"`
	IncludeUID            *[]uint32 `json:"include-uid,omitempty"`
	ExcludeUID            *[]uint32 `json:"exclude-uid,omitempty"`
	ExcludeSrcPort        *[]uint16 `json:"exclude-src-port,omitempty"`
	ExcludeDstPort        *[]uint16 `json:"exclude-dst-port,omitempty"`
}

type WebAPISettingsRuntimeCommand struct {
	WebAPIMutation
	Patch WebAPISettingsRuntimePatch `json:"patch"`
}

type WebAPISettingsNetworkCommand struct {
	WebAPIMutation
	Patch WebAPISettingsNetworkPatch `json:"patch"`
}

type WebAPISettingsCurrentProfileCommand struct {
	WebAPIMutation
	CurrentProfileID *WebAPIID `json:"currentProfileId" settings:"nullable"`
}

type WebAPISettingsDAVCommand struct {
	WebAPIMutation
	DAV *WebAPISettingsDAVPatch `json:"dav" settings:"nullable"`
}

type WebAPISettingsSelectedMapCommand struct {
	WebAPIMutation
	ProfileID WebAPIID `json:"profileId"`
	GroupName string   `json:"groupName"`
	ProxyName *string  `json:"proxyName" settings:"nullable"`
}

type WebAPISettingsSelectedMap struct {
	ProfileID WebAPIID `json:"profileId"`
	GroupName string   `json:"groupName"`
	ProxyName *string  `json:"proxyName"`
}
