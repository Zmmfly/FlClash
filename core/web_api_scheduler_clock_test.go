//go:build webpanel && !android

package core

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebAPISchedulerFakeClockTimerRunsWithoutAnyBrowser(t *testing.T) {
	clock := newWebSchedulerFakeClock()
	store := newWebSchedulerStore(t, clock)
	webSchedulerSeedProfile(t, store, clock, true)
	var calls atomic.Int32
	committed := make(chan struct{}, 4)
	commit := webSchedulerFakeCommit(store, clock)
	s := webSchedulerNew(t, store, clock, WebAPISchedulerBackend{RefreshContent: func(ctx context.Context, task WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
		calls.Add(1)
		effect, err := commit(ctx, task)
		committed <- struct{}{}
		return effect, err
	}})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	wait := func() {
		webSchedulerSignal(t, committed)
		s.mu.Lock()
		flight := s.flight
		s.mu.Unlock()
		if flight != nil {
			webSchedulerSignal(t, flight.done)
			if flight.err != nil {
				t.Fatal(flight.err)
			}
		}
	}
	wait()
	for {
		clock.mu.Lock()
		armed := false
		for _, due := range clock.timers {
			armed = armed || due.Equal(clock.now.Add(s.options.PollInterval))
		}
		clock.mu.Unlock()
		if armed {
			break
		}
		webSchedulerSignal(t, clock.armed)
	}
	clock.Advance(time.Hour)
	wait()
	if calls.Load() != 2 {
		t.Fatal("fake timer did not honor the committed period")
	}
	s.Close()
	clock.Advance(24 * time.Hour)
	if calls.Load() != 2 {
		t.Fatal("closed timer started more work")
	}
}

func TestWebAPISchedulerCancelFlightRetainsUnknownAndDoesNotRedo(t *testing.T) {
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
	if !s.CancelFlight() {
		t.Fatal("active flight was not cancelled")
	}
	if err := webSchedulerDone(t, done); err == nil {
		t.Fatal("cancelled work was reported completed")
	}
	if s.CancelFlight() {
		t.Fatal("completed flight was retained")
	}
	clock.Advance(24 * time.Hour)
	if err := s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	state := webSchedulerTargetStatus(t, s, WebAPISchedulerProfile, string(id))
	if calls.Load() != 1 || state.Phase != "unknown" {
		t.Fatal("cancelled effect was retried", state)
	}
}
