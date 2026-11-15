//go:build webpanel && !android

package core

type WebAPISettings struct {
	Runtime            WebAPISettingsRuntime       `json:"runtime"`
	Network            WebAPISettingsNetwork       `json:"network"`
	CurrentProfileID   *WebAPIID                   `json:"currentProfileId"`
	DAV                *WebAPISettingsDAV          `json:"dav"`
	NativePresentation *WebAPISettingsPresentation `json:"nativePresentation,omitempty"`
}

type WebAPISettingsRuntime struct {
	PatchClashConfig WebAPISettingsClash `json:"patchClashConfig"`
	AppSetting       WebAPISettingsApp   `json:"appSetting"`
}

type WebAPISettingsApp struct {
	OnlyStatisticsProxy        bool     `json:"onlyStatisticsProxy"`
	ShowNotificationStopAction bool     `json:"showNotificationStopAction"`
	AutoLaunch                 bool     `json:"autoLaunch"`
	SilentLaunch               bool     `json:"silentLaunch"`
	AutoRun                    bool     `json:"autoRun"`
	OpenLogs                   bool     `json:"openLogs"`
	CloseConnections           bool     `json:"closeConnections"`
	RequestFeed                string   `json:"requestFeed"`
	DelayTestTimeout           int32    `json:"delayTestTimeout"`
	AutoCheckUpdate            bool     `json:"autoCheckUpdate"`
	RestoreStrategy            string   `json:"restoreStrategy"`
	CheckCertificate           bool     `json:"checkCertificate"`
	UserAgents                 []string `json:"userAgents"`
}

type WebAPISettingsNetwork struct {
	NetworkProps WebAPISettingsNetworkProps `json:"networkProps"`
	VPNProps     WebAPISettingsVPN          `json:"vpnProps"`
	ExcludeSSIDs []string                   `json:"excludeSSIDs"`
}

type WebAPISettingsNetworkProps struct {
	SystemProxy        bool                         `json:"systemProxy"`
	BypassDomain       []string                     `json:"bypassDomain"`
	BypassPrivateRoute bool                         `json:"bypassPrivateRoute"`
	AutoSetSystemDNS   bool                         `json:"autoSetSystemDns"`
	AppendSystemDNS    bool                         `json:"appendSystemDns"`
	Authentication     WebAPISettingsAuthentication `json:"authentication"`
}

