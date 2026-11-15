package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/constant"
	"go.yaml.in/yaml/v3"
)

func TestDependencyMetadataValidatorRejectsWithSanitizedPolicy(t *testing.T) {
	private := map[string]any{"server": "private-server", "password": "private-password", "raw": "private-mapping"}
	for _, test := range []struct {
		name string
		raw  *config.RawConfig
	}{
		{"nil configuration", nil},
		{"proxy dialer", dependencyTestRaw([]map[string]any{{"name": "A", "type": "socks5", "dialer-proxy": private}}, nil, nil)},
		{"proxy name", dependencyTestRaw([]map[string]any{{"name": private, "type": "socks5"}}, nil, nil)},
		{"proxy type", dependencyTestRaw([]map[string]any{{"name": "A", "type": private}}, nil, nil)},
		{"group members", dependencyTestRaw(nil, []map[string]any{{"name": "G", "type": "select", "proxies": private}}, nil)},
		{"group filter", dependencyTestRaw(nil, []map[string]any{{"name": "G", "type": "select", "filter": private}}, nil)},
		{"group provider use", dependencyTestRaw(nil, []map[string]any{{"name": "G", "type": "select", "use": private}}, nil)},
		{"provider dialer", dependencyTestRaw(nil, nil, map[string]map[string]any{"P": {"type": "http", "dialer-proxy": private}})},
		{"provider filter", dependencyTestRaw(nil, nil, map[string]map[string]any{"P": {"type": "inline", "filter": private}})},
		{"provider override", dependencyTestRaw(nil, nil, map[string]map[string]any{"P": {"type": "http", "override": []any{"private-password"}}})},
		{"provider override dialer", dependencyTestRaw(nil, nil, map[string]map[string]any{"P": {"type": "http", "override": map[string]any{"dialer-proxy": private}}})},
		{"provider payload", dependencyTestRaw(nil, nil, map[string]map[string]any{"P": {"type": "inline", "payload": private}})},
		{"provider payload dialer", dependencyTestRaw(nil, nil, map[string]map[string]any{"P": {"type": "inline", "payload": []map[string]any{{"name": "member", "type": "socks5", "dialer-proxy": private}}}})},
		{"invalid rename regex", dependencyTestRaw(nil, nil, map[string]map[string]any{"P": {"type": "inline", "override": map[string]any{"proxy-name": []map[string]any{{"pattern": "[private-password", "target": "renamed"}}}}})},
		{"nil rename pattern", dependencyTestRaw(nil, nil, map[string]map[string]any{"P": {"type": "http", "override": map[string]any{"proxy-name": []map[string]any{{"pattern": nil, "target": "renamed"}}}}})},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := dependencyReason(t, ValidateProxyDependencies(test.raw), "invalid_dependency_metadata")
			encoded, err := json.Marshal(asMethodError(policy))
			if err != nil {
				t.Fatal(err)
			}
			if len(policy.Path) == 0 || bytesContainAny(encoded, "private-server", "private-password", "private-mapping") {
				t.Fatal("metadata policy lost its safe witness or disclosed raw metadata")
			}
		})
	}
}

