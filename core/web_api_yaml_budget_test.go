//go:build webpanel && !android

package core

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// These cases pin build/api-yaml-budget-contract.md through the public helpers.
func TestWebAPIYAMLWorkBudgetContractBasicCounts(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want WebAPIYAMLWork
	}{
		{"scalar", "value\n", WebAPIYAMLWork{2, 2, 0}},
		{"mapping", "a: one\nb: two\n", WebAPIYAMLWork{6, 3, 1}},
		{"nested", "a: [one, {b: two, c: three}]\n", WebAPIYAMLWork{10, 5, 1}},
		{"alias", "a: &a [one, two]\nb: *a\n", WebAPIYAMLWork{10, 4, 1}},
		{"complex_key", "? {a: one, b: two}\n: fake\n", WebAPIYAMLWork{8, 4, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := webAPIYAMLBudgetParse(t, tc.yaml)
			webAPIYAMLBudgetCheck(t, document, tc.want, "")
		})
	}
	webAPIYAMLBudgetCheck(t, nil, WebAPIYAMLWork{}, "")
	webAPIYAMLBudgetCheck(t, &yaml.Node{}, WebAPIYAMLWork{1, 1, 0}, "")
}

func TestWebAPIYAMLWorkBudgetWideMappingBoundary(t *testing.T) {
	for _, tc := range []struct {
		keys        int
		comparisons uint64
		reason      string
	}{
		{8192, 33_550_336, ""},
		{8193, WebAPIYAMLMaxMappingComparisons + 1, "mapping_comparisons"},
	} {
		t.Run(fmt.Sprint(tc.keys), func(t *testing.T) {
			var source strings.Builder
			for i := 0; i < tc.keys; i++ {
				fmt.Fprintf(&source, "k%d: value\n", i)
			}
			document := webAPIYAMLBudgetParse(t, source.String())
			webAPIYAMLBudgetCheck(t, document, WebAPIYAMLWork{uint64(2 + 2*tc.keys), 3, tc.comparisons}, tc.reason)
		})
	}
}

func TestWebAPIYAMLWorkBudgetMappingComparisonLimitInclusive(t *testing.T) {
	sequence := &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{
		webAPIYAMLBudgetMapping(8192),
		webAPIYAMLBudgetMapping(91),
		webAPIYAMLBudgetMapping(2),
	}}
	document := webAPIYAMLBudgetDocument(sequence)
	webAPIYAMLBudgetCheck(t, document, WebAPIYAMLWork{16_575, 4, WebAPIYAMLMaxMappingComparisons}, "")
	sequence.Content = append(sequence.Content, webAPIYAMLBudgetMapping(2))
	webAPIYAMLBudgetCheck(t, document, WebAPIYAMLWork{16_580, 4, WebAPIYAMLMaxMappingComparisons + 1}, "mapping_comparisons")
}

func TestWebAPIYAMLWorkBudgetAccumulatesAllMappings(t *testing.T) {
	for _, tc := range []struct {
		maps        int
		comparisons uint64
		reason      string
	}{
		{4, 33_546_240, ""},
		{5, WebAPIYAMLMaxMappingComparisons + 1, "mapping_comparisons"},
	} {
		t.Run(fmt.Sprint(tc.maps), func(t *testing.T) {
			sequence := &yaml.Node{Kind: yaml.SequenceNode}
			for i := 0; i < tc.maps; i++ {
				sequence.Content = append(sequence.Content, webAPIYAMLBudgetMapping(4096))
			}
			webAPIYAMLBudgetCheck(t, webAPIYAMLBudgetDocument(sequence), WebAPIYAMLWork{uint64(2 + tc.maps*8193), 4, tc.comparisons}, tc.reason)
		})
	}
}

