//go:build webpanel && !android

package core

// WEBPANEL.md persistent credentials fail closed and distinguish atomic publication from durability.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebBootstrapPasswordMustRemainJSONLoginCompatible(t *testing.T) {
	for _, password := range []string{"", string([]byte{0xff}), strings.Repeat("x", 257), strings.Repeat("x", 9000), strings.Repeat("密", 257), "valid\npassword", "valid\rpassword"} {
		base := t.TempDir()
		filename := filepath.Join(base, "credentials.json")
		server, err := NewWebServer(WebServerOptions{DataDir: filepath.Join(base, "data"), AuthFile: filename, Username: "admin", Password: password})
		if server != nil {
			server.Close()
		}
		if err == nil {
			t.Fatal("unusable bootstrap password accepted")
		}
		if _, err := os.Stat(filename); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("unusable bootstrap credentials were persisted")
		}
		if strings.Contains(errString(err), "\xff") || strings.Contains(errString(err), strings.Repeat("x", 257)) {
			t.Fatal("bootstrap error disclosed password")
		}
	}
	for _, password := range []string{"x", strings.Repeat("\x01", 128) + strings.Repeat("密", 128)} {
		server, err := NewWebServer(WebServerOptions{DataDir: filepath.Join(t.TempDir(), "data"), Username: "admin", Password: password})
		if err != nil {
			t.Fatal(err)
		}
		webLoginAs(t, server, "admin", password)
		server.Close()
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestWebAuthFileRejectsFinalSymlinksButAllowsPrivateParentAlias(t *testing.T) {
	base := t.TempDir()
	opts := WebServerOptions{DataDir: filepath.Join(base, "data"), AuthFile: filepath.Join(base, "private", "credentials.json"), Username: "admin", Password: "bootstrap password"}
	server, err := NewWebServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	for _, target := range []string{opts.AuthFile, filepath.Join(base, "missing.json"), filepath.Join(opts.DataDir, "exposed.json")} {
		alias := filepath.Join(t.TempDir(), "store.json")
		if err := os.Symlink(target, alias); err != nil {
			t.Fatal(err)
		}
		aliased := opts
		aliased.AuthFile = alias
		server, err := NewWebServer(aliased)
		if server != nil {
			server.Close()
		}
		if err == nil {
			t.Fatal("final credential symlink accepted")
		}
	}
	parentAlias := filepath.Join(base, "private-alias")
	os.Symlink(filepath.Dir(opts.AuthFile), parentAlias)
	opts.AuthFile = filepath.Join(parentAlias, "credentials.json")
	server, err = NewWebServer(opts)
	if err != nil {
		t.Fatal("safe private parent alias rejected:", err)
	}
	server.Close()
}

func TestWebCredentialPublicationFailureBoundaries(t *testing.T) {
	t.Run("file sync before publish", func(t *testing.T) {
		s := newWebTestServer(t)
		cookie := webLogin(t, s)
		before, _ := os.ReadFile(s.opts.AuthFile)
		snapshot := s.auth
		s.syncAuthFile = func(*os.File) error { return errors.New("injected file sync failure") }
		webAssertAuthError(t, webChangeRequest(s, cookie, "correct horse", "new-admin", "new password"), 500, "credentials_save_failed")
		after, _ := os.ReadFile(s.opts.AuthFile)
		if !bytes.Equal(before, after) || s.auth != snapshot {
			t.Fatal("pre-publication failure changed visible credentials")
		}
		if w := webRequest(s, "GET", "/api/auth/session", "", cookie); w.Code != 200 {
			t.Fatal("pre-publication failure revoked session")
		}
		files, _ := filepath.Glob(filepath.Join(filepath.Dir(s.opts.AuthFile), ".credentials-*.tmp"))
		if len(files) != 0 {
			t.Fatal("failed transaction left temporary credential files")
		}
	})
	t.Run("directory sync after publish", func(t *testing.T) {
		s := newWebTestServer(t)
		cookie := webLogin(t, s)
		s.syncAuthDirectory = func(*os.Root) error { return errors.New("injected directory sync failure") }
		w := webChangeRequest(s, cookie, "correct horse", "published-admin", "published password")
		var result struct {
			OK      bool   `json:"ok"`
			Warning string `json:"warning"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.OK || result.Warning != "durability_unconfirmed" {
			t.Fatalf("post-publication result: %d %s", w.Code, w.Body.String())
		}
		if s.auth.record.Username != "published-admin" {
			t.Fatal("post-publication memory retained old credentials")
		}
		if w := webRequest(s, "GET", "/api/auth/session", "", cookie); w.Code != 401 {
			t.Fatal("post-publication old session remained valid")
		}
		webLoginAs(t, s, "published-admin", "published password")
		restarted, err := NewWebServer(WebServerOptions{DataDir: s.opts.DataDir, AuthFile: s.opts.AuthFile})
		if err != nil {
			t.Fatal(err)
		}
		defer restarted.Close()
		webLoginAs(t, restarted, "published-admin", "published password")
	})
}

func TestWebCredentialInitializationSyncFailureNeverOverwritesPublishedStore(t *testing.T) {
	for _, afterPublish := range []bool{false, true} {
		base := t.TempDir()
		opts := WebServerOptions{DataDir: filepath.Join(base, "data"), AuthFile: filepath.Join(base, "auth", "credentials.json"), Username: "initial-admin", Password: "initial password"}
		if err := os.MkdirAll(opts.DataDir, 0700); err != nil {
			t.Fatal(err)
		}
		server := &WebServer{opts: opts, derivePassword: webDerivePassword}
		if afterPublish {
			server.syncAuthDirectory = func(*os.Root) error { return errors.New("injected directory sync failure") }
		} else {
			server.syncAuthFile = func(*os.File) error { return errors.New("injected file sync failure") }
		}
		if err := server.initializeCredentials(opts); err == nil {
			t.Fatal("initialization failed to report injected sync failure")
		}
		server.Close()
		_, statErr := os.Stat(opts.AuthFile)
		if afterPublish && statErr != nil {
			t.Fatal("published store was removed after failed durability confirmation")
		}
		if !afterPublish && !errors.Is(statErr, os.ErrNotExist) {
			t.Fatal("failed pre-publication bootstrap created a store")
		}
		opts.Username = "replacement-admin"
		opts.Password = "replacement password"
		restarted, err := NewWebServer(opts)
		if err != nil {
			t.Fatal(err)
		}
		if afterPublish {
			webLoginAs(t, restarted, "initial-admin", "initial password")
		} else {
			webLoginAs(t, restarted, "replacement-admin", "replacement password")
		}
		restarted.Close()
	}
}

func TestWebBootstrapUsernameRejectsLineBreaksBeforeTrimming(t *testing.T) {
	for _, username := range []string{"first\nsecond", "first\rsecond", "\nadmin", "admin\r\n"} {
		base := t.TempDir()
		filename := filepath.Join(base, "credentials.json")
		server, err := NewWebServer(WebServerOptions{DataDir: filepath.Join(base, "data"), AuthFile: filename, Username: username, Password: "valid password"})
		if server != nil {
			server.Close()
		}
		if err == nil {
			t.Fatal("bootstrap username with line breaks accepted")
		}
		if _, err := os.Stat(filename); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("invalid bootstrap username persisted")
		}
	}
}
