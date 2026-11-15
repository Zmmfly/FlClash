//go:build webpanel && !android

package core

// WEBPANEL.md login and localization are embedded only in the dedicated Web build.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func webExtractLoginMessages(t *testing.T, page []byte) map[string]map[string]string {
	t.Helper()
	prefix := []byte("const messages = ")
	start := bytes.Index(page, prefix)
	if start < 0 {
		t.Fatal("login message assignment missing")
	}
	start += len(prefix)
	var messages map[string]map[string]string
	if err := json.NewDecoder(bytes.NewReader(page[start:])).Decode(&messages); err != nil {
		t.Fatal(err)
	}
	return messages
}

func TestWebLoginRendererInjectsSevenLocalesWithoutCredentials(t *testing.T) {
	s, err := NewWebServer(WebServerOptions{DataDir: filepath.Join(t.TempDir(), "data"), Username: "private-username-marker", Password: "private-password-marker"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w := webRequest(s, "GET", "/login?updated=1", "", nil)
	page := w.Body.Bytes()
	if w.Code != 200 || bytes.Contains(page, []byte("__FLCLASH_LOGIN_MESSAGES__")) {
		t.Fatal("login template was not rendered")
	}
	messages := webExtractLoginMessages(t, page)
	if len(messages) != 7 || messages["en"]["webUsername"] != "Username" || messages["zh_CN"]["webUsername"] != "用户名" {
		t.Fatal("English/Chinese login messages missing")
	}
	for _, locale := range []string{"en", "zh_CN", "ja", "ru", "fr", "es", "ko"} {
		if messages[locale]["webCredentialsUpdated"] == "" || messages[locale]["webLoginAction"] == "" {
			t.Fatalf("locale %s incomplete", locale)
		}
	}
	for _, secret := range []string{"private-username-marker", "private-password-marker", s.auth.record.Hash, s.auth.record.Salt, s.opts.AuthFile} {
		if bytes.Contains(page, []byte(secret)) {
			t.Fatal("login page disclosed credential material")
		}
	}
	if strings.Contains(w.Body.String(), "src=\"") || strings.Contains(w.Body.String(), "href=\"") {
		t.Fatal("login page depends on protected app assets")
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatal("login CSP missing")
	}
}

func TestWebLoginRendererJSONEscapesHTMLAndRejectsIncompleteAssets(t *testing.T) {
	danger := "</script><script>alert('unsafe')</script>&\u2028\u2029"
	assets := fstest.MapFS{"webui/login.html": &fstest.MapFile{Data: []byte("<script>const messages = __FLCLASH_LOGIN_MESSAGES__;</script>")}}
	for _, locale := range []string{"en", "zh_CN", "ja", "ru", "fr", "es", "ko"} {
		data, _ := json.Marshal(map[string]string{"webUsername": danger})
		assets["webui/intl_"+locale+".arb"] = &fstest.MapFile{Data: data}
	}
	page, err := renderWebLoginFS(assets)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(page, []byte("</script>")) != 1 || !bytes.Contains(page, []byte(`\u003c/script\u003e`)) || !bytes.Contains(page, []byte(`\u2028`)) {
		t.Fatal("messages could escape their script context")
	}
	if messages := webExtractLoginMessages(t, page); messages["en"]["webUsername"] != danger {
		t.Fatal("safe JSON encoding changed translation content")
	}
	delete(assets, "webui/intl_ko.arb")
	if _, err := renderWebLoginFS(assets); err == nil {
		t.Fatal("missing locale accepted")
	}
	assets["webui/login.html"] = &fstest.MapFile{Data: []byte("__FLCLASH_LOGIN_MESSAGES____FLCLASH_LOGIN_MESSAGES__")}
	if _, err := renderWebLoginFS(assets); err == nil {
		t.Fatal("ambiguous message injection accepted")
	}
}

func TestWebNativeGoAndAndroidExcludeWebResources(t *testing.T) {
	for _, android := range []bool{false, true} {
		args := []string{"list", "-json", "."}
		if android {
			args = []string{"list", "-tags=webpanel,with_gvisor", "-json", "."}
		}
		command := exec.Command("go", args...)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "GOFLAGS=") && !strings.HasPrefix(entry, "GOOS=") && !strings.HasPrefix(entry, "GOARCH=") && !strings.HasPrefix(entry, "CGO_ENABLED=") {
				command.Env = append(command.Env, entry)
			}
		}
		command.Env = append(command.Env, "GOFLAGS=", "CGO_ENABLED=0")
		if android {
			command.Env = append(command.Env, "GOOS=android", "GOARCH=arm64")
		}
		output, err := command.Output()
		if err != nil {
			t.Fatal(err)
		}
		var result struct{ GoFiles, EmbedFiles, IgnoredGoFiles []string }
		if json.Unmarshal(output, &result) != nil {
			t.Fatal("invalid Go build inventory")
		}
		for _, name := range result.GoFiles {
			if strings.HasPrefix(name, "web_") {
				t.Fatalf("native build included %s", name)
			}
		}
		for _, name := range result.EmbedFiles {
			if strings.HasPrefix(name, "webui/") {
				t.Fatalf("native build embedded %s", name)
			}
		}
		ignored := false
		for _, name := range result.IgnoredGoFiles {
			if name == "web_login.go" {
				ignored = true
			}
		}
		if !ignored {
			t.Fatal("Web renderer build tag not observed")
		}
	}
}
