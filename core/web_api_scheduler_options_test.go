//go:build webpanel && !android

package core

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestWebAPISchedulerRejectsBrowserOwnedBackgroundContext(t *testing.T) {
	store := newWebSchedulerStore(t)
	browser := WithWebAPIRequestAuthorization(t.Context(), "synthetic-browser", func(context.Context) (func(), error) { return func() {}, nil })
	if _, err := NewWebAPIScheduler(store, WebAPISchedulerOptions{InternalContext: browser}); err == nil {
		t.Fatal("scheduler retained browser grants/session authority")
	}
}

func TestWebAPISchedulerSafeModeAllowsOnlyExplicitSyntheticBackend(t *testing.T) {
	clock := newWebSchedulerFakeClock()
	store := newWebSchedulerStore(t, clock)
	webSchedulerSeedProfile(t, store, clock, true)
	var calls atomic.Int32
	commit := webSchedulerFakeCommit(store, clock)
	backend := WebAPISchedulerBackend{Synthetic: true, RefreshContent: func(ctx context.Context, task WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
		calls.Add(1)
		return commit(ctx, task)
	}}
	s, err := NewWebAPIScheduler(store, WebAPISchedulerOptions{InternalContext: webSchedulerInternalContext(), Clock: clock, Backend: backend, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("explicit SafeMode fake backend was suppressed")
	}
}
