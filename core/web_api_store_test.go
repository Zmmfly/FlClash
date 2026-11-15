//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	bolt "github.com/metacubex/bbolt"
)

func webAPITestErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	var e *WebAPIError
	if !errors.As(err, &e) || e.Code != want {
		t.Fatalf("error=%v want %s", err, want)
	}
}

func TestWebAPIPrivateStorePathsPermissionsAndLinks(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	static := filepath.Join(base, "static")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(static, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenWebAPIStore(WebAPIStoreOptions{DataDir: data, ManagedRoots: []string{static}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	private := filepath.Join(base, "data.api-store")
	for name, mode := range map[string]os.FileMode{private: 0o700, filepath.Join(private, webAPIStoreFile): 0o600} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s mode or error: %v %v", name, info, err)
		}
	}
	if entries, err := os.ReadDir(data); err != nil || len(entries) != 0 {
		t.Fatalf("managed root was written: %v %v", entries, err)
	}
	for _, path := range []string{data, filepath.Join(data, "profiles", "private"), base, filepath.Join(static, "db"), static} {
		if s, err := OpenWebAPIStore(WebAPIStoreOptions{DataDir: data, StoreDir: path, ManagedRoots: []string{static}}); err == nil {
			s.Close()
			t.Fatalf("overlap accepted %s", path)
		}
	}
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "alias")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	for _, opts := range []WebAPIStoreOptions{{DataDir: data, StoreDir: link}, {DataDir: data, StoreDir: filepath.Join(link, "new")}, {DataDir: link, StoreDir: filepath.Join(base, "separate")}, {DataDir: data, ManagedRoots: []string{link}, StoreDir: filepath.Join(base, "different")}} {
		if s, err := OpenWebAPIStore(opts); err == nil {
			s.Close()
			t.Fatal("symlink accepted")
		}
	}
	badDir := filepath.Join(base, "bad-mode")
	if err := os.Mkdir(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenWebAPIStore(WebAPIStoreOptions{DataDir: data, StoreDir: badDir}); err == nil {
		s.Close()
		t.Fatal("insecure mode accepted")
	}
	for _, kind := range []string{"symlink", "hardlink", "mode"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(base, kind)
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(base, kind+"-source")
			if err := os.WriteFile(source, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(path, webAPIStoreFile)
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(source, dest)
			case "hardlink":
				err = os.Link(source, dest)
			case "mode":
				err = os.WriteFile(dest, []byte("not a database"), 0o644)
			}
			if err != nil {
				t.Skip(err)
			}
			if s, err := OpenWebAPIStore(WebAPIStoreOptions{DataDir: data, StoreDir: path}); err == nil {
				s.Close()
				t.Fatal("unsafe file accepted")
			}
			body, err := os.ReadFile(source)
			if err != nil || string(body) != "untouched" {
				t.Fatal("source changed")
			}
		})
	}
}

func TestWebAPIPrivateDirectorySyncFailure(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			base := t.TempDir()
			calls := 0
			opts := WebAPIStoreOptions{DataDir: filepath.Join(base, "data"), syncDirectory: func(f *os.File) error {
				calls++
				if calls == failAt {
					return errors.New("injected directory sync failure")
				}
				return f.Sync()
			}}
			store, err := OpenWebAPIStore(opts)
			if err == nil {
				store.Close()
				t.Fatalf("sync failure %d was ignored; calls=%d", failAt, calls)
			}
			opts.syncDirectory = nil
			store, err = OpenWebAPIStore(opts)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			rev, err := store.View(context.Background(), func(tx *WebAPIReadTx) error {
				meta, err := tx.Meta()
				if len(meta.StoreID) != 36 {
					t.Fatal(meta)
				}
				return err
			})
			if err != nil || rev != "0" {
				t.Fatal(rev, err)
			}
		})
	}
}

