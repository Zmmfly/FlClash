//go:build webpanel && !android

package core

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestWebCustomSourceHTTPStagingCapacityAndClose(t *testing.T) {
	f := newWebCustomSourceHTTPRig(t, false, "proxies: []\n")
	for i := 0; i < 16; i++ {
		f.prepare(t, "")
	}
	before := f.fetches.Load()
	status, body := f.request(t, "POST", "/api/v1/profiles:prepare-custom-import", WebAPICustomSourcePrepare{ExpectedRevision: f.revision, ProfileID: f.owner, URL: f.url + "/root"}, f.cookie)
	if status != 429 || f.fetches.Load() != before {
		t.Fatalf("full staging fetched another body %d: %s", status, body)
	}
	f.requireUnchanged(t)
	f.custom.Close()
	f.custom.mu.Lock()
	defer f.custom.mu.Unlock()
	if f.custom.bytes != 0 || len(f.custom.jobs) != 0 || len(f.custom.extra) != 0 {
		t.Fatal("Close retained staging")
	}
}

func TestWebCustomSourceHTTPDecompressedLimitAndAliasBudgetZeroWrites(t *testing.T) {
	f := newWebCustomSourceHTTPRig(t, false, "proxies: []\n")
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	writer.Write([]byte("proxies: []\n#"))
	io.Copy(writer, io.LimitReader(strings.NewReader(strings.Repeat("x", WebAPIMaxContentBytes)), WebAPIMaxContentBytes))
	writer.Close()
	f.mu.Lock()
	f.bodies["/root"] = compressed.String()
	f.mu.Unlock()
	status, _ := f.request(t, "POST", "/api/v1/profiles:prepare-custom-import", WebAPICustomSourcePrepare{ExpectedRevision: f.revision, ProfileID: f.owner, URL: f.url + "/root"}, f.cookie)
	if status != 413 {
		t.Fatalf("decompressed oversized content accepted: %d", status)
	}
	f.requireUnchanged(t)
	f.mu.Lock()
	f.bodies["/root"] = "a: &a [*a]\nproxies: []\n"
	f.mu.Unlock()
	status, _ = f.request(t, "POST", "/api/v1/profiles:prepare-custom-import", WebAPICustomSourcePrepare{ExpectedRevision: f.revision, ProfileID: f.owner, URL: f.url + "/root"}, f.cookie)
	if status != 413 {
		t.Fatalf("recursive alias accepted: %d", status)
	}
	f.requireUnchanged(t)
}

func TestWebCustomSourceHTTPRedirectPinningAndCredentialStripping(t *testing.T) {
	f := newWebCustomSourceHTTPRig(t, false, "proxies: []\n")
	other := strings.Replace(f.url, "source.test", "other.test", 1)
	f.mu.Lock()
	f.bodies["/root"] = "redirect:" + other + "/public"
	f.bodies["/public"] = "proxies: []\n"
	f.mu.Unlock()
	url := strings.Replace(f.url, "http://", "http://synthetic:secret@", 1) + "/root"
	status, body := f.request(t, "POST", "/api/v1/profiles:prepare-custom-import", WebAPICustomSourcePrepare{ExpectedRevision: f.revision, ProfileID: f.owner, URL: url}, f.cookie)
	if status != 200 {
		t.Fatalf("public redirect %d: %s", status, body)
	}
	f.mu.Lock()
	first, second := f.headers["/root"], f.headers["/public"]
	f.mu.Unlock()
	if !strings.HasPrefix(first, "Basic ") || second != "" {
		t.Fatal("cross-origin source credentials leaked or Basic was dropped")
	}
	f.custom.options.SourceOptions.LookupNetIP = func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if host == "other.test" {
			return []netip.Addr{netip.MustParseAddr("10.1.2.3")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	before := f.fetches.Load()
	status, _ = f.request(t, "POST", "/api/v1/profiles:prepare-custom-import", WebAPICustomSourcePrepare{ExpectedRevision: f.revision, ProfileID: f.owner, URL: f.url + "/root"}, f.cookie)
	if status != 403 || f.fetches.Load() != before+1 {
		t.Fatalf("redirect authorized private egress: %d fetches=%d", status, f.fetches.Load()-before)
	}
	f.requireUnchanged(t)
}

func TestWebCustomSourceHTTPCancelAndCloseCancelActualFetch(t *testing.T) {
	for _, mode := range []string{"discard", "close"} {
		t.Run(mode, func(t *testing.T) {
			f := newWebCustomSourceHTTPRig(t, false, "proxy-providers:\n  p: {type: http, url: SOURCE/proxy}\nproxy-groups: [{name: G, type: select, use: [p]}]\n")
			job := f.prepare(t, "")
			f.mu.Lock()
			f.bodies["/proxy"] = "block"
			f.entered = make(chan struct{})
			f.completed = make(chan struct{})
			entered, completed := f.entered, f.completed
			f.mu.Unlock()
			body, _ := json.Marshal(f.confirm(job))
			request, _ := http.NewRequest("POST", f.server.URL+"/api/v1/profiles:import-custom", bytes.NewReader(body))
			request.AddCookie(f.cookie)
			request.Header.Set("Content-Type", "application/json")
			answer := make(chan int, 1)
			go func() {
				response, err := f.server.Client().Do(request)
				if err != nil {
					answer <- 0
					return
				}
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
				answer <- response.StatusCode
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("dependency fetch did not begin")
			}
			if mode == "discard" {
				status, body := f.request(t, "POST", "/api/v1/profiles:discard-custom-import", map[string]string{"preparedJobId": job.PreparedJobID}, f.cookie)
				if status != 204 {
					t.Fatalf("discard %d: %s", status, body)
				}
			} else {
				f.custom.Close()
			}
			select {
			case <-completed:
			case <-time.After(3 * time.Second):
				t.Fatal("source request was not canceled")
			}
			select {
			case status := <-answer:
				if status == 200 {
					t.Fatal("canceled import committed")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("confirmation did not finish")
			}
			f.requireUnchanged(t)
			f.custom.mu.Lock()
			defer f.custom.mu.Unlock()
			if f.custom.bytes != 0 || len(f.custom.jobs) != 0 {
				t.Fatal("canceled staging retained")
			}
		})
	}
}

func TestWebCustomSourceHTTPParsedUnknownDependenciesFailClosed(t *testing.T) {
	f := newWebCustomSourceHTTPRig(t, false, "proxies:\n - {name: A, type: trojan, server: synthetic.test, port: 443, password: synthetic, dialer-proxy: Missing}\nproxy-groups: [{name: G, type: select, proxies: [A]}]\n")
	job := f.prepare(t, "")
	status, body := f.request(t, "POST", "/api/v1/profiles:import-custom", f.confirm(job), f.cookie)
	if status != 409 {
		t.Fatalf("unsafe dependency accepted %d: %s", status, body)
	}
	f.requireUnchanged(t)
	f.mu.Lock()
	f.bodies["/root"] = "proxy-providers: {p: {type: arbitrary-network-adapter, url: http://unapproved.test}}\n"
	f.mu.Unlock()
	status, _ = f.request(t, "POST", "/api/v1/profiles:prepare-custom-import", WebAPICustomSourcePrepare{ExpectedRevision: f.revision, ProfileID: f.owner, URL: f.url + "/root"}, f.cookie)
	if status != 400 {
		t.Fatal("unknown provider type accepted")
	}
	f.requireUnchanged(t)
}
