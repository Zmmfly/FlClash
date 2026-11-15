//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/constant"
)

// These legacy transport checks do not qualify the future separated Web storage API.
func TestWebDependencyConfigPathRemainsWithinVirtualRoots(t *testing.T) {
	s := newWebTestServer(t)
	s.coreEnabled.Store(true)
	oldInit := isInit.Load()
	oldHome := constant.Path.HomeDir()
	oldSetup := setupConfig
	isInit.Store(true)
	constant.SetHomeDir(s.opts.DataDir)
	defer func() { isInit.Store(oldInit); constant.SetHomeDir(oldHome); setupConfig = oldSetup }()
	outside := filepath.Join(t.TempDir(), "candidate.yaml")
	os.WriteFile(outside, []byte("rules: ['MATCH,DIRECT']\n"), 0600)
	s.data.Symlink(filepath.Dir(outside), "escape")
	called := false
	setupConfig = func(params *SetupParams) error {
		called = true
		if params.Base != nil || params.ConfigPath == "" || webWithin(s.opts.DataDir, params.ConfigPath) {
			t.Error("untrusted path was forwarded without an isolated snapshot")
		}
		data, err := os.ReadFile(params.ConfigPath)
		if err != nil {
			return err
		}
		raw, err := unmarshalRawConfig(data)
		if err != nil {
			return err
		}
		if raw.MixedPort != 1234 {
			t.Error("candidate contents changed")
		}
		return nil
	}
	invoke := func(params SetupParams) MethodResponse {
		args, _ := json.Marshal(params)
		var response MethodResponse
		json.Unmarshal(s.webRPC(&MethodCall{ID: "setup", Method: configSetupMethod, Arguments: args}), &response)
		return response
	}
	for _, path := range []string{outside, s.opts.AuthFile, "/etc/passwd", "/data/../candidate.yaml", "/data/escape/candidate.yaml", "/tmp-other/candidate.yaml"} {
		called = false
		response := invoke(SetupParams{ConfigPath: path})
		if response.Error == nil || called {
			t.Fatalf("ConfigPath escape accepted: %s", path)
		}
	}
	s.temporary.WriteFile("candidate.yaml", []byte("mixed-port: 1234\nrules: ['MATCH,DIRECT']\n"), 0600)
	called = false
	if response := invoke(SetupParams{ConfigPath: "/tmp/candidate.yaml"}); response.Error != nil || !called {
		t.Fatalf("virtual candidate failed: %+v", response)
	}
	called = false
	response := invoke(SetupParams{Base: &SetupBase{Path: "/data/irrelevant", Overlay: "{}"}, ConfigPath: "/tmp/candidate.yaml"})
	if response.Error == nil || response.Error.Code != "outbound_policy_invalid" || called {
		t.Fatal("mutually exclusive setup sources lost typed rejection")
	}
}

func TestWebDependencyPolicyRejectKeepsRunningStateAndTypedErrors(t *testing.T) {
	s := newWebTestServer(t)
	s.coreEnabled.Store(true)
	oldInit := isInit.Load()
	oldHome := constant.Path.HomeDir()
	oldSetup := setupConfig
	oldConfig := currentConfig
	oldRunning := isRunning.Load()
	isInit.Store(true)
	constant.SetHomeDir(s.opts.DataDir)
	setupConfig = applyConfig
	sentinel := &config.Config{}
	currentConfig = sentinel
	isRunning.Store(true)
	defer func() {
		isInit.Store(oldInit)
		constant.SetHomeDir(oldHome)
		setupConfig = oldSetup
		currentConfig = oldConfig
		isRunning.Store(oldRunning)
	}()
	oldDisk := []byte("rules: ['MATCH,DIRECT']\n")
	s.data.WriteFile("config.yaml", oldDisk, 0600)
	s.data.WriteFile("profile.yaml", []byte(dependencyBaseProfile), 0600)
	cyclic := []byte("proxies: [{name: A, type: socks5, server: 127.0.0.1, port: 1, dialer-proxy: G}]\nproxy-groups: [{name: G, type: select, proxies: [A]}]\nrules: ['MATCH,A']\n")
	s.temporary.WriteFile("cycle.yaml", cyclic, 0600)
	for _, params := range []SetupParams{{ConfigPath: "/tmp/cycle.yaml"}, {Base: &SetupBase{Path: "/data/profile.yaml", Overlay: "{}", DialerProxies: map[string]string{"B": "A"}}}} {
		args, _ := json.Marshal(params)
		var response MethodResponse
		json.Unmarshal(s.webRPC(&MethodCall{ID: "reject", Method: configSetupMethod, Arguments: args}), &response)
		if response.Error == nil || response.Error.Code != "outbound_policy_invalid" {
			t.Fatalf("Web policy error wrapped: %+v", response.Error)
		}
		if currentConfig != sentinel || !isRunning.Load() {
			t.Fatal("Web policy rejection fell back or stopped the old core")
		}
		after, _ := s.data.ReadFile("config.yaml")
		if !bytes.Equal(after, oldDisk) {
			t.Fatal("Web rejected candidate changed running disk state")
		}
	}
	for _, dialers := range []map[string]string{{"A": "Safe"}, {"A": ""}} {
		args, _ := json.Marshal(SetupBase{Path: "/data/profile.yaml", Overlay: "{}", DialerProxies: dialers})
		var response MethodResponse
		json.Unmarshal(s.webRPC(&MethodCall{ID: "preview", Method: profileBuildMethod, Arguments: args}), &response)
		if response.Error != nil {
			t.Fatal(response.Error)
		}
		path, ok := response.Result.(string)
		if !ok {
			t.Fatal("preview did not return a virtual file")
		}
		data, err := s.readRPCPath(path)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := unmarshalRawConfig(data)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := raw.Proxy[0]["dialer-proxy"].(string); got != dialers["A"] {
			t.Fatal("Web preview lost dialer override")
		}
	}
	args, _ := json.Marshal(SetupBase{Path: "/data/profile.yaml", Overlay: "{}", DialerProxies: map[string]string{"Missing": "Safe"}})
	var response MethodResponse
	json.Unmarshal(s.webRPC(&MethodCall{ID: "preview-reject", Method: profileBuildMethod, Arguments: args}), &response)
	if response.Error == nil || response.Error.Code != "outbound_policy_invalid" {
		t.Fatal("Web preview lost typed source validation")
	}
}
