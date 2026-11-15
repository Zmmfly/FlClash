//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type webAdmissionBody struct {
	io.Reader
	reads atomic.Int64
}

func (b *webAdmissionBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	return b.Reader.Read(p)
}
func (*webAdmissionBody) Close() error { return nil }

type webAdmissionWriter struct {
	*httptest.ResponseRecorder
	ctx     context.Context
	entered chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (w *webAdmissionWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { w.entered <- struct{}{} })
	select {
	case <-w.ctx.Done():
		return 0, w.ctx.Err()
	case <-w.release:
		return w.ResponseRecorder.Write(p)
	}
}

func webAdmissionRequest(f *webAPIIntegration, method, path string, cookies []*http.Cookie, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, f.http.URL+path, body)
	r.Header.Set("Origin", f.http.URL)
	r.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	return r
}

func webAdmissionWait(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(8 * time.Second):
		t.Fatal("HTTP admission fixture did not reach its deterministic boundary")
	}
}

func webAdmissionBusy(t *testing.T, w *httptest.ResponseRecorder, lane string) {
	t.Helper()
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("admission status %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		Error struct {
			Code    string            `json:"code"`
			Details map[string]string `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Error.Code != "api_busy" || result.Error.Details["state"] != "not_accepted" || result.Error.Details["lane"] != lane {
		t.Fatalf("untyped admission rejection: %s (%v)", w.Body.String(), err)
	}
}

func TestWebAPIAdmissionRejectsBeforeBodyAndPrivateStoreInitialization(t *testing.T) {
	root := t.TempDir()
	s := &WebServer{opts: WebServerOptions{DataDir: filepath.Join(root, "managed"), APIStoreDir: filepath.Join(root, "private")}}
	releases := []func(){}
	for range webAPIHeavyCapacity {
		release, err := s.acquireAPIAdmission(context.Background(), false)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, release := range releases {
			release()
			release()
		}
	}()
	body := &webAdmissionBody{Reader: bytes.NewBufferString(`{"expectedRevision":"0","operationId":"admission-rejected"}`)}
	r := httptest.NewRequest(http.MethodPost, "http://panel.test/api/v1/profiles:create", body)
	r.Header.Set("Origin", "http://panel.test")
	w := httptest.NewRecorder()
	s.serveAPI(w, r)
	webAdmissionBusy(t, w, "heavy")
	if body.reads.Load() != 0 || s.api != nil {
		t.Fatal("rejected request read its body or initialized the private services")
	}
	if _, err := os.Stat(s.opts.APIStoreDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected request touched the private store: %v", err)
	}
	if err := s.closeAPI(); err != nil {
		t.Fatal(err)
	}
}

func TestWebAPIAdmissionControlClassificationIsExact(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		control      bool
	}{
		{http.MethodGet, "/api/v1/meta", true},
		{http.MethodHead, "/api/v1/meta", true},
		{http.MethodGet, "/api/v1/runtime/status", true},
		{http.MethodGet, "/api/v1/operations/admission-operation", true},
		{http.MethodPost, "/api/v1/cache-operations/admission-operation:resolve", true},
		{http.MethodGet, "/api/v1/source-operations/admission-operation", true},
		{http.MethodGet, "/api/v1/runtime/operations/admission-operation", true},
		{http.MethodPost, "/api/v1/runtime/operations/admission-operation:resolve", false},
		{http.MethodGet, "/api/v1/operations/../profiles", false},
		{http.MethodGet, "/api/v1/runtime/proxies", false},
		{http.MethodGet, "/api/v1/runtime/applied-counts", false},
		{http.MethodGet, "/api/v1/content/profile/1/download", false},
		{http.MethodPost, "/api/v1/sources:prepare", false},
		{http.MethodPost, "/api/v1/meta", false},
	} {
		r := httptest.NewRequest(tc.method, "http://panel.test"+tc.path, nil)
		if actual := webAPIControlRequest(r); actual != tc.control {
			t.Errorf("%s %s control=%v", tc.method, tc.path, actual)
		}
	}
}

func TestWebAPIAdmissionHeavySlotsAreGlobalAndReserveLogoutAndControl(t *testing.T) {
	f := newWebAPIIntegration(t)
	u, _ := url.Parse(f.http.URL)
	first := f.client.Jar.Cookies(u)
	login := httptest.NewRequest(http.MethodPost, f.http.URL+"/api/auth/login", bytes.NewBufferString(`{"username":"api-fixture","password":"synthetic-api-fixture"}`))
	login.Header.Set("Origin", f.http.URL)
	login.Header.Set("Content-Type", "application/json")
	loginResult := httptest.NewRecorder()
	f.server.ServeHTTP(loginResult, login)
	if loginResult.Code != 200 {
		t.Fatalf("second independent login: %d %s", loginResult.Code, loginResult.Body.String())
	}
	second := loginResult.Result().Cookies()
	entered := make(chan struct{}, webAPIHeavyCapacity)
	release := make(chan struct{})
	defer close(release)
	cancels := []context.CancelFunc{}
	done := []chan struct{}{}
	writers := []*webAdmissionWriter{}
	for i := range webAPIHeavyCapacity {
		cookies := first
		if i%2 != 0 {
			cookies = second
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		finished := make(chan struct{})
		done = append(done, finished)
		r := webAdmissionRequest(f, http.MethodGet, "/api/v1/profiles", cookies, nil).WithContext(ctx)
		w := &webAdmissionWriter{ResponseRecorder: httptest.NewRecorder(), ctx: ctx, entered: entered, release: release}
		writers = append(writers, w)
		go func() { defer close(finished); f.server.ServeHTTP(w, r) }()
	}
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
		for _, finished := range done {
			webAdmissionWait(t, finished)
		}
	}()
	for range webAPIHeavyCapacity {
		webAdmissionWait(t, entered)
	}
	for _, writer := range writers {
		if writer.Code != 200 {
			t.Fatalf("fixture was rejected before API admission: %d", writer.Code)
		}
	}
	if len(f.server.apiHeavySlots) != webAPIHeavyCapacity {
		t.Fatal("fixture did not occupy every heavy slot")
	}
	body := &webAdmissionBody{Reader: bytes.NewBufferString(`{"expectedRevision":"0","operationId":"admission-rejected-rule","rule":{"ruleAction":"DOMAIN_SUFFIX","content":"example.test","ruleTarget":"DIRECT"}}`)}
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, webAdmissionRequest(f, http.MethodPost, "/api/v1/rules:create", second, body))
	webAdmissionBusy(t, w, "heavy")
	if body.reads.Load() != 0 {
		t.Fatal("fifth request body was read")
	}
	meta := httptest.NewRecorder()
	f.server.ServeHTTP(meta, webAdmissionRequest(f, http.MethodGet, "/api/v1/meta", second, nil))
	if meta.Code != 200 || !bytes.Contains(meta.Body.Bytes(), []byte(`"revision":"0"`)) {
		t.Fatalf("reserved control lane was blocked or a rejected intent committed: %d %s", meta.Code, meta.Body.String())
	}
	logout := httptest.NewRecorder()
	f.server.ServeHTTP(logout, webAdmissionRequest(f, http.MethodPost, "/api/auth/logout", first, nil))
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout blocked: %d", logout.Code)
	}
	cancels[0]()
	webAdmissionWait(t, done[0])
	free := httptest.NewRecorder()
	f.server.ServeHTTP(free, webAdmissionRequest(f, http.MethodGet, "/api/v1/profiles", second, nil))
	if free.Code != 200 {
		t.Fatalf("cancelled slot was not reclaimed: %d %s", free.Code, free.Body.String())
	}
}

func TestWebAPIAdmissionControlNinthRejectsBeforeResolveAndHeavyStillWorks(t *testing.T) {
	f := newWebAPIIntegration(t)
	u, _ := url.Parse(f.http.URL)
	cookies := f.client.Jar.Cookies(u)
	entered := make(chan struct{}, webAPIControlCapacity)
	release := make(chan struct{})
	defer close(release)
	cancels := []context.CancelFunc{}
	done := []chan struct{}{}
	for range webAPIControlCapacity {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		finished := make(chan struct{})
		done = append(done, finished)
		r := webAdmissionRequest(f, http.MethodGet, "/api/v1/meta", cookies, nil).WithContext(ctx)
		w := &webAdmissionWriter{ResponseRecorder: httptest.NewRecorder(), ctx: ctx, entered: entered, release: release}
		go func() { defer close(finished); f.server.ServeHTTP(w, r) }()
	}
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
		for _, finished := range done {
			webAdmissionWait(t, finished)
		}
	}()
	for range webAPIControlCapacity {
		webAdmissionWait(t, entered)
	}
	body := &webAdmissionBody{Reader: bytes.NewBufferString(`{"intent":"resolve-unknown-result"}`)}
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, webAdmissionRequest(f, http.MethodPost, "/api/v1/operations/admission-unresolved:resolve", cookies, body))
	webAdmissionBusy(t, w, "control")
	if body.reads.Load() != 0 {
		t.Fatal("ninth request consumed resolve body")
	}
	heavy := httptest.NewRecorder()
	f.server.ServeHTTP(heavy, webAdmissionRequest(f, http.MethodGet, "/api/v1/profiles", cookies, nil))
	if heavy.Code != 200 {
		t.Fatalf("heavy lane shared control capacity: %d", heavy.Code)
	}
	cancels[0]()
	webAdmissionWait(t, done[0])
	status := httptest.NewRecorder()
	f.server.ServeHTTP(status, webAdmissionRequest(f, http.MethodGet, "/api/v1/operations/admission-unresolved", cookies, nil))
	if status.Code != http.StatusNotFound {
		t.Fatalf("rejected resolve persisted a tombstone: %d %s", status.Code, status.Body.String())
	}
}
