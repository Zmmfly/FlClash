//go:build webpanel && !android

package core

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestWebUtilityHTTPAllSixFixedIPProvidersParseThroughPinnedTransport(t *testing.T) {
	for _, fixture := range []struct{ source, host, path, query, body, kind string }{
		{"identMe", "ident.me", "/json", "", `{"ip":"8.8.8.8","type":"isp"}`, "residential"},
		{"ipApiCom", "ip-api.com", "/json/8.8.8.8", "fields=status,isp,org,as,mobile,proxy,hosting", `{"status":"success","hosting":true}`, "hosting"},
		{"ipQuery", "api.ipquery.io", "/8.8.8.8", "format=json", `{"ip":"8.8.8.8","risk":{"is_mobile":true}}`, "mobile"},
		{"ipLocate", "iplocate.io", "/api/lookup/8.8.8.8", "", `{"company":{"type":"business"}}`, "business"},
		{"proxyCheck", "proxycheck.io", "/v3/8.8.8.8", "", `{"status":"ok","8.8.8.8":{"network":{"type":"Residential"}}}`, "residential"},
		{"ipApiIs", "api.ipapi.is", "/", "q=8.8.8.8", `{"company":{"type":"education"}}`, "business"},
	} {
		t.Run(fixture.source, func(t *testing.T) {
			f := newWebUtilityHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.Split(r.Host, ":")[0] != fixture.host {
					w.WriteHeader(429)
					return
				}
				if r.URL.Path != fixture.path || r.URL.RawQuery != fixture.query {
					t.Errorf("fixed provider target changed: %s?%s", r.URL.Path, r.URL.RawQuery)
					http.Error(w, "invalid target", 400)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, fixture.body)
			})
			status, body := f.request(t, f.clients[0], "POST", "/api/v1/ip-quality:lookup", WebAPIUtilityIPLookup{IP: "8.8.8.8"})
			if status != 200 {
				t.Fatalf("fixed provider %d: %s", status, body)
			}
			var result WebAPIEnvelope[WebAPIIPQualityRecord]
			if err := json.Unmarshal(body, &result); err != nil {
				t.Fatal(err)
			}
			if result.Data.Quality.Source != fixture.source || result.Data.Quality.Type != fixture.kind {
				t.Fatalf("provider parser mismatch %+v", result.Data.Quality)
			}
		})
	}
}

func TestWebUtilityHTTPIPWavesPreferDeclaredTypeAndHedgeBeforeQuotaLimitedSources(t *testing.T) {
	for _, hedged := range []bool{false, true} {
		t.Run(map[bool]string{false: "inferred-fallback", true: "hedged-wave"}[hedged], func(t *testing.T) {
			started := make(chan struct{})
			var once sync.Once
			f := newWebUtilityHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch strings.Split(r.Host, ":")[0] {
				case "ip-api.com":
					if hedged {
						once.Do(func() { close(started) })
						<-r.Context().Done()
						return
					}
					io.WriteString(w, `{"status":"success"}`)
				case "iplocate.io":
					if hedged {
						select {
						case <-started:
						default:
							t.Error("hedge launched before earlier wave")
						}
					}
					io.WriteString(w, `{"company":{"type":"business"}}`)
				default:
					w.WriteHeader(429)
				}
			})
			status, body := f.request(t, f.clients[0], "POST", "/api/v1/ip-quality:lookup", WebAPIUtilityIPLookup{IP: "8.8.8.8"})
			if status != 200 {
				t.Fatalf("wave lookup %d: %s", status, body)
			}
			var result WebAPIEnvelope[WebAPIIPQualityRecord]
			json.Unmarshal(body, &result)
			if result.Data.Quality.Source != "ipLocate" || result.Data.Quality.Type != "business" {
				t.Fatal("source silence replaced a declared classification")
			}
			f.mu.Lock()
			lateQueries := f.counts["api.ipapi.is/"] + f.counts["proxycheck.io/v3/8.8.8.8"]
			f.mu.Unlock()
			if lateQueries != 0 {
				t.Fatal("quota-limited later waves launched after an answer")
			}
		})
	}
}
