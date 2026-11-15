//go:build webpanel && !android

package core

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	bolt "github.com/metacubex/bbolt"
)

func TestWebAPISchedulerCommitUncertaintySurvivesReopenWithoutRedo(t *testing.T) {
	clock := newWebSchedulerFakeClock()
	options := WebAPIStoreOptions{DataDir: filepath.Join(t.TempDir(), "managed"), Now: clock.Now}
	store, err := OpenWebAPIStore(options)
	if err != nil {
		t.Fatal(err)
	}
	id := webSchedulerSeedProfile(t, store, clock, true)
	var calls atomic.Int32
	commit := webSchedulerFakeCommit(store, clock)
	s := webSchedulerNew(t, store, clock, WebAPISchedulerBackend{RefreshContent: func(ctx context.Context, task WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
		calls.Add(1)
		store.mu.Lock()
		store.commit = func(tx *bolt.Tx) error {
			if err := tx.Commit(); err != nil {
				return err
			}
			return errors.New("synthetic sync response loss")
		}
		store.mu.Unlock()
		return commit(ctx, task)
	}})
	if err = s.RunOnce(t.Context()); err == nil {
		t.Fatal("uncertain commit was reported completed")
	}
	s.Close()
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenWebAPIStore(options)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reopened := webSchedulerNew(t, store, clock, WebAPISchedulerBackend{RefreshContent: func(context.Context, WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
		calls.Add(1)
		return WebAPISchedulerEffect{}, nil
	}})
	if err = reopened.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	state := webSchedulerTargetStatus(t, reopened, WebAPISchedulerProfile, string(id))
	if calls.Load() != 1 || state.Phase != "unknown" || state.LastSuccess != nil {
		t.Fatal("uncertain effect was automatically redone", state)
	}
}

func TestWebAPISchedulerChainsOnlyItsOwnConfirmedRevision(t *testing.T) {
	clock := newWebSchedulerFakeClock()
	store := newWebSchedulerStore(t, clock)
	webSchedulerMutate(t, store, "scheduler-two-profiles", func(tx *WebAPIWriteTx) (any, error) {
		for _, label := range []string{"First", "Second"} {
			if _, err := tx.CreateProfile(WebAPIProfileInput{Type: "url", Label: label, URL: "http://public.test/profile", ExtendType: "standard", AutoUpdate: true, AutoUpdateDurationMillis: 3600000, SelectedMap: map[string]string{}, UnfoldSet: []string{}}); err != nil {
				return nil, err
			}
		}
		return true, nil
	})
	var calls atomic.Int32
	commit := webSchedulerFakeCommit(store, clock)
	s := webSchedulerNew(t, store, clock, WebAPISchedulerBackend{RefreshContent: func(ctx context.Context, task WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
		calls.Add(1)
		return commit(ctx, task)
	}})
	if err := s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || webSchedulerRevision(t, store) != "3" {
		t.Fatal("own confirmed commits did not advance the bounded round")
	}
}