func TestWebAPIAtomicRollbackIdentityContentAndCacheScopes(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := context.Background()
	_, err := r.store.Transaction(ctx, WebAPIMutation{"0", "rollback-all-state"}, "test:rollback", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		p, err := tx.CreateProfile(WebAPIProfileInput{Type: "file", Label: "Rollback"})
		if err != nil {
			return nil, err
		}
		if err = tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: p.ID}, MediaType: "text/yaml", Bytes: []byte("safe")}); err != nil {
			return nil, err
		}
		return nil, errors.New("reject candidate")
	})
	if err == nil {
		t.Fatal("failed transaction committed")
	}
	r.call("GET", "/api/v1/operations/rollback-all-state", nil, 404)
	_, err = r.store.View(ctx, func(tx *WebAPIReadTx) error {
		profiles, err := tx.Profiles()
		if len(profiles) != 0 {
			t.Fatal(profiles)
		}
		contents, e := tx.Contents()
		if len(contents) != 0 {
			t.Fatal(contents)
		}
		return errors.Join(err, e)
	})
	if err != nil {
		t.Fatal(err)
	}
	p := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "create-after-rollback", map[string]any{"profile": webAPITestProfile("Retained")}), 200))
	if p.Data.ID != "1" {
		t.Fatal("rolled back allocation was retained")
	}
	content := WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: p.Data.ID}, MediaType: "text/yaml", Bytes: []byte("rules: []")}
	_, err = r.store.Transaction(ctx, WebAPIMutation{"1", "persist-source-content"}, "test:content", content, func(tx *WebAPIWriteTx) (any, error) { return struct{}{}, tx.PutContent(content) })
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.View(ctx, func(tx *WebAPIReadTx) error {
		got, err := tx.Content(content.Key)
		if !bytes.Equal(got.Bytes, content.Bytes) || len(got.SHA256) != 64 {
			t.Fatal(got)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	input := WebAPIIconRecord{URL: "https://example.invalid/icon.png", LastAccessed: "123"}
	for i := 0; i < 2; i++ {
		receipt, err := r.store.CacheTransaction(ctx, WebAPIMutation{"0", "cache-idempotency"}, "icons:put", input, func(tx *WebAPICacheTx) (any, error) { return input, tx.PutIconRecord(input) })
		if err != nil || receipt.Revision != "1" || receipt.Replayed != (i == 1) {
			t.Fatal(receipt, err)
		}
	}
	if r.commits.Load() != 2 {
		t.Fatal("cache changed global callback")
	}
	_, err = r.store.CacheTransaction(ctx, WebAPIMutation{"0", "cache-stale-revision"}, "icons:put", input, func(tx *WebAPICacheTx) (any, error) { return nil, nil })
	webAPITestErrorCode(t, err, "revision_conflict")
	meta := webAPITestData[WebAPIStoreMeta](t, r.call("GET", "/api/v1/meta", nil, 200))
	if meta.Revision != "2" {
		t.Fatal(meta)
	}
	_, err = r.store.Transaction(ctx, WebAPIMutation{"2", "configuration-reset"}, "test:reset", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return struct{}{}, tx.ResetConfiguration() })
	if err != nil {
		t.Fatal(err)
	}
	r.call("GET", "/api/v1/operations/create-after-rollback", nil, 200)
	after := webAPITestData[WebAPIStoreMeta](t, r.call("GET", "/api/v1/meta", nil, 200))
	if after.Revision != "3" || after.Data.StoreID != meta.Data.StoreID {
		t.Fatal(after)
	}
	newp := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("3", "identity-after-reset", map[string]any{"profile": webAPITestProfile("New")}), 200))
	if newp.Data.ID != "2" {
		t.Fatal("reset reused identity")
	}
}

