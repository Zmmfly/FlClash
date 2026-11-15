package core

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/metacubex/mihomo/adapter/outboundgroup"
	"github.com/metacubex/mihomo/common/structure"
	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	RC "github.com/metacubex/mihomo/rules/common"
)

type DependencyNode struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
}

type DependencyPolicyError struct {
	Reason string
	Source string
	Target string
	Path   []DependencyNode
}

func (e *DependencyPolicyError) Error() string {
	return "outbound dependency policy rejected: " + e.Reason
}
func (e *DependencyPolicyError) methodError() *MethodError {
	details := map[string]any{"reason": e.Reason}
	if e.Source != "" {
		details["source"] = e.Source
	}
	if e.Target != "" {
		details["target"] = e.Target
	}
	if len(e.Path) > 0 {
		details["path"] = e.Path
	}
	return &MethodError{Code: "outbound_policy_invalid", Message: e.Error(), Details: details}
}

func dependencyPolicy(reason, source, target string) error {
	return &DependencyPolicyError{Reason: reason, Source: source, Target: target}
}

func invalidDependencyMetadata(section string) error {
	return &DependencyPolicyError{Reason: "invalid_dependency_metadata", Path: []DependencyNode{{Kind: "metadata", Name: section}}}
}

func isDependencyPolicyError(err error) bool {
	var policy *DependencyPolicyError
	return errors.As(err, &policy)
}

type dependencyProxy struct {
	Name   string `proxy:"name"`
	Type   string `proxy:"type"`
	Dialer string `proxy:"dialer-proxy,omitempty"`
}

type dependencyProviderOverride struct {
	Dialer      *string            `provider:"dialer-proxy,omitempty"`
	Prefix      *string            `provider:"additional-prefix,omitempty"`
	Suffix      *string            `provider:"additional-suffix,omitempty"`
	Rename      []dependencyRename `provider:"proxy-name,omitempty"`
	Expressions []any              `provider:"override-expr,omitempty"`
}

type dependencyRename struct {
	Pattern *regexp2.Regexp `provider:"pattern"`
	Target  string          `provider:"target"`
}

type dependencyProviderSchema struct {
	Type          string                     `provider:"type"`
	Filter        string                     `provider:"filter,omitempty"`
	ExcludeFilter string                     `provider:"exclude-filter,omitempty"`
	ExcludeType   string                     `provider:"exclude-type,omitempty"`
	Dialer        string                     `provider:"dialer-proxy,omitempty"`
	Payload       []map[string]any           `provider:"payload,omitempty"`
	Override      dependencyProviderOverride `provider:"override,omitempty"`
}

type dependencyGraphNode struct {
	ref       DependencyNode
	edges     []int
	unknown   bool
	auxiliary bool
	proxyType string
}

type dependencyProviderView struct {
	node       int
	guaranteed bool
}

type dependencyProvider struct {
	name    string
	node    int
	members []int
	known   bool
}

type dependencyGraph struct {
	nodes     []dependencyGraphNode
	roots     map[string]int
	proxies   []int
	groups    map[string]outboundgroup.GroupCommonOption
	providers map[string]dependencyProvider
	views     map[string]dependencyProviderView
	dialers   []int
}

func decodeDependencyProxy(mapping map[string]any) (dependencyProxy, error) {
	var result dependencyProxy
	decoder := structure.NewDecoder(structure.Option{TagName: "proxy", WeaklyTypedInput: true, KeyReplacer: structure.DefaultKeyReplacer})
	if decoder.Decode(mapping, &result) != nil {
		return result, invalidDependencyMetadata("proxies")
	}
	return result, nil
}

func (g *dependencyGraph) addNode(ref DependencyNode, proxyType string, auxiliary bool) int {
	index := len(g.nodes)
	g.nodes = append(g.nodes, dependencyGraphNode{ref: ref, proxyType: proxyType, auxiliary: auxiliary})
	return index
}

