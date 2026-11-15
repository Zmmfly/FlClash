package core

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/constant"
)

const dependencyBaseProfile = "proxies:\n  - name: A\n    type: socks5\n    server: 127.0.0.1\n    port: 1\n    dialer-proxy: B\n  - name: B\n    type: socks5\n    server: 127.0.0.1\n    port: 2\nproxy-groups:\n  - name: Safe\n    type: select\n    proxies: [B]\nrules: ['MATCH,A']\n"

func TestOutboundBaseDialerOverridesInheritClearAndReplace(t *testing.T) {
	path := writeBaseProfile(t, dependencyBaseProfile)
	for _, test := range []struct {
		name    string
		dialers map[string]string
		want    string
	}{
		{"inherit", nil, "B"}, {"clear", map[string]string{"A": ""}, ""}, {"replace", map[string]string{"A": "Safe"}, "Safe"},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := &SetupBase{Path: path, Overlay: "{}", DialerProxies: test.dialers}
			document, err := mergeProfileBase(base)
			if err != nil {
				t.Fatal(err)
			}
			var raw config.RawConfig
			if err := document.Decode(&raw); err != nil {
				t.Fatal(err)
			}
			value, _ := raw.Proxy[0]["dialer-proxy"].(string)
			if value != test.want {
				t.Fatalf("effective dialer=%q", value)
			}
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			cfg, err := loadConfigWithBase(base, configPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Proxies["A"].ProxyInfo().DialerProxy; got != test.want {
				t.Fatalf("parsed dialer=%q", got)
			}
			reloaded, err := loadConfig(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := reloaded.Proxies["A"].ProxyInfo().DialerProxy; got != test.want {
				t.Fatal("cold reload dropped the override")
			}
			withSpillHome(t, responseSpillThreshold)
			built, err := handleBuildProfile(base)
			if err != nil {
				t.Fatal(err)
			}
			exported, err := readRawConfig(built)
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := exported.Proxy[0]["dialer-proxy"].(string); got != test.want {
				t.Fatal("preview/export dropped the override")
			}
		})
	}
	unchanged, _ := os.ReadFile(path)
	if string(unchanged) != dependencyBaseProfile {
		t.Fatal("overrides edited the subscription source")
	}
}

func TestOutboundOverrideRejectsMissingDuplicateAndUnsupportedSources(t *testing.T) {
	for _, test := range []struct {
		name, profile string
		dialers       map[string]string
		reason        string
	}{
		{"source missing", dependencyBaseProfile, map[string]string{"Missing": "B"}, "missing_source"},
		{"empty source", dependencyBaseProfile, map[string]string{"": "B"}, "missing_source"},
		{"group source", dependencyBaseProfile, map[string]string{"Safe": "B"}, "unsupported_source"},
		{"builtin source", dependencyBaseProfile, map[string]string{"DIRECT": "B"}, "unsupported_source"},
		{"target missing", dependencyBaseProfile, map[string]string{"A": "Missing"}, "missing_target"},
		{"target exact", dependencyBaseProfile, map[string]string{"A": " B "}, "missing_target"},
		{"cycle", dependencyBaseProfile, map[string]string{"B": "A"}, "cycle"},
		{"duplicate", dependencyBaseProfile + "", map[string]string{"A": "Safe"}, "duplicate_outbound"},
		{"unsupported", "proxies: [{name: A, type: direct}]", map[string]string{"A": "DIRECT"}, "unsupported_source"},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := test.profile
			if test.name == "duplicate" {
				profile = strings.Replace(profile, "name: B", "name: A", 1)
			}
			base := &SetupBase{Path: writeBaseProfile(t, profile), Overlay: "{}", DialerProxies: test.dialers}
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			old := []byte("old disk bytes\n")
			os.WriteFile(configPath, old, 0600)
			_, err := loadConfigWithBase(base, configPath)
			dependencyReason(t, err, test.reason)
			after, _ := os.ReadFile(configPath)
			if !bytes.Equal(old, after) {
				t.Fatal("rejected base changed running config on disk")
			}
			_, err = mergeProfileBase(base)
			dependencyReason(t, err, test.reason)
		})
	}
	for _, kind := range []string{"direct", "dns", "reject", "rematch", "unknown"} {
		base := &SetupBase{Path: writeBaseProfile(t, "proxies: [{name: A, type: "+kind+"}]"), Overlay: "{}", DialerProxies: map[string]string{"A": "DIRECT"}}
		_, err := mergeProfileBase(base)
		dependencyReason(t, err, "unsupported_source")
		base.DialerProxies["A"] = ""
		if _, err := mergeProfileBase(base); err != nil {
			t.Fatal("clearing an ignored dialer should remain possible")
		}
	}
}

