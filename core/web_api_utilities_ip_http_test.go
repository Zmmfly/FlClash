//go:build webpanel && !android

package core

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	bolt "github.com/metacubex/bbolt"
)

func TestWebUtilityHTTPIPConcurrentTwoSessionsDeduplicateAndRevocationCannotCommit(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f := newWebUtilityHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Host, "ip-api.com") {
			once.Do(func() { close(started) })
			<-release
			io.WriteString(w, `{"status":"success","hosting":true}`)
			return
		}
		w.WriteHeader(429)
	})
	results := make(chan int, 2)
	go func() {
		status, _ := f.request(t, f.clients[0], "POST", "/api/v1/ip-quality:lookup", WebAPIUtilityIPLookup{IP: "8.8.8.8"})
		results <- status
	}()
	select {
	case <-started:
	case status := <-results:
		t.Fatalf("provider was not entered before HTTP completed with %d", status)
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start within the fixture budget")
	}
	go func() {
		status, _ := f.request(t, f.clients[1], "POST", "/api/v1/ip-quality:lookup", WebAPIUtilityIPLookup{IP: "8.8.8.8"})
		results <- status
	}()
	close(release)
	for i := 0; i < 2; i++ {
		if status := <-results; status != 200 {
			t.Fatalf("concurrent lookup %d", status)
		}
	}
	f.mu.Lock()
	calls := f.counts["ip-api.com/json/8.8.8.8"]
	f.mu.Unlock()
	if calls != 1 {
		t.Fatalf("two sessions duplicated lookup: %d", calls)
	}

	started, release = make(chan struct{}), make(chan struct{})
	once = sync.Once{}
	go func() {
		status, _ := f.request(t, f.clients[0], "POST", "/api/v1/ip-quality:lookup", WebAPIUtilityIPLookup{IP: "1.1.1.1"})
		results <- status
	}()
	select {
	case <-started:
	case status := <-results:
		t.Fatalf("provider was not entered before HTTP completed with %d", status)
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start within the fixture budget")
	}
	if status, body := f.request(t, f.clients[0], "POST", "/api/auth/logout", map[string]any{}); status != 204 {
		t.Fatalf("logout %d: %s", status, body)
	}
	close(release)
	if status := <-results; status != 401 {
		t.Fatalf("revoked lookup committed or published: %d", status)
	}
	_, err := f.store.CacheView(context.Background(), func(tx *WebAPICacheTx) error {
		rows, err := tx.IPQualities()
		for _, row := range rows {
			if row.IP == "1.1.1.1" {
				t.Error("revoked query wrote derived cache")
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebUtilityHTTPIPDerivedCacheFailureIsAtomicAndUnknownSyncReopens(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "commit-response-unknown"}[committed], func(t *testing.T) {
			f := newWebUtilityHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.Host, "ip-api.com") {
					io.WriteString(w, `{"status":"success","mobile":true}`)
				} else {
					w.WriteHeader(429)
				}
			})
			f.store.commit = func(tx *bolt.Tx) error {
				if committed {
					if err := tx.Commit(); err != nil {
						return err
					}
				} else {
					if err := tx.Rollback(); err != nil {
						return err
					}
				}
				return errors.New("injected utility cache sync uncertainty")
			}
			status, body := f.request(t, f.clients[0], "POST", "/api/v1/ip-quality:lookup", WebAPIUtilityIPLookup{IP: "8.8.4.4"})
			if status < 500 {
				t.Fatalf("uncertain cache commit reported success %d: %s", status, body)
			}
			if err := f.store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := OpenWebAPIStore(f.storeOptions)
			if err != nil {
				t.Fatal(err)
			}
			f.store = store
			f.service.store = store
			revision, err := store.CacheView(context.Background(), func(tx *WebAPICacheTx) error {
				rows, err := tx.IPQualities()
				expected := 0
				if committed {
					expected = 1
				}
				if len(rows) != expected {
					t.Fatalf("partially stored quality count %d, expected %d", len(rows), expected)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if committed && revision != "1" || !committed && revision != "0" {
				t.Fatalf("wrong derived revision after reopen %s", revision)
			}
			business, err := store.View(context.Background(), func(*WebAPIReadTx) error { return nil })
			if err != nil || business != "0" {
				t.Fatalf("business state changed with derived cache %s %v", business, err)
			}
			before := f.totalFetches()
			if status, body := f.request(t, f.clients[1], "POST", "/api/v1/ip-quality:lookup", WebAPIUtilityIPLookup{IP: "8.8.4.4"}); status != 200 {
				t.Fatalf("reopened lookup %d: %s", status, body)
			}
			if committed && f.totalFetches() != before {
				t.Fatal("known fresh derived cache refetched after lost response")
			}
		})
	}
}
