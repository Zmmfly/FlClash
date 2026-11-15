//go:build webpanel && !android

package core

type webAPISettingsReleasedConfigFixture struct {
	Tag                   string
	Schema                int
	GeneratedConfigSHA256 string
	GeneratedClashSHA256  string
	Config                string
}

var webAPISettingsReleasedFixtures = []webAPISettingsReleasedConfigFixture{
	{Tag: "v0.8.95", Schema: 2, GeneratedConfigSHA256: "b16c2995d32ccd0bf643e1ce8e60665d1ec080c5647be81b6178204f3c7a5bf5", GeneratedClashSHA256: "fc8f8554b7c9eb17e3786a333c38e4e182b3bfc9eb04be3e7be7c9cdf9741a1f", Config: webAPISettingsReleasedConfig0},
	{Tag: "v0.8.96", Schema: 2, GeneratedConfigSHA256: "b16c2995d32ccd0bf643e1ce8e60665d1ec080c5647be81b6178204f3c7a5bf5", GeneratedClashSHA256: "fc8f8554b7c9eb17e3786a333c38e4e182b3bfc9eb04be3e7be7c9cdf9741a1f", Config: webAPISettingsReleasedConfig0},
	{Tag: "v0.8.97", Schema: 3, GeneratedConfigSHA256: "518d4cec69af949f49eacceccf30bb020f6128e12b30096f8b7145cd2776e5ec", GeneratedClashSHA256: "cc50b6678ab87f62c47f62b6e24737ecd7e97c6fd475fb6bb8af93853595881d", Config: webAPISettingsReleasedConfig1},
	{Tag: "v0.8.98", Schema: 3, GeneratedConfigSHA256: "518d4cec69af949f49eacceccf30bb020f6128e12b30096f8b7145cd2776e5ec", GeneratedClashSHA256: "cc50b6678ab87f62c47f62b6e24737ecd7e97c6fd475fb6bb8af93853595881d", Config: webAPISettingsReleasedConfig1},
	{Tag: "v0.8.99", Schema: 10, GeneratedConfigSHA256: "8af64d31fadda19102ddc4272b466c6f780c5f1c983ff6773063973001e8fcc4", GeneratedClashSHA256: "20a3dd3a103b674964c340c7d8da006df9e62cd04125577ebb0b3eb91574ce73", Config: webAPISettingsReleasedConfig2},
	{Tag: "v0.9.0", Schema: 20, GeneratedConfigSHA256: "4832c1327ddbfb65fb1b56bfeb807a0a217f0fdcd30fcfb8a8090d7ba10faaf3", GeneratedClashSHA256: "03a4d0204f1c9a58de7b2d7e076954d3368c6dbae6d7deafc9d2c26f238ddb5b", Config: webAPISettingsReleasedConfig3},
	{Tag: "v0.9.1", Schema: 20, GeneratedConfigSHA256: "f3cda1bb6ebb41be14e68ec77a7a9a8f52a14d5f9b7ac3b0c262d25762dc1afc", GeneratedClashSHA256: "16e9bab49e73ced195da43d61a579f2203d4e29faa9e17d0b6348575676f0086", Config: webAPISettingsReleasedConfig4},
	{Tag: "v0.9.2", Schema: 21, GeneratedConfigSHA256: "01bb5117a1de24d09aa9002b87faf7f04ede06f922bafe0ae8adb360dee0ada0", GeneratedClashSHA256: "7d03f16f471397a62b222985d852e17f18425cd85c10e198f96d1be28154ac69", Config: webAPISettingsReleasedConfig5},
	{Tag: "v0.9.3", Schema: 23, GeneratedConfigSHA256: "551f096facc6bb7a7c98e689f3e0a0f55037315359b2f3e93bc7f5a6ec7e38b8", GeneratedClashSHA256: "1b9ae2e04f07184d4c33f02dc6ad797dfb2a4d21334ebacafde87279c4b050ee", Config: webAPISettingsReleasedConfig6},
}

