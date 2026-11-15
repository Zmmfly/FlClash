//go:build webpanel && !android

package core

import (
	"bytes"
	"io"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

func webEvaluateSourceDeclares(root *yaml.Node, key string) bool {
	stack := []*yaml.Node{root}
	seen := map[*yaml.Node]bool{}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if node == nil || seen[node] {
			continue
		}
		seen[node] = true
		switch node.Kind {
		case yaml.AliasNode:
			stack = append(stack, node.Alias)
		case yaml.SequenceNode:
			stack = append(stack, node.Content...)
		case yaml.MappingNode:
			for i := 0; i+1 < len(node.Content); i += 2 {
				name := node.Content[i]
				if name.Kind != yaml.ScalarNode {
					continue
				}
				if name.Value == key {
					return true
				}
				if name.Tag == "!!merge" {
					stack = append(stack, node.Content[i+1])
				}
			}
		}
	}
	return false
}

func webEvaluateParseSource(data []byte) (*yaml.Node, error) {
	if len(data) > webEvaluateMaxBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, webAPIError(413, "evaluation_input_limit", "The source exceeds its UTF-8 byte budget")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document, next yaml.Node
	if decoder.Decode(&document) != nil || decoder.Decode(&next) != io.EOF {
		return nil, webAPIError(400, "invalid_profile_content", "One source document is required")
	}
	if err := WebAPIValidateYAMLWork(&document); err != nil {
		return nil, err
	}
	if err := validateDependencyDocumentTree(&document); err != nil {
		return nil, err
	}
	root, err := documentMapping(&document)
	if err != nil {
		return nil, webAPIError(400, "invalid_profile_content", "The source must be a mapping")
	}
	retagShortIDs(root)
	return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}, nil
}
