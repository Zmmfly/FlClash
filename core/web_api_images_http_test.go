//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func webImageRegisterRequest(url, revision, operation string) WebAPIImageRegister {
	return WebAPIImageRegister{WebAPIMutation: WebAPIMutation{ExpectedRevision: WebAPIRevision(revision), OperationID: operation}, URL: url, Intent: "preview-icon"}
}

func TestWebImageHTTPExplicitPreviewCacheCASReplayAndNoArbitraryRelay(t *testing.T) {
	png := webImageTestPNG(t)
	f := newWebUtilityHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) { w.Write(png) })
	url := "http://icons.test/owned.png"
	if status, body := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:load", WebAPIImageLoad{URL: url}); status != 404 {
		t.Fatalf("unregistered load %d: %s", status, body)
	}
	if f.totalFetches() != 0 {
		t.Fatal("unregistered URL performed a network fetch")
	}
	command := webImageRegisterRequest(url, "0", "image-register-first")
	status, first := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:register", command)
	if status != 200 {
		t.Fatalf("register %d: %s", status, first)
	}
	status, replay := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:register", command)
	if status != 200 || !bytes.Equal(first, replay) {
		t.Fatal("icon preview receipt did not replay exactly")
	}
	changed := command
	changed.URL = "http://icons.test/other.png"
	if status, _ := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:register", changed); status != 409 {
		t.Fatalf("changed command replay %d", status)
	}
	if status, _ := f.request(t, f.clients[1], "POST", "/api/v1/icon-images:register", webImageRegisterRequest(changed.URL, "0", "image-register-conflict")); status != 409 {
		t.Fatalf("two-session cache CAS %d", status)
	}
	for _, input := range []map[string]any{{"url": url, "method": "POST"}, {"url": url, "headers": map[string]string{"Cookie": "secret"}}, {"url": url, "savePath": "/data/credentials.json"}} {
		if status, _ := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:load", input); status != 400 {
			t.Fatal("image route accepted arbitrary HTTP or filesystem fields")
		}
	}
	for _, route := range []struct {
		method, path string
		status       int
	}{{"GET", "/api/v1/icon-images:load", 405}, {"POST", "/api/v1/icon-images:load?method=PUT", 400}, {"POST", "/api/http-proxy", 404}, {"GET", "/api/fs/read?path=/data/database.sqlite", 404}} {
		if status, body := f.request(t, f.clients[0], route.method, route.path, map[string]any{}); status != route.status {
			t.Fatalf("old/generic path %s %s = %d: %s", route.method, route.path, status, body)
		}
	}
	for _, client := range f.clients {
		for repeat := 0; repeat < 2; repeat++ {
			status, body := f.request(t, client, "POST", "/api/v1/icon-images:load", WebAPIImageLoad{URL: url})
			if status != 200 {
				t.Fatalf("load %d: %s", status, body)
			}
			var result WebAPIEnvelope[WebAPIImage]
			if err := json.Unmarshal(body, &result); err != nil {
				t.Fatal(err)
			}
			if result.Revision != "0" || !bytes.Equal(result.Data.Bytes, png) || result.Data.Size != len(png) || result.Data.MediaType != "image/png" || result.Data.SHA256 != webImageSHA(png) || result.Data.SourceID != webImageSourceID(url) {
				t.Fatal("typed image envelope changed authority or bytes")
			}
		}
	}
	if f.totalFetches() != 2 {
		t.Fatalf("expected session-bound cache reuse, got %d fetches", f.totalFetches())
	}
	rev, err := f.store.CacheView(context.Background(), func(tx *WebAPICacheTx) error {
		rows, err := tx.IconRecords()
		if len(rows) != 1 {
			t.Fatal("preview partially registered")
		}
		return err
	})
	if err != nil || rev != "1" {
		t.Fatalf("image byte reads touched IconRecord cache revision %s %v", rev, err)
	}
}