const webAPISettingsReleasedConfig0 = `{
  "version": 1,
  "currentProfileId": 9007199254740993,
  "overrideDns": false,
  "hotKeyActions": [
    {
      "action": "start",
      "key": 458789,
      "modifiers": [
        "control",
        "alt"
      ]
    }
  ],
  "appSettingProps": {
    "locale": "zh_CN",
    "dashboardWidgets": [
      "networkDetection",
      "outboundModeV2",
      "memoryInfo"
    ],
    "onlyStatisticsProxy": true,
    "autoLaunch": false,
    "silentLaunch": true,
    "autoRun": false,
    "openLogs": true,
    "closeConnections": false,
    "testUrl": "https://probe.fixture.invalid/generate_204",
    "isAnimateToPage": false,
    "autoCheckUpdate": false,
    "showLabel": true,
    "disclaimerAccepted": false,
    "crashlyticsTip": false,
    "crashlytics": false,
    "minimizeOnExit": false,
    "hidden": true,
    "developerMode": true,
    "restoreStrategy": "override",
    "showTrayTitle": false,
    "customUserAgent": "FixtureLegacy/7.0"
  },
  "davProps": {
    "uri": "https://dav.fixture.invalid/backup?token=fixture-dav-token",
    "user": "fixture-dav-user",
    "password": "fixture-dav-password",
    "fileName": "fixture-backup.zip"
  },
  "networkProps": {
    "systemProxy": false,
    "bypassDomain": [
      "localhost",
      "*.fixture.invalid"
    ],
    "routeMode": "config",
    "autoSetSystemDns": false,
    "appendSystemDns": true
  },
  "vpnProps": {
    "enable": true,
    "systemProxy": false,
    "ipv6": true,
    "allowBypass": false,
    "dnsHijacking": true,
    "accessControlProps": {
      "enable": true,
      "mode": "acceptSelected",
      "acceptList": [
        "org.fixture.allowed"
      ],
      "rejectList": [],
      "sort": "name",
      "isFilterSystemApp": false,
      "isFilterNonInternetApp": true
    }
  },
  "themeProps": {
    "primaryColor": 4279383126,
    "primaryColors": [
      4279383126,
      4289449455
    ],
    "themeMode": "dark",
    "schemeVariant": "neutral",
    "pureBlack": true,
    "textScale": {
      "enable": true,
      "scale": 1.2
    }
  },
  "proxiesStyleProps": {
    "type": "tab",
    "sortType": "delay",
    "layout": "tight",
    "iconStyle": "standard",
    "cardType": "expand"
  },
  "windowProps": {
    "width": 1040.0,
    "height": 780.0,
    "top": 13.5,
    "left": 42.0
  },
  "patchClashConfig": {
    "mixed-port": 17890,
    "socks-port": 10808,
    "port": 18081,
    "redir-port": 0,
    "tproxy-port": 0,
    "mode": "global",
    "allow-lan": false,
    "log-level": "debug",
    "ipv6": false,
    "find-process-mode": "always",
    "keep-alive-interval": 41,
    "unified-delay": false,
    "tcp-concurrent": false,
    "tun": {
      "enable": false,
      "device": "FixtureTun",
      "auto-route": true,
      "stack": "mixed",
      "dns-hijack": [
        "any:53"
      ],
      "route-address": [
        "198.18.0.0/15"
      ]
    },
    "dns": {
      "enable": true,
      "listen": "127.0.0.1:1053",
      "prefer-h3": true,
      "use-hosts": false,
      "use-system-hosts": true,
      "respect-rules": false,
      "ipv6": true,
      "default-nameserver": [
        "223.5.5.5"
      ],
      "enhanced-mode": "fake-ip",
      "fake-ip-range": "198.18.0.1/16",
      "fake-ip-filter": [
        "*.fixture.invalid"
      ],
      "nameserver-policy": {
        "fixture.invalid": "https://policy.fixture.invalid/query"
      },
      "nameserver": [
        "https://resolver.fixture.invalid/query?token=fixture-dns-token"
      ],
      "fallback": [
        "tls://8.8.4.4"
      ],
      "proxy-server-nameserver": [
        "https://proxy-dns.fixture.invalid/query"
      ],
      "fallback-filter": {
        "geoip": false,
        "geoip-code": "zz",
        "geosite": [
          "private"
        ],
        "ipcidr": [
          "240.0.0.0/4"
        ],
        "domain": [
          "+.fixture.invalid"
        ]
      }
    },
    "geox-url": {
      "mmdb": "https://geo.fixture.invalid/geo.mmdb",
      "asn": "https://geo.fixture.invalid/asn.mmdb",
      "geoip": "https://geo.fixture.invalid/geoip.dat",
      "geosite": "https://geo.fixture.invalid/geosite.dat"
    },
    "geodata-loader": "standard",
    "global-ua": "FixtureGlobal/4.2",
    "external-controller": "127.0.0.1:9090",
    "hosts": {
      "fixture.invalid": "192.0.2.1"
    },
    "geo-auto-update": true,
    "geo-update-interval": 13
  },
  "excludeSSIDs": [
    "FixtureSSID"
  ]
}`

const webAPISettingsReleasedConfig1 = `{
  "version": 1,
  "currentProfileId": 9007199254740993,
  "overrideDns": false,
  "hotKeyActions": [
    {
      "action": "start",
      "key": 458789,
      "modifiers": [
        "control",
        "alt"
      ]
    }
  ],
  "appSettingProps": {
    "locale": "zh_CN",
    "dashboardWidgets": [
      "networkDetection",
      "outboundModeV2",
      "memoryInfo"
    ],
    "onlyStatisticsProxy": true,
    "showNotificationStopAction": false,
    "autoLaunch": false,
    "silentLaunch": true,
    "autoRun": false,
    "openLogs": true,
    "closeConnections": false,
    "testUrl": "https://probe.fixture.invalid/generate_204",
    "isAnimateToPage": false,
    "autoCheckUpdate": false,
    "showLabel": true,
    "disclaimerAccepted": false,
    "crashlyticsTip": false,
    "crashlytics": false,
    "minimizeOnExit": false,
    "hidden": true,
    "developerMode": true,
    "restoreStrategy": "override",
    "showTrayTitle": false,
    "checkCertificate": false,
    "customUserAgent": "FixtureLegacy/7.0"
  },
  "davProps": {
    "uri": "https://dav.fixture.invalid/backup?token=fixture-dav-token",
    "user": "fixture-dav-user",
    "password": "fixture-dav-password",
    "fileName": "fixture-backup.zip"
  },
  "networkProps": {
    "systemProxy": false,
    "bypassDomain": [
      "localhost",
      "*.fixture.invalid"
    ],
    "routeMode": "config",
    "autoSetSystemDns": false,
    "appendSystemDns": true,
    "authentication": {
      "enable": true,
      "username": "fixture-inbound-user",
      "password": "fixture-inbound-password"
    }
  },
  "vpnProps": {
    "enable": true,
    "systemProxy": false,
    "ipv6": true,
    "allowBypass": false,
    "dnsHijacking": true,
    "accessControlProps": {
      "enable": true,
      "mode": "acceptSelected",
      "acceptList": [
        "org.fixture.allowed"
      ],
      "rejectList": [],
      "sort": "name",
      "isFilterSystemApp": false,
      "isFilterNonInternetApp": true
    }
  },
  "themeProps": {
    "primaryColor": 4279383126,
    "primaryColors": [
      4279383126,
      4289449455
    ],
    "themeMode": "dark",
    "schemeVariant": "neutral",
    "pureBlack": true,
    "textScale": {
      "enable": true,
      "scale": 1.2
    }
  },
  "proxiesStyleProps": {
    "type": "tab",
    "sortType": "delay",
    "layout": "tight",
    "iconStyle": "standard",
    "cardType": "expand"
  },
  "windowProps": {
    "width": 1040.0,
    "height": 780.0,
    "top": 13.5,
    "left": 42.0
  },
  "patchClashConfig": {
    "mixed-port": 17890,
    "socks-port": 10808,
    "port": 18081,
    "redir-port": 0,
    "tproxy-port": 0,
    "mode": "global",
    "allow-lan": false,
    "log-level": "debug",
    "ipv6": false,
    "find-process-mode": "always",
    "interface-name-mode": "follow",
    "interface-name": "eth-fixture",
    "keep-alive-interval": 41,
    "unified-delay": false,
    "tcp-concurrent": false,
    "tun": {
      "enable": false,
      "device": "FixtureTun",
      "auto-route": true,
      "stack": "mixed",
      "dns-hijack": [
        "any:53"
      ],
      "route-address": [
        "198.18.0.0/15"
      ]
    },
    "dns": {
      "enable": true,
      "listen": "127.0.0.1:1053",
      "prefer-h3": true,
      "use-hosts": false,
      "use-system-hosts": true,
      "respect-rules": false,
      "ipv6": true,
      "default-nameserver": [
        "223.5.5.5"
      ],
      "enhanced-mode": "fake-ip",
      "fake-ip-range": "198.18.0.1/16",
      "fake-ip-filter": [
        "*.fixture.invalid"
      ],
      "nameserver-policy": {
        "fixture.invalid": "https://policy.fixture.invalid/query"
      },
      "nameserver": [
        "https://resolver.fixture.invalid/query?token=fixture-dns-token"
      ],
      "fallback": [
        "tls://8.8.4.4"
      ],
      "proxy-server-nameserver": [
        "https://proxy-dns.fixture.invalid/query"
      ],
      "fallback-filter": {
        "geoip": false,
        "geoip-code": "zz",
        "geosite": [
          "private"
        ],
        "ipcidr": [
          "240.0.0.0/4"
        ],
        "domain": [
          "+.fixture.invalid"
        ]
      }
    },
    "geox-url": {
      "mmdb": "https://geo.fixture.invalid/geo.mmdb",
      "asn": "https://geo.fixture.invalid/asn.mmdb",
      "geoip": "https://geo.fixture.invalid/geoip.dat",
      "geosite": "https://geo.fixture.invalid/geosite.dat"
    },
    "geodata-loader": "standard",
    "global-ua": "FixtureGlobal/4.2",
    "external-controller": "127.0.0.1:9090",
    "hosts": {
      "fixture.invalid": "192.0.2.1"
    },
    "geo-auto-update": true,
    "geo-update-interval": 13
  },
  "excludeSSIDs": [
    "FixtureSSID"
  ]
}`

