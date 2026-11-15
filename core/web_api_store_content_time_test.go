//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	bolt "github.com/metacubex/bbolt"
)

func TestWebAPIContentModifiedAtIsServerClockAndReplayDoesNotRestamp(t *testing.T) {
	now := time.Date(2026, 11, 15, 8, 0, 0, 123456789, time.FixedZone("test", 3600))
	calls := 0
	store, err := OpenWebAPIStore(WebAPIStoreOptions{DataDir: filepath.Join(t.TempDir(), "data"), Now: func() time.Time { calls++; return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	content := WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: "1"}, MediaType: "text/yaml", Bytes: []byte("rules: []"), ModifiedAt: "1900-01-01T00:00:00Z"}
	request := WebAPIMutation{"0", "content-server-time"}
	first, err := store.Transaction(context.Background(), request, "test:content", content, func(tx *WebAPIWriteTx) (any, error) {
		if _, err := tx.CreateProfile(WebAPIProfileInput{Type: "file"}); err != nil {
			return nil, err
		}
		if err := tx.PutContent(content); err != nil {
			return nil, err
		}
		return tx.Content(content.Key)
	})
	if err != nil {
		t.Fatal(err)
	}
	stored := webAPITestData[WebAPIContent](t, first.Body).Data
	if stored.ModifiedAt != now.UTC().Format(time.RFC3339Nano) || calls != 1 {
		t.Fatal(stored.ModifiedAt, calls)
	}
	now = now.Add(time.Hour)
	replay, err := store.Transaction(context.Background(), request, "test:content", content, func(*WebAPIWriteTx) (any, error) { t.Fatal("replay wrote content"); return nil, nil })
	if err != nil || !bytes.Equal(first.Body, replay.Body) || calls != 1 {
		t.Fatal(replay, err, calls)
	}
	_, err = store.Transaction(context.Background(), WebAPIMutation{"1", "content-restore-new-time"}, "test:restore", stored, func(tx *WebAPIWriteTx) (any, error) { return struct{}{}, tx.PutContent(stored) })
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.View(context.Background(), func(tx *WebAPIReadTx) error {
		value, err := tx.Content(content.Key)
		if value.ModifiedAt != now.UTC().Format(time.RFC3339Nano) || calls != 2 {
			t.Fatal(value.ModifiedAt, calls)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.db.Update(func(tx *bolt.Tx) error {
		return webAPIPut(tx, "content", "profile:1:", WebAPIContent{Key: content.Key, MediaType: content.MediaType, Bytes: content.Bytes})
	}); err != nil {
		t.Fatal(err)
	}
	_, err = store.View(context.Background(), func(tx *WebAPIReadTx) error {
		old, err := tx.Content(content.Key)
		if old.ModifiedAt != "" {
			t.Fatal("legacy content was assigned a fictitious timestamp")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
