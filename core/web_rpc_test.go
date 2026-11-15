//go:build webpanel && !android

package core

// WEBPANEL.md and README Web capability claims are exercised through these public routes and RPCs.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/metacubex/mihomo/constant"
)

func TestWebSlowRPCDoesNotBlockQueriesOrFileWrites(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	beforeInit := isInit.Load()
	isInit.Store(true)
	defer isInit.Store(beforeInit)
	s.coreEnabled.Store(true)
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	method := CoreMethod("web.test.slow")
	methodHandlers[method] = func(*MethodCall) (any, error) { close(started); <-release; return true, nil }
	defer delete(methodHandlers, method)
	go func() { defer close(finished); s.webRPC(&MethodCall{ID: "slow", Method: method}) }()
	<-started
	fast := make(chan error, 1)
	go func() {
		response := s.webRPC(&MethodCall{ID: "fast", Method: coreInitializedMethod})
		if !bytes.Contains(response, []byte(`"result":true`)) {
			fast <- errors.New("fast query failed")
			return
		}
		if w := webRequest(s, "POST", "/api/fs/write?path=/data/concurrent", "hello", cookie); w.Code != 200 {
			fast <- errors.New("file write failed")
			return
		}
		fast <- nil
	}()
	var err error
	select {
	case err = <-fast:
	case <-time.After(2 * time.Second):
		err = errors.New("slow RPC blocked unrelated work")
	}
	close(release)
	<-finished
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebProxyConversionUsesVirtualRootAndPreservesImport(t *testing.T) {
	s := newWebTestServer(t)
	link := "ss://YWVzLTEyOC1nY206cGFzc3dvcmQ@example.com:8388#test-node\n"
	if err := s.data.WriteFile("subscription.txt", []byte(link), 0600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/data/subscription.txt", "/etc/passwd", "/data/../outside"} {
		args, _ := json.Marshal(PathParams{Path: p})
		var response MethodResponse
		json.Unmarshal(s.webRPC(&MethodCall{ID: "convert", Method: proxyConvertMethod, Arguments: args}), &response)
		if p == "/data/subscription.txt" {
			if response.Error != nil || !strings.Contains(string(s.webRPC(&MethodCall{Method: proxyConvertMethod, Arguments: args})), "test-node") {
				t.Fatalf("virtual import failed: %+v", response)
			}
		} else if response.Error == nil {
			t.Fatal("proxy.convert escaped root")
		}
	}
}

func TestWebSetupValidatesSnapshotAndKeepsPersistentHome(t *testing.T) {
	s := newWebTestServer(t)
	s.coreEnabled.Store(true)
	beforeInit := isInit.Load()
	beforeHome := constant.Path.HomeDir()
	beforeSetup := setupConfig
	isInit.Store(true)
	constant.SetHomeDir(s.opts.DataDir)
	defer func() { isInit.Store(beforeInit); constant.SetHomeDir(beforeHome); setupConfig = beforeSetup }()
	s.data.WriteFile("profile.yaml", []byte("proxies: []\nrules:\n - MATCH,DIRECT\n"), 0600)
	called := false
	setupConfig = func(params *SetupParams) error {
		called = true
		if params.Base == nil || webWithin(s.opts.DataDir, params.Base.Path) {
			return errors.New("setup did not isolate its input snapshot")
		}
		document, err := mergeProfileBase(params.Base)
		if err != nil {
			return err
		}
		return writeConfigDocument(runningConfigPath(), document)
	}
	params := SetupParams{Base: &SetupBase{Path: "/data/profile.yaml", Overlay: "mixed-port: 7890\nproxy-providers:\n a:\n  type: http\n  url: https://example.com/sub\n  path: /data/providers/a.yaml\nrules: []\n"}}
	args, _ := json.Marshal(params)
	var response MethodResponse
	json.Unmarshal(s.webRPC(&MethodCall{ID: "setup", Method: configSetupMethod, Arguments: args}), &response)
	if response.Error != nil || !called {
		t.Fatalf("setup failed: %+v", response)
	}
	data, err := s.data.ReadFile("config.yaml")
	if err != nil || !bytes.Contains(data, []byte(filepath.ToSlash(s.opts.DataDir))) || bytes.Count(data, []byte("MATCH,DIRECT")) != 1 {
		t.Fatalf("persistent config: %s %v", data, err)
	}
	if constant.Path.HomeDir() != s.opts.DataDir {
		t.Fatal("persistent home changed")
	}
	params.Base.Overlay = "external-controller: 127.0.0.1:9090\n"
	args, _ = json.Marshal(params)
	called = false
	json.Unmarshal(s.webRPC(&MethodCall{ID: "unsafe", Method: configSetupMethod, Arguments: args}), &response)
	if response.Error == nil || called {
		t.Fatal("unsafe config reached native setup")
	}
}

func TestWebDerivedNativePathsRejectSymlinks(t *testing.T) {
	s := newWebTestServer(t)
	s.coreEnabled.Store(true)
	beforeInit := isInit.Load()
	beforeHome := constant.Path.HomeDir()
	isInit.Store(true)
	constant.SetHomeDir(s.opts.DataDir)
	defer func() { isInit.Store(beforeInit); constant.SetHomeDir(beforeHome) }()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "list"), []byte("example.com\n"), 0600)
	if err := s.data.Symlink(outside, "providers/rules"); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(CompileRuleSetParams{Name: "list"})
	var response MethodResponse
	json.Unmarshal(s.webRPC(&MethodCall{Method: ruleSetCompileMethod, Arguments: args}), &response)
	if response.Error == nil {
		t.Fatal("derived rule-set path followed symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "list.mrs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rule-set compiled outside root")
	}
	if err := s.data.Symlink(outside, "profiles/providers"); err != nil {
		t.Fatal(err)
	}
	args, _ = json.Marshal(ClearEffectParams{ProfileID: 1})
	json.Unmarshal(s.webRPC(&MethodCall{Method: profileClearEffectMethod, Arguments: args}), &response)
	if response.Error == nil {
		t.Fatal("derived profile deletion followed symlink")
	}
}

