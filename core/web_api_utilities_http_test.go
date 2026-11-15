//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

type webUtilityHTTPFixture struct {
	store         *WebAPIStore
	storeOptions  WebAPIStoreOptions
	auth          *WebServer
	service       *WebAPIUtilityService
	server        *httptest.Server
	clients       [2]*http.Client
	sources       *WebAPISourceService
	sourceOptions WebAPISourceFetchOptions
	mu            sync.Mutex
	clock         time.Time
	counts        map[string]int
	lookups       []string
	respond       func(http.ResponseWriter, *http.Request)
}

func webUtilityTestTLS(t *testing.T, handler http.Handler) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "FlClash utility fixture"}, DNSNames: []string{"api.github.com", "ident.me", "api.ipquery.io", "iplocate.io", "proxycheck.io", "api.ipapi.is", "icons.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(certificate)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, pool
}

func newWebUtilityHTTPFixture(t *testing.T, respond func(http.ResponseWriter, *http.Request)) *webUtilityHTTPFixture {
	t.Helper()
	root := t.TempDir()
	f := &webUtilityHTTPFixture{clock: time.Now().UTC().Truncate(time.Millisecond), counts: map[string]int{}, respond: respond, storeOptions: WebAPIStoreOptions{DataDir: filepath.Join(root, "application")}}
	var err error
	f.store, err = OpenWebAPIStore(f.storeOptions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.store.Close() })
	f.auth, err = NewWebServer(WebServerOptions{DataDir: filepath.Join(root, "auth"), Username: "admin", Password: "correct horse", SafeMode: true, EmbeddedFS: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("fixture")}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.auth.Close() })
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" || r.Header.Get("Proxy-Authorization") != "" {
			t.Error("source request crossed Web cookie/header/method boundary")
		}
		host := strings.Split(r.Host, ":")[0]
		f.mu.Lock()
		f.counts[host+r.URL.Path]++
		f.mu.Unlock()
		f.respond(w, r)
	})
	plain := httptest.NewServer(handler)
	t.Cleanup(plain.Close)
	secure, pool := webUtilityTestTLS(t, handler)
	plainAddress := strings.TrimPrefix(plain.URL, "http://")
	secureAddress := strings.TrimPrefix(secure.URL, "https://")
	f.sourceOptions = WebAPISourceFetchOptions{TLSRootCAs: pool, Now: func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.clock }, LookupNetIP: func(ctx context.Context, network, host string) ([]netip.Addr, error) {
		f.mu.Lock()
		f.lookups = append(f.lookups, host)
		f.mu.Unlock()
		switch host {
		case "api.github.com", "ident.me", "ip-api.com", "api.ipquery.io", "iplocate.io", "proxycheck.io", "api.ipapi.is", "icons.test":
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		case "lan.test":
			return []netip.Addr{netip.MustParseAddr("10.20.30.40")}, nil
		case "mixed.test":
			return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("169.254.169.254")}, nil
		case "rebinding.test":
			return []netip.Addr{netip.MustParseAddr("10.20.30.41")}, nil
		}
		return nil, errors.New("unexpected fixture DNS target")
	}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if host != "93.184.216.34" && host != "10.20.30.40" && host != "10.20.30.41" {
			return nil, errors.New("dial was not pinned to fixture addresses")
		}
		target := plainAddress
		if port == "443" {
			target = secureAddress
		} else if port != "80" {
			return nil, errors.New("unexpected fixture port")
		}
		return (&net.Dialer{}).DialContext(ctx, network, target)
	}}
	mux := http.NewServeMux()
	RegisterWebAPIResourceRoutes(mux, f.store)
	RegisterWebAPICatalogRoutes(mux, f.store)
	RegisterWebAPICacheRoutes(mux, f.store)
	RegisterWebAPIContentRoutes(mux, f.store, WebAPIContentOptions{SourceOptions: f.sourceOptions, Now: f.sourceOptions.Now})
	f.service = RegisterWebAPIUtilityRoutes(mux, f.store, WebAPIUtilityOptions{SourceOptions: f.sourceOptions, Now: f.sourceOptions.Now, RemoteEnabled: true})
	f.sources = RegisterWebAPISourceRoutes(mux, f.store, WebAPISourceOptions{WebAPISourceFetchOptions: f.sourceOptions, ResolveUtilitySource: ResolveWebAPIUtilitySource})
	api := WebAPIRuntimeSessionHandler(f.auth, mux)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/auth/") {
			f.auth.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	}))
	t.Cleanup(f.server.Close)
	for i := range f.clients {
		jar, _ := cookiejar.New(nil)
		f.clients[i] = &http.Client{Jar: jar}
		status, body := f.request(t, f.clients[i], "POST", "/api/auth/login", map[string]string{"username": "admin", "password": "correct horse"})
		if status != 200 {
			t.Fatalf("login %d: %s", status, body)
		}
	}
	return f
}