func TestWebAPIInt64IdentityLimitsAndCreateNeverUpserts(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := context.Background()
	id := WebAPIID("9223372036854775806")
	_, err := r.store.Transaction(ctx, WebAPIMutation{"0", "import-large-identity"}, "test:import", id, func(tx *WebAPIWriteTx) (any, error) {
		v := WebAPIProfile{ID: id, WebAPIProfileInput: webAPINormalizeProfile(WebAPIProfileInput{Type: "file", Label: "Legacy"})}
		return v, tx.InsertProfile(v)
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.Transaction(ctx, WebAPIMutation{"1", "duplicate-import-id"}, "test:import", id, func(tx *WebAPIWriteTx) (any, error) {
		return nil, tx.InsertProfile(WebAPIProfile{ID: id, WebAPIProfileInput: WebAPIProfileInput{Type: "file", Label: "Overwrite"}})
	})
	webAPITestErrorCode(t, err, "duplicate_id")
	list := webAPITestData[[]WebAPIProfile](t, r.call("GET", "/api/v1/profiles?id="+string(id), nil, 200))
	if list.Data[0].ID != id || list.Data[0].Label != "Legacy" {
		t.Fatal(list)
	}
	last := webAPITestData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", webAPITestMutation("1", "maximum-create-id", map[string]any{"profile": webAPITestProfile("Max")}), 200))
	if last.Data.ID != "9223372036854775807" {
		t.Fatal(last)
	}
	r.call("POST", "/api/v1/profiles:create", webAPITestMutation("2", "exhausted-create-id", map[string]any{"profile": webAPITestProfile("Never")}), 409)
	r.server.Close()
	r.store.Close()
	r.store, err = OpenWebAPIStore(r.opts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.View(ctx, func(tx *WebAPIReadTx) error {
		got, err := tx.Profile(id)
		if got.ID != id {
			t.Fatal(got)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebAPICommitFailureQuarantinesAndReopenReconciles(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			r := newWebAPITestRig(t)
			r.store.commit = func(tx *bolt.Tx) error {
				if committed {
					if err := tx.Commit(); err != nil {
						return err
					}
				}
				return errors.New("injected acknowledgement failure")
			}
			input := webAPITestMutation("0", "ambiguous-commit-id", map[string]any{"profile": webAPITestProfile("Uncertain")})
			body := r.call("POST", "/api/v1/profiles:create", input, 503)
			if !bytes.Contains(body, []byte(`"durability_unknown"`)) {
				t.Fatal(string(body))
			}
			r.call("POST", "/api/v1/profiles:create", input, 503)
			r.call("GET", "/api/v1/operations/ambiguous-commit-id", nil, 503)
			if r.commits.Load() != 0 {
				t.Fatal("unconfirmed commit notified")
			}
			r.server.Close()
			r.store.Close()
			var err error
			r.store, err = OpenWebAPIStore(r.opts)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := r.store.Receipt(context.Background(), "ambiguous-commit-id")
			if committed {
				if err != nil || receipt.Revision != "1" {
					t.Fatal(receipt, err)
				}
			} else {
				webAPITestErrorCode(t, err, "not_found")
			}
		})
	}
}

func TestWebAPIOperationResolutionTombstonesAndRace(t *testing.T) {
	r := newWebAPITestRig(t)
	input := webAPITestMutation("0", "late-request-after-resolve", map[string]any{"profile": webAPITestProfile("Late")})
	r.call("GET", "/api/v1/operations/late-request-after-resolve", nil, 404)
	resolved := r.call("POST", "/api/v1/operations/late-request-after-resolve:resolve", map[string]any{"intent": "resolve-unknown-result"}, 409)
	if !bytes.Contains(resolved, []byte(`"operation_not_committed"`)) {
		t.Fatal(string(resolved))
	}
	r.call("POST", "/api/v1/profiles:create", input, 409)
	meta := webAPITestData[WebAPIStoreMeta](t, r.call("GET", "/api/v1/meta", nil, 200))
	if meta.Revision != "0" {
		t.Fatal(meta)
	}
	r.server.Close()
	r.store.Close()
	var err error
	r.store, err = OpenWebAPIStore(r.opts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.Receipt(context.Background(), "late-request-after-resolve")
	webAPITestErrorCode(t, err, "operation_not_committed")
	entered := make(chan struct{})
	release := make(chan struct{})
	committed := make(chan WebAPIReceipt, 1)
	commitErr := make(chan error, 1)
	go func() {
		receipt, err := r.store.Transaction(context.Background(), WebAPIMutation{"0", "resolve-inflight-commit"}, "test:commit", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
			close(entered)
			<-release
			return tx.CreateProfile(WebAPIProfileInput{Type: "file", Label: "Winner"})
		})
		committed <- receipt
		commitErr <- err
	}()
	<-entered
	resolvedReceipt := make(chan WebAPIReceipt, 1)
	resolvedErr := make(chan error, 1)
	go func() {
		receipt, err := r.store.ResolveOperation(context.Background(), "resolve-inflight-commit")
		resolvedReceipt <- receipt
		resolvedErr <- err
	}()
	close(release)
	a, b := <-committed, <-resolvedReceipt
	if err := <-commitErr; err != nil {
		t.Fatal(err)
	}
	if err := <-resolvedErr; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Body, b.Body) || b.Revision != "1" {
		t.Fatal(a, b)
	}
	_, err = r.store.ResolveCacheOperation(context.Background(), "cache-never-committed")
	webAPITestErrorCode(t, err, "operation_not_committed")
	_, err = r.store.CacheTransaction(context.Background(), WebAPIMutation{"0", "cache-never-committed"}, "cache:late", struct{}{}, func(*WebAPICacheTx) (any, error) { t.Fatal("tombstone was executed"); return nil, nil })
	webAPITestErrorCode(t, err, "operation_not_committed")
}

func TestWebAPICommitAuthorizationHeldThroughCommitReleasedBeforeNotify(t *testing.T) {
	r := newWebAPITestRig(t)
	var held atomic.Bool
	r.store.onCommit = func(WebAPICommit) {
		if held.Load() {
			t.Error("authorization held in OnCommit")
		}
	}
	ctx := WithWebAPIRequestAuthorization(context.Background(), "synthetic-session", func(context.Context) (func(), error) {
		if !held.CompareAndSwap(false, true) {
			t.Fatal("authorization acquired twice")
		}
		return func() { held.Store(false) }, nil
	})
	r.store.commit = func(tx *bolt.Tx) error {
		if !held.Load() {
			t.Error("authorization released before commit")
		}
		return tx.Commit()
	}
	_, err := r.store.Transaction(ctx, WebAPIMutation{"0", "authorized-commit"}, "test:authorized", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		if !held.Load() {
			t.Error("business before authorization")
		}
		return tx.CreateProfile(WebAPIProfileInput{Type: "file"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if held.Load() {
		t.Fatal("authorization leaked")
	}
	ctx = WithWebAPIRequestAuthorization(context.Background(), "revoked-session", func(context.Context) (func(), error) { return nil, webAPIError(401, "unauthorized", "revoked") })
	_, err = r.store.Transaction(ctx, WebAPIMutation{"1", "revoked-commit"}, "test:revoked", struct{}{}, func(*WebAPIWriteTx) (any, error) { t.Fatal("revoked mutation ran"); return nil, nil })
	webAPITestErrorCode(t, err, "unauthorized")
	_, err = r.store.ResolveOperation(ctx, "revoked-resolution")
	webAPITestErrorCode(t, err, "unauthorized")
}

func TestWebAPIAtomicProviderProfileRenameAndReferenceChecks(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := context.Background()
	_, err := r.store.Transaction(ctx, WebAPIMutation{"0", "seed-provider-references"}, "test:seed", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		provider, err := tx.CreateProfile(WebAPIProfileInput{Type: "file", Label: "Old"})
		if err != nil {
			return nil, err
		}
		custom, err := tx.CreateProfile(WebAPIProfileInput{Type: "custom", Label: "Consumer"})
		if err != nil {
			return nil, err
		}
		groupID, err := tx.AllocateID()
		if err != nil {
			return nil, err
		}
		if err = tx.InsertProxyGroup(WebAPIProxyGroup{ID: groupID, ProfileID: custom.ID, Definition: WebAPIProxyGroupDefinition{Name: "Group", Type: "select", Use: []string{provider.Label}, UsePosition: map[string]int32{provider.Label: 0}}}); err != nil {
			return nil, err
		}
		custom.Overrides.ProxyProviders = map[string]WebAPIProxyProviderOptions{"Old": {}}
		return custom, tx.SaveProfile(custom)
	})
	if err != nil {
		t.Fatal(err)
	}
	r.call("POST", "/api/v1/profiles:delete", webAPITestMutation("1", "delete-in-use-provider", map[string]any{"id": "1"}), 409)
	r.call("POST", "/api/v1/profiles:rename", webAPITestMutation("1", "rename-provider-atomically", map[string]any{"id": "1", "label": "New"}), 200)
	_, err = r.store.View(ctx, func(tx *WebAPIReadTx) error {
		profiles, err := tx.Profiles()
		if err != nil {
			return err
		}
		groups, err := tx.ProxyGroups()
		if err != nil {
			return err
		}
		if profiles[0].Label != "New" || !reflect.DeepEqual(groups[0].Definition.Use, []string{"New"}) {
			t.Fatal(profiles, groups)
		}
		if _, ok := profiles[1].Overrides.ProxyProviders["New"]; !ok {
			t.Fatal(profiles[1])
		}
		if _, ok := groups[0].Definition.UsePosition["Old"]; ok {
			t.Fatal(groups[0])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.Transaction(ctx, WebAPIMutation{"2", "bad-cross-domain-reference"}, "test:bad-reference", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		id, err := tx.AllocateID()
		if err != nil {
			return nil, err
		}
		return nil, tx.InsertProxyGroup(WebAPIProxyGroup{ID: id, ProfileID: "1", Definition: WebAPIProxyGroupDefinition{Name: "Wrong", Type: "select"}})
	})
	webAPITestErrorCode(t, err, "owner_conflict")
}

func TestWebAPICanonicalRequestHashAndCancellationRollback(t *testing.T) {
	r := newWebAPITestRig(t)
	first := r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "canonical-json-hash", map[string]any{"profile": webAPITestProfile("Canonical")}), 200)
	reordered := []byte(`{ "profile": {"label":"Canonical", "autoUpdateDurationMillis":3600000, "type":"file"}, "operationId":"canonical-json-hash", "expectedRevision":"0" }`)
	status, body := r.raw("POST", "/api/v1/profiles:create", reordered)
	if status != 200 || !bytes.Equal(body, first) {
		t.Fatal(status, string(body))
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := r.store.Transaction(ctx, WebAPIMutation{"1", "cancel-before-commit"}, "test:cancel", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		p, err := tx.CreateProfile(WebAPIProfileInput{Type: "file", Label: "Cancelled"})
		cancel()
		return p, err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	r.call("GET", "/api/v1/operations/cancel-before-commit", nil, 404)
	meta := webAPITestData[WebAPIStoreMeta](t, r.call("GET", "/api/v1/meta", nil, 200))
	if meta.Revision != "1" {
		t.Fatal(meta)
	}
}

func TestWebAPIRevisionOverflowAndCallbackPanic(t *testing.T) {
	r := newWebAPITestRig(t)
	r.store.onCommit = func(WebAPICommit) { panic("observer failed") }
	r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "callback-panic-receipt", map[string]any{"profile": webAPITestProfile("Durable")}), 200)
	r.call("GET", "/api/v1/operations/callback-panic-receipt", nil, 200)
	if err := r.store.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("meta")).Put([]byte("revision"), []byte("9223372036854775807"))
	}); err != nil {
		t.Fatal(err)
	}
	body := r.call("POST", "/api/v1/profiles:create", webAPITestMutation("9223372036854775807", "revision-overflow-op", map[string]any{"profile": webAPITestProfile("Overflow")}), 409)
	if !strings.Contains(string(body), "revision_exhausted") {
		t.Fatal(string(body))
	}
}

func TestWebAPICacheCASConcurrentIsolation(t *testing.T) {
	r := newWebAPITestRig(t)
	var wg sync.WaitGroup
	codes := make(chan string, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := r.store.CacheTransaction(context.Background(), WebAPIMutation{"0", fmt.Sprintf("cache-cas-%02d", i)}, "cache:put", i, func(tx *WebAPICacheTx) (any, error) {
				v := WebAPIIconRecord{URL: fmt.Sprintf("https://example.invalid/%d", i), LastAccessed: "1"}
				return v, tx.PutIconRecord(v)
			})
			if err == nil {
				codes <- "ok"
			} else {
				var e *WebAPIError
				if errors.As(err, &e) {
					codes <- e.Code
				} else {
					codes <- err.Error()
				}
			}
		}(i)
	}
	wg.Wait()
	close(codes)
	counts := map[string]int{}
	for code := range codes {
		counts[code]++
	}
	if counts["ok"] != 1 || counts["revision_conflict"] != 9 {
		t.Fatal(counts)
	}
	meta := webAPITestData[WebAPIStoreMeta](t, r.call("GET", "/api/v1/meta", nil, 200))
	if meta.Revision != "0" || r.commits.Load() != 0 {
		t.Fatal(meta)
	}
}

func TestWebAPIImportedIDsUseCanonicalJSONStrings(t *testing.T) {
	for _, text := range []string{`9223372036854775807`, `"9223372036854775808"`, `"01"`, `null`, `"-1"`} {
		var id WebAPIID
		if json.Unmarshal([]byte(text), &id) == nil {
			t.Fatalf("accepted %s", text)
		}
	}
	var id WebAPIID
	if err := json.Unmarshal([]byte(`"9223372036854775807"`), &id); err != nil || id != "9223372036854775807" {
		t.Fatal(id, err)
	}
}
