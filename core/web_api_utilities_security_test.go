//go:build webpanel && !android

package core

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestWebUtilitySourceHTTPRedirectAndUtilityPreparationCannotWidenProviderPolicy(t *testing.T) {
	f := newWebUtilityHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Host, "api.github.com") {
			w.Header().Set("Location", "https://icons.test/fake-release")
			w.WriteHeader(302)
			return
		}
		t.Error("fixed utility followed an unapproved provider redirect")
		io.WriteString(w, `{"tag_name":"v1.2.3","published_at":"2026-01-01T00:00:00Z"}`)
	})
	if status, body := f.request(t, f.clients[0], "GET", "/api/v1/app/release", nil); status < 400 {
		t.Fatalf("release redirected to an untrusted source %d: %s", status, body)
	}
	if f.totalFetches() != 1 {
		t.Fatal("release redirect was fetched")
	}
	for _, purpose := range []WebAPISourcePurpose{"app-release", "ip-quality-lookup"} {
		for _, url := range []string{"http://lan.test/private", "https://icons.test/other"} {
			request := WebAPISourcePrepare{ExpectedRevision: "0", Purpose: purpose, URL: url}
			if status, _ := f.request(t, f.clients[0], "POST", "/api/v1/sources:prepare", request); status != 400 {
				t.Fatalf("utility source prepare accepted user URL/purpose %s %s: %d", purpose, url, status)
			}
		}
	}
	before := f.totalFetches()
	if _, err := WebAPIFetchSource(context.Background(), f.store, WebAPISourceRequest{Purpose: "ip-quality-lookup", URL: "http://lan.test/private"}, f.sourceOptions); err == nil {
		t.Fatal("fixed utility direct fetch accepted arbitrary private URL")
	}
	if f.totalFetches() != before {
		t.Fatal("invalid utility target caused fetch")
	}
}

func TestWebUtilityHTTPReleaseBodyLimitAndIPTimeoutArePurposeBounded(t *testing.T) {
	f := newWebUtilityHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(webUtilityReleaseMetadataLimit+1))
		io.WriteString(w, `{}`)
	})
	if status, body := f.request(t, f.clients[0], "GET", "/api/v1/app/release", nil); status != 413 {
		t.Fatalf("metadata purpose limit %d: %s", status, body)
	}
	f.service.options.SourceOptions.DialContext = func(context.Context, string, string) (net.Conn, error) { return nil, context.DeadlineExceeded }
	status, body := f.request(t, f.clients[0], "POST", "/api/v1/ip-quality:lookup", WebAPIUtilityIPLookup{IP: "8.8.8.8"})
	if status != 502 {
		t.Fatalf("timeout lookup %d: %s", status, body)
	}
	if !strings.Contains(string(body), `"status":"timeout"`) || strings.Contains(string(body), `"status":"failed"`) {
		t.Fatalf("source timeout status was flattened: %s", body)
	}
}

func TestWebUtilityDefaultsDisableRemoteEvenWithProtectedFakeOptions(t *testing.T) {
	f := newWebUtilityHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("default utility made network request") })
	utility := NewWebAPIUtilityService(f.store, WebAPIUtilityOptions{SourceOptions: f.sourceOptions})
	_, err := utility.releaseLatest(context.Background())
	webAPITestErrorCode(t, err, "utility_remote_disabled")
	_, err = utility.lookupIP(context.Background(), WebAPIUtilityIPLookup{IP: "8.8.8.8"})
	webAPITestErrorCode(t, err, "utility_remote_disabled")
	if f.totalFetches() != 0 {
		t.Fatal("default service contacted a provider")
	}
}