const webAPISettingsReleasedConfig2 = `{
  "version": 1,
  "currentProfileId": 9007199254740993,
  "overrideDns": false,
  "overrideNtp": false,
  "hotKeyActions": [
    {
      "action": "start",
      "key": 458789,
      "modifiers": [
        "control",
        "alt"
      ]
    }
  ],
  "appSettingProps": {
    "locale": "zh_CN",
    "dashboardWidgets": [
      "networkDetection",
      "connections",
      "memoryInfo"
    ],
    "onlyStatisticsProxy": true,
    "showNotificationStopAction": false,
    "autoLaunch": false,
    "silentLaunch": true,
    "autoRun": false,
    "openLogs": true,
    "closeConnections": false,
    "testUrl": "https://probe.fixture.invalid/generate_204",
    "tabAnimation": "fade",
    "floatingNavigationBar": false,
    "autoCheckUpdate": false,
    "sidebarExpanded": true,
    "disclaimerAccepted": false,
    "crashlyticsTip": false,
    "crashlytics": false,
    "minimizeOnExit": false,
    "hidden": true,
    "developerMode": true,
    "restoreStrategy": "override",
    "showTrayTitle": false,
    "checkCertificate": false,
    "userAgents": [
      "FixtureCurrent/2.0"
    ],
    "hideIp": true,
    "editorLineWrap": true,
    "editorFontSize": "large",
    "serviceOrder": [
      "fixture-service"
    ],
    "disabledServices": [
      "fixture-off"
    ],
    "currentService": null
  },
  "davProps": {
    "uri": "https://dav.fixture.invalid/backup?token=fixture-dav-token",
    "user": "fixture-dav-user",
    "password": "fixture-dav-password",
    "fileName": "fixture-backup.zip"
  },
  "networkProps": {
    "systemProxy": false,
    "bypassDomain": [
      "localhost",
      "*.fixture.invalid"
    ],
    "routeMode": "config",
    "autoSetSystemDns": false,
    "appendSystemDns": true,
    "authentication": {
      "enable": true,
      "username": "fixture-inbound-user",
      "password": "fixture-inbound-password"
    }
  },
  "vpnProps": {
    "enable": true,
    "systemProxy": false,
    "ipv6": true,
    "allowBypass": false,
    "dnsHijacking": true,
    "accessControlProps": {
      "enable": true,
      "mode": "acceptSelected",
      "acceptList": [
        "org.fixture.allowed"
      ],
      "rejectList": [],
      "sort": "name",
      "isFilterSystemApp": false,
      "isFilterNonInternetApp": true
    }
  },
  "themeProps": {
    "primaryColor": 4279383126,
    "primaryColors": [
      4279383126,
      4289449455
    ],
    "themeMode": "dark",
    "schemeVariant": "neutral",
    "pureBlack": true,
    "sidebarBlur": true,
    "textScale": {
      "enable": true,
      "scale": 1.2
    }
  },
  "proxiesStyleProps": {
    "type": "tab",
    "sortType": "delay",
    "layout": "tight",
    "iconStyle": "plain",
    "cardType": "shrink",
    "hideTimeoutProxies": true
  },
  "windowProps": {
    "width": 1040.0,
    "height": 780.0,
    "top": 13.5,
    "left": 42.0
  },
  "patchClashConfig": {
    "mixed-port": 17890,
    "socks-port": 10808,
    "port": 18081,
    "redir-port": 0,
    "tproxy-port": 0,
    "mode": "global",
    "allow-lan": false,
    "log-level": "debug",
    "ipv6": false,
    "find-process-mode": "always",
    "interface-name-mode": "follow",
    "interface-name": "eth-fixture",
    "keep-alive-interval": 41,
    "unified-delay": false,
    "tcp-concurrent": false,
    "tun": {
      "enable": false,
      "device": "FixtureTun",
      "auto-route": true,
      "stack": "mixed",
      "dns-hijack": [
        "any:53"
      ],
      "route-address": [
        "198.18.0.0/15"
      ]
    },
    "dns": {
      "enable": true,
      "listen": "127.0.0.1:1053",
      "listen-routing-mark": 0,
      "prefer-h3": true,
      "use-hosts": false,
      "use-system-hosts": true,
      "respect-rules": false,
      "ipv6": true,
      "ipv6-timeout": 333,
      "cache-algorithm": "arc",
      "cache-max-size": 2048,
      "default-nameserver": [
        "223.5.5.5"
      ],
      "enhanced-mode": "fake-ip",
      "fake-ip-range": "198.18.0.1/16",
      "fake-ip-range6": "fdfe::1/64",
      "fake-ip-filter": [
        "*.fixture.invalid"
      ],
      "fake-ip-filter-mode": "whitelist",
      "fake-ip-ttl": 37,
      "nameserver-policy": {
        "fixture.invalid": "https://policy.fixture.invalid/query"
      },
      "nameserver": [
        "https://resolver.fixture.invalid/query?token=fixture-dns-token"
      ],
      "fallback": [
        "tls://8.8.4.4"
      ],
      "fallback-lazy-query": true,
      "proxy-server-nameserver": [
        "https://proxy-dns.fixture.invalid/query"
      ],
      "proxy-server-nameserver-policy": {
        "proxy.fixture.invalid": "https://proxy-policy.fixture.invalid/query"
      },
      "direct-nameserver": [
        "1.1.1.1"
      ],
      "direct-nameserver-follow-policy": true,
      "fallback-filter": {
        "geoip": false,
        "geoip-code": "zz",
        "geosite": [
          "private"
        ],
        "ipcidr": [
          "240.0.0.0/4"
        ],
        "domain": [
          "+.fixture.invalid"
        ]
      }
    },
    "dns-override-keys": [],
    "ntp": {
      "enable": false,
      "server": "time.apple.com",
      "port": 123,
      "interval": 30,
      "dialer-proxy": "",
      "write-to-system": false
    },
    "ntp-override-keys": [],
    "geox-url": {
      "mmdb": "https://geo.fixture.invalid/geo.mmdb",
      "asn": "https://geo.fixture.invalid/asn.mmdb",
      "geoip": "https://geo.fixture.invalid/geoip.dat",
      "geosite": "https://geo.fixture.invalid/geosite.dat"
    },
    "geodata-loader": "standard",
    "global-ua": "FixtureGlobal/4.2",
    "external-controller": "127.0.0.1:9090",
    "hosts": {
      "fixture.invalid": "192.0.2.1"
    },
    "geo-auto-update": true,
    "geo-update-interval": 13
  },
  "excludeSSIDs": [
    "FixtureSSID"
  ]
}`

