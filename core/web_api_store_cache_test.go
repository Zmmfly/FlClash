//go:build webpanel && !android

package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	bolt "github.com/metacubex/bbolt"
)

func TestWebAPIDerivedCacheSharesCASWithoutConfigMutation(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := context.Background()
	value := WebAPIIconRecord{URL: "https://example.invalid/icon", LastAccessed: "1"}
	revision, err := r.store.DerivedCacheUpdate(ctx, func(tx *WebAPICacheTx) (bool, error) { return true, tx.PutIconRecord(value) })
	if err != nil || revision != "1" {
		t.Fatal(revision, err)
	}
	_, err = r.store.CacheTransaction(ctx, WebAPIMutation{"0", "derived-stale-cas"}, "icons:delete", value.URL, func(tx *WebAPICacheTx) (any, error) { return nil, tx.DeleteIconRecord(value.URL) })
	webAPITestErrorCode(t, err, "revision_conflict")
	receipt, err := r.store.CacheTransaction(ctx, WebAPIMutation{"1", "derived-explicit-cas"}, "icons:delete", value.URL, func(tx *WebAPICacheTx) (any, error) { return struct{}{}, tx.DeleteIconRecord(value.URL) })
	if err != nil || receipt.Revision != "2" {
		t.Fatal(receipt, err)
	}
	revision, err = r.store.DerivedCacheUpdate(ctx, func(tx *WebAPICacheTx) (bool, error) {
		if err := tx.PutIconRecord(value); err != nil {
			return false, err
		}
		return false, nil
	})
	if err != nil || revision != "2" {
		t.Fatal(revision, err)
	}
	_, err = r.store.CacheView(ctx, func(tx *WebAPICacheTx) error {
		items, err := tx.IconRecords()
		if len(items) != 0 {
			t.Fatal("unchanged callback writes were committed")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.DerivedCacheUpdate(ctx, func(tx *WebAPICacheTx) (bool, error) {
		if err := tx.PutIconRecord(value); err != nil {
			return false, err
		}
		return true, errors.New("derived query failed")
	})
	if err == nil {
		t.Fatal("failed derived cache update committed")
	}
	configRev, err := r.store.View(ctx, func(*WebAPIReadTx) error { return nil })
	if err != nil || configRev != "0" || r.commits.Load() != 0 {
		t.Fatal(configRev, err)
	}
}

func TestWebAPIDerivedCacheConcurrentRevisionAndAuthorization(t *testing.T) {
	r := newWebAPITestRig(t)
	var wg sync.WaitGroup
	results := make(chan WebAPIRevision, 12)
	failures := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			revision, err := r.store.DerivedCacheUpdate(context.Background(), func(tx *WebAPICacheTx) (bool, error) {
				return true, tx.PutIconRecord(WebAPIIconRecord{URL: fmt.Sprintf("https://example.invalid/%d", i), LastAccessed: "1"})
			})
			results <- revision
			failures <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(failures)
	seen := map[WebAPIRevision]bool{}
	for revision := range results {
		seen[revision] = true
	}
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 12 {
		t.Fatal(seen)
	}
	revoked := WithWebAPIRequestAuthorization(context.Background(), "revoked", func(context.Context) (func(), error) { return nil, webAPIError(401, "unauthorized", "revoked") })
	_, err := r.store.DerivedCacheUpdate(revoked, func(*WebAPICacheTx) (bool, error) { t.Fatal("revoked derived update executed"); return true, nil })
	webAPITestErrorCode(t, err, "unauthorized")
	if err = r.store.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("cache-meta")).Put([]byte("revision"), []byte("9223372036854775807"))
	}); err != nil {
		t.Fatal(err)
	}
	_, err = r.store.DerivedCacheUpdate(context.Background(), func(tx *WebAPICacheTx) (bool, error) { return true, tx.DeleteIconRecord("https://example.invalid/0") })
	webAPITestErrorCode(t, err, "revision_exhausted")
}