func TestOutboundOverrideClearsInheritedYamlMergeValue(t *testing.T) {
	profile := "defaults: &defaults\n  type: socks5\n  server: 127.0.0.1\n  port: 1\n  dialer-proxy: B\nproxies:\n  - <<: *defaults\n    name: A\n  - {name: B, type: socks5, server: 127.0.0.1, port: 2}\nrules: ['MATCH,A']\n"
	base := &SetupBase{Path: writeBaseProfile(t, profile), Overlay: "{}", DialerProxies: map[string]string{"A": ""}}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if _, err := loadConfigWithBase(base, path); err != nil {
		t.Fatal(err)
	}
	raw, err := readRawConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Proxy[0]["dialer-proxy"] != "" {
		t.Fatal("clearing inherited YAML dialer re-enabled the default")
	}
}

func TestOutboundPolicyRejectKeepsDiskOptionsAndRunningState(t *testing.T) {
	home := t.TempDir()
	beforeHome := constant.Path.HomeDir()
	constant.SetHomeDir(home)
	defer constant.SetHomeDir(beforeHome)
	oldConfig := currentConfig
	sentinel := &config.Config{}
	currentConfig = sentinel
	defer func() { currentConfig = oldConfig }()
	oldRunning := isRunning.Load()
	isRunning.Store(true)
	defer isRunning.Store(oldRunning)
	beforeSkip := skipCertVerify.Load()
	beforeURL := currentTestURL()
	beforeDefault := constant.DefaultTestURL
	beforePointer := testURL.Load()
	defer func() {
		skipCertVerify.Store(beforeSkip)
		testURL.Store(beforePointer)
		constant.DefaultTestURL = beforeDefault
	}()
	oldDisk := []byte("rules: ['MATCH,DIRECT']\n")
	if err := os.WriteFile(runningConfigPath(), oldDisk, 0600); err != nil {
		t.Fatal(err)
	}
	profile := writeBaseProfile(t, dependencyBaseProfile)
	cyclic := strings.Replace(dependencyBaseProfile, "dialer-proxy: B", "dialer-proxy: A", 1)
	candidate := writeBaseProfile(t, cyclic)
	for _, params := range []*SetupParams{
		{Base: &SetupBase{Path: profile, Overlay: "{}", DialerProxies: map[string]string{"B": "A"}}},
		{ConfigPath: candidate},
		{ConfigPath: filepath.Join(t.TempDir(), "missing.yaml")},
		{ConfigPath: t.TempDir()},
		{Base: &SetupBase{Path: profile, Overlay: "{}"}, ConfigPath: candidate},
	} {
		params.Options = CoreOptions{TestURL: "https://must-not-apply.invalid/", SkipCertVerify: !beforeSkip}
		err := applyConfig(params)
		if !isDependencyPolicyError(err) {
			t.Fatalf("policy rejection lost type: %v", err)
		}
		if currentConfig != sentinel || !isRunning.Load() {
			t.Fatal("policy rejection replaced or stopped the running core")
		}
		if skipCertVerify.Load() != beforeSkip || currentTestURL() != beforeURL || constant.DefaultTestURL != beforeDefault {
			t.Fatal("policy rejection applied new options")
		}
		data, _ := os.ReadFile(runningConfigPath())
		if !bytes.Equal(data, oldDisk) {
			t.Fatal("policy rejection changed persisted running configuration")
		}
	}
}