const webAPISettingsReleasedConfig3 = `{
  "version": 1,
  "currentProfileId": 9007199254740993,
  "hotKeyActions": [
    {
      "action": "start",
      "key": 458789,
      "modifiers": [
        "control",
        "alt"
      ]
    }
  ],
  "appSettingProps": {
    "locale": "zh_CN",
    "dashboardWidgets": [
      "networkDetection",
      "connections",
      "memoryInfo"
    ],
    "onlyStatisticsProxy": true,
    "showNotificationStopAction": false,
    "autoLaunch": false,
    "silentLaunch": true,
    "autoRun": false,
    "openLogs": true,
    "closeConnections": false,
    "testUrl": "https://probe.fixture.invalid/generate_204",
    "tabAnimation": "fade",
    "floatingNavigationBar": false,
    "autoCheckUpdate": false,
    "sidebarExpanded": true,
    "acceptedDisclaimerVersion": 3,
    "crashlyticsTip": false,
    "crashlytics": false,
    "minimizeOnExit": false,
    "hidden": true,
    "developerMode": true,
    "restoreStrategy": "override",
    "showTrayTitle": false,
    "checkCertificate": false,
    "userAgents": [
      "FixtureCurrent/2.0"
    ],
    "filters": [
      {
        "label": "Fixture",
        "regex": "HK|港"
      }
    ],
    "hideIp": true,
    "editorLineWrap": true,
    "editorFontSize": "large",
    "serviceOrder": [
      "fixture-service"
    ],
    "disabledServices": [
      "fixture-off"
    ],
    "currentService": null
  },
  "davProps": {
    "uri": "https://dav.fixture.invalid/backup?token=fixture-dav-token",
    "user": "fixture-dav-user",
    "password": "fixture-dav-password",
    "fileName": "fixture-backup.zip"
  },
  "networkProps": {
    "systemProxy": false,
    "bypassDomain": [
      "localhost",
      "*.fixture.invalid"
    ],
    "bypassPrivateRoute": false,
    "autoSetSystemDns": false,
    "appendSystemDns": true,
    "authentication": {
      "enable": true,
      "username": "fixture-inbound-user",
      "password": "fixture-inbound-password"
    }
  },
  "vpnProps": {
    "enable": true,
    "systemProxy": false,
    "ipv6": true,
    "allowBypass": false,
    "dnsHijacking": true,
    "accessControlProps": {
      "enable": true,
      "mode": "acceptSelected",
      "acceptList": [
        "org.fixture.allowed"
      ],
      "rejectList": [],
      "sort": "name",
      "isFilterSystemApp": false,
      "isFilterNonInternetApp": true
    }
  },
  "themeProps": {
    "primaryColor": 4279383126,
    "primaryColors": [
      4279383126,
      4289449455
    ],
    "themeMode": "dark",
    "schemeVariant": "neutral",
    "pureBlack": true,
    "sidebarBlur": true,
    "textScale": {
      "enable": true,
      "scale": 1.2
    },
    "systemFontFamily": "Fixture Font"
  },
  "proxiesStyleProps": {
    "type": "tab",
    "sortType": "delay",
    "layout": "tight",
    "iconStyle": "plain",
    "cardType": "shrink",
    "hideTimeoutProxies": true
  },
  "windowProps": {
    "width": 1040.0,
    "height": 780.0,
    "top": 13.5,
    "left": 42.0,
    "scale": 1.25
  },
  "patchClashConfig": {
    "mixed-port": 17890,
    "socks-port": 10808,
    "port": 18081,
    "redir-port": 0,
    "tproxy-port": 0,
    "mode": "global",
    "allow-lan": false,
    "log-level": "debug",
    "ipv6": false,
    "find-process-mode": "always",
    "interface-name-mode": "follow",
    "interface-name": "eth-fixture",
    "keep-alive-interval": 41,
    "keep-alive-idle": 17,
    "disable-keep-alive": false,
    "routing-mark": 0,
    "unified-delay": false,
    "tcp-concurrent": false,
    "tun": {
      "enable": false,
      "device": "FixtureTun",
      "auto-route": true,
      "stack": "mixed",
      "dns-hijack": [
        "any:53"
      ],
      "route-address": [
        "198.18.0.0/15"
      ],
      "mtu": 8192,
      "congestion-controller": "reno",
      "strict-route": false,
      "route-exclude-address": [
        "10.0.0.0/8"
      ]
    },
    "dns": {
      "enable": true,
      "listen": "127.0.0.1:1053",
      "listen-routing-mark": 0,
      "prefer-h3": true,
      "use-hosts": false,
      "use-system-hosts": true,
      "respect-rules": false,
      "ipv6": true,
      "ipv6-timeout": 333,
      "cache-algorithm": "arc",
      "cache-max-size": 2048,
      "default-nameserver": [
        "223.5.5.5"
      ],
      "enhanced-mode": "fake-ip",
      "fake-ip-range": "198.18.0.1/16",
      "fake-ip-range6": "fdfe::1/64",
      "fake-ip-filter": [
        "*.fixture.invalid"
      ],
      "fake-ip-filter-mode": "whitelist",
      "fake-ip-ttl": 37,
      "nameserver-policy": {
        "fixture.invalid": "https://policy.fixture.invalid/query"
      },
      "nameserver": [
        "https://resolver.fixture.invalid/query?token=fixture-dns-token"
      ],
      "fallback": [
        "tls://8.8.4.4"
      ],
      "fallback-lazy-query": true,
      "proxy-server-nameserver": [
        "https://proxy-dns.fixture.invalid/query"
      ],
      "proxy-server-nameserver-policy": {
        "proxy.fixture.invalid": "https://proxy-policy.fixture.invalid/query"
      },
      "direct-nameserver": [
        "1.1.1.1"
      ],
      "direct-nameserver-follow-policy": true,
      "fallback-filter": {
        "geoip": false,
        "geoip-code": "zz",
        "geosite": [
          "private"
        ],
        "ipcidr": [
          "240.0.0.0/4"
        ],
        "domain": [
          "+.fixture.invalid"
        ]
      }
    },
    "dns-override-keys": [
      "nameserver",
      "fake-ip-filter"
    ],
    "geox-url": {
      "mmdb": "https://geo.fixture.invalid/geo.mmdb",
      "asn": "https://geo.fixture.invalid/asn.mmdb",
      "geoip": "https://geo.fixture.invalid/geoip.dat",
      "geosite": "https://geo.fixture.invalid/geosite.dat"
    },
    "geodata-loader": "standard",
    "global-ua": "FixtureGlobal/4.2",
    "external-controller": "127.0.0.1:9090",
    "secret": "fixture-controller-secret",
    "hosts": {
      "fixture.invalid": "192.0.2.1"
    },
    "geo-auto-update": true,
    "geo-update-interval": 13
  },
  "excludeSSIDs": [
    "FixtureSSID"
  ]
}`

