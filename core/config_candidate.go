package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"sort"

	"github.com/metacubex/mihomo/component/age"
	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/hub/executor"
	"go.yaml.in/yaml/v3"
)

type preparedConfig struct {
	raw                *config.RawConfig
	document           *yaml.Node
	bytes              []byte
	publishPath        string
	publishBeforeParse bool
}

func prepareSetupConfig(params *SetupParams) (*preparedConfig, error) {
	if params.Base != nil && params.ConfigPath != "" {
		return nil, dependencyPolicy("invalid_setup_source", "base", "config-path")
	}
	if params.Base != nil {
		document, raw, err := prepareProfileBase(params.Base)
		if err != nil {
			return nil, err
		}
		return &preparedConfig{raw: raw, document: document, publishPath: runningConfigPath(), publishBeforeParse: true}, nil
	}
	if params.ConfigPath != "" {
		data, err := os.ReadFile(params.ConfigPath)
		if err != nil {
			return nil, dependencyPolicy("invalid_setup_source", "config-path", "")
		}
		document, err := readCandidateDocument(data)
		if err != nil {
			return nil, err
		}
		raw, err := prepareDependencyDocument(document, nil)
		if err != nil {
			return nil, err
		}
		return &preparedConfig{raw: raw, document: document, publishPath: runningConfigPath()}, nil
	}
	return prepareExistingConfig(runningConfigPath())
}

func prepareExistingConfig(path string) (*preparedConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, err := unmarshalRawConfig(data)
	if err != nil {
		if document, parseErr := readCandidateDocument(data); parseErr == nil {
			if metadataErr := failedDependencyDocument(document); metadataErr != nil {
				return nil, metadataErr
			}
		}
		return nil, err
	}
	if err := ValidateProxyDependencies(raw); err != nil {
		return nil, err
	}
	return &preparedConfig{raw: raw, bytes: data}, nil
}

func (candidate *preparedConfig) load() (*config.Config, error) {
	if candidate.publishBeforeParse {
		if err := writeConfigDocument(candidate.publishPath, candidate.document); err != nil {
			return nil, err
		}
	}
	var cfg *config.Config
	var err error
	if candidate.document == nil {
		cfg, err = executor.ParseWithBytes(candidate.bytes)
	} else {
		cfg, err = config.ParseRawConfig(candidate.raw)
	}
	if err != nil {
		return nil, err
	}
	if candidate.publishPath != "" && !candidate.publishBeforeParse {
		if err := writeConfigDocument(candidate.publishPath, candidate.document); err != nil {
			return nil, err
		}
	}
	return withPlatformLimits(cfg), nil
}

func readCandidateDocument(data []byte) (*yaml.Node, error) {
	data, err := age.DecryptBytes(data)
	if err != nil {
		return nil, errors.New("cannot decrypt candidate configuration")
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	root, err := documentMapping(&document)
	if err != nil {
		return nil, err
	}
	if bytes.Contains(data, []byte("short-id")) {
		retagShortIDs(root)
	}
	return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}, nil
}

func failedDependencyDocument(document *yaml.Node) error {
	root, err := documentMapping(document)
	if err != nil {
		return nil
	}
	if err := validateDependencyDocumentTree(root); err != nil {
		return err
	}
	sections := []string{"proxies", "proxy-groups", "proxy-providers", "rules", "sub-rules"}
	projection := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, section := range sections {
		count := 0
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Kind == yaml.ScalarNode && root.Content[i].Value == section {
				count++
			}
		}
		if count > 1 {
			return invalidDependencyMetadata(section)
		}
		if value := mappingValue(root, section); value != nil {
			setMappingValue(projection, section, value)
		}
	}
	metadata := config.DefaultRawConfig()
	if err := projection.Decode(metadata); err != nil {
		return invalidDependencyMetadata("configuration")
	}
	return ValidateProxyDependencies(metadata)
}

func validateDependencyDocumentTree(document *yaml.Node) error {
	type frame struct {
		node *yaml.Node
		next int
	}
	color := map[*yaml.Node]uint8{}
	stack := []frame{{node: document}}
	for len(stack) > 0 {
		current := &stack[len(stack)-1]
		node := current.node
		if node == nil || node.Kind == yaml.AliasNode && node.Alias == nil || node.Kind == yaml.MappingNode && len(node.Content)%2 != 0 {
			return invalidDependencyMetadata("dialer-proxies")
		}
		color[node] = 1
		children := len(node.Content)
		if node.Kind == yaml.AliasNode {
			children++
		}
		if current.next == children {
			color[node] = 2
			stack = stack[:len(stack)-1]
			continue
		}
		var child *yaml.Node
		if current.next < len(node.Content) {
			child = node.Content[current.next]
		} else {
			child = node.Alias
		}
		current.next++
		if color[child] == 1 {
			return invalidDependencyMetadata("dialer-proxies")
		}
		if color[child] == 0 {
			stack = append(stack, frame{node: child})
		}
	}
	return nil
}

