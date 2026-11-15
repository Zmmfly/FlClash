//go:build webpanel && !android

package core

// Retired RPC transport stays unavailable; internal dispatch and typed HTTP retain isolation coverage.

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metacubex/mihomo/constant"
)

func TestWebSlowInternalRPCDoesNotBlockTypedQueriesOrWrites(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	beforeInit := isInit.Load()
	isInit.Store(true)
	defer isInit.Store(beforeInit)
	s.coreEnabled.Store(true)
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	method := CoreMethod("web.test.slow")
	methodHandlers[method] = func(*MethodCall) (any, error) { close(started); <-release; return true, nil }
	defer delete(methodHandlers, method)
	go func() { defer close(finished); s.webRPC(&MethodCall{ID: "slow", Method: method}) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("internal dispatch never entered")
	}
	fast := make(chan error, 1)
	go func() {
		response := s.webRPC(&MethodCall{ID: "fast", Method: coreInitializedMethod})
		if !bytes.Contains(response, []byte(`"result":true`)) {
			fast <- errors.New("fast internal query failed")
			return
		}
		body, _ := json.Marshal(webAPITestMutation("0", "slow-rpc-concurrent", map[string]any{"profile": webAPITestProfile("parallel")}))
		r := httptest.NewRequest("POST", "http://panel.test/api/v1/profiles:create", bytes.NewReader(body))
		r.AddCookie(cookie)
		r.Header.Set("Origin", "http://panel.test")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 200 {
			fast <- errors.New("parallel typed write failed: " + w.Body.String())
			return
		}
		fast <- nil
	}()
	select {
	case err := <-fast:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slow internal dispatch blocked typed HTTP")
	}
	unblock()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("internal dispatch did not finish")
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
		if params.Base != nil || params.ConfigPath == "" || webWithin(s.opts.DataDir, params.ConfigPath) {
			return errors.New("setup did not isolate its input snapshot")
		}
		data, err := os.ReadFile(params.ConfigPath)
		if err != nil {
			return err
		}
		document, err := readCandidateDocument(data)
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

func TestWebRetiredMkdirAndSlowTypedBodyQueryIsolation(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	for _, p := range []string{"/data", "/tmp", "/data/.", "/tmp//"} {
		if w := webRequest(s, "POST", "/api/fs/mkdir?path="+p, "", cookie); w.Code != 404 {
			t.Fatalf("retired mkdir %s = %d", p, w.Code)
		}
	}
	body, _ := json.Marshal(webAPITestMutation("0", "slow-typed-body", map[string]any{"profile": webAPITestProfile("slow")}))
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan *httptest.ResponseRecorder, 1)
	var once sync.Once
	defer once.Do(func() { close(release) })
	reader := &webRPCSlowBody{reader: strings.NewReader(string(body)), started: started, release: release}
	r := httptest.NewRequest("POST", "http://panel.test/api/v1/profiles:create", reader)
	r.AddCookie(cookie)
	r.Header.Set("Origin", "http://panel.test")
	r.Header.Set("Content-Type", "application/json")
	go func() { w := httptest.NewRecorder(); s.ServeHTTP(w, r); done <- w }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("typed body did not enter")
	}
	fast := make(chan int, 1)
	go func() { fast <- webRequest(s, "GET", "/api/v1/profiles", "", cookie).Code }()
	select {
	case code := <-fast:
		if code != 200 {
			t.Fatal(code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slow typed body held global query lock")
	}
	once.Do(func() { close(release) })
	select {
	case w := <-done:
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("typed body did not finish")
	}
}

type webRPCSlowBody struct {
	reader           *strings.Reader
	started, release chan struct{}
	once             sync.Once
}

func (r *webRPCSlowBody) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started); <-r.release })
	return r.reader.Read(p)
}

func TestWebInternalDispatchFiftyWayBatchAndRetiredSocket(t *testing.T) {
	s := newWebTestServer(t)
	s.coreEnabled.Store(true)
	before := isInit.Load()
	isInit.Store(true)
	defer isInit.Store(before)
	started := make(chan struct{}, delayTestConcurrency)
	release := make(chan struct{})
	done := make(chan []byte, delayTestConcurrency)
	var once sync.Once
	defer once.Do(func() { close(release) })
	method := CoreMethod("web.test.delayBatch")
	methodHandlers[method] = func(*MethodCall) (any, error) { started <- struct{}{}; <-release; return true, nil }
	defer delete(methodHandlers, method)
	for i := 0; i < delayTestConcurrency; i++ {
		go func() { done <- s.webRPC(&MethodCall{ID: "batch", Method: method}) }()
	}
	for i := 0; i < delayTestConcurrency; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("internal 50-way batch did not enter")
		}
	}
	control := s.webRPC(&MethodCall{ID: "control", Method: coreInitializedMethod})
	if !bytes.Contains(control, []byte(`"result":true`)) {
		t.Fatal("batch blocked internal control")
	}
	once.Do(func() { close(release) })
	for i := 0; i < delayTestConcurrency; i++ {
		select {
		case reply := <-done:
			if !bytes.Contains(reply, []byte(`"result":true`)) {
				t.Fatal("batch internal response failed")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("internal batch did not finish")
		}
	}
	cookie := webLogin(t, s)
	if w := webRequest(s, "GET", "/ws/core", "", cookie); w.Code != 404 {
		t.Fatal("retired RPC websocket returned", w.Code)
	}
}