const webAPISettingsReleasedConfig4 = `{
  "version": 1,
  "currentProfileId": 9007199254740993,
  "hotKeyActions": [
    {
      "action": "start",
      "key": 458789,
      "modifiers": [
        "control",
        "alt"
      ]
    }
  ],
  "appSettingProps": {
    "locale": "zh_CN",
    "dashboardWidgets": [
      "currentOutbound",
      "networkSpeed",
      "intranetIp",
      "memoryInfo"
    ],
    "onlyStatisticsProxy": true,
    "showNotificationStopAction": false,
    "autoLaunch": false,
    "silentLaunch": true,
    "autoRun": false,
    "openLogs": true,
    "closeConnections": false,
    "testUrl": "https://probe.fixture.invalid/generate_204",
    "tabAnimation": "fade",
    "floatingNavigationBar": false,
    "autoCheckUpdate": false,
    "sidebarExpanded": true,
    "acceptedDisclaimerVersion": 3,
    "minimizeOnExit": false,
    "hidden": true,
    "developerMode": true,
    "restoreStrategy": "override",
    "showTrayTitle": false,
    "checkCertificate": false,
    "userAgents": [
      "FixtureCurrent/2.0"
    ],
    "filters": [
      {
        "label": "Fixture",
        "regex": "HK|港"
      }
    ],
    "hideIp": true,
    "editorLineWrap": true,
    "editorFontSize": "large",
    "serviceOrder": [
      "fixture-service"
    ],
    "disabledServices": [
      "fixture-off"
    ],
    "tableColumnWidths": {
      "records": {
        "name": 271.5
      }
    }
  },
  "davProps": {
    "uri": "https://dav.fixture.invalid/backup?token=fixture-dav-token",
    "user": "fixture-dav-user",
    "password": "fixture-dav-password",
    "fileName": "fixture-backup.zip"
  },
  "networkProps": {
    "systemProxy": false,
    "bypassDomain": [
      "localhost",
      "*.fixture.invalid"
    ],
    "bypassPrivateRoute": false,
    "autoSetSystemDns": false,
    "appendSystemDns": true,
    "authentication": {
      "enable": true,
      "username": "fixture-inbound-user",
      "password": "fixture-inbound-password"
    }
  },
  "vpnProps": {
    "enable": true,
    "systemProxy": false,
    "ipv6": true,
    "allowBypass": false,
    "dnsHijacking": true,
    "accessControlProps": {
      "enable": true,
      "mode": "acceptSelected",
      "acceptList": [
        "org.fixture.allowed"
      ],
      "rejectList": [],
      "sort": "name",
      "isFilterSystemApp": false,
      "isFilterNonInternetApp": true
    }
  },
  "themeProps": {
    "primaryColor": 4279383126,
    "primaryColors": [
      4279383126,
      4289449455
    ],
    "themeMode": "dark",
    "colorStyle": "soft",
    "pureBlack": true,
    "sidebarBlur": true,
    "liquidGlass": "ultra",
    "textScale": {
      "enable": true,
      "scale": 1.2
    },
    "systemFontFamily": "Fixture Font"
  },
  "proxiesStyleProps": {
    "type": "tab",
    "sortType": "delay",
    "layout": "tight",
    "iconStyle": "plain",
    "cardType": "compact",
    "hideTimeoutProxies": true
  },
  "windowGeometry": {
    "width": 1040.0,
    "height": 780.0,
    "top": 13.5,
    "left": 42.0,
    "scale": 1.25
  },
  "patchClashConfig": {
    "mixed-port": 17890,
    "socks-port": 10808,
    "port": 18081,
    "redir-port": 0,
    "tproxy-port": 0,
    "mode": "global",
    "allow-lan": false,
    "log-level": "debug",
    "ipv6": false,
    "find-process-mode": "always",
    "interface-name-mode": "follow",
    "interface-name": "eth-fixture",
    "keep-alive-interval": 41,
    "keep-alive-idle": 17,
    "disable-keep-alive": false,
    "routing-mark": 0,
    "unified-delay": false,
    "tcp-concurrent": false,
    "tun": {
      "enable": false,
      "device": "FixtureTun",
      "stack": "mixed",
      "route-address": [
        "198.18.0.0/15"
      ],
      "mtu": 8192,
      "congestion-controller": "reno",
      "route-exclude-address": [
        "10.0.0.0/8"
      ]
    },
    "dns": {
      "enable": true,
      "listen": "127.0.0.1:1053",
      "listen-routing-mark": 0,
      "prefer-h3": true,
      "use-hosts": false,
      "use-system-hosts": true,
      "respect-rules": false,
      "ipv6": true,
      "ipv6-timeout": 333,
      "cache-algorithm": "arc",
      "cache-max-size": 2048,
      "default-nameserver": [
        "223.5.5.5"
      ],
      "enhanced-mode": "fake-ip",
      "fake-ip-range": "198.18.0.1/16",
      "fake-ip-range6": "fdfe::1/64",
      "fake-ip-filter": [
        "*.fixture.invalid"
      ],
      "fake-ip-filter-mode": "whitelist",
      "fake-ip-ttl": 37,
      "nameserver-policy": {
        "fixture.invalid": "https://policy.fixture.invalid/query"
      },
      "nameserver": [
        "https://resolver.fixture.invalid/query?token=fixture-dns-token"
      ],
      "fallback": [
        "tls://8.8.4.4"
      ],
      "fallback-lazy-query": true,
      "proxy-server-nameserver": [
        "https://proxy-dns.fixture.invalid/query"
      ],
      "proxy-server-nameserver-policy": {
        "proxy.fixture.invalid": "https://proxy-policy.fixture.invalid/query"
      },
      "direct-nameserver": [
        "1.1.1.1"
      ],
      "direct-nameserver-follow-policy": true,
      "fallback-filter": {
        "geoip": false,
        "geoip-code": "zz",
        "geosite": [
          "private"
        ],
        "ipcidr": [
          "240.0.0.0/4"
        ],
        "domain": [
          "+.fixture.invalid"
        ]
      }
    },
    "dns-override-keys": [
      "nameserver",
      "fake-ip-filter"
    ],
    "tun-override": {
      "mtu": 8192,
      "congestion-controller": "reno",
      "strict-route": false,
      "route-address": [
        "198.18.0.0/15"
      ],
      "route-exclude-address": [
        "10.0.0.0/8"
      ],
      "dns-hijack": [
        "any:53"
      ],
      "udp-timeout": 601,
      "disable-icmp-forwarding": false,
      "gso": false,
      "include-interface": [],
      "exclude-interface": [
        "lo"
      ],
      "include-uid": [],
      "exclude-uid": [
        1001
      ],
      "exclude-src-port": [
        1234
      ],
      "exclude-dst-port": [
        4321
      ]
    },
    "tun-override-keys": [
      "mtu",
      "strict-route",
      "route-address",
      "dns-hijack"
    ],
    "geox-url": {
      "mmdb": "https://geo.fixture.invalid/geo.mmdb",
      "asn": "https://geo.fixture.invalid/asn.mmdb",
      "geoip": "https://geo.fixture.invalid/geoip.dat",
      "geosite": "https://geo.fixture.invalid/geosite.dat"
    },
    "geodata-loader": "standard",
    "global-ua": "FixtureGlobal/4.2",
    "external-controller": "127.0.0.1:9090",
    "secret": "fixture-controller-secret",
    "hosts": {
      "fixture.invalid": "192.0.2.1"
    },
    "geo-auto-update": true,
    "geo-update-interval": 13
  },
  "excludeSSIDs": [
    "FixtureSSID"
  ]
}`

