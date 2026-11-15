//go:build webpanel && !android

package core

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebAPIStatefulContractPrivateModesAndTraversalDenial(t *testing.T) {
	f := newWebStatefulFixture(t)
	webStatefulTestHome(t, f.opts.DataDir)
	bound := f.bind(webStatefulTestPlan(t, f.profile.ID, webStatefulTestDefinition("tailscale")))
	manager, _ := NewWebAPIStatefulManager(f.opts.DataDir)
	defer manager.Close()
	paths, err := manager.Materialize(bound)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(f.opts.DataDir, webStatefulDirectory), filepath.Join(f.opts.DataDir, webStatefulDirectory, webStatefulVersion), paths[0].StateDir} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("private path has wrong mode: %v %v", info, err)
		}
	}
	file := filepath.Join(paths[0].StateDir, "synthetic-identity")
	if err := os.WriteFile(file, []byte("synthetic-state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("synthetic private file mode")
	}
	// Native consumers may create 0644 files; the immutable 0700 identity root protects them.
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Materialize(bound); err != nil {
		t.Fatal("private-root-protected native state rejected", err)
	}
	for _, identity := range []string{"../escape", "/tmp/escape", "..", "a/b", "C:\\outside"} {
		malicious := bound
		malicious.Bindings = append([]WebAPIStatefulBinding(nil), bound.Bindings...)
		malicious.Bindings[0].Identity = identity
		_, err := manager.Materialize(malicious)
		webAPITestErrorCode(t, err, "stateful_ledger_invalid")
	}
}

func TestWebAPIStatefulContractRejectLinksAndUnsafeRoots(t *testing.T) {
	for _, scenario := range []string{"private-symlink", "version-symlink", "identity-symlink", "file-symlink", "directory-symlink", "file-hardlink", "identity-mode", "private-mode", "data-symlink"} {
		t.Run(scenario, func(t *testing.T) {
			f := newWebStatefulFixture(t)
			webStatefulTestHome(t, f.opts.DataDir)
			bound := f.bind(webStatefulTestPlan(t, f.profile.ID, webStatefulTestDefinition("zerotier")))
			private := filepath.Join(f.opts.DataDir, webStatefulDirectory)
			version := filepath.Join(private, webStatefulVersion)
			identity := filepath.Join(version, bound.Bindings[0].Identity)
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.Mkdir(outside, 0o700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(outside, "identity.secret")
			if err := os.WriteFile(file, []byte("outside-synthetic-state"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(identity, 0o700); err != nil {
				t.Fatal(err)
			}
			dataDir := f.opts.DataDir
			switch scenario {
			case "private-symlink", "version-symlink", "identity-symlink":
				target := map[string]string{"private-symlink": private, "version-symlink": version, "identity-symlink": identity}[scenario]
				if err := os.RemoveAll(target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, target); err != nil {
					t.Skipf("symlinks unsupported: %v", err)
				}
			case "file-symlink":
				if err := os.Symlink(file, filepath.Join(identity, "identity.secret")); err != nil {
					t.Skipf("symlinks unsupported: %v", err)
				}
			case "directory-symlink":
				if err := os.Symlink(outside, filepath.Join(identity, "nested")); err != nil {
					t.Skipf("symlinks unsupported: %v", err)
				}
			case "file-hardlink":
				if err := os.Link(file, filepath.Join(identity, "identity.secret")); err != nil {
					t.Skipf("hardlinks unsupported: %v", err)
				}
			case "identity-mode":
				if err := os.Chmod(identity, 0o755); err != nil {
					t.Fatal(err)
				}
			case "private-mode":
				if err := os.Chmod(private, 0o755); err != nil {
					t.Fatal(err)
				}
			case "data-symlink":
				alias := filepath.Join(filepath.Dir(f.opts.DataDir), "alias")
				if err := os.Symlink(f.opts.DataDir, alias); err != nil {
					t.Skipf("symlinks unsupported: %v", err)
				}
				dataDir = alias
				webStatefulTestHome(t, filepath.Dir(dataDir))
			}
			manager, _ := NewWebAPIStatefulManager(dataDir)
			defer manager.Close()
			_, err := manager.Materialize(bound)
			webAPITestErrorCode(t, err, "stateful_storage_unsafe")
			if data, err := os.ReadFile(file); err != nil || string(data) != "outside-synthetic-state" {
				t.Fatal("unsafe tree touched the external synthetic identity")
			}
			if strings.Contains(err.Error(), outside) || strings.Contains(err.Error(), dataDir) {
				t.Fatal("storage failure disclosed a host path")
			}
		})
	}
}

func TestWebAPIStatefulContractRequiresCoreSafePathWithoutCreatingState(t *testing.T) {
	f := newWebStatefulFixture(t)
	webStatefulTestHome(t, filepath.Join(t.TempDir(), "different-core-home"))
	bound := f.bind(webStatefulTestPlan(t, f.profile.ID, webStatefulTestDefinition("easytier")))
	manager, _ := NewWebAPIStatefulManager(f.opts.DataDir)
	defer manager.Close()
	_, err := manager.Materialize(bound)
	webAPITestErrorCode(t, err, "stateful_storage_unsafe")
	if entries, err := os.ReadDir(f.opts.DataDir); err != nil || len(entries) != 0 {
		t.Fatal("unsafe core path was created")
	}
}

func TestWebAPIStatefulContractHTTPAndBackupCannotReadPrivateState(t *testing.T) {
	f := newWebAPIIntegration(t)
	secret := []byte("synthetic-stateful-secret-not-an-account")
	identity := strings.Repeat("a", 64)
	private := filepath.Join(f.server.opts.DataDir, webStatefulDirectory, webStatefulVersion, identity)
	if err := os.MkdirAll(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "identity.secret"), secret, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		"/.api-proxy-state/v1/" + identity + "/identity.secret",
		"/data/.api-proxy-state/v1/" + identity + "/identity.secret",
		"/api/fs/read?path=/data/.api-proxy-state/v1/" + identity + "/identity.secret",
		"/api/v1/content/profile/1?path=.api-proxy-state/v1/" + identity + "/identity.secret",
		"/api/v1/content/proxy-state/1",
	} {
		response := f.request(t, "GET", route, nil, true)
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || bytes.Contains(body, secret) || bytes.Contains(body, []byte(f.server.opts.DataDir)) {
			t.Fatalf("HTTP route exposed private state: %s", route)
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			t.Fatalf("private state route unexpectedly accepted: %s", route)
		}
	}
	response := f.request(t, "GET", "/api/v1/backups?format=flclash-v1", nil, true)
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("synthetic backup response: %d %v", response.StatusCode, err)
	}
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range archive.File {
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil || strings.Contains(entry.Name, webStatefulDirectory) || strings.Contains(entry.Name, webStatefulBucket) || bytes.Contains(data, secret) {
			t.Fatal("application archive included private protocol state")
		}
	}
}