func TestOutboundConfigPathPublishesOnlyValidatedCandidateAndReloads(t *testing.T) {
	home := t.TempDir()
	previous := constant.Path.HomeDir()
	constant.SetHomeDir(home)
	defer constant.SetHomeDir(previous)
	old := []byte("rules: ['MATCH,REJECT']\n")
	os.WriteFile(runningConfigPath(), old, 0600)
	path := writeBaseProfile(t, dependencyBaseProfile)
	cfg, err := loadSetupConfig(&SetupParams{ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Proxies["A"].ProxyInfo().DialerProxy != "B" {
		t.Fatal("candidate dialer was not parsed")
	}
	published, _ := os.ReadFile(runningConfigPath())
	if bytes.Equal(published, old) {
		t.Fatal("candidate was not published")
	}
	reloaded, err := loadSetupConfig(&SetupParams{})
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Proxies["A"].ProxyInfo().DialerProxy != "B" {
		t.Fatal("Android/no-field cold load lost the chain")
	}
	bad := writeBaseProfile(t, "proxies: [{name: A, type: no-such-protocol}]\n")
	if _, err := loadSetupConfig(&SetupParams{ConfigPath: bad}); err == nil {
		t.Fatal("invalid protocol candidate parsed")
	}
	after, _ := os.ReadFile(runningConfigPath())
	if !bytes.Equal(after, published) {
		t.Fatal("new ConfigPath published before the native parser accepted it")
	}
}

func TestOutboundPolicyValidationCoversAllReadAndBuildEntrypoints(t *testing.T) {
	profile := writeBaseProfile(t, "proxies: [{name: A, type: socks5, server: 127.0.0.1, port: 1, dialer-proxy: G}]\nproxy-groups: [{name: G, type: select, proxies: [A]}]\nrules: ['MATCH,A']\n")
	if message := handleValidateConfig(PathParams{Path: profile}); message == nil || !strings.Contains(*message, "cycle") {
		t.Fatal("config.validate skipped combined dependencies")
	}
	_, err := loadConfig(profile)
	dependencyReason(t, err, "cycle")
	_, err = handleBuildProfile(&SetupBase{Path: profile, Overlay: "{}"})
	dependencyReason(t, err, "cycle")
	path := writeBaseProfile(t, "rules: ['MATCH,Missing']\n")
	if message := handleValidateConfig(PathParams{Path: path}); message == nil || !strings.Contains(*message, "missing_rule_target") {
		t.Fatal("config.validate skipped rule targets")
	}
}

func TestOutboundMetadataContainsDependencyFieldsButNoCredentials(t *testing.T) {
	profile := dependencyBaseProfile + "proxy-providers:\n  P: {type: http, url: 'https://must-not-return.example/sub', header: {Authorization: ['secret-header']}}\nrule-providers:\n  R: {type: http, url: 'https://private-rule.example/list', path: /private/rule-path}\nsub-rules:\n  retained-subrule: ['MATCH,private-subrule-target']\n"
	profile = strings.Replace(profile, "    port: 1\n", "    port: 1\n    username: private-username\n    password: private-password\n", 1)
	profile = strings.Replace(profile, "    proxies: [B]", "    proxies: [B]\n    use: [P]\n    include-all: true\n    include-all-proxies: true\n    include-all-providers: true\n    filter: '^B$'\n    exclude-filter: '^A$'\n    exclude-type: Socks5\n    empty-fallback: DIRECT", 1)
	metadata, err := handleGetProfileOutbounds(PathParams{Path: writeBaseProfile(t, profile)})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Proxies[0].DialerProxy != "B" || !reflect.DeepEqual(metadata.ProxyGroups[0].Proxies, []string{"B"}) || !metadata.ProxyGroups[0].IncludeAll || metadata.ProxyGroups[0].EmptyFallback != "DIRECT" {
		t.Fatal("dependency metadata fields missing")
	}
	if _, exists := metadata.ProxyProviders["P"]; !exists || metadata.ProxyProviders["P"] != nil {
		t.Fatal("provider metadata must expose names only")
	}
	encoded, _ := json.Marshal(metadata)
	if bytesContainAny(encoded, "private-password", "private-username", "127.0.0.1", "must-not-return.example", "secret-header", "private-rule.example", "/private/rule-path", "private-subrule-target") {
		t.Fatal("metadata exposed connection credentials")
	}
	if _, ok := metadata.RuleProviders["R"]; !ok || metadata.RuleProviders["R"] != nil {
		t.Fatal("rule provider metadata included values or lost names")
	}
	if _, ok := metadata.SubRules["retained-subrule"]; !ok || metadata.SubRules["retained-subrule"] != nil {
		t.Fatal("sub-rule metadata included rules or lost names")
	}
	var roundTrip map[string]any
	if json.Unmarshal(encoded, &roundTrip) != nil {
		t.Fatal("invalid metadata JSON")
	}
	allowedTop := map[string]bool{"proxies": true, "proxy-groups": true, "proxy-providers": true, "rule-providers": true, "sub-rules": true}
	for key := range roundTrip {
		if !allowedTop[key] {
			t.Fatalf("unapproved metadata field %s", key)
		}
	}
	allowedProxy := map[string]bool{"name": true, "type": true, "dialer-proxy": true}
	allowedGroup := map[string]bool{"name": true, "type": true, "proxies": true, "use": true, "include-all": true, "include-all-proxies": true, "include-all-providers": true, "filter": true, "exclude-filter": true, "exclude-type": true, "empty-fallback": true}
	for _, collection := range []string{"proxies", "proxy-groups"} {
		allowed := allowedProxy
		if collection == "proxy-groups" {
			allowed = allowedGroup
		}
		for _, item := range roundTrip[collection].([]any) {
			for key := range item.(map[string]any) {
				if !allowed[key] {
					t.Fatalf("unapproved %s field %s", collection, key)
				}
			}
		}
	}
}
