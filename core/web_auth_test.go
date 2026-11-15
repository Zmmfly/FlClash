//go:build webpanel && !android

package core

// WEBPANEL.md account management must persist securely and invalidate every old session.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func webLoginAs(t *testing.T, s *WebServer, username, password string) *http.Cookie {
	t.Helper()
	data, _ := json.Marshal(map[string]string{"username": username, "password": password})
	w := webRequest(s, "POST", "/api/auth/login", string(data), nil)
	if w.Code != 200 {
		t.Fatalf("login status %d", w.Code)
	}
	return w.Result().Cookies()[0]
}

func webChangeRequest(s *WebServer, cookie *http.Cookie, current, username, password string) *httptest.ResponseRecorder {
	data, _ := json.Marshal(webCredentialChange{CurrentPassword: current, Username: username, NewPassword: password})
	return webRequest(s, "POST", "/api/auth/credentials", string(data), cookie)
}

func webAssertAuthError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var response map[string]string
	if w.Code != status || json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response) != 1 || response["error"] != code {
		t.Fatalf("auth error %d %s", w.Code, w.Body.String())
	}
}

func TestWebCredentialsUsernamePasswordAndCombinedUpdates(t *testing.T) {
	for _, test := range []struct{ name, username, password, expectedUsername, expectedPassword string }{
		{"username", "  alice  ", "", "alice", "correct horse"},
		{"password", "admin", "  new strong 密码  ", "admin", "  new strong 密码  "},
		{"both", "新管理员", "eight888", "新管理员", "eight888"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newWebTestServer(t)
			first := webLogin(t, s)
			second := webLogin(t, s)
			server := httptest.NewServer(s)
			defer server.Close()
			connection, _, err := webDial(t, server, second, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			connection.SetDeadline(time.Now().Add(3 * time.Second))
			before, err := os.ReadFile(s.opts.AuthFile)
			if err != nil {
				t.Fatal(err)
			}
			w := webChangeRequest(s, first, "correct horse", test.username, test.password)
			if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"ok":true}` {
				t.Fatalf("credential update: %d %s", w.Code, w.Body.String())
			}
			cookies := w.Result().Cookies()
			if len(cookies) != 1 || cookies[0].MaxAge != -1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
				t.Fatal("successful change did not clear session cookie")
			}
			for _, cookie := range []*http.Cookie{first, second} {
				if response := webRequest(s, "GET", "/api/auth/session", "", cookie); response.Code != 401 {
					t.Fatal("old session remained valid")
				}
			}
			buffer := make([]byte, 1)
			if _, err := connection.Read(buffer); err == nil {
				t.Fatal("old websocket remained connected")
			}
			oldLogin := webRequest(s, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse"}`, nil)
			if oldLogin.Code != 401 {
				t.Fatal("old credentials accepted")
			}
			fresh := webLoginAs(t, s, test.expectedUsername, test.expectedPassword)
			var session struct {
				OK       bool   `json:"ok"`
				Username string `json:"username"`
			}
			response := webRequest(s, "GET", "/api/auth/session", "", fresh)
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &session) != nil || !session.OK || session.Username != test.expectedUsername {
				t.Fatal("session did not report current username")
			}
			after, err := os.ReadFile(s.opts.AuthFile)
			if err != nil || bytes.Equal(before, after) || bytes.Contains(after, []byte("correct horse")) || (test.password != "" && bytes.Contains(after, []byte(test.password))) {
				t.Fatal("credential store was unchanged or contained plaintext")
			}
			info, err := os.Stat(s.opts.AuthFile)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("credential store permissions are not 0600")
			}
			var record map[string]json.RawMessage
			if json.Unmarshal(after, &record) != nil || len(record) != 4 {
				t.Fatal("credential store has unexpected fields")
			}
			if !s.passwordMatches(s.auth, test.expectedPassword) {
				t.Fatal("stored KDF does not match new password")
			}
		})
	}
}