func (g *dependencyGraph) addRoot(ref DependencyNode, proxyType string) (int, error) {
	if _, exists := g.roots[ref.Name]; exists {
		return 0, dependencyPolicy("duplicate_outbound", ref.Name, ref.Name)
	}
	index := g.addNode(ref, proxyType, false)
	g.roots[ref.Name] = index
	return index, nil
}

func (g *dependencyGraph) addDialer(node int, target string) error {
	if target == "" {
		return nil
	}
	other, exists := g.roots[target]
	if !exists {
		return dependencyPolicy("missing_target", g.nodes[node].ref.Name, target)
	}
	g.nodes[node].edges = append(g.nodes[node].edges, other)
	g.dialers = append(g.dialers, node)
	return nil
}

func ValidateProxyDependencies(raw *config.RawConfig) error {
	_, err := buildDependencyGraph(raw)
	if err != nil && !isDependencyPolicyError(err) {
		return invalidDependencyMetadata("configuration")
	}
	return err
}

func buildDependencyGraph(raw *config.RawConfig) (*dependencyGraph, error) {
	g := &dependencyGraph{roots: map[string]int{}, groups: map[string]outboundgroup.GroupCommonOption{}, providers: map[string]dependencyProvider{}, views: map[string]dependencyProviderView{}}
	if raw == nil {
		return nil, invalidDependencyMetadata("configuration")
	}
	builtins := map[string]string{"DIRECT": "Direct", "REJECT": "Reject", "REJECT-DROP": "RejectDrop", "COMPATIBLE": "Compatible", "PASS": "Pass", "PASS-RULE": "PassRule"}
	for _, name := range sortedDependencyKeys(builtins) {
		g.addRoot(DependencyNode{Kind: "builtin", Name: name}, builtins[name])
	}
	definitions := make(map[string]dependencyProxy, len(raw.Proxy))
	for _, mapping := range raw.Proxy {
		proxy, err := decodeDependencyProxy(mapping)
		if err != nil {
			return nil, err
		}
		if proxy.Name == "" {
			return nil, dependencyPolicy("missing_source", "", "")
		}
		if proxy.Dialer != "" && !dependencySupportsDialer(proxy.Type) {
			return nil, dependencyPolicy("unsupported_source", proxy.Name, "")
		}
		index, err := g.addRoot(DependencyNode{Kind: "proxy", Name: proxy.Name}, dependencyTypeName(proxy.Type))
		if err != nil {
			return nil, err
		}
		g.proxies = append(g.proxies, index)
		definitions[proxy.Name] = proxy
	}
	decoder := structure.NewDecoder(structure.Option{TagName: "group", WeaklyTypedInput: true})
	for _, mapping := range raw.ProxyGroup {
		var group outboundgroup.GroupCommonOption
		if decoder.Decode(mapping, &group) != nil {
			return nil, invalidDependencyMetadata("proxy-groups")
		}
		if group.Name == "" {
			return nil, dependencyPolicy("missing_source", "", "")
		}
		if dialer, ok := mapping["dialer-proxy"]; ok && dialer != nil && dialer != "" {
			return nil, dependencyPolicy("unsupported_source", group.Name, "")
		}
		if _, err := g.addRoot(DependencyNode{Kind: "group", Name: group.Name}, dependencyTypeName(group.Type)); err != nil {
			return nil, err
		}
		g.groups[group.Name] = group
	}
	if root, exists := g.roots[globalProxyName]; !exists {
		global, _ := g.addRoot(DependencyNode{Kind: "group", Name: globalProxyName}, "Selector")
		for _, name := range sortedDependencyKeys(g.roots) {
			index := g.roots[name]
			if index == global || name == "COMPATIBLE" || name == "REJECT-DROP" || name == "PASS" || name == "PASS-RULE" {
				continue
			}
			if g.nodes[index].proxyType == "Pass" || g.nodes[index].proxyType == "PassRule" {
				continue
			}
			g.nodes[global].edges = append(g.nodes[global].edges, index)
		}
	} else if g.nodes[root].ref.Kind != "group" {
		return nil, dependencyPolicy("duplicate_outbound", globalProxyName, globalProxyName)
	}
	for _, name := range sortedDependencyKeys(definitions) {
		if err := g.addDialer(g.roots[name], definitions[name].Dialer); err != nil {
			return nil, err
		}
	}
	for _, name := range sortedDependencyKeys(raw.ProxyProvider) {
		if err := g.addProvider(name, raw.ProxyProvider[name]); err != nil {
			return nil, err
		}
	}
	for _, name := range sortedDependencyKeys(g.groups) {
		if err := g.addGroup(g.groups[name]); err != nil {
			return nil, err
		}
	}
	if err := g.validateRules(raw); err != nil {
		return nil, err
	}
	for i := range g.nodes {
		sort.Ints(g.nodes[i].edges)
		g.nodes[i].edges = uniqueDependencyIDs(g.nodes[i].edges)
	}
	if err := g.findCycle(); err != nil {
		return nil, err
	}
	if err := g.rejectUnknownDialers(); err != nil {
		return nil, err
	}
	return g, nil
}

func sortedDependencyKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func uniqueDependencyIDs(values []int) []int {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func (g *dependencyGraph) addProvider(name string, mapping map[string]any) error {
	decoder := structure.NewDecoder(structure.Option{TagName: "provider", WeaklyTypedInput: true})
	var schema dependencyProviderSchema
	if decoder.Decode(mapping, &schema) != nil {
		return invalidDependencyMetadata("proxy-providers")
	}
	for _, rename := range schema.Override.Rename {
		if rename.Pattern == nil {
			return invalidDependencyMetadata("proxy-providers.override.proxy-name")
		}
	}
	node := g.addNode(DependencyNode{Kind: "provider", Name: name}, "", false)
	pd := dependencyProvider{name: name, node: node}
	forced := schema.Override.Dialer
	if forced == nil && schema.Dialer != "" {
		value := schema.Dialer
		forced = &value
	}
	if len(schema.Override.Expressions) > 0 {
		g.nodes[node].unknown = true
		if forced != nil {
			if *forced != "" {
				g.dialers = append(g.dialers, node)
			}
		} else {
			for _, item := range schema.Payload {
				if value, _ := item["dialer-proxy"].(string); value != "" {
					g.dialers = append(g.dialers, node)
					break
				}
			}
		}
		g.providers[name] = pd
		return nil
	}
	if schema.Type != "inline" {
		if forced != nil {
			if err := g.addDialer(node, *forced); err != nil {
				return err
			}
		} else {
			g.nodes[node].unknown = true
			for _, item := range schema.Payload {
				if value, _ := item["dialer-proxy"].(string); value != "" {
					g.dialers = append(g.dialers, node)
					break
				}
			}
		}
		g.providers[name] = pd
		return nil
	}
	filter, err := newDependencyFilter(schema.Filter, schema.ExcludeFilter, schema.ExcludeType)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var source []dependencyProxy
	for _, mapping := range schema.Payload {
		if _, ok := mapping["name"].(string); !ok {
			continue
		}
		proxy, err := decodeDependencyProxy(mapping)
		if err != nil {
			return err
		}
		source = append(source, proxy)
	}
	patterns := filter.include
	if len(patterns) == 0 {
		patterns = []*regexp2.Regexp{nil}
	}
	for _, pattern := range patterns {
		for i := range source {
			proxy := &source[i]
			if seen[proxy.Name] {
				continue
			}
			matches, err := filter.matches(proxy.Name, proxy.Type, false)
			if err != nil {
				return err
			}
			if !matches {
				continue
			}
			if pattern != nil {
				matched, err := pattern.MatchString(proxy.Name)
				if err != nil {
					return dependencyPolicy("invalid_filter", name, "")
				}
				if !matched {
					continue
				}
			}
			originalName := proxy.Name
			if schema.Dialer != "" {
				proxy.Dialer = schema.Dialer
			}
			if schema.Override.Dialer != nil {
				proxy.Dialer = *schema.Override.Dialer
			}
			for _, rename := range schema.Override.Rename {
				rename.Pattern.MatchTimeout = 100 * time.Millisecond
				proxy.Name, err = rename.Pattern.Replace(proxy.Name, rename.Target, 0, -1)
				if err != nil {
					return dependencyPolicy("invalid_filter", name, "")
				}
			}
			if schema.Override.Prefix != nil {
				proxy.Name = *schema.Override.Prefix + proxy.Name
			}
			if schema.Override.Suffix != nil {
				proxy.Name += *schema.Override.Suffix
			}
			member := g.addNode(DependencyNode{Kind: "provider-proxy", Name: proxy.Name, Provider: name}, dependencyTypeName(proxy.Type), false)
			pd.members = append(pd.members, member)
			g.nodes[node].edges = append(g.nodes[node].edges, member)
			if err := g.addDialer(member, proxy.Dialer); err != nil {
				return err
			}
			seen[originalName] = true
		}
	}
	pd.known = true
	g.providers[name] = pd
	return nil
}

type dependencyFilter struct {
	include, exclude []*regexp2.Regexp
	types            []string
}

func dependencyRegex(pattern string) ([]*regexp2.Regexp, error) {
	if pattern == "" {
		return nil, nil
	}
	var result []*regexp2.Regexp
	for _, part := range strings.Split(pattern, "`") {
		compiled, err := regexp2.Compile(part, regexp2.None)
		if err != nil {
			return nil, dependencyPolicy("invalid_filter", "", "")
		}
		compiled.MatchTimeout = 100 * time.Millisecond
		result = append(result, compiled)
	}
	return result, nil
}
func newDependencyFilter(include, exclude, types string) (dependencyFilter, error) {
	var result dependencyFilter
	var err error
	result.include, err = dependencyRegex(include)
	if err != nil {
		return result, err
	}
	result.exclude, err = dependencyRegex(exclude)
	if err != nil {
		return result, err
	}
	if types != "" {
		result.types = strings.Split(types, "|")
	}
	return result, nil
}
func dependencyRegexMatches(patterns []*regexp2.Regexp, name string) (bool, error) {
	for _, pattern := range patterns {
		matched, err := pattern.MatchString(name)
		if err != nil {
			return false, dependencyPolicy("invalid_filter", "", "")
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}
func (f dependencyFilter) matches(name, proxyType string, include bool) (bool, error) {
	if include && len(f.include) > 0 {
		matched, err := dependencyRegexMatches(f.include, name)
		if err != nil || !matched {
			return false, err
		}
	}
	if len(f.exclude) > 0 {
		matched, err := dependencyRegexMatches(f.exclude, name)
		if err != nil || matched {
			return false, err
		}
	}
	for _, kind := range f.types {
		if strings.EqualFold(kind, proxyType) {
			return false, nil
		}
	}
	return true, nil
}

func dependencyViewKey(prefix, include, exclude, types string) string {
	encoded, _ := json.Marshal([4]string{prefix, include, exclude, types})
	return string(encoded)
}

func (g *dependencyGraph) staticView(group outboundgroup.GroupCommonOption) (dependencyProviderView, error) {
	key := dependencyViewKey("static", group.Filter, group.ExcludeFilter, group.ExcludeType)
	if view, exists := g.views[key]; exists {
		return view, nil
	}
	filter, err := newDependencyFilter(group.Filter, group.ExcludeFilter, group.ExcludeType)
	if err != nil {
		return dependencyProviderView{}, err
	}
	node := g.addNode(DependencyNode{Kind: "aggregate", Name: "static-proxies"}, "", true)
	for _, proxy := range g.proxies {
		info := g.nodes[proxy]
		matched, err := filter.matches(info.ref.Name, info.proxyType, true)
		if err != nil {
			return dependencyProviderView{}, err
		}
		if matched {
			g.nodes[node].edges = append(g.nodes[node].edges, proxy)
		}
	}
	view := dependencyProviderView{node: node, guaranteed: len(g.nodes[node].edges) > 0}
	g.views[key] = view
	return view, nil
}

func (g *dependencyGraph) providerView(name string, group outboundgroup.GroupCommonOption) (dependencyProviderView, error) {
	pd, exists := g.providers[name]
	if !exists {
		return dependencyProviderView{}, dependencyPolicy("missing_target", group.Name, name)
	}
	if !pd.known {
		return dependencyProviderView{node: pd.node}, nil
	}
	key := dependencyViewKey("provider:"+name, group.Filter, group.ExcludeFilter, group.ExcludeType)
	if view, exists := g.views[key]; exists {
		return view, nil
	}
	filter, err := newDependencyFilter(group.Filter, group.ExcludeFilter, group.ExcludeType)
	if err != nil {
		return dependencyProviderView{}, err
	}
	node := g.addNode(DependencyNode{Kind: "aggregate", Name: name}, "", true)
	for _, member := range pd.members {
		info := g.nodes[member]
		matched, err := filter.matches(info.ref.Name, info.proxyType, true)
		if err != nil {
			return dependencyProviderView{}, err
		}
		if matched {
			g.nodes[node].edges = append(g.nodes[node].edges, member)
		}
	}
	view := dependencyProviderView{node: node, guaranteed: len(g.nodes[node].edges) > 0}
	g.views[key] = view
	return view, nil
}

func (g *dependencyGraph) allProviderView(group outboundgroup.GroupCommonOption) (dependencyProviderView, error) {
	key := dependencyViewKey("all-providers", group.Filter, group.ExcludeFilter, group.ExcludeType)
	if view, exists := g.views[key]; exists {
		return view, nil
	}
	node := g.addNode(DependencyNode{Kind: "aggregate", Name: "all-providers"}, "", true)
	guaranteed := false
	for _, name := range sortedDependencyKeys(g.providers) {
		view, err := g.providerView(name, group)
		if err != nil {
			return dependencyProviderView{}, err
		}
		g.nodes[node].edges = append(g.nodes[node].edges, view.node)
		guaranteed = guaranteed || view.guaranteed
	}
	view := dependencyProviderView{node: node, guaranteed: guaranteed}
	g.views[key] = view
	return view, nil
}

func (g *dependencyGraph) addGroup(group outboundgroup.GroupCommonOption) error {
	node := g.roots[group.Name]
	if group.EmptyFallback == "" {
		group.EmptyFallback = "COMPATIBLE"
	}
	fallback, exists := g.roots[group.EmptyFallback]
	if !exists || g.nodes[fallback].ref.Kind == "group" {
		return dependencyPolicy("missing_target", group.Name, group.EmptyFallback)
	}
	filter, err := newDependencyFilter(group.Filter, group.ExcludeFilter, group.ExcludeType)
	if err != nil {
		return err
	}
	guaranteed := false
	for _, name := range group.Proxies {
		member, exists := g.roots[name]
		if !exists {
			return dependencyPolicy("missing_target", group.Name, name)
		}
		matched, err := filter.matches(name, g.nodes[member].proxyType, false)
		if err != nil {
			return err
		}
		if matched {
			g.nodes[node].edges = append(g.nodes[node].edges, member)
			guaranteed = true
		}
	}
	if group.IncludeAll || group.IncludeAllProxies {
		view, err := g.staticView(group)
		if err != nil {
			return err
		}
		g.nodes[node].edges = append(g.nodes[node].edges, view.node)
		guaranteed = guaranteed || view.guaranteed
	}
	if group.IncludeAll || group.IncludeAllProviders {
		view, err := g.allProviderView(group)
		if err != nil {
			return err
		}
		g.nodes[node].edges = append(g.nodes[node].edges, view.node)
		guaranteed = guaranteed || view.guaranteed
	} else {
		for _, name := range group.Use {
			view, err := g.providerView(name, group)
			if err != nil {
				return err
			}
			g.nodes[node].edges = append(g.nodes[node].edges, view.node)
			guaranteed = guaranteed || view.guaranteed
		}
	}
	if !guaranteed {
		g.nodes[node].edges = append(g.nodes[node].edges, fallback)
	}
	return nil
}

func (g *dependencyGraph) validateRules(raw *config.RawConfig) error {
	check := func(source string, rules []string) error {
		for _, line := range rules {
			kind, _, target, _ := RC.ParseRulePayload(line, true)
			if target == "" {
				continue
			}
			if kind == "SUB-RULE" {
				if _, exists := raw.SubRules[target]; exists {
					continue
				}
			} else if _, exists := g.roots[target]; exists {
				continue
			}
			return dependencyPolicy("missing_rule_target", source, target)
		}
		return nil
	}
	if err := check("rules", raw.Rule); err != nil {
		return err
	}
	for _, name := range sortedDependencyKeys(raw.SubRules) {
		if err := check("sub-rules:"+name, raw.SubRules[name]); err != nil {
			return err
		}
	}
	return nil
}

func (g *dependencyGraph) path(nodes []int) []DependencyNode {
	result := make([]DependencyNode, 0, len(nodes))
	for _, node := range nodes {
		if !g.nodes[node].auxiliary {
			result = append(result, g.nodes[node].ref)
		}
	}
	return result
}

func (g *dependencyGraph) findCycle() error {
	type frame struct{ node, next int }
	color := make([]uint8, len(g.nodes))
	position := make([]int, len(g.nodes))
	var stack []frame
	for start := range g.nodes {
		if color[start] != 0 {
			continue
		}
		stack = append(stack[:0], frame{node: start})
		color[start] = 1
		position[start] = 0
		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			edges := g.nodes[top.node].edges
			if top.next == len(edges) {
				color[top.node] = 2
				stack = stack[:len(stack)-1]
				continue
			}
			child := edges[top.next]
			top.next++
			if color[child] == 1 {
				cycle := make([]int, 0, len(stack)-position[child]+1)
				for _, step := range stack[position[child]:] {
					cycle = append(cycle, step.node)
				}
				cycle = append(cycle, child)
				path := g.path(cycle)
				if len(path) > 0 && path[0] != path[len(path)-1] {
					path = append(path, path[0])
				}
				return &DependencyPolicyError{Reason: "cycle", Path: path}
			}
			if color[child] == 0 {
				position[child] = len(stack)
				color[child] = 1
				stack = append(stack, frame{node: child})
			}
		}
	}
	return nil
}

func (g *dependencyGraph) rejectUnknownDialers() error {
	predecessor := make([]int, len(g.nodes))
	for i := range predecessor {
		predecessor[i] = -2
	}
	sort.Ints(g.dialers)
	queue := make([]int, 0, len(g.nodes))
	for _, node := range g.dialers {
		if predecessor[node] == -2 {
			predecessor[node] = -1
			queue = append(queue, node)
		}
	}
	for index := 0; index < len(queue); index++ {
		node := queue[index]
		if g.nodes[node].unknown {
			var path []int
			for current := node; current >= 0; current = predecessor[current] {
				path = append(path, current)
			}
			for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
				path[i], path[j] = path[j], path[i]
			}
			return &DependencyPolicyError{Reason: "unknown_provider_dependency", Target: g.nodes[node].ref.Name, Path: g.path(path)}
		}
		for _, next := range g.nodes[node].edges {
			if predecessor[next] == -2 {
				predecessor[next] = node
				queue = append(queue, next)
			}
		}
	}
	return nil
}

func dependencyTypeName(kind string) string {
	switch kind {
	case "ss":
		return C.Shadowsocks.String()
	case "ssr":
		return C.ShadowsocksR.String()
	case "select":
		return C.Selector.String()
	case "url-test":
		return C.URLTest.String()
	case "load-balance":
		return C.LoadBalance.String()
	case "reject-drop":
		return C.RejectDrop.String()
	case "gost-relay":
		return C.GostRelay.String()
	case "pass-rule":
		return C.PassRule.String()
	}
	return kind
}
