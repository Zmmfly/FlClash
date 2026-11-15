//go:build webpanel && !android

package core

import (
	"net/http"

	"go.yaml.in/yaml/v3"
)

const (
	WebAPIYAMLMaxExpandedNodes      = 1_000_000
	WebAPIYAMLMaxDepth              = 128
	WebAPIYAMLMaxMappingComparisons = 32 << 20
)

// Work counts alias-replaced occurrences, including the document wrapper;
// each field saturates at its budget plus one. See build/api-yaml-budget-contract.md.
type WebAPIYAMLWork struct {
	ExpandedNodes      uint64
	Depth              uint64
	MappingComparisons uint64
}

func WebAPIValidateYAMLWork(document *yaml.Node) error {
	_, err := MeasureWebAPIYAMLWork(document)
	return err
}

func MeasureWebAPIYAMLWork(document *yaml.Node) (WebAPIYAMLWork, error) {
	if document == nil {
		return WebAPIYAMLWork{}, nil
	}
	type memoEntry struct {
		work     WebAPIYAMLWork
		visiting bool
	}
	type frame struct {
		node *yaml.Node
		next int
		work WebAPIYAMLWork
	}
	memo := map[*yaml.Node]memoEntry{document: {visiting: true}}
	stack := []frame{{node: document, work: webAPIYAMLNodeWork(document)}}
	for len(stack) > 0 {
		current := &stack[len(stack)-1]
		node := current.node
		if node.Kind == yaml.MappingNode && len(node.Content)%2 != 0 {
			return WebAPIYAMLWork{}, webAPIYAMLWorkError("invalid_node_graph")
		}
		children := len(node.Content)
		if node.Kind == yaml.AliasNode {
			children = 1
		}
		if current.next == children {
			memo[node] = memoEntry{work: current.work}
			stack = stack[:len(stack)-1]
			continue
		}
		var child *yaml.Node
		if node.Kind == yaml.AliasNode {
			child = node.Alias
		} else {
			child = node.Content[current.next]
		}
		if child == nil {
			return WebAPIYAMLWork{}, webAPIYAMLWorkError("invalid_node_graph")
		}
		entry, found := memo[child]
		if entry.visiting {
			return WebAPIYAMLWork{}, webAPIYAMLWorkError("cycle")
		}
		if !found {
			memo[child] = memoEntry{visiting: true}
			stack = append(stack, frame{node: child, work: webAPIYAMLNodeWork(child)})
			continue
		}
		current.next++
		current.work.ExpandedNodes = webAPIYAMLSaturatingAdd(current.work.ExpandedNodes, entry.work.ExpandedNodes, WebAPIYAMLMaxExpandedNodes+1)
		current.work.MappingComparisons = webAPIYAMLSaturatingAdd(current.work.MappingComparisons, entry.work.MappingComparisons, WebAPIYAMLMaxMappingComparisons+1)
		depth := entry.work.Depth
		if node.Kind != yaml.AliasNode {
			depth = webAPIYAMLSaturatingAdd(depth, 1, WebAPIYAMLMaxDepth+1)
		}
		if depth > current.work.Depth {
			current.work.Depth = depth
		}
	}

	work := memo[document].work
	switch {
	case work.ExpandedNodes > WebAPIYAMLMaxExpandedNodes:
		return work, webAPIYAMLWorkError("expanded_nodes")
	case work.Depth > WebAPIYAMLMaxDepth:
		return work, webAPIYAMLWorkError("depth")
	case work.MappingComparisons > WebAPIYAMLMaxMappingComparisons:
		return work, webAPIYAMLWorkError("mapping_comparisons")
	default:
		return work, nil
	}
}

func webAPIYAMLNodeWork(node *yaml.Node) WebAPIYAMLWork {
	if node.Kind == yaml.AliasNode {
		return WebAPIYAMLWork{}
	}
	work := WebAPIYAMLWork{ExpandedNodes: 1, Depth: 1}
	if node.Kind == yaml.MappingNode {
		work.MappingComparisons = webAPIYAMLMappingComparisonWork(uint64(len(node.Content) / 2))
	}
	return work
}

func webAPIYAMLMappingComparisonWork(keys uint64) uint64 {
	if keys < 2 {
		return 0
	}
	a, b := keys, keys-1
	if a%2 == 0 {
		a /= 2
	} else {
		b /= 2
	}
	return webAPIYAMLSaturatingMultiply(a, b, WebAPIYAMLMaxMappingComparisons+1)
}

func webAPIYAMLSaturatingAdd(a, b, cap uint64) uint64 {
	if a >= cap || b >= cap-a {
		return cap
	}
	return a + b
}

func webAPIYAMLSaturatingMultiply(a, b, cap uint64) uint64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a >= cap || b > cap/a {
		return cap
	}
	return a * b
}

func webAPIYAMLWorkError(reason string) *WebAPIError {
	err := webAPIError(http.StatusRequestEntityTooLarge, "yaml_too_complex", "YAML document exceeds the Web API work budget")
	err.Details = map[string]any{"reason": reason}
	return err
}
