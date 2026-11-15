//go:build webpanel && !android

package core

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type webSchedulerFakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*webSchedulerFakeTimer]time.Time
	armed  chan struct{}
}
type webSchedulerFakeTimer struct {
	clock   *webSchedulerFakeClock
	channel chan time.Time
}

func newWebSchedulerFakeClock() *webSchedulerFakeClock {
	return &webSchedulerFakeClock{now: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC), timers: map[*webSchedulerFakeTimer]time.Time{}, armed: make(chan struct{}, 256)}
}
func (c *webSchedulerFakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *webSchedulerFakeClock) NewTimer(d time.Duration) WebAPISchedulerTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &webSchedulerFakeTimer{clock: c, channel: make(chan time.Time, 1)}
	c.timers[timer] = c.now.Add(d)
	select {
	case c.armed <- struct{}{}:
	default:
	}
	return timer
}
func (c *webSchedulerFakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	for timer, due := range c.timers {
		if !due.After(c.now) {
			timer.channel <- c.now
			delete(c.timers, timer)
		}
	}
}
func (t *webSchedulerFakeTimer) C() <-chan time.Time { return t.channel }
func (t *webSchedulerFakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	_, ok := t.clock.timers[t]
	delete(t.clock.timers, t)
	return ok
}

func webSchedulerInternalContext() context.Context {
	return WithWebAPIInternalAuthorization(context.Background(), func(ctx context.Context) (func(), error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return func() {}, nil
	})
}
func newWebSchedulerStore(t *testing.T, clocks ...*webSchedulerFakeClock) *WebAPIStore {
	t.Helper()
	options := WebAPIStoreOptions{DataDir: filepath.Join(t.TempDir(), "managed")}
	if len(clocks) == 1 {
		options.Now = clocks[0].Now
	}
	s, err := OpenWebAPIStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func webSchedulerRevision(t *testing.T, store *WebAPIStore) WebAPIRevision {
	t.Helper()
	rev, err := store.View(t.Context(), func(*WebAPIReadTx) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return rev
}
func webSchedulerMutate(t *testing.T, store *WebAPIStore, operation string, apply func(*WebAPIWriteTx) (any, error)) WebAPIReceipt {
	t.Helper()
	mutation := WebAPIMutation{ExpectedRevision: webSchedulerRevision(t, store), OperationID: operation}
	receipt, err := store.Transaction(webSchedulerInternalContext(), mutation, "scheduler-fixture", mutation, apply)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}
func webSchedulerSeedProfile(t *testing.T, store *WebAPIStore, clock *webSchedulerFakeClock, auto bool) WebAPIID {
	t.Helper()
	var id WebAPIID
	webSchedulerMutate(t, store, "scheduler-seed-profile", func(tx *WebAPIWriteTx) (any, error) {
		p, err := tx.CreateProfile(WebAPIProfileInput{Type: "url", Label: "Synthetic", URL: "http://public.test/profile", ExtendType: "standard", AutoUpdate: auto, AutoUpdateDurationMillis: 3600000, SelectedMap: map[string]string{}, UnfoldSet: []string{}})
		if err != nil {
			return nil, err
		}
		id = p.ID
		old := clock.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano)
		p.LastUpdateDate = &old
		return p, tx.SaveProfile(p)
	})
	return id
}
func webSchedulerNew(t *testing.T, store *WebAPIStore, clock *webSchedulerFakeClock, backend WebAPISchedulerBackend) *WebAPIScheduler {
	t.Helper()
	s, err := NewWebAPIScheduler(store, WebAPISchedulerOptions{Clock: clock, InternalContext: webSchedulerInternalContext(), Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}
func webSchedulerTargetStatus(t *testing.T, s *WebAPIScheduler, kind WebAPISchedulerKind, id string) WebAPISchedulerTarget {
	t.Helper()
	_, status, err := s.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range status.Targets {
		if v.Kind == kind && v.ID == id {
			return v
		}
	}
	t.Fatalf("missing typed scheduler target %s", kind)
	return WebAPISchedulerTarget{}
}
func webSchedulerDone(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not settle")
		return nil
	}
}
func webSchedulerSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not reach the controlled barrier")
	}
}
func webSchedulerFakeCommit(store *WebAPIStore, clock *webSchedulerFakeClock) WebAPISchedulerCallback {
	return func(ctx context.Context, task WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
		receipt, err := store.Transaction(ctx, task.Mutation, "scheduler-fixture-refresh", task, func(tx *WebAPIWriteTx) (any, error) {
			p, err := tx.Profile(WebAPIID(task.ID))
			if err != nil {
				return nil, err
			}
			now := clock.Now().Format(time.RFC3339Nano)
			p.LastUpdateDate = &now
			return p, tx.SaveProfile(p)
		})
		return WebAPISchedulerEffect{Revision: receipt.Revision, OperationID: receipt.OperationID, Phase: "completed", RequiresApply: true}, err
	}
}

