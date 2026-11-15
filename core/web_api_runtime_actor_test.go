//go:build webpanel && !android

package core

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func webRuntimeStoredOperation(t *testing.T, s *webRuntimeTestRig, id string) WebAPIRuntimeOperation {
	t.Helper()
	var state WebAPIRuntimeOperation
	if _, err := s.store.View(context.Background(), func(tx *WebAPIReadTx) error { var err error; state, err = tx.RuntimeOperation(id); return err }); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestWebRuntimeUncancelableDeadlineDoesNotStartAnotherEffectOrBlockClose(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var resets atomic.Int32
	var path string
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{
		SelectProxy: func(context.Context, string, string) (*ChangeProxyResult, error) {
			t.Error("selection entered an uncertain runtime")
			return &ChangeProxyResult{}, nil
		},
		SetListener: func(context.Context, bool) error {
			close(started)
			<-release
			if _, err := os.Stat(path); err != nil {
				t.Error("workspace was deleted while the effect still used it")
			}
			close(returned)
			return nil
		},
		ResetTraffic: func(context.Context) error { resets.Add(1); return nil },
	})
	s.runtime.workspace.options.EffectTimeout = 20 * time.Millisecond
	var err error
	path, err = s.runtime.workspace.put([]byte("private effect input"), ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	code, body := s.request(t, "POST", "/api/v1/runtime/listener:set-enabled", `{"expectedRevision":"1","operationId":"uncancelable-listener","enabled":true}`, s.cookie, "")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("effect did not start")
	}
	code, body = s.request(t, "POST", "/api/v1/runtime/proxies:select", `{"expectedRevision":"2","operationId":"selection-during-effect","groupName":"G","proxyName":"P","runtimeGeneration":"7"}`, s.cookie, "")
	if code != 409 {
		t.Fatalf("selection blocked or accepted during transition: %d %s", code, body)
	}
	if state := s.phase(t, "uncancelable-listener"); state.Phase != WebAPIRuntimeUnknown {
		t.Fatalf("deadline was not observable immediately: %+v", state)
	}
	if code, body = s.request(t, "POST", "/api/v1/runtime/traffic:reset", `{"expectedRevision":"2","operationId":"queued-behind-unknown"}`, s.cookie, ""); code != 200 {
		t.Fatalf("queue %d %s", code, body)
	}
	if state := webRuntimeStoredOperation(t, s, "queued-behind-unknown"); state.Phase != WebAPIRuntimePending || resets.Load() != 0 {
		t.Fatal("a second effect overlapped the uncertain first effect")
	}
	if code, body = s.request(t, "GET", "/api/v1/runtime/state", "", s.cookie, ""); code != 200 {
		t.Fatalf("status read blocked: %d %s", code, body)
	}
	closed := make(chan struct{})
	go func() { s.runtime.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close waited on an uncooperative backend")
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("Close deleted an in-use workspace")
	}
	once.Do(func() { close(release) })
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("effect did not return after release")
	}
	deadline := time.Now().Add(time.Second)
	for {
		_, err = os.Stat(path)
		if os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("completed in-use workspace was not eventually cleaned")
		}
		time.Sleep(time.Millisecond)
	}
	if state := webRuntimeStoredOperation(t, s, "uncancelable-listener"); state.Phase != WebAPIRuntimeUnknown {
		t.Fatal("late completion rewrote Unknown into success")
	}
	if resets.Load() != 0 {
		t.Fatal("closing actor started a queued effect")
	}
}

