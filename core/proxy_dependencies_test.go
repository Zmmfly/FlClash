package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/metacubex/mihomo/config"
	"go.yaml.in/yaml/v3"
)

func dependencyTestProxy(name, dialer string) map[string]any {
	proxy := map[string]any{"name": name, "type": "socks5"}
	if dialer != "" {
		proxy["dialer-proxy"] = dialer
	}
	return proxy
}
func dependencyTestRaw(proxies []map[string]any, groups []map[string]any, providers map[string]map[string]any) *config.RawConfig {
	return &config.RawConfig{Proxy: proxies, ProxyGroup: groups, ProxyProvider: providers}
}
func dependencyReason(t *testing.T, err error, reason string) *DependencyPolicyError {
	t.Helper()
	if reason == "" {
		if err != nil {
			t.Fatal(err)
		}
		return nil
	}
	var policy *DependencyPolicyError
	if !errors.As(err, &policy) || policy.Reason != reason {
		t.Fatalf("error=%v, want typed %s", err, reason)
	}
	if method := asMethodError(err); method.Code != "outbound_policy_invalid" {
		t.Fatal("policy error lost its external code")
	}
	return policy
}

func TestProxyDependenciesStaticCombinedGraph(t *testing.T) {
	for _, test := range []struct {
		name    string
		proxies []map[string]any
		groups  []map[string]any
		reason  string
	}{
		{"chain", []map[string]any{dependencyTestProxy("A", "B"), dependencyTestProxy("B", "")}, nil, ""},
		{"self", []map[string]any{dependencyTestProxy("A", "A")}, nil, "cycle"},
		{"two proxies", []map[string]any{dependencyTestProxy("A", "B"), dependencyTestProxy("B", "A")}, nil, "cycle"},
		{"missing target", []map[string]any{dependencyTestProxy("A", "missing")}, nil, "missing_target"},
		{"proxy group", []map[string]any{dependencyTestProxy("A", "G")}, []map[string]any{{"name": "G", "type": "select", "proxies": []string{"A"}}}, "cycle"},
		{"two mixed groups", []map[string]any{dependencyTestProxy("A", "G1"), dependencyTestProxy("B", "G2")}, []map[string]any{{"name": "G1", "type": "select", "proxies": []string{"B"}}, {"name": "G2", "type": "select", "proxies": []string{"A"}}}, "cycle"},
		{"group only", nil, []map[string]any{{"name": "G1", "type": "select", "proxies": []string{"G2"}}, {"name": "G2", "type": "select", "proxies": []string{"G1"}}}, "cycle"},
		{"duplicate proxy", []map[string]any{dependencyTestProxy("A", ""), dependencyTestProxy("A", "")}, nil, "duplicate_outbound"},
		{"builtin collision", []map[string]any{dependencyTestProxy("DIRECT", "")}, nil, "duplicate_outbound"},
		{"group proxy collision", []map[string]any{dependencyTestProxy("G", "")}, []map[string]any{{"name": "G", "type": "select", "proxies": []string{"DIRECT"}}}, "duplicate_outbound"},
		{"implicit GLOBAL", []map[string]any{dependencyTestProxy("A", "GLOBAL")}, nil, "cycle"},
		{"explicit GLOBAL", []map[string]any{dependencyTestProxy("A", "GLOBAL"), dependencyTestProxy("B", "")}, []map[string]any{{"name": "GLOBAL", "type": "select", "proxies": []string{"B"}}}, ""},
		{"group dialer unsupported", []map[string]any{dependencyTestProxy("A", "")}, []map[string]any{{"name": "G", "type": "select", "proxies": []string{"A"}, "dialer-proxy": "DIRECT"}}, "unsupported_source"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dependencyReason(t, ValidateProxyDependencies(dependencyTestRaw(test.proxies, test.groups, nil)), test.reason)
		})
	}
}