func TestDependencyMetadataApplyConfigKeepsTransactionUnchanged(t *testing.T) {
	home := t.TempDir()
	previousHome, previousConfig := constant.Path.HomeDir(), currentConfig
	previousRunning := isRunning.Load()
	previousSkip, previousURL := skipCertVerify.Load(), testURL.Load()
	previousDefault := constant.DefaultTestURL
	constant.SetHomeDir(home)
	keepTunnel(t)
	t.Cleanup(func() {
		constant.SetHomeDir(previousHome)
		currentConfig = previousConfig
		isRunning.Store(previousRunning)
		skipCertVerify.Store(previousSkip)
		testURL.Store(previousURL)
		constant.DefaultTestURL = previousDefault
	})
	badValue := "{server: private-server, password: private-password}"
	cases := []struct {
		name, profile string
		dialers       map[string]string
	}{
		{"proxy dialer", "proxies: [{name: A, type: socks5, dialer-proxy: B}, {name: B, type: socks5, dialer-proxy: " + badValue + "}]", nil},
		{"group members", "proxy-groups: [{name: G, type: select, proxies: " + badValue + "}]", nil},
		{"group include all", "proxy-groups: [{name: G, type: select, include-all: " + badValue + "}]", nil},
		{"provider dialer", "proxy-providers: {P: {type: http, dialer-proxy: " + badValue + "}}", nil},
		{"provider override dialer", "proxy-providers: {P: {type: http, override: {dialer-proxy: " + badValue + "}}}", nil},
		{"provider rename regex", "proxy-providers: {P: {type: inline, override: {proxy-name: [{pattern: '[private-password', target: renamed}]}}}", nil},
		{"provider nil rename", "proxy-providers: {P: {type: http, override: {proxy-name: [{pattern: null, target: renamed}]}}}", nil},
		{"proxy sequence shape", "proxies: {name: A, type: socks5}", nil},
		{"duplicate dependency section", "proxies: []\nproxies: [{name: A, type: socks5, dialer-proxy: " + badValue + "}]", nil},
		{"provider mapping shape", "proxy-providers: {P: [private-password]}", nil},
		{"override sequence shape", "proxies: {name: A, type: socks5}", map[string]string{"A": "DIRECT"}},
		{"override null sequence", "proxies: null", map[string]string{"A": "DIRECT"}},
		{"override invalid name", "proxies: [{name: " + badValue + ", type: socks5}]", map[string]string{"A": "DIRECT"}},
		{"override invalid type", "proxies: [{name: A, type: " + badValue + "}]", map[string]string{"A": ""}},
		{"override cyclic alias", "proxies: [&shared {<<: *shared, name: A, type: socks5}]", map[string]string{"A": "DIRECT"}},
		{"override cannot assemble root", "[private-password]", map[string]string{"A": "DIRECT"}},
		{"override cannot assemble syntax", "proxies: [private-password", map[string]string{"A": "DIRECT"}},
	}
	for _, test := range cases {
		for _, source := range []string{"base", "candidate", "existing"} {
			if len(test.dialers) > 0 && source != "base" {
				continue
			}
			t.Run(test.name+"/"+source, func(t *testing.T) {
				path := writeBaseProfile(t, test.profile)
				oldDisk := []byte("rules: ['MATCH,DIRECT']\n")
				params := &SetupParams{startListeners: true, Options: CoreOptions{TestURL: "https://must-not-apply.invalid/", SkipCertVerify: !previousSkip}}
				switch source {
				case "base":
					params.Base = &SetupBase{Path: path, Overlay: test.profile, DialerProxies: test.dialers}
				case "candidate":
					params.ConfigPath = path
				case "existing":
					oldDisk = []byte(test.profile)
				}
				if err := os.WriteFile(runningConfigPath(), oldDisk, 0600); err != nil {
					t.Fatal(err)
				}
				sentinel := &config.Config{}
				currentConfig = sentinel
				isRunning.Store(false)
				skipCertVerify.Store(previousSkip)
				testURL.Store(previousURL)
				constant.DefaultTestURL = previousDefault
				beforeURL := currentTestURL()
				policy := dependencyReason(t, applyConfig(params), "invalid_dependency_metadata")
				if currentConfig != sentinel || isRunning.Load() {
					t.Fatal("metadata rejection changed config or listener intent")
				}
				if skipCertVerify.Load() != previousSkip || currentTestURL() != beforeURL || constant.DefaultTestURL != previousDefault {
					t.Fatal("metadata rejection applied options")
				}
				after, err := os.ReadFile(runningConfigPath())
				if err != nil || !bytes.Equal(after, oldDisk) {
					t.Fatal("metadata rejection changed running disk bytes")
				}
				encoded, _ := json.Marshal(asMethodError(policy))
				if bytesContainAny(encoded, "private-server", "private-password") {
					t.Fatal("metadata apply diagnostic disclosed input")
				}
			})
		}
	}
}

func TestDependencyMetadataBrokenOverrideNodesAreTyped(t *testing.T) {
	key := func(value string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value} }
	for _, test := range []struct {
		name string
		node *yaml.Node
	}{
		{"nil document", nil},
		{"nil alias", &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Content: []*yaml.Node{key("proxies"), {Kind: yaml.AliasNode}}}}}},
		{"nil occurrence", &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{key("proxies"), {Kind: yaml.SequenceNode, Content: []*yaml.Node{nil}}}}},
		{"odd mapping", &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{key("proxies")}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dependencyReason(t, applyDocumentDialers(test.node, map[string]string{"A": "DIRECT"}), "invalid_dependency_metadata")
		})
	}
}

func TestDependencyMetadataOrdinaryNativeParseRetainsFallback(t *testing.T) {
	defer settleMessageBatcher()
	home := t.TempDir()
	previousHome, previousConfig := constant.Path.HomeDir(), currentConfig
	previousRunning := isRunning.Load()
	previousSkip, previousURL := skipCertVerify.Load(), testURL.Load()
	previousDefault := constant.DefaultTestURL
	constant.SetHomeDir(home)
	isRunning.Store(false)
	keepTunnel(t)
	t.Cleanup(func() {
		constant.SetHomeDir(previousHome)
		currentConfig = previousConfig
		isRunning.Store(previousRunning)
		skipCertVerify.Store(previousSkip)
		testURL.Store(previousURL)
		constant.DefaultTestURL = previousDefault
	})
	for _, profile := range []string{
		"proxies: [{name: A, type: socks5, server: {bad: server}, port: 1}]",
		"proxies: [{name: A, type: no-such-protocol}]",
		"mixed-port: {bad: port}\nproxies: [{name: A, type: socks5, server: 127.0.0.1, port: 1}]",
		"mixed-port: 1\nmixed-port: 2\nproxies: [{name: A, type: socks5, server: 127.0.0.1, port: 1}]",
	} {
		path := writeBaseProfile(t, profile)
		params := &SetupParams{ConfigPath: path, Options: CoreOptions{TestURL: "https://fallback-option.invalid/", SkipCertVerify: !previousSkip}}
		sentinel := &config.Config{}
		currentConfig = sentinel
		err := applyConfig(params)
		var method *MethodError
		if isDependencyPolicyError(err) || !errors.As(err, &method) || method.Code != "config_rejected" {
			t.Fatalf("ordinary native parse failure changed its fallback contract: %v", err)
		}
		if currentConfig == sentinel || currentConfig == nil || isRunning.Load() {
			t.Fatal("ordinary parse did not apply the listener-free default")
		}
		if currentTestURL() != params.Options.TestURL || skipCertVerify.Load() != params.Options.SkipCertVerify {
			t.Fatal("ordinary fallback unexpectedly skipped options")
		}
	}
}