func TestWebCredentialsInputErrorsOriginAndRateLimit(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	beforeStore, _ := os.ReadFile(s.opts.AuthFile)
	if w := webChangeRequest(s, nil, "correct horse", "new-admin", "new password"); w.Code != 401 {
		t.Fatal("unauthenticated credential update accepted")
	}
	body := `{"currentPassword":"correct horse","username":"new-admin","newPassword":"new password"}`
	r := httptest.NewRequest("POST", "http://panel.test/api/auth/credentials", strings.NewReader(body))
	r.AddCookie(cookie)
	r.Header.Set("Origin", "http://evil.test")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("cross-origin credential update accepted")
	}
	if w := webRequest(s, "GET", "/api/auth/credentials", "", cookie); w.Code != 405 {
		t.Fatal("credential update allowed GET")
	}
	webAssertAuthError(t, webChangeRequest(s, cookie, "wrong", "admin", "new password"), 403, "invalid_current_password")
	for _, test := range []struct{ username, password string }{{"", "new password"}, {" \t\n", "new password"}, {strings.Repeat("用", 65), "new password"}, {"admin", "short"}, {"admin", strings.Repeat("密", 257)}, {"admin", strings.Repeat(" ", 8)}, {"first\nsecond", ""}, {"\nadmin", ""}, {"admin", "valid\npassword"}, {"admin", "valid\rpassword"}} {
		s.credentialsAttempts = 0
		webAssertAuthError(t, webChangeRequest(s, cookie, "correct horse", test.username, test.password), 400, "invalid_credentials")
	}
	for _, data := range []string{`{`, `{}`, body + ` {}`, `{"username":"admin","unknown":true}`} {
		s.credentialsAttempts = 0
		webAssertAuthError(t, webRequest(s, "POST", "/api/auth/credentials", data, cookie), 400, "invalid_credentials")
	}
	if w := webRequest(s, "GET", "/api/auth/session", "", cookie); w.Code != 200 {
		t.Fatal("invalid requests revoked existing session")
	}
	afterStore, _ := os.ReadFile(s.opts.AuthFile)
	if !bytes.Equal(beforeStore, afterStore) || s.auth.record.Username != "admin" {
		t.Fatal("invalid input changed old credentials")
	}
	s.mu.Lock()
	s.credentialsAttempts = 0
	s.credentialsWindow = s.now()
	s.mu.Unlock()
	for i := 0; i < 10; i++ {
		webAssertAuthError(t, webChangeRequest(s, cookie, "wrong", "admin", ""), 403, "invalid_current_password")
	}
	webAssertAuthError(t, webChangeRequest(s, cookie, "correct horse", "admin", "new password"), 429, "rate_limited")
	if s.credentialsAttempts != 10 {
		t.Fatal("credential retry limiter grew without a bound")
	}
}

func TestWebCredentialsUnicodeLengthAndWhitespacePreservation(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	username := strings.Repeat("🦊", 64)
	password := strings.Repeat("密", 256)
	if w := webChangeRequest(s, cookie, "correct horse", username, password); w.Code != 200 {
		t.Fatalf("Unicode limits rejected: %d", w.Code)
	}
	cookie = webLoginAs(t, s, username, password)
	if w := webChangeRequest(s, cookie, password, username, "  meaningful password  "); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	webLoginAs(t, s, username, "  meaningful password  ")
	data, _ := json.Marshal(map[string]string{"username": username, "password": "meaningful password"})
	if w := webRequest(s, "POST", "/api/auth/login", string(data), nil); w.Code != 401 {
		t.Fatal("meaningful leading/trailing password spaces were trimmed")
	}
}

