//go:build webpanel && !android

package core

// WEBPANEL.md and README Web capability claims are exercised through these public routes and RPCs.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/metacubex/mihomo/constant"
)

func newWebTestServer(t *testing.T) *WebServer {
	t.Helper()
	s, err := NewWebServer(WebServerOptions{DataDir: t.TempDir(), Username: "admin", Password: "correct horse", EmbeddedFS: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("test panel")}, "app.js": &fstest.MapFile{Data: []byte("test script")}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func webRequest(s *WebServer, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://panel.test"+path, strings.NewReader(body))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func webLogin(t *testing.T, s *WebServer) *http.Cookie {
	t.Helper()
	w := webRequest(s, http.MethodPost, "/api/auth/login", `{"username":"admin","password":"correct horse"}`, nil)
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies: %v", cookies)
	}
	return cookies[0]
}

func TestWebAuthenticationFullRouteMatrix(t *testing.T) {
	s := newWebTestServer(t)
	routes := []string{"/ws/core", "/ws/unknown", "/api/auth/session", "/api/auth/logout", "/api/fs/snapshot", "/api/fs/interfaces", "/api/fs/stat?path=/data", "/api/fs/read?path=/data/config.yaml", "/api/fs/write?path=/data/test", "/api/fs/mkdir?path=/data/dir", "/api/fs/delete?path=/data/dir", "/api/fs/rename", "/api/fs/copy", "/api/http-proxy", "/api/unknown"}
	for _, path := range routes {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
			w := webRequest(s, method, path, "{}", nil)
			if w.Code != 401 {
				t.Errorf("unauth %s %s = %d", method, path, w.Code)
			}
		}
	}
	for _, path := range []string{"/", "/app.js", "/assets/font.woff2", "/deep/link", "/favicon.ico"} {
		w := webRequest(s, "GET", path, "", nil)
		if w.Code != 303 || w.Header().Get("Location") != "/login" {
			t.Errorf("static auth %s = %d", path, w.Code)
		}
	}
	w := webRequest(s, "GET", "/login", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "登录") || !strings.Contains(w.Body.String(), "Username") || strings.Contains(w.Body.String(), "src=") {
		t.Fatal("self-contained bilingual login missing")
	}
	wrong := []string{`{"username":"admin","password":"wrong"}`, `{"username":"wrong","password":"correct horse"}`, `{}`, `bad JSON`, `{"username":"admin","password":"correct horse"} {}`}
	var errorBody string
	for _, body := range wrong {
		w := webRequest(s, "POST", "/api/auth/login", body, nil)
		if w.Code != 401 || len(w.Result().Cookies()) != 0 {
			t.Fatalf("wrong login = %d", w.Code)
		}
		if errorBody == "" {
			errorBody = w.Body.String()
		} else if w.Body.String() != errorBody {
			t.Fatal("login errors differ")
		}
	}
	cookie := webLogin(t, s)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Secure || cookie.MaxAge != 43200 {
		t.Fatalf("cookie flags: %+v", cookie)
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || len(raw) < 32 {
		t.Fatal("session token has insufficient entropy")
	}
	for _, path := range []string{"/api/auth/session", "/", "/app.js", "/deep/link", "/api/fs/snapshot"} {
		if w := webRequest(s, "GET", path, "", cookie); w.Code != 200 {
			t.Errorf("authenticated %s = %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := webRequest(s, "POST", "/api/auth/logout", "", cookie); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w := webRequest(s, "GET", "/api/auth/session", "", cookie); w.Code != 401 {
		t.Fatal("logged out session accepted")
	}
}

func TestWebSessionAbsoluteExpiryAndRestart(t *testing.T) {
	s := newWebTestServer(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	cookie := webLogin(t, s)
	now = now.Add(webSessionTTL - time.Second)
	if w := webRequest(s, "GET", "/api/auth/session", "", cookie); w.Code != 200 {
		t.Fatal(w.Code)
	}
	now = now.Add(time.Second)
	if w := webRequest(s, "GET", "/api/auth/session", "", cookie); w.Code != 401 {
		t.Fatal("absolute expiry was refreshed by access")
	}
	other := newWebTestServer(t)
	if w := webRequest(other, "GET", "/api/auth/session", "", cookie); w.Code != 401 {
		t.Fatal("new process accepted old session")
	}
}

func TestWebLoginRateLimitIsGloballyBounded(t *testing.T) {
	s := newWebTestServer(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	for i := 0; i < 1000; i++ {
		r := httptest.NewRequest("POST", "http://panel.test/api/auth/login", strings.NewReader(`{"username":"no","password":"no"}`))
		r.RemoteAddr = "192.0.2." + string(rune(i%250+1)) + ":1234"
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	if s.loginAttempts != 20 {
		t.Fatalf("unbounded limiter: %d", s.loginAttempts)
	}
	if w := webRequest(s, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse"}`, nil); w.Code != 401 {
		t.Fatal("limit bypassed")
	}
	now = now.Add(time.Minute)
	webLogin(t, s)
}

func TestWebOriginAndMethodPolicy(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	for _, path := range []string{"/login", "/api/auth/login", "/api/auth/logout", "/api/fs/write?path=/data/a", "/ws/core"} {
		r := httptest.NewRequest("POST", "http://panel.test"+path, strings.NewReader("{}"))
		r.AddCookie(cookie)
		r.Header.Set("Origin", "http://evil.test")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("cross-origin %s = %d", path, w.Code)
		}
	}
	for _, origin := range []string{"null", "http://panel.test/", "https://panel.test", "http://panel.test.evil", "http://panel.test@evil.test", "http://panel.test?query"} {
		r := httptest.NewRequest("GET", "http://panel.test/api/auth/session", nil)
		r.AddCookie(cookie)
		r.Header.Set("Origin", origin)
		r.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Errorf("bad origin %q accepted", origin)
		}
	}
	for _, path := range []string{"/api/auth/logout", "/api/fs/write?path=/data/a", "/api/fs/mkdir?path=/data/a", "/api/fs/delete?path=/data/a", "/api/fs/copy", "/api/fs/rename", "/api/http-proxy"} {
		if w := webRequest(s, "GET", path, "", cookie); w.Code != 405 {
			t.Errorf("GET mutation %s = %d", path, w.Code)
		}
	}
	for _, path := range []string{"/api/auth/session", "/api/fs/read?path=/data/a", "/api/fs/stat?path=/data", "/api/fs/snapshot", "/api/fs/interfaces", "/ws/core"} {
		if w := webRequest(s, "POST", path, "", cookie); w.Code != 405 {
			t.Errorf("POST read %s = %d", path, w.Code)
		}
	}
	s.opts.SecureCookie = true
	r := httptest.NewRequest("POST", "http://panel.test/api/auth/login", strings.NewReader(`{"username":"admin","password":"correct horse"}`))
	r.Header.Set("Origin", "https://panel.test")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 || !w.Result().Cookies()[0].Secure {
		t.Fatal("HTTPS reverse proxy origin/cookie rejected")
	}
}

func TestWebFilesVirtualRootsSymlinksAndProtocol(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	for _, p := range []string{"/etc/passwd", "/tmpx/file", "/data/../secret", "/tmp/../../secret", "relative", "/data/a\\b", "/data/.tmp/a"} {
		for _, op := range []string{"read", "write", "mkdir", "delete", "stat"} {
			method := "POST"
			if op == "read" || op == "stat" {
				method = "GET"
			}
			w := webRequest(s, method, "/api/fs/"+op+"?path="+url.QueryEscape(p), "test", cookie)
			if w.Code != 400 {
				t.Errorf("%s %s = %d", op, p, w.Code)
			}
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("OUTSIDE"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.data.Symlink(outside, "escape"); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"read", "write", "mkdir", "stat", "delete"} {
		method := "POST"
		if op == "read" || op == "stat" {
			method = "GET"
		}
		w := webRequest(s, method, "/api/fs/"+op+"?path=/data/escape/secret", "overwritten", cookie)
		if w.Code == 200 {
			t.Errorf("symlink %s succeeded", op)
		}
	}
	data, _ := os.ReadFile(filepath.Join(outside, "secret"))
	if string(data) != "OUTSIDE" {
		t.Fatal("outside file changed")
	}
	for _, p := range []string{"/data", "/tmp", "/tmp/.", "/data//"} {
		if w := webRequest(s, "POST", "/api/fs/delete?path="+url.QueryEscape(p), "", cookie); w.Code != 400 {
			t.Fatalf("deleted root %s", p)
		}
		for _, body := range []string{`{"from":"` + p + `","to":"/data/other"}`, `{"from":"/data/other","to":"` + p + `"}`} {
			if w := webRequest(s, "POST", "/api/fs/rename", body, cookie); w.Code != 400 {
				t.Fatalf("renamed root %s: %d", p, w.Code)
			}
		}
	}
	for _, p := range []string{"/data/nested/test.txt", "/tmp/test.txt"} {
		if w := webRequest(s, "POST", "/api/fs/write?path="+p, "hello", cookie); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		if w := webRequest(s, "GET", "/api/fs/read?path="+p, "", cookie); w.Code != 200 || w.Body.String() != "hello" || w.Header().Get("X-File-Mtime") == "" {
			t.Fatal("read protocol failed")
		}
		if w := webRequest(s, "GET", "/api/fs/stat?path="+p, "", cookie); w.Code != 200 || !strings.Contains(w.Body.String(), `"exists":true`) {
			t.Fatal("stat protocol failed")
		}
	}
	if w := webRequest(s, "POST", "/api/fs/write?path=/data/nested/test.txt&mode=append", " world", cookie); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := webRequest(s, "POST", "/api/fs/copy", `{"from":"/data/nested/test.txt","to":"/tmp/copy.txt"}`, cookie); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := webRequest(s, "POST", "/api/fs/rename", `{"from":"/tmp/copy.txt","to":"/data/moved.txt"}`, cookie); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := webRequest(s, "GET", "/api/fs/read?path=/data/moved.txt", "", cookie); w.Body.String() != "hello world" {
		t.Fatal(w.Body.String())
	}
	if w := webRequest(s, "GET", "/api/fs/snapshot", "", cookie); w.Code != 200 || strings.Contains(w.Body.String(), "OUTSIDE") {
		t.Fatal("snapshot leaked symlink")
	}
}

func TestWebStaticSymlinksAndPasswordFilePlacement(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	webDir := filepath.Join(base, "www")
	outside := filepath.Join(base, "password")
	os.MkdirAll(dataDir, 0700)
	os.MkdirAll(webDir, 0700)
	os.WriteFile(outside, []byte("secret\n"), 0600)
	os.WriteFile(filepath.Join(webDir, "index.html"), []byte("index"), 0600)
	os.Symlink(outside, filepath.Join(webDir, "leak"))
	for _, opts := range []WebServerOptions{{DataDir: dataDir, Password: "x"}, {DataDir: dataDir, Username: "admin"}, {DataDir: dataDir, Username: "admin", Password: "x", PasswordFile: outside}} {
		if server, err := NewWebServer(opts); err == nil {
			server.Close()
			t.Fatal("invalid credentials accepted")
		}
	}
	for _, dir := range []string{dataDir, webDir} {
		file := filepath.Join(dir, "pass")
		os.WriteFile(file, []byte("secret"), 0600)
		if server, err := NewWebServer(WebServerOptions{DataDir: dataDir, WebDir: webDir, Username: "admin", PasswordFile: file}); err == nil {
			server.Close()
			t.Fatal("public password file accepted")
		}
	}
	alias := filepath.Join(base, "alias")
	os.Symlink(filepath.Join(dataDir, "pass"), alias)
	if server, err := NewWebServer(WebServerOptions{DataDir: dataDir, Username: "admin", PasswordFile: alias}); err == nil {
		server.Close()
		t.Fatal("password file alias accepted")
	}
	s, err := NewWebServer(WebServerOptions{DataDir: dataDir, WebDir: webDir, Username: "admin", PasswordFile: outside})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w := webRequest(s, "POST", "/api/auth/login", `{"username":"admin","password":"secret"}`, nil)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	cookie := w.Result().Cookies()[0]
	if w := webRequest(s, "GET", "/leak", "", cookie); strings.Contains(w.Body.String(), "secret") {
		t.Fatal("static symlink escaped root")
	}
}

func webDial(t *testing.T, server *httptest.Server, cookie *http.Cookie, origin string) (net.Conn, *http.Response, error) {
	t.Helper()
	header := http.Header{}
	if cookie != nil {
		header.Set("Cookie", cookie.String())
	}
	if origin != "" {
		header.Set("Origin", origin)
	}
	dialer := ws.Dialer{Timeout: 3 * time.Second, Header: ws.HandshakeHeaderHTTP(header)}
	conn, _, _, err := dialer.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http")+"/ws/core")
	return conn, nil, err
}

func TestWebSocketAuthenticationOriginLogoutExpiryAndSize(t *testing.T) {
	s := newWebTestServer(t)
	server := httptest.NewServer(s)
	defer server.Close()
	if conn, _, err := webDial(t, server, nil, server.URL); err == nil {
		conn.Close()
		t.Fatal("unauthenticated websocket accepted")
	}
	cookie := webLogin(t, s)
	if conn, _, err := webDial(t, server, cookie, "http://evil.test"); err == nil {
		conn.Close()
		t.Fatal("cross-origin websocket accepted")
	}
	conn, _, err := webDial(t, server, cookie, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	payload := []byte(`{"id":"one","method":"core.shutdown"}`)
	if err := wsutil.WriteClientMessage(conn, ws.OpText, payload); err != nil {
		t.Fatal(err)
	}
	response, _, err := wsutil.ReadServerData(conn)
	if err != nil || !bytes.Contains(response, []byte(`"result":true`)) {
		t.Fatalf("RPC response: %s %v", response, err)
	}
	if w := webRequest(s, "POST", "/api/auth/logout", "", cookie); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if _, _, err := wsutil.ReadServerData(conn); err == nil {
		t.Fatal("logout did not close websocket")
	}
	cookie = webLogin(t, s)
	key := sha256.Sum256([]byte(cookie.Value))
	s.mu.Lock()
	s.sessions[key].expires = time.Now().Add(60 * time.Millisecond)
	s.mu.Unlock()
	conn, _, err = webDial(t, server, cookie, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := wsutil.ReadServerData(conn); err == nil {
		t.Fatal("absolute expiry did not close idle websocket")
	}
	cookie = webLogin(t, s)
	conn, _, err = webDial(t, server, cookie, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if err := ws.WriteHeader(conn, ws.Header{Fin: true, OpCode: ws.OpText, Masked: true, Length: webMaxWS + 1}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := wsutil.ReadServerData(conn); err == nil {
		t.Fatal("oversized websocket frame accepted")
	}
}

func TestWebProxyPreservesProtocolAndVerifiesTLS(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer subscription" {
			t.Error("upstream headers lost")
		}
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(201)
		io.WriteString(w, "download")
	}))
	defer upstream.Close()
	body, _ := json.Marshal(webProxyRequest{URL: upstream.URL, Headers: map[string]string{"Authorization": "Bearer subscription"}, SavePath: "/data/download"})
	w := webRequest(s, "POST", "/api/http-proxy", string(body), cookie)
	var response webProxyResponse
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || !response.OK || response.StatusCode != 201 || response.SavedSize != 8 || !response.InlinedBody {
		t.Fatal(w.Body.String())
	}
	if w := webRequest(s, "GET", "/api/fs/read?path=/data/download", "", cookie); w.Body.String() != "download" {
		t.Fatal(w.Body.String())
	}
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "insecure") }))
	defer tls.Close()
	body, _ = json.Marshal(webProxyRequest{URL: tls.URL})
	w = webRequest(s, "POST", "/api/http-proxy", string(body), cookie)
	response = webProxyResponse{}
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.OK || response.Error == "" {
		t.Fatal("untrusted upstream TLS certificate accepted")
	}
	for _, p := range []string{"/etc/pwn", "/tmpx/pwn", "/data/../pwn"} {
		body, _ = json.Marshal(webProxyRequest{URL: upstream.URL, SavePath: p})
		if w := webRequest(s, "POST", "/api/http-proxy", string(body), cookie); w.Code != 400 {
			t.Fatal("proxy path escape accepted")
		}
	}
}

func TestWebRPCFilePathsProfilesAndLifecycleIsolation(t *testing.T) {
	s := newWebTestServer(t)
	connMu.Lock()
	beforeConn := conn
	connMu.Unlock()
	beforeSpill := spillThreshold
	beforeInit := isInit.Load()
	beforeHome := constant.Path.HomeDir()
	profile := "proxies:\n  - name: direct\n    type: direct\nrules:\n  - MATCH,DIRECT\n"
	if err := s.data.WriteFile("profile.yaml", []byte(profile), 0600); err != nil {
		t.Fatal(err)
	}
	invoke := func(method CoreMethod, args any) MethodResponse {
		t.Helper()
		arguments, _ := json.Marshal(args)
		var result MethodResponse
		data := s.webRPC(&MethodCall{ID: "test", Method: method, Arguments: arguments})
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, method := range []CoreMethod{configReadMethod, configValidateMethod, configDecryptMethod, profileBaseMethod, profileOutboundsMethod, ruleSetDumpMethod, proxyConvertMethod} {
		for _, p := range []string{"/etc/passwd", "/data/../secret", "/tmpx/secret"} {
			if result := invoke(method, PathParams{Path: p}); result.Error == nil {
				t.Fatalf("RPC %s path escape accepted", method)
			}
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	os.WriteFile(outside, []byte(profile), 0600)
	s.data.Symlink(outside, "escape.yaml")
	for _, method := range []CoreMethod{configReadMethod, profileBaseMethod, profileBuildMethod} {
		args := any(PathParams{Path: "/data/escape.yaml"})
		if method == profileBuildMethod {
			args = SetupBase{Path: "/data/escape.yaml", Overlay: "{}"}
		}
		if result := invoke(method, args); result.Error == nil {
			t.Fatalf("RPC %s symlink escape accepted", method)
		}
	}
	for _, method := range []CoreMethod{configReadMethod, configValidateMethod, profileBaseMethod, profileOutboundsMethod} {
		if result := invoke(method, PathParams{Path: "/data/profile.yaml"}); result.Error != nil {
			t.Fatalf("RPC %s failed: %+v", method, result.Error)
		}
	}
	built := invoke(profileBuildMethod, SetupBase{Path: "/data/profile.yaml", Overlay: "mixed-port: 7890\nrules: []\n"})
	if built.Error != nil {
		t.Fatal(built.Error)
	}
	filename, ok := built.Result.(string)
	if !ok || !strings.HasPrefix(filename, "/tmp/") {
		t.Fatalf("build result %v", built.Result)
	}
	data, err := s.readRPCPath(filename)
	if err != nil || !bytes.Contains(data, []byte("MATCH,DIRECT")) {
		t.Fatalf("profile build %s %v", data, err)
	}
	if result := invoke(coreCrashMethod, nil); result.Error == nil {
		t.Fatal("crash RPC accepted")
	}
	if result := invoke("core.shutdown", nil); result.Error != nil || result.Result != true {
		t.Fatal(result)
	}
	connMu.Lock()
	afterConn := conn
	connMu.Unlock()
	if beforeConn != afterConn || spillThreshold != beforeSpill || isInit.Load() != beforeInit || constant.Path.HomeDir() != beforeHome {
		t.Fatal("web handler or RPC changed native lifecycle globals")
	}
	s.coreEnabled.Store(true)
	if result := invoke(coreInitMethod, InitParams{HomeDir: "/etc"}); result.Error == nil {
		t.Fatal("core.init escaped home")
	}
	if result := invoke(coreInitMethod, InitParams{HomeDir: "/data"}); result.Error == nil {
		t.Fatal("core.init accepted symlink data tree")
	}
	s.data.Remove("escape.yaml")
	defer func() { constant.SetHomeDir(beforeHome); isInit.Store(beforeInit) }()
	if result := invoke(coreInitMethod, InitParams{HomeDir: "/data", Version: 1}); result.Error != nil {
		t.Fatal(result.Error)
	}
	if constant.Path.HomeDir() != s.opts.DataDir {
		t.Fatal("web home not persistent DataDir")
	}
	if result := invoke(configUpdateMethod, UpdateParams{ExternalController: webStringPtr("127.0.0.1:9090")}); result.Error == nil || result.Error.Code != "unsupported" {
		t.Fatal("external controller RPC accepted")
	}
}

func webStringPtr(value string) *string { return &value }

func TestWebYAMLFilePathsAndControllerPolicy(t *testing.T) {
	s := newWebTestServer(t)
	for _, data := range []string{"external-controller: 127.0.0.1:9090", "external-controller-unix: /tmp/core.sock", "external-ui: /etc", "proxy-providers:\n  a:\n    type: file\n    path: /etc/passwd", "proxies:\n  - type: ssh\n    private-key: /etc/id_rsa", "proxies:\n  - type: tailscale\n    state-dir: /var/lib/state"} {
		if _, err := s.sanitizeConfig([]byte(data)); err == nil {
			t.Fatalf("unsafe YAML accepted: %s", data)
		}
	}
	s.data.WriteFile("provider.yaml", []byte("proxies: []"), 0600)
	for _, data := range []string{"proxy-providers:\n  a:\n    type: file\n    path: /data/provider.yaml", "proxy-providers:\n  a:\n    type: http\n    path: providers/a.yaml\n    url: https://example.test/sub", "proxies:\n  - type: vmess\n    ws-opts:\n      path: /ws", "proxies:\n  - type: wireguard\n    private-key: base64notafile", "script: browser extension must remain untouched"} {
		if _, err := s.sanitizeConfig([]byte(data)); err != nil {
			t.Fatalf("legitimate YAML rejected: %s: %v", data, err)
		}
	}
	s.data.Symlink("provider.yaml", "alias.yaml")
	if _, err := s.sanitizeConfig([]byte("proxy-providers:\n  a:\n    type: file\n    path: /data/alias.yaml")); err == nil {
		t.Fatal("native YAML symlink path accepted")
	}
}

func TestWebEventDeliveryBoundedAndNoGlobalSpillChange(t *testing.T) {
	s := newWebTestServer(t)
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	client := &webClient{conn: left, queue: make(chan []byte, 1), done: make(chan struct{})}
	key := [32]byte{1}
	s.sessions[key] = &webSession{expires: time.Now().Add(time.Hour), clients: map[*webClient]struct{}{client: {}}}
	sink := &webEventSink{server: s}
	frame := []byte(`{"method":"message","arguments":[]}`)
	if _, err := writeFrame(sink, frame); err != nil {
		t.Fatal(err)
	}
	if got := <-client.queue; !bytes.Equal(got, frame) {
		t.Fatal("event framing changed")
	}
	s.broadcast(frame)
	s.broadcast(frame)
	select {
	case <-client.done:
	default:
		t.Fatal("slow client did not close on bounded queue")
	}
	before := spillThreshold
	if _, err := sink.Write([]byte{255, 255, 255, 255}); err == nil {
		t.Fatal("unbounded sink frame accepted")
	}
	if spillThreshold != before {
		t.Fatal("global spill setting changed")
	}
}