func TestWebImageHTTPDroppedResponsesRecoverCacheReceiptAndReuseCompleteBytes(t *testing.T) {
	png := webImageTestPNG(t)
	f := newWebUtilityHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) { w.Write(png) })
	url := "http://icons.test/dropped.png"
	command := webImageRegisterRequest(url, "0", "dropped-image-register")
	for _, requestValue := range []struct {
		path string
		body any
	}{
		{"/api/v1/icon-images:register", command},
		{"/api/v1/icon-images:load", WebAPIImageLoad{URL: url}},
	} {
		encoded, _ := json.Marshal(requestValue.body)
		request, err := http.NewRequest("POST", f.server.URL+requestValue.path, bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", f.server.URL)
		response, err := f.clients[0].Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			t.Fatalf("drop response %d: %s", response.StatusCode, body)
		}
		response.Body.Close()
	}
	if status, body := f.request(t, f.clients[0], "GET", "/api/v1/cache-operations/dropped-image-register", nil); status != 200 || !bytes.Contains(body, []byte(command.OperationID)) {
		t.Fatalf("lost preview receipt not recoverable %d: %s", status, body)
	}
	if status, body := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:load", WebAPIImageLoad{URL: url}); status != 200 {
		t.Fatalf("dropped byte response could not retry %d: %s", status, body)
	}
	if f.totalFetches() != 1 {
		t.Fatalf("dropped read response repeated fetch: %d", f.totalFetches())
	}
}

func TestWebImageHTTPReal32MiBImageFits48MiBJSON(t *testing.T) {
	png := webImageTestLargePNG(t, WebAPIImageMaxBytes)
	f := newWebUtilityHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(png)
	})
	url := "http://icons.test/large.png"
	if status, body := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:register", webImageRegisterRequest(url, "0", "image-large-register")); status != 200 {
		t.Fatalf("register %d: %s", status, body)
	}
	status, body := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:load", WebAPIImageLoad{URL: url})
	if status != 200 {
		t.Fatalf("32MiB load %d: %.100s", status, body)
	}
	if len(body) <= WebAPIImageMaxBytes || len(body) > WebAPIImageJSONMaxBytes {
		t.Fatalf("incorrect base64 envelope budget %d", len(body))
	}
	var result WebAPIEnvelope[WebAPIImage]
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.Size != WebAPIImageMaxBytes || !bytes.Equal(result.Data.Bytes, png) {
		t.Fatal("32MiB image was truncated")
	}
	if f.service.images.bytes > webImageCacheBytes {
		t.Fatal("image byte cache exceeded total LRU budget")
	}
}

func TestWebImageHTTPRejectsMetadataBodyWithoutPartialCacheAndDeduplicatesConcurrentLoad(t *testing.T) {
	png := webImageTestPNG(t)
	var mu sync.Mutex
	bad := true
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f := newWebUtilityHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/concurrent.png" {
			once.Do(func() { close(started) })
			<-release
			w.Write(png)
			return
		}
		mu.Lock()
		isBad := bad
		mu.Unlock()
		w.Header().Set("Content-Type", "image/png")
		if isBad {
			io.WriteString(w, `{"secret":"not an image"}`)
		} else {
			w.Write(png)
		}
	})
	badURL := "http://icons.test/bad.png"
	if status, _ := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:register", webImageRegisterRequest(badURL, "0", "image-bad-register")); status != 200 {
		t.Fatal("register failed")
	}
	if status, _ := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:load", WebAPIImageLoad{URL: badURL}); status != 422 {
		t.Fatalf("metadata body accepted %d", status)
	}
	f.service.images.mu.Lock()
	if f.service.images.bytes != 0 || len(f.service.images.entries) != 0 {
		t.Error("failed image partially cached")
	}
	f.service.images.mu.Unlock()
	mu.Lock()
	bad = false
	mu.Unlock()
	if status, body := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:load", WebAPIImageLoad{URL: badURL}); status != 200 {
		t.Fatalf("retry did not recover %d: %s", status, body)
	}
	url := "http://icons.test/concurrent.png"
	if status, _ := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:register", webImageRegisterRequest(url, "1", "image-concurrent-register")); status != 200 {
		t.Fatal("register failed")
	}
	results := make(chan int, 8)
	for i := 0; i < 8; i++ {
		go func() {
			status, _ := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:load", WebAPIImageLoad{URL: url})
			results <- status
		}()
	}
	select {
	case <-started:
	case status := <-results:
		t.Fatalf("provider was not entered before HTTP completed with %d", status)
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start within the fixture budget")
	}
	close(release)
	for i := 0; i < 8; i++ {
		if status := <-results; status != 200 {
			t.Fatalf("concurrent load %d", status)
		}
	}
	f.mu.Lock()
	fetches := f.counts["icons.test/concurrent.png"]
	f.mu.Unlock()
	if fetches != 1 {
		t.Fatalf("concurrent image missed single-flight: %d", fetches)
	}
}

