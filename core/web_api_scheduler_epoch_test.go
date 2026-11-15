//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebAPISchedulerRestoreEpochIsolatesIdenticalIDAndLateOldCallback(t *testing.T) {
	clock := newWebSchedulerFakeClock()
	store := newWebSchedulerStore(t, clock)
	id := webSchedulerSeedProfile(t, store, clock, true)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	commit := webSchedulerFakeCommit(store, clock)
	s := webSchedulerNew(t, store, clock, WebAPISchedulerBackend{RefreshContent: func(ctx context.Context, task WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return commit(ctx, task)
	}})
	before, err := s.snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.RunOnce(t.Context()) }()
	webSchedulerSignal(t, entered)
	webSchedulerMutate(t, store, "scheduler-identical-restore", func(tx *WebAPIWriteTx) (any, error) {
		if err := tx.ResetAllSourceGrants(); err != nil {
			return nil, err
		}
		if err := tx.ResetSchedulerAttempts(); err != nil {
			return nil, err
		}
		p, err := tx.Profile(id)
		if err != nil {
			return nil, err
		}
		return p, tx.SaveProfile(p)
	})
	after, err := s.snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if before.epoch == after.epoch || after.epoch == "" {
		t.Fatal("restore reused scheduler authority generation")
	}
	close(release)
	if err = webSchedulerDone(t, done); err == nil {
		t.Fatal("late old callback crossed restore epoch")
	}
	state := webSchedulerTargetStatus(t, s, WebAPISchedulerProfile, string(id))
	if state.Phase != "due" || state.LastSuccess != nil || state.OperationID != "" {
		t.Fatal("old attempt leaked into restored identity", state)
	}
	if err = s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("new restored identity was blocked by an old attempt")
	}
}

func TestWebAPISchedulerHugeNativeIntervalCannotOverflowIntoImmediateDownload(t *testing.T) {
	clock := newWebSchedulerFakeClock()
	store := newWebSchedulerStore(t, clock)
	id := webSchedulerSeedProfile(t, store, clock, true)
	webSchedulerMutate(t, store, "scheduler-long-interval", func(tx *WebAPIWriteTx) (any, error) {
		p, err := tx.Profile(id)
		if err != nil {
			return nil, err
		}
		p.AutoUpdateDurationMillis = 9007199254740991
		now := clock.Now().Format(time.RFC3339Nano)
		p.LastUpdateDate = &now
		return p, tx.SaveProfile(p)
	})
	var calls atomic.Int32
	s := webSchedulerNew(t, store, clock, WebAPISchedulerBackend{RefreshContent: func(context.Context, WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
		calls.Add(1)
		return WebAPISchedulerEffect{}, nil
	}})
	if err := s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, status, err := s.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = json.Marshal(status); err != nil {
		t.Fatal("long due time made typed HTTP unencodable", err)
	}
	state := webSchedulerTargetStatus(t, s, WebAPISchedulerProfile, string(id))
	if calls.Load() != 0 || state.Phase != "idle" {
		t.Fatal("duration overflow caused a download", state)
	}
}
