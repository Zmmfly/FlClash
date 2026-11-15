//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func webCustomSourceTestProxy(name, server string) WebAPIProxyDefinition {
	data, _ := json.Marshal(map[string]any{"name": name, "type": "trojan", "server": server, "port": 443, "password": "synthetic"})
	var proxy WebAPIProxyDefinition
	json.Unmarshal(data, &proxy)
	return proxy
}

func TestWebCustomSourceNativeImportMappingAndReferences(t *testing.T) {
	// build/api-custom-source-contract.md: mirror actual CustomImportApp reuse, not ordinary profile import.
	snapshot := webCustomSourceSnapshot{Target: WebAPIProfile{ID: "1"}, Profiles: []WebAPIProfile{{ID: "2", WebAPIProfileInput: WebAPIProfileInput{Type: "url", Label: "Held provider", URL: "https://provider.test/sub", AgeSecretKey: "synthetic-age"}}}, Providers: []WebAPIClashProvider{{ID: "3", Label: "Held rules", URL: "https://provider.test/rules"}}, Proxies: []WebAPICustomProxy{{ID: "4", Definition: webCustomSourceTestProxy("Held", "node.test")}, {ID: "5", Definition: webCustomSourceTestProxy("Move", "held.test")}}}
	data := []byte(`proxy-providers:
  same: {type: http, url: https://provider.test/sub}
  local: {type: file, path: /never/read/host.yaml}
rule-providers:
  same: {type: http, url: https://provider.test/rules}
proxies:
  - {name: Renamed, type: trojan, server: node.test, port: "443", password: synthetic, dialer-proxy: Group}
  - {name: Move, type: trojan, server: moved.test, port: 443, password: synthetic}
proxy-groups:
  - {name: Group, type: select, proxies: [Move, DIRECT], use: [same, local], use-position: {same: 1, local: 2}}
rules:
  - RULE-SET,same,Group
  - AND,((RULE-SET,same),(NETWORK,UDP)),REJECT
  - SUB-RULE,(NETWORK,tcp),unsupported
  - MATCH,Group
dns:
  nameserver: ["https://dns.test/dns-query#Move"]
  nameserver-policy: {"rule-set:same": "https://dns.test/query#Move"}
ntp: {enable: true, dialer-proxy: Move}
sniffer: {enable: true, sniff: {tls: {ports: [443]}}}
tun: {enable: true, exclude-interface: [synthetic0]}
`)
	parsed, err := webCustomSourceParse(data, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	custom := parsed.Custom
	if len(custom.ProxyProviders) != 1 || custom.ProxyProviders[0].Label != "local" || !strings.Contains(custom.ProxyProviders[0].Content, "[]") || len(custom.RuleProviders) != 0 || len(parsed.Dependencies) != 0 {
		t.Fatalf("provider reuse: %#v", custom)
	}
	if len(custom.Proxies) != 1 || webAPIProxyString(custom.Proxies[0], "name") != "Move-01" || custom.Dialers["Held"] != "Group" {
		t.Fatalf("proxy reuse %#v", custom)
	}
	group := custom.ProxyGroups[0].Definition
	if group.Proxies[0] != "Move-01" || group.Use[0] != "Held provider" || group.UsePosition["Held provider"] != 1 || len(group.UsePosition) != 1 {
		t.Fatalf("group references %#v", group)
	}
	if *custom.Rules[0].RuleProvider != "Held rules" || !strings.Contains(*custom.Rules[1].Content, "Held rules") || parsed.DroppedSubRules != 1 {
		t.Fatalf("rule references %#v", custom.Rules)
	}
	if custom.Overrides.NTP.DialerProxy != "Move-01" || !strings.HasSuffix(custom.Overrides.DNS.Nameserver[0], "#Move-01") || custom.Overrides.Sniffer.Sniff["TLS"].Ports[0] != "443" || len(custom.Overrides.TunOverrideKeys) != 1 {
		t.Fatalf("native overrides %#v", custom.Overrides)
	}
}

func TestWebCustomSourceStructuredIdentitiesRemainExactAndCSVSeparate(t *testing.T) {
	names := []string{"Comma,name", `Quote"name`, `Back\slash`, " edge spaces "}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			quoted, _ := json.Marshal(name)
			data := []byte("proxies:\n - {name: " + string(quoted) + ", type: trojan, server: node.test, port: 443, password: synthetic}\nproxy-groups:\n - {name: G, type: select, proxies: [" + string(quoted) + "]}\nproxy-providers:\n " + string(quoted) + ": {type: inline, payload: []}\n")
			parsed, err := webCustomSourceParse(data, webCustomSourceSnapshot{})
			if err != nil {
				t.Fatal(err)
			}
			if got := webAPIProxyString(parsed.Custom.Proxies[0], "name"); got != name {
				t.Fatalf("name changed %q", got)
			}
			if parsed.Custom.ProxyGroups[0].Definition.Proxies[0] != name || parsed.Custom.ProxyProviders[0].Label != name {
				t.Fatal("structured reference changed")
			}
		})
	}
	_, err := webCustomSourceParse([]byte("rules:\n - 'MATCH,Comma,name'\n"), webCustomSourceSnapshot{})
	if err == nil {
		t.Fatal("unrepresentable rule target accepted")
	}
}