func TestProxyDependenciesEveryGroupStrategyAndFilterSemantics(t *testing.T) {
	for _, kind := range []string{"select", "url-test", "fallback", "load-balance"} {
		t.Run(kind, func(t *testing.T) {
			raw := dependencyTestRaw([]map[string]any{dependencyTestProxy("A", "G"), dependencyTestProxy("Safe", "")}, []map[string]any{{"name": "G", "type": kind, "proxies": []string{"Safe", "A"}}}, nil)
			dependencyReason(t, ValidateProxyDependencies(raw), "cycle")
		})
	}
	for _, test := range []struct {
		name   string
		group  map[string]any
		reason string
	}{
		{"explicit filter does not filter own proxies", map[string]any{"proxies": []string{"A"}, "filter": "^Safe$"}, "cycle"},
		{"explicit exclude", map[string]any{"proxies": []string{"A", "Safe"}, "exclude-filter": "^A$"}, ""},
		{"all", map[string]any{"include-all-proxies": true}, "cycle"},
		{"all filtered safe", map[string]any{"include-all-proxies": true, "filter": "^Safe$"}, ""},
		{"all filtered loop", map[string]any{"include-all-proxies": true, "filter": "^A$"}, "cycle"},
		{"all excluded loop", map[string]any{"include-all-proxies": true, "exclude-filter": "^A$"}, ""},
		{"all excludes type", map[string]any{"include-all-proxies": true, "exclude-type": "sOcKs5"}, ""},
		{"all backtick union", map[string]any{"include-all-proxies": true, "filter": "^Safe$`^A$"}, "cycle"},
		{"lookbehind", map[string]any{"include-all-proxies": true, "filter": "(?<=S)afe"}, ""},
		{"empty fallback loop", map[string]any{"include-all-proxies": true, "filter": "^missing$", "empty-fallback": "A"}, "cycle"},
		{"fallback unused", map[string]any{"proxies": []string{"Safe"}, "empty-fallback": "A"}, ""},
		{"group fallback forbidden", map[string]any{"proxies": []string{"Safe"}, "empty-fallback": "G"}, "missing_target"},
	} {
		t.Run(test.name, func(t *testing.T) {
			group := map[string]any{"name": "G", "type": "select"}
			for key, value := range test.group {
				group[key] = value
			}
			raw := dependencyTestRaw([]map[string]any{dependencyTestProxy("A", "G"), dependencyTestProxy("Safe", "")}, []map[string]any{group}, nil)
			dependencyReason(t, ValidateProxyDependencies(raw), test.reason)
		})
	}
}