const dependencyAliasProfile = `templates: &template
  type: socks5
  server: 127.0.0.1
  port: 1
  dialer-proxy: C
  custom-field: &custom {nested: [preserved, {short-id: 0123}]}
proxies:
  - &shared
    <<: *template
    name: A
  - <<: *shared
    name: B
  - {name: C, type: socks5, server: 127.0.0.1, port: 2}
  - {name: D, type: socks5, server: 127.0.0.1, port: 3}
rules: ['MATCH,A']
`

func TestDependencyMetadataOverrideDetachesAliasOccurrences(t *testing.T) {
	for _, test := range []struct {
		name    string
		dialers map[string]string
		want    map[string]string
	}{
		{"replace anchor", map[string]string{"A": "D"}, map[string]string{"A": "D", "B": "C"}},
		{"clear anchor", map[string]string{"A": ""}, map[string]string{"A": "", "B": "C"}},
		{"replace peer", map[string]string{"B": "D"}, map[string]string{"A": "C", "B": "D"}},
		{"both independent", map[string]string{"A": "D", "B": ""}, map[string]string{"A": "D", "B": ""}},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, err := readCandidateDocument([]byte(dependencyAliasProfile))
			if err != nil {
				t.Fatal(err)
			}
			root, _ := documentMapping(document)
			original := mappingValue(root, "proxies")
			if err := applyDocumentDialers(document, test.dialers); err != nil {
				t.Fatal(err)
			}
			var raw config.RawConfig
			if err := document.Decode(&raw); err != nil {
				t.Fatal(err)
			}
			for _, mapping := range raw.Proxy {
				name, _ := mapping["name"].(string)
				if want, check := test.want[name]; check && mapping["dialer-proxy"] != want {
					t.Fatalf("dialer for %s = %v, want %q", name, mapping["dialer-proxy"], want)
				}
			}
			var untouched []map[string]any
			if err := original.Decode(&untouched); err != nil {
				t.Fatal(err)
			}
			if untouched[0]["dialer-proxy"] != "C" || untouched[1]["dialer-proxy"] != "C" {
				t.Fatal("override mutated the source alias graph")
			}
			for i := 0; i < 2; i++ {
				if !reflect.DeepEqual(raw.Proxy[i]["custom-field"], untouched[i]["custom-field"]) {
					t.Fatal("detachment lost nested unknown metadata or short-id tagging")
				}
			}
			custom := raw.Proxy[0]["custom-field"].(map[string]any)
			if custom["nested"].([]any)[1].(map[string]any)["short-id"] != "0123" {
				t.Fatal("detachment lost the literal short-id")
			}
			if resolveAlias(mappingValue(resolveAlias(mappingValue(root, "templates")), "dialer-proxy")).Value != "C" {
				t.Fatal("override changed the template")
			}

			home := withSpillHome(t, responseSpillThreshold)
			path := writeBaseProfile(t, dependencyAliasProfile)
			base := &SetupBase{Path: path, Overlay: "{}", DialerProxies: test.dialers}
			assertParsed := func(cfg *config.Config, err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
				for name, want := range test.want {
					if cfg.Proxies[name].ProxyInfo().DialerProxy != want {
						t.Fatalf("parsed alias peer %s changed", name)
					}
				}
			}
			assertParsed(loadSetupConfig(&SetupParams{Base: base}))
			assertParsed(loadConfig(runningConfigPath()))
			publishedRaw, err := readRawConfig(runningConfigPath())
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if !reflect.DeepEqual(publishedRaw.Proxy[i]["custom-field"], raw.Proxy[i]["custom-field"]) {
					t.Fatal("published config lost unknown metadata")
				}
			}
			preview, err := handleBuildProfile(base)
			if err != nil {
				t.Fatal(err)
			}
			previewRaw, err := readRawConfig(preview)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if !reflect.DeepEqual(previewRaw.Proxy[i]["custom-field"], raw.Proxy[i]["custom-field"]) {
					t.Fatal("preview lost unknown metadata")
				}
			}
			for _, mapping := range previewRaw.Proxy {
				name, _ := mapping["name"].(string)
				if want, check := test.want[name]; check && mapping["dialer-proxy"] != want {
					t.Fatalf("preview alias peer %s changed", name)
				}
			}
			data, _ := os.ReadFile(path)
			if string(data) != dependencyAliasProfile {
				t.Fatal("override edited the base source file")
			}
			if _, err := os.Stat(filepath.Join(home, "config.yaml")); err != nil {
				t.Fatal(err)
			}
		})
	}
}