type WebAPISettingsAuthentication struct {
	Enable   bool   `json:"enable"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type WebAPISettingsVPN struct {
	Enable             bool                        `json:"enable"`
	SystemProxy        bool                        `json:"systemProxy"`
	IPv6               bool                        `json:"ipv6"`
	AllowBypass        bool                        `json:"allowBypass"`
	DNSHijacking       bool                        `json:"dnsHijacking"`
	AccessControlProps WebAPISettingsAccessControl `json:"accessControlProps"`
}

type WebAPISettingsAccessControl struct {
	Enable                 bool     `json:"enable"`
	Mode                   string   `json:"mode"`
	AcceptList             []string `json:"acceptList"`
	RejectList             []string `json:"rejectList"`
	Sort                   string   `json:"sort"`
	IsFilterSystemApp      bool     `json:"isFilterSystemApp"`
	IsFilterNonInternetApp bool     `json:"isFilterNonInternetApp"`
}

type WebAPISettingsDAV struct {
	URI      string `json:"uri"`
	User     string `json:"user"`
	Password string `json:"password"`
	FileName string `json:"fileName"`
}

type WebAPISettingsClash struct {
	MixedPort           uint16                `json:"mixed-port"`
	SocksPort           uint16                `json:"socks-port"`
	Port                uint16                `json:"port"`
	RedirPort           uint16                `json:"redir-port"`
	TProxyPort          uint16                `json:"tproxy-port"`
	Mode                string                `json:"mode"`
	AllowLAN            bool                  `json:"allow-lan"`
	LogLevel            string                `json:"log-level"`
	IPv6                bool                  `json:"ipv6"`
	FindProcessMode     string                `json:"find-process-mode"`
	InterfaceNameMode   string                `json:"interface-name-mode"`
	InterfaceName       string                `json:"interface-name"`
	KeepAliveInterval   int32                 `json:"keep-alive-interval"`
	KeepAliveIdle       int32                 `json:"keep-alive-idle"`
	DisableKeepAlive    bool                  `json:"disable-keep-alive"`
	RoutingMark         int32                 `json:"routing-mark"`
	UnifiedDelay        bool                  `json:"unified-delay"`
	TCPConcurrent       bool                  `json:"tcp-concurrent"`
	Tun                 WebAPISettingsTun     `json:"tun"`
	DNS                 WebAPIDNS             `json:"dns"`
	DNSOverrideKeys     []string              `json:"dns-override-keys"`
	TunOverride         WebAPIProfileTun      `json:"tun-override"`
	TunOverrideKeys     []string              `json:"tun-override-keys"`
	GeoXURL             WebAPISettingsGeoXURL `json:"geox-url"`
	GeodataLoader       string                `json:"geodata-loader"`
	GlobalUA            *string               `json:"global-ua"`
	ExternalController  string                `json:"external-controller"`
	Secret              string                `json:"secret"`
	Hosts               map[string]string     `json:"hosts"`
	GeoAutoUpdate       bool                  `json:"geo-auto-update"`
	GeoUpdateInterval   int32                 `json:"geo-update-interval"`
	TestURL             string                `json:"test-url"`
	GeneralOverrideKeys []string              `json:"general-override-keys"`
	Mitm                WebAPISettingsMitm    `json:"mitm"`
}

type WebAPISettingsGeoXURL struct {
	MMDB    string `json:"mmdb"`
	ASN     string `json:"asn"`
	GEOIP   string `json:"geoip"`
	GEOSITE string `json:"geosite"`
}

type WebAPISettingsTun struct {
	Enable               bool     `json:"enable"`
	Device               string   `json:"device"`
	Stack                string   `json:"stack"`
	RouteAddress         []string `json:"route-address"`
	MTU                  int32    `json:"mtu"`
	CongestionController string   `json:"congestion-controller"`
	RouteExcludeAddress  []string `json:"route-exclude-address"`
}

type WebAPISettingsMitm struct {
	Enable               bool     `json:"enable"`
	Hostname             []string `json:"hostname"`
	SkipServerCertVerify bool     `json:"skip-server-cert-verify"`
	AutoQUICBlock        bool     `json:"auto-quic-block"`
	H2                   bool     `json:"h2"`
	DecryptUntrusted     bool     `json:"decrypt-untrusted"`
	TrustedFingerprint   *string  `json:"trusted-fingerprint"`
	Capture              bool     `json:"capture"`
	CaptureEntries       *int32   `json:"capture-entries"`
}

func DefaultWebAPISettings() WebAPISettings {
	return WebAPISettings{
		Runtime: WebAPISettingsRuntime{
			AppSetting: WebAPISettingsApp{ShowNotificationStopAction: true, CloseConnections: true, RequestFeed: "live", DelayTestTimeout: 8000, AutoCheckUpdate: true, RestoreStrategy: "compatible", CheckCertificate: true, UserAgents: []string{"clash-verge/v2.4.2", "ClashforWindows/0.19.23"}},
			PatchClashConfig: WebAPISettingsClash{
				MixedPort: 7890, Mode: "rule", LogLevel: "error", FindProcessMode: "off", InterfaceNameMode: "clear", KeepAliveInterval: 30, KeepAliveIdle: 15, UnifiedDelay: true, TCPConcurrent: true,
				Tun:             WebAPISettingsTun{Device: "FlClash", Stack: "mips", RouteAddress: []string{}, MTU: 9000, CongestionController: "cubic", RouteExcludeAddress: []string{}},
				DNS:             WebAPIDNS{UseHosts: true, UseSystemHosts: true, IPv6Timeout: 100, CacheAlgorithm: "lru", CacheMaxSize: 4096, DefaultNameserver: []string{"114.114.114.114", "223.5.5.5", "8.8.8.8", "1.0.0.1"}, EnhancedMode: "redir-host", FakeIPRange: "198.18.0.1/16", FakeIPFilter: []string{"dns.msftnsci.com", "www.msftnsci.com", "www.msftconnecttest.com"}, FakeIPFilterMode: "blacklist", FakeIPTTL: 1, NameserverPolicy: map[string]string{}, Nameserver: []string{"https://doh.pub/dns-query", "tls://223.5.5.5:853"}, Fallback: []string{}, ProxyServerNameserver: []string{}, ProxyServerNameserverPolicy: map[string]string{}, DirectNameserver: []string{}, FallbackFilter: WebAPIFallbackFilter{GeoIP: true, Geosite: []string{}, IPCIDR: []string{}, Domain: []string{}}},
				DNSOverrideKeys: []string{}, TunOverride: WebAPIProfileTun{MTU: 9000, CongestionController: "cubic", RouteAddress: []string{}, RouteExcludeAddress: []string{}, DNSHijack: []string{"any:53"}, UDPTimeout: 300, IncludeInterface: []string{}, ExcludeInterface: []string{}, IncludeUID: []uint32{}, ExcludeUID: []uint32{}, ExcludeSrcPort: []uint16{}, ExcludeDstPort: []uint16{}}, TunOverrideKeys: []string{},
				GeoXURL:       WebAPISettingsGeoXURL{MMDB: "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip.metadb", ASN: "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/GeoLite2-ASN.mmdb", GEOIP: "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip.dat", GEOSITE: "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geosite.dat"},
				GeodataLoader: "memconservative", Hosts: map[string]string{}, GeoUpdateInterval: 24, TestURL: "https://www.gstatic.com/generate_204", GeneralOverrideKeys: []string{}, Mitm: WebAPISettingsMitm{Hostname: []string{}, AutoQUICBlock: true},
			},
		},
		Network: WebAPISettingsNetwork{
			NetworkProps: WebAPISettingsNetworkProps{SystemProxy: true, BypassDomain: []string{"*zhihu.com", "*zhimg.com", "*jd.com", "100ime-iat-api.xfyun.cn", "*360buyimg.com", "localhost", "*.local", "127.*", "10.*", "172.16.*", "172.17.*", "172.18.*", "172.19.*", "172.2*", "172.30.*", "172.31.*", "192.168.*"}, BypassPrivateRoute: true, AutoSetSystemDNS: true},
			VPNProps:     WebAPISettingsVPN{Enable: true, SystemProxy: true, AllowBypass: true, AccessControlProps: WebAPISettingsAccessControl{Mode: "rejectSelected", AcceptList: []string{}, RejectList: []string{}, Sort: "none", IsFilterSystemApp: true, IsFilterNonInternetApp: true}}, ExcludeSSIDs: []string{},
		},
	}
}
