//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type webSchedulerHTTPFixture struct {
	server    *httptest.Server
	auth      *WebServer
	cookie    *http.Cookie
	store     *WebAPIStore
	scheduler *WebAPIScheduler
}

func newWebSchedulerHTTPFixture(t *testing.T, store *WebAPIStore, s *WebAPIScheduler, contentOptions ...WebAPIContentOptions) *webSchedulerHTTPFixture {
	t.Helper()
	auth := newWebTestServer(t)
	mux := http.NewServeMux()
	RegisterWebAPISchedulerRoutes(mux, s)
	RegisterWebAPIResourceRoutes(mux, store)
	if len(contentOptions) == 1 {
		RegisterWebAPIContentRoutes(mux, store, contentOptions[0])
	}
	authorized := WebAPIRuntimeSessionHandler(auth, mux)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/auth/") {
			auth.ServeHTTP(w, r)
			return
		}
		if !auth.sameOrigin(r) {
			WebAPIWriteError(w, webAPIError(403, "origin_mismatch", "same origin required"))
			return
		}
		authorized.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return &webSchedulerHTTPFixture{server: server, auth: auth, cookie: webLogin(t, auth), store: store, scheduler: s}
}
func (f *webSchedulerHTTPFixture) request(t *testing.T, method, path string, body any, cookie *http.Cookie) (int, []byte) {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Origin", f.server.URL)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	response, err := f.server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	out, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, out
}
func webSchedulerHTTPData[T any](t *testing.T, status int, body []byte) WebAPIEnvelope[T] {
	t.Helper()
	if status != 200 {
		t.Fatalf("HTTP %d %s", status, body)
	}
	var out WebAPIEnvelope[T]
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestWebAPISchedulerActualHTTPStrictRoutesAuthAndTwoTriggersSameFlight(t *testing.T) {
	clock := newWebSchedulerFakeClock()
	store := newWebSchedulerStore(t, clock)
	webSchedulerSeedProfile(t, store, clock, true)
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
	f := newWebSchedulerHTTPFixture(t, store, s)
	if status, _ := f.request(t, "GET", "/api/v1/scheduler", nil, nil); status != 401 {
		t.Fatalf("anonymous HTTP %d", status)
	}
	status, body := f.request(t, "GET", "/api/v1/scheduler", nil, f.cookie)
	initial := webSchedulerHTTPData[WebAPISchedulerStatus](t, status, body)
	request := WebAPISchedulerTrigger{WebAPIMutation: WebAPIMutation{ExpectedRevision: initial.Revision, OperationID: "scheduler-tab-one"}, Intent: "run-due-updates"}
	status, body = f.request(t, "POST", "/api/v1/scheduler:trigger", request, f.cookie)
	first := webSchedulerHTTPData[WebAPISchedulerAccepted](t, status, body)
	webSchedulerSignal(t, entered)
	request.ExpectedRevision, request.OperationID = first.Revision, "scheduler-tab-two"
	status, body = f.request(t, "POST", "/api/v1/scheduler:trigger", request, f.cookie)
	second := webSchedulerHTTPData[WebAPISchedulerAccepted](t, status, body)
	if first.Data.FlightID != second.Data.FlightID || !second.Data.Coalesced || calls.Load() != 1 {
		t.Fatal(first, second, calls.Load())
	}
	status, replay := f.request(t, "POST", "/api/v1/scheduler:trigger", request, f.cookie)
	if status != 200 || !bytes.Equal(body, replay) || calls.Load() != 1 {
		t.Fatal("coalesced trigger replay duplicated work")
	}
	request.ExpectedRevision = "0"
	request.OperationID = "scheduler-stale-tab"
	if status, raw := f.request(t, "POST", "/api/v1/scheduler:trigger", request, f.cookie); status != 409 || !bytes.Contains(raw, []byte("revision_conflict")) {
		t.Fatalf("stale trigger %d %s", status, raw)
	}
	for _, bad := range []map[string]any{
		{"expectedRevision": second.Revision, "operationId": "scheduler-generic-method", "intent": "run-due-updates", "method": "download"},
		{"expectedRevision": second.Revision, "operationId": "scheduler-generic-url", "intent": "run-due-updates", "url": "http://127.0.0.1/secret"},
		{"expectedRevision": second.Revision, "operationId": "scheduler-generic-function", "intent": "run-due-updates", "function": "refreshAll"},
		{"expectedRevision": second.Revision, "operationId": "scheduler-generic-path", "intent": "run-due-updates", "path": "/private/resources.db"},
		{"expectedRevision": second.Revision, "operationId": "scheduler-no-intent", "intent": "force"},
	} {
		if status, _ := f.request(t, "POST", "/api/v1/scheduler:trigger", bad, f.cookie); status != 400 {
			t.Fatalf("generic trigger was accepted: %d", status)
		}
	}
	if status, _ := f.request(t, "GET", "/api/v1/scheduler?url=file:///secret", nil, f.cookie); status != 400 {
		t.Fatal(status)
	}
	if status, _ := f.request(t, "POST", "/api/v1/scheduler", nil, f.cookie); status != 405 {
		t.Fatal(status)
	}
	if status, _ := f.request(t, "GET", "/api/v1/scheduler:trigger", nil, f.cookie); status != 405 {
		t.Fatal(status)
	}
	s.mu.Lock()
	flight := s.flight
	s.mu.Unlock()
	close(release)
	webSchedulerSignal(t, flight.done)
	if flight.err == nil {
		t.Fatal("concurrent trigger revision was silently fresh-stamped after fetch")
	}
	clock.Advance(time.Minute)
	if err := s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("same-flight triggers queued an extra download")
	}
}

func TestWebAPISchedulerPublicBackgroundSurvivesLogoutWithoutRetainingSession(t *testing.T) {
	clock := newWebSchedulerFakeClock()
	store := newWebSchedulerStore(t, clock)
	id := webSchedulerSeedProfile(t, store, clock, true)
	entered, release := make(chan struct{}), make(chan struct{})
	commit := webSchedulerFakeCommit(store, clock)
	s := webSchedulerNew(t, store, clock, WebAPISchedulerBackend{RefreshContent: func(ctx context.Context, task WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
		if WebAPIRequestSessionID(ctx) != "" || !WebAPIInternalAuthorized(ctx) {
			t.Error("background reused browser authority")
		}
		close(entered)
		<-release
		return commit(ctx, task)
	}})
	f := newWebSchedulerHTTPFixture(t, store, s)
	done := make(chan error, 1)
	go func() { done <- s.RunOnce(t.Context()) }()
	webSchedulerSignal(t, entered)
	if status, _ := f.request(t, "POST", "/api/auth/logout", nil, f.cookie); status != 204 {
		t.Fatal(status)
	}
	if status, _ := f.request(t, "GET", "/api/v1/scheduler", nil, f.cookie); status != 401 {
		t.Fatal("logged-out scheduler status remained public")
	}
	close(release)
	if err := webSchedulerDone(t, done); err != nil {
		t.Fatal(err)
	}
	state := webSchedulerTargetStatus(t, s, WebAPISchedulerProfile, string(id))
	if state.LastSuccess == nil || !state.RequiresApply {
		t.Fatal("logout incorrectly revoked independent public background authority", state)
	}
}
