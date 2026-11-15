//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	C "github.com/metacubex/mihomo/constant"
)

type webStatefulFixture struct {
	t       *testing.T
	store   *WebAPIStore
	opts    WebAPIStoreOptions
	ctx     context.Context
	profile WebAPIProfile
	serial  int
}

func newWebStatefulFixture(t *testing.T) *webStatefulFixture {
	t.Helper()
	f := &webStatefulFixture{t: t, opts: WebAPIStoreOptions{DataDir: filepath.Join(t.TempDir(), "data")}}
	if err := os.Mkdir(f.opts.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var err error
	f.store, err = OpenWebAPIStore(f.opts)
	if err != nil {
		t.Fatal(err)
	}
	f.ctx = WithWebAPIRequestAuthorization(context.Background(), "synthetic-stateful-session", func(context.Context) (func(), error) { return func() {}, nil })
	t.Cleanup(func() { f.store.Close() })
	f.mutate(func(tx *WebAPIWriteTx) (any, error) {
		var err error
		f.profile, err = tx.CreateProfile(WebAPIProfileInput{Type: "file", Label: "synthetic-profile", AutoUpdateDurationMillis: 3600000})
		return f.profile, err
	})
	return f
}

func (f *webStatefulFixture) mutation() WebAPIMutation {
	f.t.Helper()
	revision, err := f.store.View(f.ctx, func(*WebAPIReadTx) error { return nil })
	if err != nil {
		f.t.Fatal(err)
	}
	f.serial++
	return WebAPIMutation{ExpectedRevision: revision, OperationID: fmt.Sprintf("stateful-test-%08d", f.serial)}
}

func (f *webStatefulFixture) mutate(callback func(*WebAPIWriteTx) (any, error)) WebAPIReceipt {
	f.t.Helper()
	mutation := f.mutation()
	receipt, err := f.store.Transaction(f.ctx, mutation, "stateful-fixture", mutation, callback)
	if err != nil {
		f.t.Fatal(err)
	}
	return receipt
}

func webStatefulRecord(tx *WebAPIWriteTx, operation string, plan WebAPIStatefulPlan) (any, error) {
	state, err := tx.RecordRuntimeIntent(operation, webRuntimeApply, map[string]any{"request": map[string]any{"profileId": plan.ProfileID}})
	if err != nil {
		return nil, err
	}
	return state, tx.BindStatefulRuntime(operation, plan)
}

func (f *webStatefulFixture) bind(plan WebAPIStatefulPlan) WebAPIStatefulBindings {
	f.t.Helper()
	mutation := f.mutation()
	receipt, err := f.store.Transaction(f.ctx, mutation, "runtime:apply", plan, func(tx *WebAPIWriteTx) (any, error) { return webStatefulRecord(tx, mutation.OperationID, plan) })
	if err != nil {
		f.t.Fatal(err)
	}
	if strings.Contains(string(receipt.Body), "incarnation") || strings.Contains(string(receipt.Body), ".api-proxy-state") {
		f.t.Fatal("runtime receipt leaked private state metadata")
	}
	bound, err := f.store.StatefulRuntimeBindings(f.ctx, mutation.OperationID)
	if err != nil {
		f.t.Fatal(err)
	}
	return bound
}

func webStatefulTestHome(t *testing.T, home string) {
	t.Helper()
	old := C.Path.HomeDir()
	C.SetHomeDir(home)
	t.Cleanup(func() { C.SetHomeDir(old) })
}

func TestWebAPIStatefulContractStableReuseAndStoreScope(t *testing.T) {
	f := newWebStatefulFixture(t)
	webStatefulTestHome(t, f.opts.DataDir)
	plan := webStatefulTestPlan(t, f.profile.ID, webStatefulTestDefinition("easytier"))
	first := f.bind(plan)
	if _, err := os.Stat(filepath.Join(f.opts.DataDir, webStatefulDirectory)); !os.IsNotExist(err) {
		t.Fatal("same-CAS binding created a directory before worker effect")
	}
	manager, _ := NewWebAPIStatefulManager(f.opts.DataDir)
	paths, err := manager.Materialize(first)
	if err != nil || len(paths) != 1 {
		t.Fatal(err)
	}
	file := filepath.Join(paths[0].StateDir, "instance_id")
	if err := os.WriteFile(file, []byte("synthetic-stable-device\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := f.bind(plan)
	if first.Bindings[0].Identity != second.Bindings[0].Identity {
		t.Fatal("same resource reapply changed identity")
	}
	manager.Close()
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = OpenWebAPIStore(f.opts)
	if err != nil {
		t.Fatal(err)
	}
	third := f.bind(plan)
	if first.Bindings[0].Identity != third.Bindings[0].Identity {
		t.Fatal("restart changed the same resource identity")
	}
	manager, _ = NewWebAPIStatefulManager(f.opts.DataDir)
	defer manager.Close()
	reused, err := manager.Materialize(third)
	if err != nil || reused[0].StateDir != paths[0].StateDir {
		t.Fatal("restart did not reuse private state")
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "synthetic-stable-device\n" {
		t.Fatal("state file was deleted or replaced")
	}
	other, err := OpenWebAPIStore(WebAPIStoreOptions{DataDir: f.opts.DataDir, StoreDir: filepath.Join(t.TempDir(), "other-store")})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	replacement := &webStatefulFixture{t: t, store: other, ctx: f.ctx}
	replacement.mutate(func(tx *WebAPIWriteTx) (any, error) { return nil, tx.InsertProfile(f.profile) })
	otherBinding := replacement.bind(plan)
	if otherBinding.Bindings[0].Identity == first.Bindings[0].Identity {
		t.Fatal("new store inherited device identity")
	}
}

func TestWebAPIStatefulContractScopeAndCloneIsolation(t *testing.T) {
	f := newWebStatefulFixture(t)
	var clone WebAPIProfile
	f.mutate(func(tx *WebAPIWriteTx) (any, error) {
		var err error
		input := f.profile.WebAPIProfileInput
		input.Label = "synthetic-clone"
		clone, err = tx.CreateProfile(input)
		return clone, err
	})
	definition := webStatefulTestDefinition("tailscale")
	origin, _ := WebAPIStatefulOriginDomain("https://example.test/provider", nil)
	inputs := []WebAPIStatefulInput{
		{Scope: WebAPIStatefulScope{Kind: WebAPIStatefulRoot}, Proxies: []map[string]any{definition}},
		{Scope: WebAPIStatefulScope{Kind: WebAPIStatefulProfileProvider, ProviderName: "one", SourceProfileID: clone.ID}, Proxies: []map[string]any{definition}},
		{Scope: WebAPIStatefulScope{Kind: WebAPIStatefulProfileProvider, ProviderName: "two", SourceProfileID: clone.ID}, Proxies: []map[string]any{definition}},
		{Scope: WebAPIStatefulScope{Kind: WebAPIStatefulRuntimeProvider, ProviderName: "one", OriginDomain: origin}, Proxies: []map[string]any{definition}},
	}
	plan, err := WebAPIPlanStatefulResources(f.profile.ID, inputs)
	if err != nil {
		t.Fatal(err)
	}
	bound := f.bind(plan)
	identities := map[string]bool{}
	for _, binding := range bound.Bindings {
		if identities[binding.Identity] {
			t.Fatal("root/provider resources share device identity")
		}
		identities[binding.Identity] = true
	}
	clonePlan := webStatefulTestPlan(t, clone.ID, definition)
	cloneBinding := f.bind(clonePlan)
	if identities[cloneBinding.Bindings[0].Identity] {
		t.Fatal("cloned profile inherited a device identity")
	}
}

func TestWebAPIStatefulContractCASFailureReplayAndAuthority(t *testing.T) {
	f := newWebStatefulFixture(t)
	plan := webStatefulTestPlan(t, f.profile.ID, webStatefulTestDefinition("zerotier"))
	mutation := f.mutation()
	_, err := f.store.Transaction(f.ctx, mutation, "runtime:apply", plan, func(tx *WebAPIWriteTx) (any, error) { return nil, tx.BindStatefulRuntime(mutation.OperationID, plan) })
	webAPITestErrorCode(t, err, "stateful_intent_required")
	_, err = f.store.Transaction(f.ctx, mutation, "runtime:apply", plan, func(tx *WebAPIWriteTx) (any, error) {
		if _, err := webStatefulRecord(tx, mutation.OperationID, plan); err != nil {
			return nil, err
		}
		return nil, errors.New("synthetic rejected candidate")
	})
	if err == nil {
		t.Fatal("synthetic transaction failure accepted")
	}
	_, err = f.store.StatefulRuntimeBindings(f.ctx, mutation.OperationID)
	if err == nil {
		t.Fatal("failed transaction published bindings")
	}
	_, err = f.store.Transaction(context.Background(), mutation, "runtime:apply", plan, func(tx *WebAPIWriteTx) (any, error) { return webStatefulRecord(tx, mutation.OperationID, plan) })
	webAPITestErrorCode(t, err, "stateful_intent_required")
	internal := WithWebAPIInternalAuthorization(context.Background(), func(context.Context) (func(), error) { return func() {}, nil })
	_, err = f.store.Transaction(internal, mutation, "runtime:apply", plan, func(tx *WebAPIWriteTx) (any, error) { return webStatefulRecord(tx, mutation.OperationID, plan) })
	webAPITestErrorCode(t, err, "stateful_intent_required")
	first, err := f.store.Transaction(f.ctx, mutation, "runtime:apply", plan, func(tx *WebAPIWriteTx) (any, error) { return webStatefulRecord(tx, mutation.OperationID, plan) })
	if err != nil {
		t.Fatal(err)
	}
	called := false
	replayed, err := f.store.Transaction(f.ctx, mutation, "runtime:apply", plan, func(*WebAPIWriteTx) (any, error) { called = true; return nil, nil })
	if err != nil || !replayed.Replayed || called || string(first.Body) != string(replayed.Body) {
		t.Fatal("receipt replay allocated another state binding")
	}
	mutation.OperationID += "-stale"
	_, err = f.store.Transaction(f.ctx, mutation, "runtime:apply", plan, func(*WebAPIWriteTx) (any, error) { called = true; return nil, nil })
	webAPITestErrorCode(t, err, "revision_conflict")
	if called {
		t.Fatal("stale CAS invoked the state binder")
	}
	entries, _ := os.ReadDir(f.opts.DataDir)
	if len(entries) != 0 {
		t.Fatal("acceptance or rejection performed a filesystem effect")
	}
	var uncommitted WebAPIStatefulBindings
	stored, _ := f.store.StatefulRuntimeBindings(f.ctx, first.OperationID)
	data, _ := json.Marshal(stored)
	json.Unmarshal(data, &uncommitted)
	manager, _ := NewWebAPIStatefulManager(f.opts.DataDir)
	defer manager.Close()
	_, err = manager.Materialize(uncommitted)
	webAPITestErrorCode(t, err, "stateful_intent_required")
}

func TestWebAPIStatefulContractDetachRestoreResetAndUnknownPreserveFiles(t *testing.T) {
	f := newWebStatefulFixture(t)
	webStatefulTestHome(t, f.opts.DataDir)
	plan := webStatefulTestPlan(t, f.profile.ID, webStatefulTestDefinition("easytier"))
	manager, _ := NewWebAPIStatefulManager(f.opts.DataDir)
	defer manager.Close()
	first := f.bind(plan)
	paths, err := manager.Materialize(first)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(paths[0].StateDir, "instance_id")
	os.WriteFile(file, []byte("synthetic-unknown-device"), 0o600)
	before, _ := f.store.View(f.ctx, func(*WebAPIReadTx) error { return nil })
	if _, err := f.store.transitionRuntime(first.OperationID, WebAPIRuntimePending, WebAPIRuntimeUnknown, nil, webAPIError(409, "effect_unknown", "synthetic blocked effect")); err != nil {
		t.Fatal(err)
	}
	after, _ := f.store.View(f.ctx, func(*WebAPIReadTx) error { return nil })
	if before != after {
		t.Fatal("runtime status churned configuration revision")
	}
	for _, action := range []string{"restore", "delete", "reset"} {
		f.mutate(func(tx *WebAPIWriteTx) (any, error) {
			if action == "reset" {
				if err := tx.DetachAllStatefulResources(); err != nil {
					return nil, err
				}
				if err := tx.ResetConfiguration(); err != nil {
					return nil, err
				}
			} else if err := tx.DetachStatefulProfiles([]WebAPIID{f.profile.ID}); err != nil {
				return nil, err
			}
			if action == "delete" {
				if _, err := tx.DeleteProfile(f.profile.ID); err != nil {
					return nil, err
				}
			}
			if action == "restore" {
				return nil, tx.SaveProfile(f.profile)
			}
			return nil, tx.InsertProfile(f.profile)
		})
		fresh := f.bind(plan)
		if fresh.Bindings[0].Identity == first.Bindings[0].Identity {
			t.Fatalf("%s reused detached device identity", action)
		}
		if _, err := f.store.StatefulRuntimeBindings(f.ctx, first.OperationID); err != nil {
			t.Fatalf("%s deleted an unknown operation binding: %v", action, err)
		}
		if data, err := os.ReadFile(file); err != nil || string(data) != "synthetic-unknown-device" {
			t.Fatalf("%s removed in-flight/unknown state", action)
		}
		first = fresh
	}
	manager.Close()
	if _, err := os.Stat(file); err != nil {
		t.Fatal("stop/close removed persistent protocol state")
	}
}

func TestWebAPIStatefulContractReconcileRemovedMemberAndDependentScope(t *testing.T) {
	f := newWebStatefulFixture(t)
	var consumer WebAPIProfile
	f.mutate(func(tx *WebAPIWriteTx) (any, error) {
		var err error
		input := f.profile.WebAPIProfileInput
		input.Label = "consumer"
		consumer, err = tx.CreateProfile(input)
		return consumer, err
	})
	input := WebAPIStatefulInput{Scope: WebAPIStatefulScope{Kind: WebAPIStatefulProfileProvider, ProviderName: "source", SourceProfileID: f.profile.ID}, Proxies: []map[string]any{webStatefulTestDefinition("tailscale")}}
	plan, err := WebAPIPlanStatefulResources(consumer.ID, []WebAPIStatefulInput{input})
	if err != nil {
		t.Fatal(err)
	}
	first := f.bind(plan)
	f.mutate(func(tx *WebAPIWriteTx) (any, error) { return nil, tx.ReconcileStatefulResources(f.profile.ID, nil) })
	second := f.bind(plan)
	if first.Bindings[0].Identity == second.Bindings[0].Identity {
		t.Fatal("removed and re-added source member inherited consumer identity")
	}
	f.mutate(func(tx *WebAPIWriteTx) (any, error) {
		return nil, tx.ReconcileStatefulResources(consumer.ID, plan.Resources)
	})
	third := f.bind(plan)
	if second.Bindings[0].Identity != third.Bindings[0].Identity {
		t.Fatal("unchanged complete inventory churned identity")
	}
}