func cloneDependencyNode(node *yaml.Node, copies map[*yaml.Node]*yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	if copy := copies[node]; copy != nil {
		return copy
	}
	copy := *node
	copies[node] = &copy
	copy.Alias = cloneDependencyNode(node.Alias, copies)
	copy.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		copy.Content[i] = cloneDependencyNode(child, copies)
	}
	return &copy
}

func prepareDependencyDocument(document *yaml.Node, dialers map[string]string) (*config.RawConfig, error) {
	if len(dialers) > 0 {
		if err := applyDocumentDialers(document, dialers); err != nil {
			return nil, err
		}
	}
	raw := config.DefaultRawConfig()
	if err := document.Decode(raw); err != nil {
		if metadataErr := failedDependencyDocument(document); metadataErr != nil {
			return nil, metadataErr
		}
		return nil, err
	}
	if err := ValidateProxyDependencies(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func applyDocumentDialers(document *yaml.Node, dialers map[string]string) error {
	if err := validateDependencyDocumentTree(document); err != nil {
		return err
	}
	root, err := documentMapping(document)
	if err != nil {
		return invalidDependencyMetadata("dialer-proxies")
	}
	list := mappingValue(root, "proxies")
	byName := map[string][]*yaml.Node{}
	if list != nil {
		list = resolveAlias(list)
		if list.Kind != yaml.SequenceNode {
			return invalidDependencyMetadata("proxies")
		}
		detached := *list
		detached.Anchor = ""
		detached.Content = make([]*yaml.Node, len(list.Content))
		for i, proxy := range list.Content {
			detached.Content[i] = cloneDependencyNode(proxy, map[*yaml.Node]*yaml.Node{})
			proxy = resolveAlias(detached.Content[i])
			if proxy.Kind != yaml.MappingNode {
				return invalidDependencyMetadata("proxies")
			}
			if name := mappingValue(proxy, "name"); name != nil {
				name = resolveAlias(name)
				if name.Kind != yaml.ScalarNode || isNull(name) {
					return invalidDependencyMetadata("proxies.name")
				}
				byName[name.Value] = append(byName[name.Value], proxy)
			}
		}
		setMappingValue(root, "proxies", &detached)
	}
	keys := make([]string, 0, len(dialers))
	for source := range dialers {
		keys = append(keys, source)
	}
	sort.Strings(keys)
	for _, source := range keys {
		if source == "" {
			return dependencyPolicy("missing_source", "", "")
		}
		proxies := byName[source]
		if len(proxies) == 0 {
			if dependencyNonProxySource(root, source) {
				return dependencyPolicy("unsupported_source", source, "")
			}
			return dependencyPolicy("missing_source", source, "")
		}
		if len(proxies) != 1 {
			return dependencyPolicy("duplicate_outbound", source, source)
		}
		kind := resolveAlias(mappingValue(proxies[0], "type"))
		if kind != nil && (kind.Kind != yaml.ScalarNode || isNull(kind)) {
			return invalidDependencyMetadata("proxies.type")
		}
		if dialers[source] != "" && (kind == nil || !dependencySupportsDialer(kind.Value)) {
			return dependencyPolicy("unsupported_source", source, "")
		}
	}
	for _, source := range keys {
		setMappingValue(byName[source][0], "dialer-proxy", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: dialers[source]})
	}
	return nil
}

func dependencySupportsDialer(kind string) bool {
	switch kind {
	case "ss", "ssr", "socks5", "http", "vmess", "vless", "snell", "trojan", "hysteria", "hysteria2", "wireguard", "tuic", "shadowquic", "gost-relay", "ssh", "mieru", "anytls", "sudoku", "masque", "trusttunnel", "openvpn", "tailscale", "zerotier", "easytier":
		return true
	}
	return false
}

func dependencyNonProxySource(root *yaml.Node, name string) bool {
	switch name {
	case "DIRECT", "REJECT", "REJECT-DROP", "COMPATIBLE", "PASS", "PASS-RULE", "GLOBAL":
		return true
	}
	groups := mappingValue(root, "proxy-groups")
	if groups == nil {
		return false
	}
	groups = resolveAlias(groups)
	for _, group := range groups.Content {
		if candidate := mappingValue(resolveAlias(group), "name"); candidate != nil && resolveAlias(candidate).Value == name {
			return true
		}
	}
	return false
}

func quickSetupConfig(initParamsString, setupParamsString string) error {
	var initParams InitParams
	if json.Unmarshal([]byte(initParamsString), &initParams) != nil {
		return errors.New("init failed")
	}
	_ = handleInit(&initParams)
	var params SetupParams
	if err := json.Unmarshal([]byte(setupParamsString), &params); err != nil {
		return err
	}
	params.startListeners = true
	return handleSetupConfig(&params)
}
