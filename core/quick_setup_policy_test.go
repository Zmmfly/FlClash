package core

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/constant"
)

func TestOutboundQuickSetupPolicyRunsBeforeListenerIntent(t *testing.T) {
	home := t.TempDir()
	beforeHome := constant.Path.HomeDir()
	beforeConfig := currentConfig
	beforeInit := isInit.Load()
	beforeRunning := isRunning.Load()
	beforeSDK := sdkVersion.Load()
	beforeSetup := setupConfig
	constant.SetHomeDir(home)
	sentinel := &config.Config{}
	currentConfig = sentinel
	isRunning.Store(false)
	setupConfig = applyConfig
	defer func() {
		constant.SetHomeDir(beforeHome)
		currentConfig = beforeConfig
		isInit.Store(beforeInit)
		isRunning.Store(beforeRunning)
		sdkVersion.Store(beforeSDK)
		setupConfig = beforeSetup
	}()
	oldDisk := []byte("rules: ['MATCH,DIRECT']\n")
	os.WriteFile(runningConfigPath(), oldDisk, 0600)
	cycle := writeBaseProfile(t, "proxies: [{name: A, type: socks5, dialer-proxy: A}]\n")
	beforeSkip := skipCertVerify.Load()
	beforeURL := currentTestURL()
	beforeDefault := constant.DefaultTestURL
	initData, _ := json.Marshal(InitParams{HomeDir: home, Version: 42})
	setupData, _ := json.Marshal(SetupParams{ConfigPath: cycle, Options: CoreOptions{TestURL: "https://must-not-apply.invalid/", SkipCertVerify: !beforeSkip}})
	err := quickSetupConfig(string(initData), string(setupData))
	dependencyReason(t, err, "cycle")
	if isRunning.Load() || currentConfig != sentinel || skipCertVerify.Load() != beforeSkip || currentTestURL() != beforeURL || constant.DefaultTestURL != beforeDefault {
		t.Fatal("QuickSetup applied listener intent or options before policy acceptance")
	}
	after, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	if !bytes.Equal(after, oldDisk) {
		t.Fatal("QuickSetup policy failure changed persisted config")
	}
	var seen bool
	setupConfig = func(params *SetupParams) error {
		seen = params.startListeners
		if isRunning.Load() {
			t.Error("QuickSetup changed listener state outside common apply ownership")
		}
		return nil
	}
	if err := quickSetupConfig(string(initData), `{}`); err != nil || !seen {
		t.Fatal("QuickSetup did not carry its private listener intent")
	}
}

func TestOutboundQuickSetupAndroidExportDelegatesToTheTestedEntry(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "lib.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var entry *ast.FuncDecl
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok && function.Name.Name == "QuickSetup" {
			entry = function
		}
	}
	if entry == nil {
		t.Fatal("Android QuickSetup export missing")
	}
	delegates := false
	ast.Inspect(entry.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		name, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		if name.Name == "quickSetupConfig" {
			delegates = true
		}
		if name.Name == "setRunning" {
			t.Error("Android wrapper bypasses the preflight listener intent")
		}
		return true
	})
	if !delegates {
		t.Fatal("Android export no longer uses the runtime-tested shared QuickSetup entry")
	}
}

func TestOutboundPrivateListenerIntentIsNotOnTheWire(t *testing.T) {
	data, err := json.Marshal(SetupParams{startListeners: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "startListeners") || strings.Contains(string(data), "start-listeners") {
		t.Fatal("private listener intent was serialized")
	}
	var params SetupParams
	if err := json.Unmarshal([]byte(`{"startListeners":true,"start-listeners":true}`), &params); err != nil {
		t.Fatal(err)
	}
	if params.startListeners {
		t.Fatal("remote JSON can set the private listener intent")
	}
}
