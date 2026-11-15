//go:build webpanel && !android

package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type webRuntimeAdmissionBody struct {
	ctx     context.Context
	entered chan struct{}
	once    sync.Once
	reads   atomic.Int32
}

func (b *webRuntimeAdmissionBody) Read([]byte) (int, error) {
	b.reads.Add(1)
	b.once.Do(func() { close(b.entered) })
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (*webRuntimeAdmissionBody) Close() error { return nil }

func TestWebRuntimePreparationAdmissionBeforeBodyAndCanceledSlotsReclaimed(t *testing.T) {
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	handler := WebAPIRuntimeSessionHandler(s.auth, s.runtime)
	var canceled []context.CancelFunc
	defer func() {
		for _, cancel := range canceled {
			cancel()
		}
	}()
	var done []chan int
	for range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		canceled = append(canceled, cancel)
		body := &webRuntimeAdmissionBody{ctx: ctx, entered: make(chan struct{})}
		request := httptest.NewRequest("POST", "http://panel.test/api/v1/profiles:build", body).WithContext(ctx)
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(s.cookie)
		response := httptest.NewRecorder()
		finished := make(chan int, 1)
		done = append(done, finished)
		go func() { handler.ServeHTTP(response, request); finished <- response.Code }()
		select {
		case <-body.entered:
		case <-time.After(time.Second):
			t.Fatal("one of the two preparations never entered body decoding")
		}
	}
	thirdBody := &webRuntimeAdmissionBody{ctx: context.Background(), entered: make(chan struct{})}
	request := httptest.NewRequest("POST", "http://panel.test/api/v1/runtime:apply", thirdBody)
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(s.cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 429 || !strings.Contains(response.Body.String(), "runtime_preparation_busy") || thirdBody.reads.Load() != 0 {
		t.Fatalf("third preparation decoded or queued its body: %d %s reads=%d", response.Code, response.Body.String(), thirdBody.reads.Load())
	}
	if code, result := s.request(t, "GET", "/api/v1/runtime/state", "", s.cookie, ""); code != 200 {
		t.Fatalf("cheap status consumed a preparation slot %d %s", code, result)
	}
	for i := range canceled {
		canceled[i]()
	}
	for _, finished := range done {
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("canceled decode did not release its slot")
		}
	}
	if len(s.runtime.preparations) != 0 {
		t.Fatal("canceled preparations leaked their slots")
	}
	if code, _ := s.request(t, "POST", "/api/v1/profiles:build", `{}`, s.cookie, ""); code == 429 {
		t.Fatal("reclaimed preparation slot stayed busy")
	}
}

func TestWebRuntimePreparationAdmissionCoversProviderAndDerivedResponse(t *testing.T) {
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	releases := []func(){}
	for range 2 {
		release, err := s.runtime.acquirePreparation(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for _, path := range []string{"/api/v1/profiles/9007199254740993/base", "/api/v1/profiles/9007199254740993/setup-state", "/api/v1/runtime/provider-sources?profileId=9007199254740993", "/api/v1/runtime/provider-sources/123/content", "/api/v1/runtime/provider-sources/123/preview"} {
		if code, body := s.request(t, "GET", path, "", s.cookie, ""); code != 429 {
			t.Fatalf("snapshot parsing bypassed admission %s %d %s", path, code, body)
		}
	}
	for _, path := range []string{"/api/v1/runtime/provider-sources/123:write", "/api/v1/runtime/provider-sources/123:sideload", "/api/v1/runtime/provider-sources:refresh"} {
		if code, body := s.request(t, "POST", path, `{}`, s.cookie, ""); code != 429 {
			t.Fatalf("provider decode bypassed admission %s %d %s", path, code, body)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if release, err := s.runtime.acquirePreparation(ctx); err == nil || release != nil {
		t.Fatal("already canceled preparation acquired a slot")
	}
}

func TestWebRuntimePreparationResponseDoesNotHoldLogoutLease(t *testing.T) {
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	webRuntimeSeedProfile(t, s, "file", webRuntimeRuleSourceYAML)
	source := webRuntimeTestSource(t, s)
	writer := &webRuntimeBlockedWriter{header: http.Header{}, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	defer once.Do(func() { close(writer.release) })
	request := httptest.NewRequest("GET", "http://panel.test/api/v1/runtime/provider-sources/"+string(source.ID), nil)
	request.AddCookie(s.cookie)
	handler := WebAPIRuntimeSessionHandler(s.auth, s.runtime)
	done := make(chan struct{})
	go func() { defer close(done); handler.ServeHTTP(writer, request) }()
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatal("provider response did not reach its write")
	}
	if len(s.runtime.preparations) != 1 {
		t.Fatal("preparation released before its response completed")
	}
	logout := make(chan int, 1)
	go func() { code, _ := s.request(t, "POST", "/api/auth/logout", "", s.cookie, ""); logout <- code }()
	select {
	case code := <-logout:
		if code != 204 {
			t.Fatal(code)
		}
	case <-time.After(time.Second):
		t.Fatal("preparation response held logout authorization")
	}
	once.Do(func() { close(writer.release) })
	<-done
	if len(s.runtime.preparations) != 0 {
		t.Fatal("completed response leaked a slot")
	}
}