func (f *webUtilityHTTPFixture) request(t *testing.T, client *http.Client, method, path string, value any) (int, []byte) {
	t.Helper()
	var body io.Reader
	if value != nil {
		encoded, err := webContentMarshal(value)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, f.server.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", f.server.URL)
	if value != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, WebAPIImageJSONMaxBytes+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > WebAPIImageJSONMaxBytes {
		t.Fatal("utility response exceeded JSON envelope budget")
	}
	return response.StatusCode, data
}

func (f *webUtilityHTTPFixture) totalFetches() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, v := range f.counts {
		n += v
	}
	return n
}

func TestWebUtilityHTTPReleaseAuthenticationFixedMetadataAndResponseLoss(t *testing.T) {
	f := newWebUtilityHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Host, "api.github.com") || r.URL.Path != "/repos/chen08209/FlClash/releases/latest" {
			t.Errorf("utility changed provider target: %s%s", r.Host, r.URL.Path)
			http.Error(w, "target", 400)
			return
		}
		io.WriteString(w, `{"tag_name":"v1.2.3","name":"Fixture","body":"notes https://evil.test/body-link","published_at":"2026-01-01T00:00:00Z","html_url":"http://169.254.169.254/","assets":[{"name":"client.apk","size":123,"browser_download_url":"https://evil.test/client.apk"}]}`)
	})
	anonymous := &http.Client{}
	if status, _ := f.request(t, anonymous, "GET", "/api/v1/app/release", nil); status != 401 {
		t.Fatalf("unauth release %d", status)
	}
	for _, client := range f.clients {
		status, body := f.request(t, client, "GET", "/api/v1/app/release", nil)
		if status != 200 {
			t.Fatalf("release %d: %s", status, body)
		}
		var result WebAPIEnvelope[WebAPIUtilityRelease]
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}
		if result.Revision != "0" || result.Data.ReleaseURL != "https://github.com/chen08209/FlClash/releases/tag/v1.2.3" || result.Data.Assets[0].URL != "https://github.com/chen08209/FlClash/releases/download/v1.2.3/client.apk" {
			t.Fatal("provider URL escaped trusted derivation")
		}
	}
	if f.totalFetches() != 1 {
		t.Fatal("two sessions or lost response refetched metadata")
	}
	for _, route := range []struct {
		method, path string
		input        any
		status       int
	}{{"POST", "/api/v1/app/release", map[string]any{}, 405}, {"GET", "/api/v1/app/release?url=https://evil.test", nil, 400}, {"GET", "/api/v1/app/release?url=a&url=b", nil, 400}, {"POST", "/api/v1/ip-quality:lookup", map[string]any{"ip": "unknown"}, 400}, {"POST", "/api/v1/ip-quality:lookup", map[string]any{"ip": "8.8.8.8", "url": "http://evil.test"}, 400}} {
		if status, body := f.request(t, f.clients[0], route.method, route.path, route.input); status != route.status {
			t.Fatalf("invalid %s %s: %d %s", route.method, route.path, status, body)
		}
	}
	if status, _ := f.request(t, f.clients[0], "POST", "/api/auth/logout", map[string]any{}); status != 204 {
		t.Fatalf("logout %d", status)
	}
	if status, _ := f.request(t, f.clients[0], "GET", "/api/v1/app/release", nil); status != 401 {
		t.Fatalf("revoked utility session %d", status)
	}
	if status, _ := f.request(t, f.clients[1], "GET", "/api/v1/app/release", nil); status != 200 {
		t.Fatal("other session unexpectedly lost read authority")
	}
}