const webAPISettingsReleasedConfig5 = `{
  "version": 1,
  "currentProfileId": 9007199254740993,
  "hotKeyActions": [
    {
      "action": "start",
      "key": 458789,
      "modifiers": [
        "control",
        "alt"
      ]
    }
  ],
  "appSettingProps": {
    "locale": "zh_CN",
    "dashboardWidgets": [
      "currentOutbound",
      "networkSpeed",
      "intranetIp",
      "memoryInfo"
    ],
    "onlyStatisticsProxy": true,
    "showNotificationStopAction": false,
    "autoLaunch": false,
    "silentLaunch": true,
    "autoRun": false,
    "openLogs": true,
    "closeConnections": false,
    "delayTestTimeout": 12345,
    "tabAnimation": "fade",
    "floatingNavigationBar": false,
    "autoCheckUpdate": false,
    "sidebarExpanded": true,
    "acceptedDisclaimerVersion": 3,
    "minimizeOnExit": false,
    "hidden": true,
    "developerMode": true,
    "restoreStrategy": "override",
    "showTrayTitle": false,
    "monochromeTrayIcon": false,
    "checkCertificate": false,
    "userAgents": [
      "FixtureCurrent/2.0"
    ],
    "filters": [
      {
        "label": "Fixture",
        "regex": "HK|港"
      }
    ],
    "hideIp": true,
    "editorLineWrap": true,
    "editorFontSize": "large",
    "serviceOrder": [
      "fixture-service"
    ],
    "disabledServices": [
      "fixture-off"
    ],
    "tableColumnWidths": {
      "records": {
        "name": 271.5
      }
    }
  },
  "davProps": {
    "uri": "https://dav.fixture.invalid/backup?token=fixture-dav-token",
    "user": "fixture-dav-user",
    "password": "fixture-dav-password",
    "fileName": "fixture-backup.zip"
  },
  "networkProps": {
    "systemProxy": false,
    "bypassDomain": [
      "localhost",
      "*.fixture.invalid"
    ],
    "bypassPrivateRoute": false,
    "autoSetSystemDns": false,
    "appendSystemDns": true,
    "authentication": {
      "enable": true,
      "username": "fixture-inbound-user",
      "password": "fixture-inbound-password"
    }
  },
  "vpnProps": {
    "enable": true,
    "systemProxy": false,
    "ipv6": true,
    "allowBypass": false,
    "dnsHijacking": true,
    "accessControlProps": {
      "enable": true,
      "mode": "acceptSelected",
      "acceptList": [
        "org.fixture.allowed"
      ],
      "rejectList": [],
      "sort": "name",
      "isFilterSystemApp": false,
      "isFilterNonInternetApp": true
    }
  },
  "themeProps": {
    "primaryColor": 4279383126,
    "primaryColors": [
      4279383126,
      4289449455
    ],
    "themeMode": "dark",
    "colorStyle": "soft",
    "pureBlack": true,
    "sidebarBlur": true,
    "liquidGlass": "ultra",
    "textScale": {
      "enable": true,
      "scale": 1.2
    },
    "systemFontFamily": "Fixture Font"
  },
  "proxiesStyleProps": {
    "type": "tab",
    "sortType": "delay",
    "layout": "tight",
    "iconStyle": "plain",
    "cardType": "compact",
    "hideTimeoutProxies": true
  },
  "windowGeometry": {
    "width": 1040.0,
    "height": 780.0,
    "top": 13.5,
    "left": 42.0,
    "scale": 1.25
  },
  "patchClashConfig": {
    "mixed-port": 17890,
    "socks-port": 10808,
    "port": 18081,
    "redir-port": 0,
    "tproxy-port": 0,
    "mode": "global",
    "allow-lan": false,
    "log-level": "debug",
    "ipv6": false,
    "find-process-mode": "always",
    "interface-name-mode": "follow",
    "interface-name": "eth-fixture",
    "keep-alive-interval": 41,
    "keep-alive-idle": 17,
    "disable-keep-alive": false,
    "routing-mark": 0,
    "unified-delay": false,
    "tcp-concurrent": false,
    "tun": {
      "enable": false,
      "device": "FixtureTun",
      "stack": "mixed",
      "route-address": [
        "198.18.0.0/15"
      ],
      "mtu": 8192,
      "congestion-controller": "reno",
      "route-exclude-address": [
        "10.0.0.0/8"
      ]
    },
    "dns": {
      "enable": true,
      "listen": "127.0.0.1:1053",
      "listen-routing-mark": 0,
      "prefer-h3": true,
      "use-hosts": false,
      "use-system-hosts": true,
      "respect-rules": false,
      "ipv6": true,
      "ipv6-timeout": 333,
      "cache-algorithm": "arc",
      "cache-max-size": 2048,
      "default-nameserver": [
        "223.5.5.5"
      ],
      "enhanced-mode": "fake-ip",
      "fake-ip-range": "198.18.0.1/16",
      "fake-ip-range6": "fdfe::1/64",
      "fake-ip-filter": [
        "*.fixture.invalid"
      ],
      "fake-ip-filter-mode": "whitelist",
      "fake-ip-ttl": 37,
      "nameserver-policy": {
        "fixture.invalid": "https://policy.fixture.invalid/query"
      },
      "nameserver": [
        "https://resolver.fixture.invalid/query?token=fixture-dns-token"
      ],
      "fallback": [
        "tls://8.8.4.4"
      ],
      "fallback-lazy-query": true,
      "proxy-server-nameserver": [
        "https://proxy-dns.fixture.invalid/query"
      ],
      "proxy-server-nameserver-policy": {
        "proxy.fixture.invalid": "https://proxy-policy.fixture.invalid/query"
      },
      "direct-nameserver": [
        "1.1.1.1"
      ],
      "direct-nameserver-follow-policy": true,
      "fallback-filter": {
        "geoip": false,
        "geoip-code": "zz",
        "geosite": [
          "private"
        ],
        "ipcidr": [
          "240.0.0.0/4"
        ],
        "domain": [
          "+.fixture.invalid"
        ]
      }
    },
    "dns-override-keys": [
      "nameserver",
      "fake-ip-filter"
    ],
    "tun-override": {
      "mtu": 8192,
      "congestion-controller": "reno",
      "strict-route": false,
      "route-address": [
        "198.18.0.0/15"
      ],
      "route-exclude-address": [
        "10.0.0.0/8"
      ],
      "dns-hijack": [
        "any:53"
      ],
      "udp-timeout": 601,
      "disable-icmp-forwarding": false,
      "gso": false,
      "include-interface": [],
      "exclude-interface": [
        "lo"
      ],
      "include-uid": [],
      "exclude-uid": [
        1001
      ],
      "exclude-src-port": [
        1234
      ],
      "exclude-dst-port": [
        4321
      ]
    },
    "tun-override-keys": [
      "mtu",
      "strict-route",
      "route-address",
      "dns-hijack"
    ],
    "geox-url": {
      "mmdb": "https://geo.fixture.invalid/geo.mmdb",
      "asn": "https://geo.fixture.invalid/asn.mmdb",
      "geoip": "https://geo.fixture.invalid/geoip.dat",
      "geosite": "https://geo.fixture.invalid/geosite.dat"
    },
    "geodata-loader": "standard",
    "global-ua": "FixtureGlobal/4.2",
    "external-controller": "127.0.0.1:9090",
    "secret": "fixture-controller-secret",
    "hosts": {
      "fixture.invalid": "192.0.2.1"
    },
    "geo-auto-update": true,
    "geo-update-interval": 13,
    "test-url": "https://probe.fixture.invalid/generate_204",
    "general-override-keys": [
      "test-url",
      "unified-delay",
      "tcp-concurrent",
      "ipv6",
      "geodata-loader"
    ]
  },
  "excludeSSIDs": [
    "FixtureSSID"
  ]
}`