func TestWebAPISchedulerProfileIntervalAndDesiredSwitch(t *testing.T) {
	// build/api-scheduler-contract.md: desired values survive server-owned scheduling.
	clock := newWebSchedulerFakeClock()
	store := newWebSchedulerStore(t, clock)
	id := webSchedulerSeedProfile(t, store, clock, true)
	var calls atomic.Int32
	commit := webSchedulerFakeCommit(store, clock)
	s := webSchedulerNew(t, store, clock, WebAPISchedulerBackend{RefreshContent: func(ctx context.Context, task WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
		calls.Add(1)
		return commit(ctx, task)
	}})
	if err := s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	state := webSchedulerTargetStatus(t, s, WebAPISchedulerProfile, string(id))
	if calls.Load() != 1 || state.Phase != "idle" || !state.RequiresApply || state.LastSuccess == nil {
		t.Fatal(state)
	}
	if err := s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour - time.Nanosecond)
	if err := s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("profile interval was ignored")
	}
	clock.Advance(time.Nanosecond)
	if err := s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("due profile did not refresh")
	}
	_, err := store.View(t.Context(), func(tx *WebAPIReadTx) error {
		p, err := tx.Profile(id)
		if !p.AutoUpdate || p.AutoUpdateDurationMillis != 3600000 {
			t.Error("scheduler rewrote desired profile settings")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebAPISchedulerFailedBackoffUsesOnlyFakeClock(t *testing.T) {
	clock := newWebSchedulerFakeClock()
	store := newWebSchedulerStore(t, clock)
	id := webSchedulerSeedProfile(t, store, clock, true)
	var calls atomic.Int32
	s := webSchedulerNew(t, store, clock, WebAPISchedulerBackend{RefreshContent: func(context.Context, WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
		calls.Add(1)
		return WebAPISchedulerEffect{}, webAPIError(502, "source_fetch_failed", "synthetic")
	}})
	for n := 0; n < 3; n++ {
		if err := s.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	state := webSchedulerTargetStatus(t, s, WebAPISchedulerProfile, string(id))
	if calls.Load() != 1 || state.Phase != "backoff" || state.NextAttempt == nil || state.NextAttempt.Sub(clock.Now()) != time.Minute {
		t.Fatal(state)
	}
	clock.Advance(time.Minute)
	if err := s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	state = webSchedulerTargetStatus(t, s, WebAPISchedulerProfile, string(id))
	if calls.Load() != 2 || state.Failures != 2 || state.NextAttempt.Sub(clock.Now()) != 2*time.Minute {
		t.Fatal(state)
	}
}

func TestWebAPISchedulerUnknownAndInterruptedAttemptNeverRedo(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown_receipt", true: "process_interrupted"}[interrupted], func(t *testing.T) {
			clock := newWebSchedulerFakeClock()
			store := newWebSchedulerStore(t, clock)
			id := webSchedulerSeedProfile(t, store, clock, true)
			var calls atomic.Int32
			s := webSchedulerNew(t, store, clock, WebAPISchedulerBackend{RefreshContent: func(context.Context, WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
				calls.Add(1)
				return WebAPISchedulerEffect{}, webAPIError(503, "durability_unknown", "synthetic")
			}})
			if interrupted {
				snapshot, err := s.snapshot(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				for _, target := range snapshot.targets {
					if target.status.Kind == WebAPISchedulerProfile {
						if err = s.saveAttempt(s.ctx, target.key(), webSchedulerAttempt{Fingerprint: target.fingerprint, OperationID: "interrupted-scheduler", Phase: "inflight", ExpectedRevision: snapshot.revision}); err != nil {
							t.Fatal(err)
						}
					}
				}
			} else {
				if err := s.RunOnce(t.Context()); err == nil {
					t.Fatal("unknown effect was reported completed")
				}
			}
			clock.Advance(365 * 24 * time.Hour)
			for n := 0; n < 3; n++ {
				if err := s.RunOnce(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			state := webSchedulerTargetStatus(t, s, WebAPISchedulerProfile, string(id))
			want := int32(1)
			if interrupted {
				want = 0
			}
			if calls.Load() != want || state.Phase != "unknown" || state.NextAttempt != nil {
				t.Fatal(state)
			}
		})
	}
}

func TestWebAPISchedulerSafeModeAndMissingAuthorityCannotDial(t *testing.T) {
	for _, safe := range []bool{true, false} {
		t.Run(map[bool]string{true: "safe_mode", false: "missing_internal_authority"}[safe], func(t *testing.T) {
			clock := newWebSchedulerFakeClock()
			store := newWebSchedulerStore(t, clock)
			id := webSchedulerSeedProfile(t, store, clock, true)
			var calls atomic.Int32
			options := WebAPISchedulerOptions{Clock: clock, SafeMode: safe, Backend: WebAPISchedulerBackend{RefreshContent: func(context.Context, WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
				calls.Add(1)
				return WebAPISchedulerEffect{}, nil
			}}}
			if safe {
				options.InternalContext = webSchedulerInternalContext()
			}
			s, err := NewWebAPIScheduler(store, options)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			_ = s.Start()
			_ = s.RunOnce(t.Context())
			clock.Advance(24 * time.Hour)
			state := webSchedulerTargetStatus(t, s, WebAPISchedulerProfile, string(id))
			if calls.Load() != 0 || state.Phase != "blocked" || !state.Enabled {
				t.Fatal(state)
			}
		})
	}
}

func TestWebAPISchedulerCloseDoesNotWaitForBlockedCallback(t *testing.T) {
	clock := newWebSchedulerFakeClock()
	store := newWebSchedulerStore(t, clock)
	id := webSchedulerSeedProfile(t, store, clock, true)
	entered, release := make(chan struct{}), make(chan struct{})
	s := webSchedulerNew(t, store, clock, WebAPISchedulerBackend{RefreshContent: func(ctx context.Context, task WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
		close(entered)
		<-release
		return WebAPISchedulerEffect{}, ctx.Err()
	}})
	done := make(chan error, 1)
	go func() { done <- s.RunOnce(t.Context()) }()
	webSchedulerSignal(t, entered)
	s.Close()
	s.Close()
	if s.ctx.Err() == nil {
		t.Fatal("Close did not cancel owned work")
	}
	if err := s.RunOnce(t.Context()); err == nil {
		t.Fatal("closed scheduler accepted another runner")
	}
	close(release)
	if err := webSchedulerDone(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel result %v", err)
	}
	if state := webSchedulerTargetStatus(t, s, WebAPISchedulerProfile, string(id)); state.Phase != "unknown" {
		t.Fatal(state)
	}
}

func TestWebAPISchedulerTimeoutAndCancellationDoNotRetry(t *testing.T) {
	clock := newWebSchedulerFakeClock()
	store := newWebSchedulerStore(t, clock)
	id := webSchedulerSeedProfile(t, store, clock, true)
	entered := make(chan struct{})
	var calls atomic.Int32
	s := webSchedulerNew(t, store, clock, WebAPISchedulerBackend{RefreshContent: func(ctx context.Context, task WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		return WebAPISchedulerEffect{}, ctx.Err()
	}})
	done := make(chan error, 1)
	go func() { done <- s.RunOnce(t.Context()) }()
	webSchedulerSignal(t, entered)
	clock.Advance(90 * time.Second)
	if err := webSchedulerDone(t, done); err == nil {
		t.Fatal("expired effect was completed")
	}
	if state := webSchedulerTargetStatus(t, s, WebAPISchedulerProfile, string(id)); state.Phase != "unknown" {
		t.Fatal(state)
	}
	if err := s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("expired effect was automatically replayed")
	}
}

func TestWebAPISchedulerPendingBackendCannotClaimUpdated(t *testing.T) {
	clock := newWebSchedulerFakeClock()
	store := newWebSchedulerStore(t, clock)
	id := webSchedulerSeedProfile(t, store, clock, true)
	s := webSchedulerNew(t, store, clock, WebAPISchedulerBackend{RefreshContent: func(_ context.Context, task WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
		return WebAPISchedulerEffect{Revision: task.Mutation.ExpectedRevision, OperationID: task.Mutation.OperationID, Phase: "pending"}, nil
	}})
	if err := s.RunOnce(t.Context()); err == nil {
		t.Fatal("accepted pending was promoted to updated")
	}
	if state := webSchedulerTargetStatus(t, s, WebAPISchedulerProfile, string(id)); state.LastSuccess != nil || state.Phase != "unknown" {
		t.Fatal(state)
	}
}