func TestWebImageHTTPPrivateGrantOpaqueIdentityAndForbiddenAddresses(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M0 0h24v24z"/></svg>`)
	f := newWebUtilityHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect.svg" {
			w.Header().Set("Location", "http://lan.test/private.svg")
			w.WriteHeader(302)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write(svg)
	})
	url := "http://lan.test/private.svg"
	if status, _ := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:register", webImageRegisterRequest(url, "0", "private-icon-register")); status != 200 {
		t.Fatal("private icon registration was silently public-only")
	}
	if status, _ := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:load", WebAPIImageLoad{URL: url}); status != 403 {
		t.Fatalf("private icon bypassed resource grant: %d", status)
	}
	key := webImageResourceKey(url)
	prepare := WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourcePurpose("icon-image"), Key: &key}
	status, body := f.request(t, f.clients[0], "POST", "/api/v1/sources:prepare", prepare)
	if status != 200 {
		t.Fatalf("private opaque prepare %d: %s", status, body)
	}
	var prepared WebAPIEnvelope[WebAPISourcePreparation]
	json.Unmarshal(body, &prepared)
	if !prepared.Data.RequiresPrivateNetworkConfirmation || prepared.Data.Key.ID != key.ID || len(key.ID) < 70 {
		t.Fatal("source preparation lost private resource identity")
	}
	authorize := WebAPISourceAuthorize{ExpectedRevision: "0", OperationID: "private-image-authorize", PreparationID: prepared.Data.PreparationID, URLDigest: prepared.Data.URLDigest, Intent: "authorize-server-private-source"}
	status, body = f.request(t, f.clients[0], "POST", "/api/v1/sources:authorize", authorize)
	if status != 200 {
		t.Fatalf("authorize %d: %s", status, body)
	}
	var authorized WebAPIEnvelope[WebAPISourceAuthorizationResult]
	json.Unmarshal(body, &authorized)
	load := WebAPIImageLoad{URL: url, AuthorizationID: authorized.Data.AuthorizationID}
	status, body = f.request(t, f.clients[0], "POST", "/api/v1/icon-images:load", load)
	if status != 200 {
		t.Fatalf("authorized image %d: %s", status, body)
	}
	var image WebAPIEnvelope[WebAPIImage]
	json.Unmarshal(body, &image)
	if image.Data.MediaType != "image/svg+xml" || !bytes.Equal(image.Data.Bytes, svg) || !strings.Contains(string(body), `"bytes":`) {
		t.Fatal("SVG did not remain JSON base64 image bytes")
	}
	if status, _ := f.request(t, f.clients[1], "POST", "/api/v1/icon-images:load", load); status < 400 || status >= 500 {
		t.Fatalf("grant crossed session byte-cache boundary %d", status)
	}
	for _, bad := range []string{"http://169.254.169.254/latest/meta-data", "http://metadata.google.internal/computeMetadata/v1", "http://[fe80::1]/icon.svg"} {
		if status, _ := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:register", webImageRegisterRequest(bad, "1", "forbidden-icon-register")); status != 403 {
			t.Fatalf("metadata/link-local icon accepted %s: %d", bad, status)
		}
	}
	for index, bad := range []string{"http://mixed.test/icon.svg", "http://icons.test/redirect.svg"} {
		revision := string(rune('1' + index))
		if status, body := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:register", webImageRegisterRequest(bad, revision, "image-forbidden-"+revision)); status != 200 {
			t.Fatalf("register %d: %s", status, body)
		}
		if status, body := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:load", WebAPIImageLoad{URL: bad}); status != 403 {
			t.Fatalf("DNS/redirect private boundary %d: %s", status, body)
		}
	}
	f.mu.Lock()
	f.clock = f.clock.Add(2 * time.Hour)
	f.mu.Unlock()
	if status, _ := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:load", load); status != 409 {
		t.Fatalf("expired grant served cached private bytes %d", status)
	}
}