func TestProxyDependenciesProviderProofAndIdentity(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider map[string]any
		group    map[string]any
		proxies  []map[string]any
		reason   string
	}{
		{"ordinary unknown allowed", map[string]any{"type": "http", "url": "https://invalid.example/sub"}, nil, nil, ""},
		{"reachable unknown", map[string]any{"type": "http"}, nil, []map[string]any{dependencyTestProxy("A", "G")}, "unknown_provider_dependency"},
		{"unrelated unknown", map[string]any{"type": "http"}, nil, []map[string]any{dependencyTestProxy("A", "B"), dependencyTestProxy("B", "")}, ""},
		{"schema dialer forces future input", map[string]any{"type": "http", "dialer-proxy": "B"}, nil, []map[string]any{dependencyTestProxy("B", "")}, ""},
		{"forced clear proves leaf", map[string]any{"type": "http", "override": map[string]any{"dialer-proxy": ""}}, nil, []map[string]any{dependencyTestProxy("A", "G")}, ""},
		{"forced next hop", map[string]any{"type": "http", "override": map[string]any{"dialer-proxy": "B"}}, nil, []map[string]any{dependencyTestProxy("A", "G"), dependencyTestProxy("B", "")}, ""},
		{"forced aggregate cycle", map[string]any{"type": "http", "override": map[string]any{"dialer-proxy": "G"}}, nil, nil, "cycle"},
		{"expression is not a proof", map[string]any{"type": "http", "override": map[string]any{"dialer-proxy": "", "override-expr": []string{`.name += "x"`}}}, nil, []map[string]any{dependencyTestProxy("A", "G")}, "unknown_provider_dependency"},
		{"ordinary expression allowed", map[string]any{"type": "http", "override": map[string]any{"override-expr": []string{`.name += "x"`}}}, nil, nil, ""},
		{"inline loop", map[string]any{"type": "inline", "payload": []map[string]any{dependencyTestProxy("Member", "G")}}, nil, nil, "cycle"},
		{"inline clear", map[string]any{"type": "inline", "payload": []map[string]any{dependencyTestProxy("Member", "G")}, "override": map[string]any{"dialer-proxy": ""}}, nil, nil, ""},
		{"inline same scope name safe", map[string]any{"type": "inline", "payload": []map[string]any{dependencyTestProxy("A", "B")}}, nil, []map[string]any{dependencyTestProxy("A", "G"), dependencyTestProxy("B", "")}, ""},
		{"inline same scope name cycle", map[string]any{"type": "inline", "payload": []map[string]any{dependencyTestProxy("A", "A")}}, nil, []map[string]any{dependencyTestProxy("A", "G")}, "cycle"},
		{"bare member target missing", map[string]any{"type": "inline", "payload": []map[string]any{dependencyTestProxy("Member", "")}}, nil, []map[string]any{dependencyTestProxy("A", "Member")}, "missing_target"},
		{"provider rename and group filter", map[string]any{"type": "inline", "payload": []map[string]any{dependencyTestProxy("Member", "G")}, "override": map[string]any{"additional-prefix": "sub-"}}, map[string]any{"filter": "^sub-Member$"}, nil, "cycle"},
		{"repeated filter rename matches native parser", map[string]any{"type": "inline", "filter": "Member`sub-Member", "payload": []map[string]any{dependencyTestProxy("Member", "G")}, "override": map[string]any{"additional-prefix": "sub-"}}, map[string]any{"filter": "^sub-sub-Member$"}, nil, "cycle"},
		{"provider excluded after rename", map[string]any{"type": "inline", "payload": []map[string]any{dependencyTestProxy("Member", "G")}, "override": map[string]any{"additional-prefix": "sub-"}}, map[string]any{"filter": "^Member$"}, nil, ""},
		{"unknown empty fallback may loop", map[string]any{"type": "http", "override": map[string]any{"dialer-proxy": ""}}, map[string]any{"empty-fallback": "A"}, []map[string]any{dependencyTestProxy("A", "G")}, "cycle"},
		{"permanent own member suppresses fallback", map[string]any{"type": "http", "override": map[string]any{"dialer-proxy": ""}}, map[string]any{"empty-fallback": "A", "proxies": []string{"B"}}, []map[string]any{dependencyTestProxy("A", "G"), dependencyTestProxy("B", "")}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			group := map[string]any{"name": "G", "type": "select", "use": []string{"P"}}
			for key, value := range test.group {
				group[key] = value
			}
			raw := dependencyTestRaw(test.proxies, []map[string]any{group}, map[string]map[string]any{"P": test.provider})
			dependencyReason(t, ValidateProxyDependencies(raw), test.reason)
		})
	}
	for _, flag := range []string{"include-all-providers", "include-all"} {
		raw := dependencyTestRaw(nil, []map[string]any{{"name": "G", "type": "select", flag: true}}, map[string]map[string]any{"P": {"type": "http", "override": map[string]any{"dialer-proxy": "G"}}})
		dependencyReason(t, ValidateProxyDependencies(raw), "cycle")
	}
}

func TestProxyDependenciesRulesUseNativeTargetParser(t *testing.T) {
	raw := dependencyTestRaw([]map[string]any{dependencyTestProxy("A", "")}, []map[string]any{{"name": "G", "type": "select", "proxies": []string{"A"}}}, nil)
	raw.SubRules = map[string][]string{"sub": {"DOMAIN,example.test,A", "MATCH,DIRECT"}}
	raw.Rule = []string{"MATCH,G", "IP-CIDR,198.18.0.0/16,A,no-resolve", "AND,((DOMAIN,example.test),(NETWORK,TCP)),G", "DOMAIN-REGEX,^(a,b)$,G", "SUB-RULE,(NETWORK,TCP),sub", "RULE-SET,list,G,no-resolve"}
	dependencyReason(t, ValidateProxyDependencies(raw), "")
	for _, rule := range []string{"MATCH,Gone", "AND,((DOMAIN,a),(NETWORK,TCP)),Gone", "SUB-RULE,(NETWORK,TCP),DIRECT"} {
		raw.Rule = []string{rule}
		dependencyReason(t, ValidateProxyDependencies(raw), "missing_rule_target")
	}
	raw.Rule = []string{"MATCH,DIRECT"}
	raw.SubRules["sub"] = []string{"DOMAIN,example.test,Gone"}
	dependencyReason(t, ValidateProxyDependencies(raw), "missing_rule_target")
}

