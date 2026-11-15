package core

import (
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"go.yaml.in/yaml/v3"
)

func TestProxyDependencyMembershipMatchesNativeRegexpAndProviderOrder(t *testing.T) {
	for _, test := range []struct {
		name     string
		group    map[string]any
		provider map[string]any
	}{
		{"explicit not include-filtered", map[string]any{"proxies": []string{"A"}, "filter": "^B$"}, nil},
		{"include filtered", map[string]any{"include-all-proxies": true, "filter": "(?<=B)eta"}, nil},
		{"explicit exclusion", map[string]any{"proxies": []string{"A", "Beta"}, "exclude-filter": "^A$"}, nil},
		{"exclude type", map[string]any{"include-all-proxies": true, "exclude-type": "SoCkS5"}, nil},
		{"empty fallback", map[string]any{"include-all-proxies": true, "filter": "^nobody$", "empty-fallback": "A"}, nil},
		{"inline filtered after prefix", map[string]any{"use": []string{"P"}, "filter": "^sub-Member$"}, map[string]any{"type": "inline", "payload": []map[string]any{dependencyTestProxy("Member", "")}, "override": map[string]any{"additional-prefix": "sub-"}}},
		{"inline duplicate filters see prior renaming", map[string]any{"use": []string{"P"}, "filter": "^sub-sub-Member$"}, map[string]any{"type": "inline", "filter": "Member`sub-Member", "payload": []map[string]any{dependencyTestProxy("Member", "")}, "override": map[string]any{"additional-prefix": "sub-"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			group := map[string]any{"name": "G", "type": "select"}
			for key, value := range test.group {
				group[key] = value
			}
			var providers map[string]map[string]any
			if test.provider != nil {
				providers = map[string]map[string]any{"P": test.provider}
				for _, item := range test.provider["payload"].([]map[string]any) {
					item["server"] = "127.0.0.1"
					item["port"] = 1
				}
			}
			raw := dependencyTestRaw([]map[string]any{dependencyTestProxy("A", ""), dependencyTestProxy("Beta", "")}, []map[string]any{group}, providers)
			for _, item := range raw.Proxy {
				item["server"] = "127.0.0.1"
				item["port"] = 1
			}
			graph, err := buildDependencyGraph(raw)
			if err != nil {
				t.Fatal(err)
			}
			data, err := yaml.Marshal(map[string]any{"proxies": raw.Proxy, "proxy-groups": raw.ProxyGroup, "proxy-providers": raw.ProxyProvider})
			if err != nil {
				t.Fatal(err)
			}
			copy, err := config.UnmarshalRawConfig(data)
			if err != nil {
				t.Fatal(err)
			}
			before := append([]string(nil), config.GetProxyNameList()...)
			defer config.SetProxyNameList(before)
			cfg, err := config.ParseRawConfig(copy)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				for _, provider := range cfg.Providers {
					if closer, ok := provider.(interface{ Close() error }); ok {
						closer.Close()
					}
				}
				for _, proxy := range cfg.Proxies {
					proxy.Close()
				}
			}()
			native, ok := cfg.Proxies["G"].Adapter().(interface{ GetProxies(bool) []C.Proxy })
			if !ok {
				t.Fatal("native group has no membership view")
			}
			var want []string
			for _, proxy := range native.GetProxies(false) {
				provider := proxy.ProxyInfo().ProviderName
				kind := "provider-proxy"
				if provider == "" {
					kind = graph.nodes[graph.roots[proxy.Name()]].ref.Kind
				}
				want = append(want, fmt.Sprintf("%s:%s:%s", kind, provider, proxy.Name()))
			}
			var got []string
			pending := append([]int(nil), graph.nodes[graph.roots["G"]].edges...)
			seen := map[int]bool{}
			for len(pending) > 0 {
				node := pending[0]
				pending = pending[1:]
				if seen[node] {
					continue
				}
				seen[node] = true
				info := graph.nodes[node]
				if info.auxiliary {
					pending = append(pending, info.edges...)
					continue
				}
				got = append(got, fmt.Sprintf("%s:%s:%s", info.ref.Kind, info.ref.Provider, info.ref.Name))
			}
			sort.Strings(want)
			sort.Strings(got)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("structural membership %v, native %v", got, want)
			}
		})
	}
}
