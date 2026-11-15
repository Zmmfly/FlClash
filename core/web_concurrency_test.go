//go:build webpanel && !android

package core

// WEBPANEL.md and README Web capability claims are exercised through these public routes and RPCs.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type webBlockingReader struct {
	started, release chan struct{}
	once             sync.Once
	data             *strings.Reader
}

func (r *webBlockingReader) Read(data []byte) (int, error) {
	r.once.Do(func() { close(r.started); <-r.release })
	return r.data.Read(data)
}

type webBlockingWriter struct {
	started, release chan struct{}
	once             sync.Once
	header           http.Header
}

func (w *webBlockingWriter) Header() http.Header { return w.header }
func (*webBlockingWriter) WriteHeader(int)       {}
func (w *webBlockingWriter) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.started); <-w.release })
	return len(data), nil
}

func TestWebSlowUploadDoesNotHoldFilesystemLock(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	for _, operation := range []string{"write?path=/data/slow", "rename", "copy"} {
		t.Run(operation, func(t *testing.T) {
			reader := &webBlockingReader{started: make(chan struct{}), release: make(chan struct{}), data: strings.NewReader(`{"from":"/data/missing","to":"/tmp/missing"}`)}
			r := httptest.NewRequest("POST", "http://panel.test/api/fs/"+operation, reader)
			r.AddCookie(cookie)
			done := make(chan struct{})
			go func() { defer close(done); s.ServeHTTP(httptest.NewRecorder(), r) }()
			<-reader.started
			fast := make(chan int, 1)
			go func() { fast <- webRequest(s, "GET", "/api/fs/stat?path=/data", "", cookie).Code }()
			status := 0
			select {
			case status = <-fast:
			case <-time.After(time.Second):
			}
			close(reader.release)
			<-done
			if status != 200 {
				t.Fatal("slow network reader held filesystem lock")
			}
		})
	}
}

func TestWebSlowDownloadDoesNotHoldFilesystemLock(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	s.data.WriteFile("slow", []byte("data"), 0600)
	writer := &webBlockingWriter{started: make(chan struct{}), release: make(chan struct{}), header: make(http.Header)}
	r := httptest.NewRequest("GET", "http://panel.test/api/fs/read?path=/data/slow", nil)
	r.AddCookie(cookie)
	done := make(chan struct{})
	go func() { defer close(done); s.ServeHTTP(writer, r) }()
	<-writer.started
	fast := make(chan int, 1)
	go func() { fast <- webRequest(s, "GET", "/api/fs/stat?path=/data", "", cookie).Code }()
	status := 0
	select {
	case status = <-fast:
	case <-time.After(time.Second):
	}
	close(writer.release)
	<-done
	if status != 200 {
		t.Fatal("slow network writer held filesystem lock")
	}
}

func TestWebProviderSideLoadPreservesShareLinksBase64AndBinary(t *testing.T) {
	s := newWebTestServer(t)
	links := "trojan://first@one.example:443#one\ntrojan://second@two.example:443#two\n"
	for _, data := range []string{links, base64.StdEncoding.EncodeToString([]byte(links)), "payload:\n - example.com\n", string([]byte{0, 255, 20, 1, 3})} {
		rewritten, err := s.sanitizeProviderData(data)
		if err != nil || rewritten != data {
			t.Fatalf("non-structured provider payload changed: %q => %q (%v)", data, rewritten, err)
		}
	}
	beforeInit := isInit.Load()
	isInit.Store(true)
	defer isInit.Store(beforeInit)
	s.coreEnabled.Store(true)
	before := methodHandlers[providerSideLoadMethod]
	defer func() { methodHandlers[providerSideLoadMethod] = before }()
	methodHandlers[providerSideLoadMethod] = func(call *MethodCall) (any, error) {
		var params SideLoadParams
		if err := call.decodeArguments(&params); err != nil {
			return nil, err
		}
		if params.Data != links {
			t.Error("share-link line breaks changed through RPC")
		}
		return s.withRPCFile("/data/subscription", func(filename string) (any, error) { return handleConvertProxies(PathParams{Path: filename}) })
	}
	s.data.WriteFile("subscription", []byte(links), 0600)
	args, _ := json.Marshal(SideLoadParams{Name: "test", Data: links})
	var response struct {
		Result []any        `json:"result"`
		Error  *MethodError `json:"error"`
	}
	json.Unmarshal(s.webRPC(&MethodCall{Method: providerSideLoadMethod, Arguments: args}), &response)
	if response.Error != nil || len(response.Result) != 2 {
		t.Fatalf("two-link provider regressed: %+v", response)
	}
}

func TestWebUploadAndUpstreamBounds(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	body := io.MultiReader(io.LimitReader(webZeroReader{}, webMaxBody), bytes.NewReader([]byte{1}))
	r := httptest.NewRequest("POST", "http://panel.test/api/fs/write?path=/data/too-large", body)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 413 {
		t.Fatalf("oversized upload = %d", w.Code)
	}
	if _, err := s.data.Stat("too-large"); err == nil {
		t.Fatal("oversized upload persisted")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer upstream.Close()
	data, _ := json.Marshal(webProxyRequest{URL: upstream.URL, TimeoutMs: 25})
	w = webRequest(s, "POST", "/api/http-proxy", string(data), cookie)
	var response webProxyResponse
	json.Unmarshal(w.Body.Bytes(), &response)
	if response.OK || response.Error == "" {
		t.Fatal("upstream timeout ignored")
	}
}

type webZeroReader struct{}

func (webZeroReader) Read(data []byte) (int, error) { clear(data); return len(data), nil }
