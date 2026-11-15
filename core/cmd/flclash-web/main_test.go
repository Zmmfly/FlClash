//go:build webpanel && !android

package main

// WEBPANEL.md deployment options use this same command parser.

import (
	"core"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func webEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestWebCommandRequiresCredentialsAndDefaultsToLoopback(t *testing.T) {
	for _, env := range []map[string]string{{}, {"FLCLASH_WEB_USERNAME": "admin"}, {"FLCLASH_WEB_PASSWORD": "secret"}, {"FLCLASH_WEB_USERNAME": "admin", "FLCLASH_WEB_PASSWORD": "secret", "FLCLASH_WEB_PASSWORD_FILE": "/private/password"}} {
		opts, _, err := parseOptions(nil, webEnv(env), io.Discard)
		if err == nil {
			opts.DataDir = t.TempDir()
			server, authErr := core.NewWebServer(opts)
			err = authErr
			if server != nil {
				server.Close()
			}
		}
		if err == nil {
			t.Fatal("missing/conflicting credentials accepted")
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("password appears in startup error")
		}
	}
	opts, _, err := parseOptions(nil, webEnv(map[string]string{"FLCLASH_WEB_USERNAME": "admin", "FLCLASH_WEB_PASSWORD": "secret"}), io.Discard)
	if err != nil || opts.ListenAddr != "127.0.0.1:8080" || opts.SecureCookie {
		t.Fatalf("defaults: %+v %v", opts, err)
	}
	if opts.EmbeddedFS == nil {
		t.Fatal("embedded staging assets missing")
	}
}

func TestWebCommandEnvironmentAndFlags(t *testing.T) {
	opts, _, err := parseOptions([]string{"--auth-file", "/private/credentials.json", "--username", "flag-admin", "--password-file", "/private/flag-password", "--secure-cookie", "--listen", "127.0.0.1:8081"}, webEnv(map[string]string{"FLCLASH_WEB_USERNAME": "env-admin", "FLCLASH_WEB_PASSWORD_FILE": "/private/env-password", "FLCLASH_WEB_LISTEN": "127.0.0.1:9999", "FLCLASH_DATA_DIR": "/data/custom", "FLCLASH_WEB_DIR": "/www"}), io.Discard)
	if err != nil || opts.AuthFile != "/private/credentials.json" || opts.Username != "flag-admin" || opts.PasswordFile != "/private/flag-password" || !opts.SecureCookie || opts.ListenAddr != "127.0.0.1:8081" || opts.DataDir != "/data/custom" || opts.WebDir != "/www" {
		t.Fatalf("options: %+v %v", opts, err)
	}
	if _, _, err := parseOptions([]string{"--password", "not-allowed"}, webEnv(nil), io.Discard); err == nil {
		t.Fatal("plaintext password flag accepted")
	}
	if _, _, err := parseOptions(nil, webEnv(map[string]string{"FLCLASH_WEB_SECURE_COOKIE": "not-bool"}), io.Discard); err == nil {
		t.Fatal("invalid secure cookie boolean accepted")
	}
	_, version, err := parseOptions([]string{"--version"}, webEnv(nil), io.Discard)
	if err != nil || !version {
		t.Fatal("version requires credentials")
	}
}

func TestWebCommandStoredAuthNeedsNoBootstrapEnvironment(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	authFile := filepath.Join(base, "auth", "credentials.json")
	opts, _, err := parseOptions([]string{"--data-dir", dataDir, "--auth-file", authFile, "--username", "bootstrap"}, webEnv(map[string]string{"FLCLASH_WEB_PASSWORD": "bootstrap password"}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	server, err := core.NewWebServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	opts, _, err = parseOptions(nil, webEnv(map[string]string{"FLCLASH_DATA_DIR": dataDir, "FLCLASH_WEB_AUTH_FILE": authFile}), io.Discard)
	if err != nil || opts.AuthFile != authFile {
		t.Fatal("auth-file environment was not parsed")
	}
	server, err = core.NewWebServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	request := httptest.NewRequest("POST", "http://panel.test/api/auth/login", strings.NewReader(`{"username":"bootstrap","password":"bootstrap password"}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatal("stored command credentials did not authenticate")
	}
}