func TestProxyDependencyCycleWitnessIsStableAndCredentialFree(t *testing.T) {
	raw := dependencyTestRaw([]map[string]any{dependencyTestProxy("A", "G")}, []map[string]any{{"name": "G", "type": "select", "include-all-proxies": true}}, nil)
	raw.Proxy[0]["password"] = "must-not-appear"
	raw.Proxy[0]["server"] = "private-server.example"
	var before string
	for i := 0; i < 20; i++ {
		policy := dependencyReason(t, ValidateProxyDependencies(raw), "cycle")
		if len(policy.Path) < 3 || policy.Path[0] != policy.Path[len(policy.Path)-1] {
			t.Fatalf("invalid cycle witness: %+v", policy.Path)
		}
		encoded, _ := json.Marshal(asMethodError(policy))
		if i == 0 {
			before = string(encoded)
		} else if before != string(encoded) {
			t.Fatal("cycle witness depends on map iteration")
		}
		if bytesContainAny(encoded, "must-not-appear", "private-server.example") {
			t.Fatal("policy diagnostic disclosed credentials")
		}
	}
}

func bytesContainAny(data []byte, values ...string) bool {
	for _, value := range values {
		if strings.Contains(string(data), value) {
			return true
		}
	}
	return false
}

func TestProxyDependencyValidatorHandlesOneHundredThousandHopsIteratively(t *testing.T) {
	const count = 100000
	raw := &config.RawConfig{Proxy: make([]map[string]any, count)}
	for i := range raw.Proxy {
		next := ""
		if i+1 < count {
			next = fmt.Sprintf("p%06d", i+1)
		}
		raw.Proxy[i] = dependencyTestProxy(fmt.Sprintf("p%06d", i), next)
	}
	dependencyReason(t, ValidateProxyDependencies(raw), "")
	raw.Proxy[count-1]["dialer-proxy"] = "p000000"
	policy := dependencyReason(t, ValidateProxyDependencies(raw), "cycle")
	if len(policy.Path) != count+1 {
		t.Fatalf("deep cycle witness has %d nodes", len(policy.Path))
	}
}

func TestProxyDependencyIncludeAllUsesSharedAggregates(t *testing.T) {
	raw := &config.RawConfig{}
	for i := 0; i < 2000; i++ {
		raw.Proxy = append(raw.Proxy, dependencyTestProxy(fmt.Sprintf("p%04d", i), ""))
	}
	for i := 0; i < 10000; i++ {
		raw.ProxyGroup = append(raw.ProxyGroup, map[string]any{"name": fmt.Sprintf("g%05d", i), "type": "select", "include-all": true})
	}
	graph, err := buildDependencyGraph(raw)
	if err != nil {
		t.Fatal(err)
	}
	edges := 0
	for _, node := range graph.nodes {
		edges += len(node.edges)
	}
	if edges > 50000 || len(graph.nodes) > 13000 {
		t.Fatalf("include-all expanded cartesian membership: %d nodes/%d edges", len(graph.nodes), edges)
	}
}

func TestProxyDependencyValidationDoesNotMutateRawConfiguration(t *testing.T) {
	raw := dependencyTestRaw([]map[string]any{dependencyTestProxy("A", "")}, []map[string]any{{"name": "G", "type": "select", "use": []string{"P"}}}, map[string]map[string]any{"P": {"type": "inline", "payload": []map[string]any{dependencyTestProxy("Member", "A")}, "override": map[string]any{"additional-prefix": "sub-", "proxy-name": []map[string]any{{"pattern": "Member", "target": "Renamed"}}}}})
	before, err := yaml.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	dependencyReason(t, ValidateProxyDependencies(raw), "")
	after, _ := yaml.Marshal(raw)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("pure graph validation rewrote the candidate")
	}
}
