//go:build webpanel && !android

package core

// WEBPANEL.md: typed transfers remain cancellable while retired tunnels never accept work.

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

func webConcurrencyWait(t *testing.T, done <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal(message)
	}
}

func webConcurrencyUnblock(release chan struct{}) func() {
	var once sync.Once
	return func() { once.Do(func() { close(release) }) }
}

func webConcurrencyReadAndLogout(t *testing.T, s *WebServer, cookie *http.Cookie) {
	t.Helper()
	done := make(chan [2]int, 1)
	go func() {
		done <- [2]int{webRequest(s, "GET", "/api/v1/meta", "", cookie).Code, webRequest(s, "POST", "/api/auth/logout", "", cookie).Code}
	}()
	select {
	case status := <-done:
		if status != [2]int{200, 204} {
			t.Fatalf("control requests returned %v", status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow transfer held a store or authentication lock")
	}
}

func TestWebSlowUploadDoesNotHoldStoreOrAuthenticationLock(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	body := `{"expectedRevision":"0","operationId":"slow-upload-contract","rule":{"profileId":null,"ruleAction":"DOMAIN_SUFFIX","content":"slow.invalid","ruleTarget":"DIRECT","noResolve":false,"src":false}}`
	reader := &webBlockingReader{started: make(chan struct{}), release: make(chan struct{}), data: strings.NewReader(body)}
	unblock := webConcurrencyUnblock(reader.release)
	defer unblock()
	request := httptest.NewRequest("POST", "http://panel.test/api/v1/rules:create", reader)
	request.AddCookie(cookie)
	request.Header.Set("Origin", "http://panel.test")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); s.ServeHTTP(response, request) }()
	webConcurrencyWait(t, reader.started, "typed upload never began reading its body")
	webConcurrencyReadAndLogout(t, s, cookie)
	unblock()
	webConcurrencyWait(t, done, "revoked upload did not settle")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked upload returned %d", response.Code)
	}
	fresh := webLogin(t, s)
	var meta WebAPIEnvelope[WebAPIStoreMeta]
	observed := webRequest(s, "GET", "/api/v1/meta", "", fresh)
	if observed.Code != 200 || json.Unmarshal(observed.Body.Bytes(), &meta) != nil || meta.Revision != "0" {
		t.Fatal("revoked slow upload changed committed state")
	}
}

func TestWebSlowDownloadDoesNotHoldStoreOrAuthenticationLock(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	writer := &webBlockingWriter{started: make(chan struct{}), release: make(chan struct{}), header: make(http.Header)}
	unblock := webConcurrencyUnblock(writer.release)
	defer unblock()
	request := httptest.NewRequest("GET", "http://panel.test/api/v1/meta", nil)
	request.AddCookie(cookie)
	done := make(chan struct{})
	go func() { defer close(done); s.ServeHTTP(writer, request) }()
	webConcurrencyWait(t, writer.started, "typed response never began writing")
	webConcurrencyReadAndLogout(t, s, cookie)
	unblock()
	webConcurrencyWait(t, done, "slow typed response did not settle")
}

func TestWebRetiredFilesystemAndRelayRejectWithoutReadingBody(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	for _, route := range []string{"/api/fs/write?path=/data/slow", "/api/fs/rename", "/api/fs/copy", "/api/fs/delete?path=/data/slow", "/api/fs/mkdir?path=/data/slow", "/api/http-proxy"} {
		t.Run(route, func(t *testing.T) {
			reader := &webBlockingReader{started: make(chan struct{}), release: make(chan struct{}), data: strings.NewReader("{}")}
			unblock := webConcurrencyUnblock(reader.release)
			defer unblock()
			request := httptest.NewRequest("POST", "http://panel.test"+route, reader)
			request.AddCookie(cookie)
			request.Header.Set("Origin", "http://panel.test")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); s.ServeHTTP(response, request) }()
			webConcurrencyWait(t, done, "retired route attempted to consume its body")
			select {
			case <-reader.started:
				t.Fatal("retired route read its body")
			default:
			}
			if response.Code != http.StatusNotFound {
				t.Fatalf("retired route returned %d", response.Code)
			}
		})
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

func TestWebTypedUploadBoundAndRetiredRelayDoesNotDial(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	body := io.MultiReader(io.LimitReader(webZeroReader{}, webAPIMaxJSONBytes), bytes.NewReader([]byte{1}))
	request := httptest.NewRequest("POST", "http://panel.test/api/v1/rules:create", body)
	request.AddCookie(cookie)
	request.Header.Set("Origin", "http://panel.test")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	s.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized typed control request = %d", response.Code)
	}
	contacted := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case contacted <- struct{}{}:
		default:
		}
		w.Write([]byte("unexpected"))
	}))
	defer upstream.Close()
	payload, _ := json.Marshal(webProxyRequest{URL: upstream.URL, TimeoutMs: 25})
	response = webRequest(s, "POST", "/api/http-proxy", string(payload), cookie)
	if response.Code != http.StatusNotFound {
		t.Fatalf("retired relay = %d", response.Code)
	}
	select {
	case <-contacted:
		t.Fatal("retired relay contacted an upstream")
	default:
	}
	var meta WebAPIEnvelope[WebAPIStoreMeta]
	observed := webRequest(s, "GET", "/api/v1/meta", "", cookie)
	if observed.Code != 200 || json.Unmarshal(observed.Body.Bytes(), &meta) != nil || meta.Revision != "0" {
		t.Fatal("rejected upload or relay changed committed state")
	}
}

type webZeroReader struct{}

func (webZeroReader) Read(data []byte) (int, error) { clear(data); return len(data), nil }