func TestWebCredentialsPersistAndOverrideBootstrapOnRestart(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	if s.opts.AuthFile != s.opts.DataDir+".auth.json" || webWithin(s.opts.DataDir, s.opts.AuthFile) {
		t.Fatal("default store is not an external sidecar")
	}
	if w := webChangeRequest(s, cookie, "correct horse", "persisted-admin", "persisted password"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	opts := WebServerOptions{DataDir: s.opts.DataDir, AuthFile: s.opts.AuthFile, Username: "env-admin", Password: "env-password", PasswordFile: filepath.Join(t.TempDir(), "removed-bootstrap-file")}
	s.Close()
	restarted, err := NewWebServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	webLoginAs(t, restarted, "persisted-admin", "persisted password")
	if w := webRequest(restarted, "GET", "/api/auth/session", "", cookie); w.Code != 401 {
		t.Fatal("restart accepted old session")
	}
	if w := webRequest(restarted, "POST", "/api/auth/login", `{"username":"env-admin","password":"env-password"}`, nil); w.Code != 401 {
		t.Fatal("bootstrap environment replaced persisted credentials")
	}
	restarted.Close()
	withoutBootstrap, err := NewWebServer(WebServerOptions{DataDir: opts.DataDir, AuthFile: opts.AuthFile})
	if err != nil {
		t.Fatal(err)
	}
	withoutBootstrap.Close()
}

func TestWebCredentialsSaveFailurePreservesOldStoreAndSessions(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	before, _ := os.ReadFile(s.opts.AuthFile)
	s.authRoot.Close()
	webAssertAuthError(t, webChangeRequest(s, cookie, "correct horse", "new-admin", "new password"), 500, "credentials_save_failed")
	after, _ := os.ReadFile(s.opts.AuthFile)
	if !bytes.Equal(before, after) {
		t.Fatal("failed save changed persistent credentials")
	}
	if w := webRequest(s, "GET", "/api/auth/session", "", cookie); w.Code != 200 {
		t.Fatal("failed save revoked old session")
	}
	webLogin(t, s)
	if s.auth.record.Username != "admin" {
		t.Fatal("failed save changed in-memory credentials")
	}
}

func TestWebCredentialsBadStoreFailsClosed(t *testing.T) {
	for _, kind := range []string{"truncated", "version", "salt", "hash", "salt_format", "hash_format", "missing", "unknown", "duplicate", "permissions", "directory"} {
		t.Run(kind, func(t *testing.T) {
			s := newWebTestServer(t)
			filename := s.opts.AuthFile
			dataDir := s.opts.DataDir
			record := s.auth.record
			s.Close()
			var data []byte
			switch kind {
			case "truncated":
				data = []byte(`{"version":1`)
			case "version":
				record.Version = 99
				data, _ = json.Marshal(record)
			case "salt":
				record.Salt = "bad"
				data, _ = json.Marshal(record)
			case "hash":
				record.Hash = base64.RawStdEncoding.EncodeToString([]byte("plain"))
				data, _ = json.Marshal(record)
			case "salt_format":
				record.Salt += "\n"
				data, _ = json.Marshal(record)
			case "hash_format":
				record.Hash += "\r\n"
				data, _ = json.Marshal(record)
			case "missing":
				data = []byte(`{"version":1,"username":"admin","salt":"bad"}`)
			case "unknown":
				data, _ = json.Marshal(record)
				data = append(data[:len(data)-1], []byte(`,"password":"must-not-be-accepted"}`)...)
			case "duplicate":
				data, _ = json.Marshal(record)
				data = append(data[:len(data)-1], []byte(`,"username":"other"}`)...)
			case "permissions":
				os.Chmod(filename, 0644)
			case "directory":
				os.Remove(filename)
				os.Mkdir(filename, 0700)
			}
			if data != nil {
				if err := os.WriteFile(filename, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			server, err := NewWebServer(WebServerOptions{DataDir: dataDir, Username: "fallback", Password: "fallback password"})
			if server != nil {
				server.Close()
			}
			if err == nil {
				t.Fatal("bad store silently reinitialized from bootstrap")
			}
		})
	}
}

func TestWebCredentialsPrivatePathsAndInitializationRace(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	webDir := filepath.Join(base, "www")
	os.MkdirAll(dataDir, 0700)
	os.MkdirAll(webDir, 0700)
	dataAlias := filepath.Join(base, "data-alias")
	webAlias := filepath.Join(base, "web-alias")
	os.Symlink(dataDir, dataAlias)
	os.Symlink(webDir, webAlias)
	for _, filename := range []string{filepath.Join(dataDir, "credentials.json"), filepath.Join(webDir, "credentials.json"), filepath.Join(dataAlias, "nested", "credentials.json"), filepath.Join(webAlias, "credentials.json")} {
		server, err := NewWebServer(WebServerOptions{DataDir: dataDir, WebDir: webDir, AuthFile: filename, Username: "admin", Password: "bootstrap password"})
		if server != nil {
			server.Close()
		}
		if err == nil {
			t.Fatal("public credential store path accepted")
		}
	}
	filename := filepath.Join(base, "private", "credentials.json")
	type initialized struct {
		server *WebServer
		err    error
	}
	results := make(chan initialized, 2)
	for _, username := range []string{"first", "second"} {
		go func(username string) {
			server, err := NewWebServer(WebServerOptions{DataDir: dataDir, AuthFile: filename, Username: username, Password: username + " password"})
			results <- initialized{server, err}
		}(username)
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent initialization: %v %v", first.err, second.err)
	}
	defer first.server.Close()
	defer second.server.Close()
	if first.server.auth.record != second.server.auth.record {
		t.Fatal("concurrent constructors overwrote credential store")
	}
	winner := first.server.auth.record.Username
	webLoginAs(t, first.server, winner, winner+" password")
	webLoginAs(t, second.server, winner, winner+" password")
}

func TestWebConcurrentCredentialChangesAndLoginSnapshot(t *testing.T) {
	t.Run("concurrent updates", func(t *testing.T) {
		s := newWebTestServer(t)
		first := webLogin(t, s)
		second := webLogin(t, s)
		started, release := make(chan struct{}), make(chan struct{})
		var blocked atomic.Bool
		s.derivePassword = func(password string, salt []byte) []byte {
			if password == "correct horse" && blocked.CompareAndSwap(false, true) {
				close(started)
				<-release
			}
			return webDerivePassword(password, salt)
		}
		firstResult := make(chan *httptest.ResponseRecorder, 1)
		go func() { firstResult <- webChangeRequest(s, first, "correct horse", "winner", "winner password") }()
		<-started
		if w := webRequest(s, "GET", "/api/auth/session", "", second); w.Code != 200 {
			t.Fatal("slow hash held global session lock")
		}
		if w := webRequest(s, "GET", "/api/fs/stat?path=/data", "", second); w.Code != 200 {
			t.Fatal("slow hash blocked filesystem requests")
		}
		secondResult := make(chan *httptest.ResponseRecorder, 1)
		go func() { secondResult <- webChangeRequest(s, second, "correct horse", "loser", "loser password") }()
		close(release)
		if response := <-firstResult; response.Code != 200 {
			t.Fatal(response.Body.String())
		}
		if response := <-secondResult; response.Code != 401 {
			t.Fatalf("concurrent stale session accepted: %d", response.Code)
		}
		webLoginAs(t, s, "winner", "winner password")
	})
	t.Run("login old snapshot", func(t *testing.T) {
		s := newWebTestServer(t)
		cookie := webLogin(t, s)
		started, release := make(chan struct{}), make(chan struct{})
		var blocked atomic.Bool
		s.derivePassword = func(password string, salt []byte) []byte {
			if password == "correct horse" && blocked.CompareAndSwap(false, true) {
				close(started)
				<-release
			}
			return webDerivePassword(password, salt)
		}
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			result <- webRequest(s, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse"}`, nil)
		}()
		<-started
		if w := webChangeRequest(s, cookie, "correct horse", "renamed-admin", ""); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		close(release)
		response := <-result
		if response.Code != 401 || len(response.Result().Cookies()) != 0 {
			t.Fatal("login minted a valid session from an old credential snapshot")
		}
		webLoginAs(t, s, "renamed-admin", "correct horse")
	})
	t.Run("logout during verification", func(t *testing.T) {
		s := newWebTestServer(t)
		cookie := webLogin(t, s)
		before, _ := os.ReadFile(s.opts.AuthFile)
		started, release := make(chan struct{}), make(chan struct{})
		var blocked atomic.Bool
		s.derivePassword = func(password string, salt []byte) []byte {
			if blocked.CompareAndSwap(false, true) {
				close(started)
				<-release
			}
			return webDerivePassword(password, salt)
		}
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() { result <- webChangeRequest(s, cookie, "correct horse", "new-admin", "new password") }()
		<-started
		if w := webRequest(s, "POST", "/api/auth/logout", "", cookie); w.Code != 204 {
			t.Fatal(w.Code)
		}
		close(release)
		if w := <-result; w.Code != 401 {
			t.Fatal("revoked session changed credentials")
		}
		after, _ := os.ReadFile(s.opts.AuthFile)
		if !bytes.Equal(before, after) {
			t.Fatal("revoked session persisted credentials")
		}
	})
}

func TestWebCredentialsHashUsesSaltedPBKDF2(t *testing.T) {
	s := newWebTestServer(t)
	if s.auth.record.Version != 1 || len(s.auth.salt) != 32 || len(s.auth.hash) != 32 || webPasswordIterations != 600000 {
		t.Fatal("unexpected persisted KDF profile")
	}
	plain := sha256.Sum256([]byte("correct horse"))
	if bytes.Equal(s.auth.hash, plain[:]) || bytes.Equal(s.auth.hash, []byte("correct horse")) {
		t.Fatal("password was stored with a fast/plain hash")
	}
	other := newWebTestServer(t)
	if bytes.Equal(s.auth.salt, other.auth.salt) || bytes.Equal(s.auth.hash, other.auth.hash) {
		t.Fatal("password KDF salt was reused")
	}
}