func TestWebRuntimeLateCompletionRequiresExplicitReconciliation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var active, maxActive, resets atomic.Int32
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{
		SelectProxy: func(context.Context, string, string) (*ChangeProxyResult, error) {
			return &ChangeProxyResult{Changed: true}, nil
		},
		SetListener: func(context.Context, bool) error {
			n := active.Add(1)
			maxActive.Store(n)
			close(started)
			<-release
			active.Add(-1)
			return nil
		},
		ResetTraffic: func(context.Context) error {
			n := active.Add(1)
			if n > maxActive.Load() {
				maxActive.Store(n)
			}
			defer active.Add(-1)
			resets.Add(1)
			return nil
		},
		Reconcile: func(context.Context, *WebAPIID, UpdateParams) (WebAPIRuntimeApplyResult, error) {
			return WebAPIRuntimeApplyResult{ActualConfig: "patched"}, nil
		},
	})
	s.runtime.workspace.options.EffectTimeout = 20 * time.Millisecond
	if code, body := s.request(t, "POST", "/api/v1/runtime/listener:set-enabled", `{"expectedRevision":"1","operationId":"late-listener","enabled":true}`, s.cookie, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	<-started
	if state := s.phase(t, "late-listener"); state.Phase != WebAPIRuntimeUnknown {
		t.Fatal(state)
	}
	if code, body := s.request(t, "POST", "/api/v1/runtime/traffic:reset", `{"expectedRevision":"2","operationId":"reset-after-late"}`, s.cookie, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if resets.Load() != 0 {
		t.Fatal("queued effect started before native completion")
	}
	once.Do(func() { close(release) })
	if state := s.phase(t, "reset-after-late"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	if maxActive.Load() != 1 {
		t.Fatalf("overlapping native effects: %d", maxActive.Load())
	}
	if state := webRuntimeStoredOperation(t, s, "late-listener"); state.Phase != WebAPIRuntimeUnknown {
		t.Fatal("late success changed durable Unknown")
	}
	code, body := s.request(t, "POST", "/api/v1/runtime/proxies:select", `{"expectedRevision":"3","operationId":"before-reconcile","groupName":"G","proxyName":"P","runtimeGeneration":"7"}`, s.cookie, "")
	if code != 409 {
		t.Fatalf("unknown runtime accepted selection %d %s", code, body)
	}
	if code, body = s.request(t, "POST", "/api/v1/runtime:reconcile", `{"expectedRevision":"3","operationId":"explicit-reconcile"}`, s.cookie, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if state := s.phase(t, "explicit-reconcile"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
}

func TestWebRuntimeRecoveredGenerationUnknownStillRequiresReconciliation(t *testing.T) {
	store, err := OpenWebAPIStore(WebAPIStoreOptions{DataDir: filepath.Join(t.TempDir(), "data")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mutation := WebAPIMutation{ExpectedRevision: "0", OperationID: "interrupted-generation-change"}
	if _, err = store.Transaction(context.Background(), mutation, "test:pending-generation", mutation, func(tx *WebAPIWriteTx) (any, error) {
		return tx.RecordRuntimeIntent(mutation.OperationID, webRuntimeListener, mutation)
	}); err != nil {
		t.Fatal(err)
	}
	rt, err := NewWebAPIRuntime(store, WebAPIRuntimeBackend{})
	if err != nil {
		t.Fatal(err)
	}
	rt.Close()
	if !rt.effect.needsReconcile.Load() {
		t.Fatal("startup forgot that a generation-changing effect was Unknown")
	}
	mutation = WebAPIMutation{ExpectedRevision: "1", OperationID: "explicit-recovered-reconcile"}
	if _, err = store.Transaction(context.Background(), mutation, "test:reconcile-generation", mutation, func(tx *WebAPIWriteTx) (any, error) {
		return tx.RecordRuntimeIntent(mutation.OperationID, webRuntimeReconcile, mutation)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.transitionRuntime(mutation.OperationID, WebAPIRuntimePending, WebAPIRuntimeApplying, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = store.transitionRuntime(mutation.OperationID, WebAPIRuntimeApplying, WebAPIRuntimeApplied, nil, nil); err != nil {
		t.Fatal(err)
	}
	rt, err = NewWebAPIRuntime(store, WebAPIRuntimeBackend{})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if rt.effect.needsReconcile.Load() {
		t.Fatal("later explicit reconciliation did not clear durable Unknown gating")
	}
}