func TestWebCustomSourceYAMLWorkAndByteBoundary(t *testing.T) {
	prefix := []byte("proxies: []\n#")
	atLimit := append(prefix, bytes.Repeat([]byte{'x'}, WebAPIMaxContentBytes-len(prefix))...)
	if _, _, err := webCustomSourceDocument(atLimit); err != nil {
		t.Fatalf("32MiB boundary rejected: %v", err)
	}
	if _, _, err := webCustomSourceDocument(append(atLimit, 'x')); err == nil {
		t.Fatal("oversize accepted")
	}
	for _, data := range []string{"<html>success</html>", "unknown: value\n", "proxies: [\n", "proxies: []\n---\nrules: []\n", "a: &a [*a]\nproxies: []\n", "a: &a [x,x,x,x,x,x,x,x,x,x]\nb: &b [*a,*a,*a,*a,*a,*a,*a,*a,*a,*a]\nc: &c [*b,*b,*b,*b,*b,*b,*b,*b,*b,*b]\nd: &d [*c,*c,*c,*c,*c,*c,*c,*c,*c,*c]\ne: &e [*d,*d,*d,*d,*d,*d,*d,*d,*d,*d]\nf: &f [*e,*e,*e,*e,*e,*e,*e,*e,*e,*e]\ng: [*f,*f,*f,*f,*f,*f,*f,*f,*f,*f]\nproxies: []\n"} {
		if _, _, err := webCustomSourceDocument([]byte(data)); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
}

func TestWebCustomSourceNamesDoNotCollapseDistinctProviderHeaders(t *testing.T) {
	data := []byte(`proxy-providers:
  first:
    type: http
    url: https://provider.test/sub
    header: {Authorization: [Bearer first]}
  second:
    type: http
    url: https://provider.test/sub
    header: {Authorization: [Bearer second]}
`)
	parsed, err := webCustomSourceParse(data, webCustomSourceSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Dependencies) != 2 || parsed.Dependencies[0].Headers["Authorization"][0] == parsed.Dependencies[1].Headers["Authorization"][0] {
		t.Fatal("distinct header identity collapsed")
	}
	for _, bad := range []string{"header: {X-Internal: [secret]}", "header: {Cookie: [one, two]}", "url: http://user:secret@provider.test/sub\n    header: {Authorization: [Bearer collision]}"} {
		_, err := webCustomSourceParse([]byte("proxy-providers:\n  p:\n    type: http\n    "+bad+"\n"), webCustomSourceSnapshot{})
		if err == nil {
			t.Fatal("invalid source headers accepted")
		}
	}
}
