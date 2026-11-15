//go:build webpanel && !android

package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	goruntime "runtime"
	"sync"
	"testing"
	"time"
)

type webRuntimeBlockedWriter struct {
	header  http.Header
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *webRuntimeBlockedWriter) Header() http.Header { return w.header }
func (*webRuntimeBlockedWriter) WriteHeader(int)       {}
func (w *webRuntimeBlockedWriter) Write(b []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(b), nil
}

func TestWebRuntimeQueryWriteDoesNotHoldLogoutAuthorization(t *testing.T) {
	for _, kind := range []string{"answer", "operation", "profileAnswer"} {
		t.Run(kind, func(t *testing.T) {
			s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
			if kind == "operation" {
				mutation := WebAPIMutation{ExpectedRevision: "0", OperationID: "blocked-query-operation"}
				if _, err := s.store.Transaction(context.Background(), mutation, "test:blocked", mutation, func(tx *WebAPIWriteTx) (any, error) {
					return tx.RecordRuntimeIntent(mutation.OperationID, webRuntimeGC, mutation)
				}); err != nil {
					t.Fatal(err)
				}
			}
			writer := &webRuntimeBlockedWriter{header: http.Header{}, entered: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			defer once.Do(func() { close(writer.release) })
			handler := WebAPIRuntimeSessionHandler(s.auth, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "answer":
					s.runtime.answer(w, r, map[string]bool{"ok": true})
				case "operation":
					s.runtime.operation(w, r)
				default:
					s.runtime.profileAnswer(w, r, "0", map[string]bool{"ok": true})
				}
			}))
			request := httptest.NewRequest("GET", "http://panel.test/api/v1/runtime/operations/blocked-query-operation", nil)
			request.AddCookie(s.cookie)
			done := make(chan struct{})
			go func() { defer close(done); handler.ServeHTTP(writer, request) }()
			select {
			case <-writer.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("query did not reach its write")
			}
			logout := make(chan int, 1)
			go func() { code, _ := s.request(t, "POST", "/api/auth/logout", "", s.cookie, ""); logout <- code }()
			select {
			case code := <-logout:
				if code != 204 {
					t.Fatal(code)
				}
			case <-time.After(time.Second):
				t.Fatal("slow query response blocked logout")
			}
			once.Do(func() { close(writer.release) })
			<-done
		})
	}
}

func TestWebRuntimeExactTxResolverSurvivesQueuedStoreClose(t *testing.T) {
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	profile := "proxy-providers:\n  remote: {type: http, url: 'https://example.test/source'}\nproxy-groups:\n  - {name: G, type: select, proxies: [DIRECT], use: [remote]}\n"
	webRuntimeSeedProfile(t, s, "file", profile)
	var descriptor webRuntimeProviderDescriptor
	if _, err := s.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		items, err := webRuntimeProviderDescriptors(tx, webRuntimeTestProfileID, s.runtime.Events.storeID)
		if err == nil {
			descriptor = items[0]
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	s.runtime.rememberProvider(descriptor)
	entered := make(chan struct{})
	queueWriter := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := s.store.View(context.Background(), func(tx *WebAPIReadTx) error {
			close(entered)
			<-queueWriter
			deadline := time.Now().Add(time.Second)
			for {
				if !s.store.access.TryRLock() {
					break
				}
				s.store.access.RUnlock()
				if time.Now().After(deadline) {
					return errors.New("writer did not queue")
				}
				goruntime.Gosched()
			}
			url, err := s.runtime.ResolveRuntimeSource(context.Background(), tx, WebAPISourceResource{Kind: "external-provider", ID: string(descriptor.Source.ID)})
			if err != nil {
				return err
			}
			if url != "https://example.test/source" {
				return errors.New("wrong source URL")
			}
			return nil
		})
		result <- err
	}()
	<-entered
	closed := make(chan error, 1)
	go func() { close(queueWriter); closed <- s.store.Close() }()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("exact-tx resolver nested a View behind a queued Close")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("store close remained blocked")
	}
}