func TestWebUtilityHTTPIPLookupTTLNoAmbientAddressAndCacheRevisionIsolation(t *testing.T) {
	f := newWebUtilityHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Host, "ip-api.com") {
			if r.URL.Path != "/json/10.0.0.1" {
				t.Errorf("queried an address other than explicit input: %s", r.URL.Path)
			}
			io.WriteString(w, `{"status":"success","hosting":true,"org":"Fixture","as":"AS123"}`)
			return
		}
		w.WriteHeader(429)
	})
	request := WebAPIUtilityIPLookup{IP: "10.0.0.1"}
	var first []byte
	for _, client := range f.clients {
		status, body := f.request(t, client, "POST", "/api/v1/ip-quality:lookup", request)
		if status != 200 {
			t.Fatalf("lookup %d: %s", status, body)
		}
		if first == nil {
			first = body
		} else if !bytes.Equal(first, body) {
			t.Fatal("cached lookup changed checkedAt on retry/session")
		}
	}
	var result WebAPIEnvelope[WebAPIIPQualityRecord]
	json.Unmarshal(first, &result)
	if result.Revision != "0" || result.Data.IP != "10.0.0.1" || result.Data.Quality.Type != "hosting" || result.Data.Quality.Source != "ipApiCom" {
		t.Fatalf("wrong typed lookup %+v", result)
	}
	_, err := f.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		meta, err := tx.Meta()
		if meta.Revision != "0" {
			t.Fatal("derived cache changed business revision")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	cacheRevision, err := f.store.CacheView(context.Background(), func(tx *WebAPICacheTx) error {
		rows, err := tx.IPQualities()
		if len(rows) != 1 || rows[0].CheckedAt != result.Data.CheckedAt {
			t.Fatal("partial or clock-forged cache")
		}
		return err
	})
	if err != nil || cacheRevision != "1" {
		t.Fatalf("cache revision %s %v", cacheRevision, err)
	}
	before := f.totalFetches()
	f.mu.Lock()
	for _, host := range f.lookups {
		if host == request.IP || strings.Contains(host, "10.0.0.1") {
			t.Error("IP input became a DNS/dial target")
		}
	}
	f.clock = f.clock.Add(73 * time.Hour)
	f.mu.Unlock()
	if status, body := f.request(t, f.clients[1], "POST", "/api/v1/ip-quality:lookup", request); status != 200 {
		t.Fatalf("expired TTL %d: %s", status, body)
	}
	if f.totalFetches() <= before {
		t.Fatal("expired quality did not refresh")
	}
}

func TestWebUtilityHTTPIP429FailureCooldownAndStrictSourceBody(t *testing.T) {
	f := newWebUtilityHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) })
	request := WebAPIUtilityIPLookup{IP: "8.8.8.8"}
	status, body := f.request(t, f.clients[0], "POST", "/api/v1/ip-quality:lookup", request)
	if status != 502 {
		t.Fatalf("rate-limited lookup %d: %s", status, body)
	}
	var result struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Failures []WebAPIUtilityIPFailure `json:"failures"`
			} `json:"details"`
		} `json:"error"`
	}
	json.Unmarshal(body, &result)
	if result.Error.Code != "ip_quality_unavailable" || len(result.Error.Details.Failures) != 6 {
		t.Fatalf("source failures lost: %s", body)
	}
	for _, failure := range result.Error.Details.Failures {
		if failure.Status != "rateLimited" {
			t.Fatalf("lost 429 status %+v", failure)
		}
	}
	before := f.totalFetches()
	if status, _ := f.request(t, f.clients[1], "POST", "/api/v1/ip-quality:lookup", request); status != 502 {
		t.Fatal("failure cooldown changed result")
	}
	if f.totalFetches() != before {
		t.Fatal("failure cooldown contacted providers again")
	}
	revision, err := f.store.CacheView(context.Background(), func(tx *WebAPICacheTx) error {
		rows, err := tx.IPQualities()
		if len(rows) != 0 {
			t.Fatal("failed quality partially cached")
		}
		return err
	})
	if err != nil || revision != "0" {
		t.Fatal("failed lookup committed cache state")
	}
}