func TestWebAPIYAMLWorkBudgetAccumulatesAliasedMappingOccurrences(t *testing.T) {
	for _, tc := range []struct {
		occurrences int
		comparisons uint64
		reason      string
	}{
		{16, 33_538_048, ""},
		{17, WebAPIYAMLMaxMappingComparisons + 1, "mapping_comparisons"},
	} {
		t.Run(fmt.Sprint(tc.occurrences), func(t *testing.T) {
			mapping := webAPIYAMLBudgetMapping(2048)
			alias := &yaml.Node{Kind: yaml.AliasNode, Alias: mapping}
			sequence := &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{mapping}}
			for i := 1; i < tc.occurrences; i++ {
				sequence.Content = append(sequence.Content, alias)
			}
			webAPIYAMLBudgetCheck(t, webAPIYAMLBudgetDocument(sequence), WebAPIYAMLWork{uint64(2 + tc.occurrences*4097), 4, tc.comparisons}, tc.reason)
			if alias.Alias != mapping || len(mapping.Content) != 4096 || sequence.Content[1] != alias {
				t.Fatal("measurement mutated the alias graph")
			}
		})
	}
}

func TestWebAPIYAMLWorkBudgetAliasAmplificationAndSaturation(t *testing.T) {
	for _, tc := range []struct {
		levels int
		want   WebAPIYAMLWork
		reason string
	}{
		{18, WebAPIYAMLWork{524_288, 20, 0}, ""},
		{100, WebAPIYAMLWork{WebAPIYAMLMaxExpandedNodes + 1, 102, 0}, "expanded_nodes"},
		{200, WebAPIYAMLWork{WebAPIYAMLMaxExpandedNodes + 1, WebAPIYAMLMaxDepth + 1, 0}, "expanded_nodes"},
	} {
		t.Run(fmt.Sprint(tc.levels), func(t *testing.T) {
			node := &yaml.Node{Kind: yaml.ScalarNode, Value: "fake"}
			for i := 0; i < tc.levels; i++ {
				alias := &yaml.Node{Kind: yaml.AliasNode, Alias: node}
				node = &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{alias, alias}}
			}
			webAPIYAMLBudgetCheck(t, webAPIYAMLBudgetDocument(node), tc.want, tc.reason)
		})
	}

	node := webAPIYAMLBudgetMapping(2)
	for i := 0; i < 100; i++ {
		alias := &yaml.Node{Kind: yaml.AliasNode, Alias: node}
		node = &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "a"}, alias,
			{Kind: yaml.ScalarNode, Value: "b"}, alias,
		}}
	}
	webAPIYAMLBudgetCheck(t, webAPIYAMLBudgetDocument(node), WebAPIYAMLWork{WebAPIYAMLMaxExpandedNodes + 1, 103, WebAPIYAMLMaxMappingComparisons + 1}, "expanded_nodes")
}

func TestWebAPIYAMLWorkBudgetExpandedNodeBoundary(t *testing.T) {
	scalar := &yaml.Node{Kind: yaml.ScalarNode, Value: "fake"}
	sequence := &yaml.Node{Kind: yaml.SequenceNode, Content: make([]*yaml.Node, WebAPIYAMLMaxExpandedNodes-2)}
	for i := range sequence.Content {
		sequence.Content[i] = scalar
	}
	document := webAPIYAMLBudgetDocument(sequence)
	webAPIYAMLBudgetCheck(t, document, WebAPIYAMLWork{WebAPIYAMLMaxExpandedNodes, 3, 0}, "")
	sequence.Content = append(sequence.Content, scalar)
	webAPIYAMLBudgetCheck(t, document, WebAPIYAMLWork{WebAPIYAMLMaxExpandedNodes + 1, 3, 0}, "expanded_nodes")
}

func TestWebAPIYAMLWorkBudgetSemanticDepthBoundary(t *testing.T) {
	for _, tc := range []struct {
		layers int
		depth  uint64
		reason string
	}{
		{126, 128, ""},
		{127, 129, "depth"},
		{100_000, 129, "depth"},
	} {
		t.Run(fmt.Sprint(tc.layers), func(t *testing.T) {
			node := webAPIYAMLBudgetNestedSequence(&yaml.Node{Kind: yaml.ScalarNode}, tc.layers)
			webAPIYAMLBudgetCheck(t, webAPIYAMLBudgetDocument(node), WebAPIYAMLWork{uint64(tc.layers + 2), tc.depth, 0}, tc.reason)
		})
	}
}

