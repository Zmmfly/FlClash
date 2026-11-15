//go:build webpanel && !android

package core

import (
	"context"
	"net/netip"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestWebSourceExactTransactionResolverAllowsConcurrentClose(t *testing.T) {
	store, err := OpenWebAPIStore(WebAPIStoreOptions{DataDir: filepath.Join(t.TempDir(), "data")})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	proceed := make(chan struct{})
	service := NewWebAPISourceService(store, WebAPISourceOptions{WebAPISourceFetchOptions: WebAPISourceFetchOptions{LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}}, ResolveRuntimeSource: func(ctx context.Context, tx *WebAPIReadTx, key WebAPISourceResource) (string, error) {
		close(entered)
		<-proceed
		if _, err := tx.Profiles(); err != nil {
			return "", err
		}
		return "https://public.invalid/provider", nil
	}})
	ctx := webSourceTestContext("synthetic-source-session")
	prepared := make(chan error, 1)
	go func() {
		_, _, err := service.Prepare(ctx, WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceExternalProviderRefresh, Key: &WebAPISourceResource{Kind: "external-provider", ID: "opaque-provider"}})
		prepared <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("resolver did not start")
	}
	closed := make(chan error, 1)
	go func() { closed <- store.Close() }()
	deadline := time.Now().Add(time.Second)
	for store.access.TryRLock() {
		store.access.RUnlock()
		if time.Now().After(deadline) {
			close(proceed)
			t.Fatal("close writer did not queue")
		}
		runtime.Gosched()
	}
	close(proceed)
	select {
	case err := <-prepared:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("source resolution deadlocked with store close")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("store close remained blocked by source resolution")
	}
}