func TestWebMkdirRootsRemainIdempotent(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	for _, p := range []string{"/data", "/tmp", "/data/.", "/tmp//"} {
		if w := webRequest(s, "POST", "/api/fs/mkdir?path="+p, "", cookie); w.Code != 200 {
			t.Fatalf("mkdir %s = %d %s", p, w.Code, w.Body.String())
		}
	}
}

func TestWebRPCSupportsDocumentedFiftyWayDelayBatch(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	s.coreEnabled.Store(true)
	beforeInit := isInit.Load()
	isInit.Store(true)
	defer isInit.Store(beforeInit)
	started := make(chan struct{}, delayTestConcurrency)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	method := CoreMethod("web.test.delayBatch")
	methodHandlers[method] = func(*MethodCall) (any, error) { started <- struct{}{}; <-release; return true, nil }
	defer delete(methodHandlers, method)
	server := httptest.NewServer(s)
	defer server.Close()
	conn, _, err := webDial(t, server, cookie, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	for i := 0; i < delayTestConcurrency; i++ {
		data, _ := json.Marshal(MethodCall{ID: fmt.Sprint(i), Method: method})
		if err := wsutil.WriteClientMessage(conn, ws.OpText, data); err != nil {
			t.Fatal(err)
		}
	}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for i := 0; i < delayTestConcurrency; i++ {
		select {
		case <-started:
		case <-timer.C:
			t.Fatal("normal 50-way batch exhausted RPC capacity")
		}
	}
	data, _ := json.Marshal(MethodCall{ID: "control", Method: coreInitializedMethod})
	if err := wsutil.WriteClientMessage(conn, ws.OpText, data); err != nil {
		t.Fatal(err)
	}
	answer, _, err := wsutil.ReadServerData(conn)
	if err != nil || !bytes.Contains(answer, []byte("control")) {
		t.Fatalf("batch blocked control query: %s %v", answer, err)
	}
	close(release)
	released = true
	for i := 0; i < delayTestConcurrency; i++ {
		answer, _, err := wsutil.ReadServerData(conn)
		if err != nil || bytes.Contains(answer, []byte("error")) {
			t.Fatalf("batch reply lost: %s %v", answer, err)
		}
	}
}