func TestWebAPIYAMLWorkBudgetDepthAtEveryAliasOccurrence(t *testing.T) {
	for _, tc := range []struct {
		layers int
		depth  uint64
		reason string
	}{
		{85, 128, ""},
		{86, 129, "depth"},
	} {
		t.Run(fmt.Sprint(tc.layers), func(t *testing.T) {
			target := webAPIYAMLBudgetNestedSequence(&yaml.Node{Kind: yaml.ScalarNode}, 40)
			alias := &yaml.Node{Kind: yaml.AliasNode, Alias: target}
			deepUse := webAPIYAMLBudgetNestedSequence(alias, tc.layers)
			root := &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{target, deepUse}}
			webAPIYAMLBudgetCheck(t, webAPIYAMLBudgetDocument(root), WebAPIYAMLWork{uint64(84 + tc.layers), tc.depth, 0}, tc.reason)
		})
	}

	target := &yaml.Node{Kind: yaml.ScalarNode, Value: "fake"}
	for i := 0; i < 10_000; i++ {
		target = &yaml.Node{Kind: yaml.AliasNode, Alias: target}
	}
	webAPIYAMLBudgetCheck(t, webAPIYAMLBudgetDocument(target), WebAPIYAMLWork{2, 2, 0}, "")
}

func TestWebAPIYAMLWorkBudgetRejectsCycles(t *testing.T) {
	self := &yaml.Node{Kind: yaml.AliasNode}
	self.Alias = self
	indirect := &yaml.Node{Kind: yaml.AliasNode}
	indirect.Alias = &yaml.Node{Kind: yaml.AliasNode, Alias: indirect}
	content := &yaml.Node{Kind: yaml.SequenceNode}
	content.Content = []*yaml.Node{content}
	for name, document := range map[string]*yaml.Node{
		"self_alias":      self,
		"indirect_alias":  indirect,
		"content_edge":    content,
		"parsed_alias":    webAPIYAMLBudgetParse(t, "a: &a [*a]\n"),
		"parsed_merge":    webAPIYAMLBudgetParse(t, "a: &a {<<: *a}\n"),
		"alias_container": webAPIYAMLBudgetDocument(&yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{self}}),
	} {
		t.Run(name, func(t *testing.T) {
			webAPIYAMLBudgetCheck(t, document, WebAPIYAMLWork{}, "cycle")
		})
	}
}

func TestWebAPIYAMLWorkBudgetRejectsMalformedGraphs(t *testing.T) {
	for name, document := range map[string]*yaml.Node{
		"nil_alias_target": {Kind: yaml.AliasNode},
		"nil_child":        {Kind: yaml.SequenceNode, Content: []*yaml.Node{nil}},
		"odd_mapping":      {Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode}}},
	} {
		t.Run(name, func(t *testing.T) {
			webAPIYAMLBudgetCheck(t, document, WebAPIYAMLWork{}, "invalid_node_graph")
		})
	}
}

func TestWebAPIYAMLWorkBudgetLargeParsedSequence(t *testing.T) {
	const items = 120_000
	document := webAPIYAMLBudgetParse(t, strings.Repeat("- fake\n", items))
	webAPIYAMLBudgetCheck(t, document, WebAPIYAMLWork{items + 2, 3, 0}, "")
}

func TestWebAPIYAMLWorkBudgetOrdinaryProxiesRulesAndMerge(t *testing.T) {
	const proxies, rules = 4_000, 20_000
	var source strings.Builder
	source.WriteString("defaults: &defaults {type: socks5, server: fake.invalid, port: 1080}\nproxies:\n")
	for i := 0; i < proxies; i++ {
		fmt.Fprintf(&source, "  - {<<: *defaults, name: p%d}\n", i)
	}
	source.WriteString("rules:\n")
	for i := 0; i < rules; i++ {
		fmt.Fprintf(&source, "  - DOMAIN,rule%d.invalid,p%d\n", i, i%proxies)
	}
	document := webAPIYAMLBudgetParse(t, source.String())
	webAPIYAMLBudgetCheck(t, document, WebAPIYAMLWork{14 + 11*proxies + rules, 6, 6 + 4*proxies}, "")
	var decoded struct {
		Proxies []map[string]any `yaml:"proxies"`
		Rules   []string         `yaml:"rules"`
	}
	if err := document.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Proxies) != proxies || len(decoded.Rules) != rules || decoded.Proxies[0]["server"] != "fake.invalid" || decoded.Proxies[proxies-1]["type"] != "socks5" {
		t.Fatal("ordinary anchor/merge content did not survive validation and decoding")
	}
}