const webAPISettingsReleasedConfig6 = `{
  "version": 1,
  "currentProfileId": 9007199254740993,
  "hotKeyActions": [
    {
      "action": "start",
      "key": 458789,
      "modifiers": [
        "control",
        "alt"
      ]
    }
  ],
  "appSettingProps": {
    "locale": "zh_CN",
    "dashboardWidgets": [
      "currentOutbound",
      "networkSpeed",
      "intranetIp",
      "memoryInfo"
    ],
    "onlyStatisticsProxy": true,
    "showNotificationStopAction": false,
    "autoLaunch": false,
    "silentLaunch": true,
    "autoRun": false,
    "openLogs": true,
    "closeConnections": false,
    "requestFeed": "recent",
    "delayTestTimeout": 12345,
    "tabAnimation": "fade",
    "floatingNavigationBar": false,
    "autoCheckUpdate": false,
    "sidebarExpanded": true,
    "acceptedDisclaimerVersion": 3,
    "minimizeOnExit": false,
    "hidden": true,
    "developerMode": true,
    "restoreStrategy": "override",
    "showTrayTitle": false,
    "monochromeTrayIcon": false,
    "checkCertificate": false,
    "userAgents": [
      "FixtureCurrent/2.0"
    ],
    "filters": [
      {
        "label": "Fixture",
        "regex": "HK|港"
      }
    ],
    "hideIp": true,
    "editorLineWrap": true,
    "editorFontSize": "large",
    "serviceOrder": [
      "fixture-service"
    ],
    "disabledServices": [
      "fixture-off"
    ],
    "tableColumnWidths": {
      "records": {
        "name": 271.5
      }
    },
    "recordGroupings": {
      "requests": "host"
    }
  },
  "davProps": {
    "uri": "https://dav.fixture.invalid/backup?token=fixture-dav-token",
    "user": "fixture-dav-user",
    "password": "fixture-dav-password",
    "fileName": "fixture-backup.zip"
  },
  "networkProps": {
    "systemProxy": false,
    "bypassDomain": [
      "localhost",
      "*.fixture.invalid"
    ],
    "bypassPrivateRoute": false,
    "autoSetSystemDns": false,
    "appendSystemDns": true,
    "authentication": {
      "enable": true,
      "username": "fixture-inbound-user",
      "password": "fixture-inbound-password"
    }
  },
  "vpnProps": {
    "enable": true,
    "systemProxy": false,
    "ipv6": true,
    "allowBypass": false,
    "dnsHijacking": true,
    "accessControlProps": {
      "enable": true,
      "mode": "acceptSelected",
      "acceptList": [
        "org.fixture.allowed"
      ],
      "rejectList": [],
      "sort": "name",
      "isFilterSystemApp": false,
      "isFilterNonInternetApp": true
    }
  },
  "themeProps": {
    "primaryColor": 4279383126,
    "primaryColors": [
      4279383126,
      4289449455
    ],
    "themeMode": "dark",
    "colorStyle": "soft",
    "pureBlack": true,
    "sidebarBlur": true,
    "liquidGlass": "ultra",
    "textScale": {
      "enable": true,
      "scale": 1.2
    },
    "systemFontFamily": "Fixture Font"
  },
  "proxiesStyleProps": {
    "type": "tab",
    "sortType": "delay",
    "layout": "tight",
    "iconStyle": "plain",
    "cardType": "compact",
    "hideTimeoutProxies": true
  },
  "windowGeometry": {
    "width": 1040.0,
    "height": 780.0,
    "top": 13.5,
    "left": 42.0,
    "scale": 1.25
  },
  "patchClashConfig": {
    "mixed-port": 17890,
    "socks-port": 10808,
    "port": 18081,
    "redir-port": 0,
    "tproxy-port": 0,
    "mode": "global",
    "allow-lan": false,
    "log-level": "debug",
    "ipv6": false,
    "find-process-mode": "always",
    "interface-name-mode": "follow",
    "interface-name": "eth-fixture",
    "keep-alive-interval": 41,
    "keep-alive-idle": 17,
    "disable-keep-alive": false,
    "routing-mark": 0,
    "unified-delay": false,
    "tcp-concurrent": false,
    "tun": {
      "enable": false,
      "device": "FixtureTun",
      "stack": "mixed",
      "route-address": [
        "198.18.0.0/15"
      ],
      "mtu": 8192,
      "congestion-controller": "reno",
      "route-exclude-address": [
        "10.0.0.0/8"
      ]
    },
    "dns": {
      "enable": true,
      "listen": "127.0.0.1:1053",
      "listen-routing-mark": 0,
      "prefer-h3": true,
      "use-hosts": false,
      "use-system-hosts": true,
      "respect-rules": false,
      "ipv6": true,
      "ipv6-timeout": 333,
      "cache-algorithm": "arc",
      "cache-max-size": 2048,
      "default-nameserver": [
        "223.5.5.5"
      ],
      "enhanced-mode": "fake-ip",
      "fake-ip-range": "198.18.0.1/16",
      "fake-ip-range6": "fdfe::1/64",
      "fake-ip-filter": [
        "*.fixture.invalid"
      ],
      "fake-ip-filter-mode": "whitelist",
      "fake-ip-ttl": 37,
      "nameserver-policy": {
        "fixture.invalid": "https://policy.fixture.invalid/query"
      },
      "nameserver": [
        "https://resolver.fixture.invalid/query?token=fixture-dns-token"
      ],
      "fallback": [
        "tls://8.8.4.4"
      ],
      "fallback-lazy-query": true,
      "proxy-server-nameserver": [
        "https://proxy-dns.fixture.invalid/query"
      ],
      "proxy-server-nameserver-policy": {
        "proxy.fixture.invalid": "https://proxy-policy.fixture.invalid/query"
      },
      "direct-nameserver": [
        "1.1.1.1"
      ],
      "direct-nameserver-follow-policy": true,
      "fallback-filter": {
        "geoip": false,
        "geoip-code": "zz",
        "geosite": [
          "private"
        ],
        "ipcidr": [
          "240.0.0.0/4"
        ],
        "domain": [
          "+.fixture.invalid"
        ]
      }
    },
    "dns-override-keys": [
      "nameserver",
      "fake-ip-filter"
    ],
    "tun-override": {
      "mtu": 8192,
      "congestion-controller": "reno",
      "strict-route": false,
      "route-address": [
        "198.18.0.0/15"
      ],
      "route-exclude-address": [
        "10.0.0.0/8"
      ],
      "dns-hijack": [
        "any:53"
      ],
      "udp-timeout": 601,
      "disable-icmp-forwarding": false,
      "gso": false,
      "include-interface": [],
      "exclude-interface": [
        "lo"
      ],
      "include-uid": [],
      "exclude-uid": [
        1001
      ],
      "exclude-src-port": [
        1234
      ],
      "exclude-dst-port": [
        4321
      ]
    },
    "tun-override-keys": [
      "mtu",
      "strict-route",
      "route-address",
      "dns-hijack"
    ],
    "geox-url": {
      "mmdb": "https://geo.fixture.invalid/geo.mmdb",
      "asn": "https://geo.fixture.invalid/asn.mmdb",
      "geoip": "https://geo.fixture.invalid/geoip.dat",
      "geosite": "https://geo.fixture.invalid/geosite.dat"
    },
    "geodata-loader": "standard",
    "global-ua": "FixtureGlobal/4.2",
    "external-controller": "127.0.0.1:9090",
    "secret": "fixture-controller-secret",
    "hosts": {
      "fixture.invalid": "192.0.2.1"
    },
    "geo-auto-update": true,
    "geo-update-interval": 13,
    "test-url": "https://probe.fixture.invalid/generate_204",
    "general-override-keys": [
      "test-url",
      "unified-delay",
      "tcp-concurrent",
      "ipv6",
      "geodata-loader"
    ],
    "mitm": {
      "enable": false,
      "hostname": [
        "*.fixture.invalid"
      ],
      "skip-server-cert-verify": false,
      "auto-quic-block": true,
      "h2": true,
      "decrypt-untrusted": false,
      "trusted-fingerprint": null,
      "capture": false,
      "capture-entries": 500
    }
  },
  "excludeSSIDs": [
    "FixtureSSID"
  ]
}`