func TestWebAPIYAMLWorkBudgetPreserves32MiBLowStructureContent(t *testing.T) {
	const size = 32 << 20
	const prefix, suffix = "payload: \"", "\"\n"
	source := prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix
	if len(source) != size {
		t.Fatalf("fixture size = %d", len(source))
	}
	document := webAPIYAMLBudgetParse(t, source)
	webAPIYAMLBudgetCheck(t, document, WebAPIYAMLWork{4, 3, 0}, "")
	if len(document.Content[0].Content[1].Value) != size-len(prefix)-len(suffix) {
		t.Fatal("large scalar was truncated")
	}
}

func TestWebAPIYAMLWorkBudgetOverflowSafeArithmetic(t *testing.T) {
	const max = ^uint64(0)
	for _, tc := range []struct {
		a, b, cap, want uint64
	}{
		{0, 0, 10, 0},
		{3, 4, 10, 7},
		{4, 6, 10, 10},
		{4, 7, 10, 10},
		{max, max, 10, 10},
		{max - 1, 2, max, max},
	} {
		if got := webAPIYAMLSaturatingAdd(tc.a, tc.b, tc.cap); got != tc.want {
			t.Errorf("add(%d, %d, %d) = %d, want %d", tc.a, tc.b, tc.cap, got, tc.want)
		}
	}
	for _, tc := range []struct {
		a, b, cap, want uint64
	}{
		{0, max, 10, 0},
		{max, 0, 10, 0},
		{3, 3, 10, 9},
		{2, 5, 10, 10},
		{2, 6, 10, 10},
		{max, max, 10, 10},
		{max / 2, 2, max, max - 1},
		{max, 2, max, max},
	} {
		if got := webAPIYAMLSaturatingMultiply(tc.a, tc.b, tc.cap); got != tc.want {
			t.Errorf("multiply(%d, %d, %d) = %d, want %d", tc.a, tc.b, tc.cap, got, tc.want)
		}
	}
	for _, tc := range []struct {
		keys, want uint64
	}{
		{0, 0}, {1, 0}, {2, 1}, {3, 3}, {8192, 33_550_336},
		{8193, WebAPIYAMLMaxMappingComparisons + 1},
		{max - 1, WebAPIYAMLMaxMappingComparisons + 1},
		{max, WebAPIYAMLMaxMappingComparisons + 1},
	} {
		if got := webAPIYAMLMappingComparisonWork(tc.keys); got != tc.want {
			t.Errorf("mapping comparisons(%d) = %d, want %d", tc.keys, got, tc.want)
		}
	}
}

func webAPIYAMLBudgetCheck(t *testing.T, document *yaml.Node, want WebAPIYAMLWork, reason string) {
	t.Helper()
	got, err := MeasureWebAPIYAMLWork(document)
	if got != want {
		t.Fatalf("work = %+v, want %+v", got, want)
	}
	webAPIYAMLBudgetErrorCheck(t, err, reason)
	webAPIYAMLBudgetErrorCheck(t, WebAPIValidateYAMLWork(document), reason)
}

func webAPIYAMLBudgetErrorCheck(t *testing.T, err error, reason string) {
	t.Helper()
	if reason == "" {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	var typed *WebAPIError
	if !errors.As(err, &typed) || typed.Code != "yaml_too_complex" || typed.Status != 413 || typed.Details["reason"] != reason {
		t.Fatalf("error = %#v, want typed 413 yaml_too_complex (%s)", err, reason)
	}
}

func webAPIYAMLBudgetParse(t *testing.T, source string) *yaml.Node {
	t.Helper()
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(source), &document); err != nil {
		t.Fatal(err)
	}
	return &document
}

func webAPIYAMLBudgetDocument(root *yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
}

func webAPIYAMLBudgetMapping(keys int) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Content: make([]*yaml.Node, 0, keys*2)}
	for i := 0; i < keys; i++ {
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprintf("k%d", i)}, &yaml.Node{Kind: yaml.ScalarNode, Value: "fake"})
	}
	return node
}

func webAPIYAMLBudgetNestedSequence(node *yaml.Node, layers int) *yaml.Node {
	for i := 0; i < layers; i++ {
		node = &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{node}}
	}
	return node
}
