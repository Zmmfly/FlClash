//go:build webpanel && !android

package core

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestWebSourceCommittedHeadersValidateAndBindFullIdentity(t *testing.T) {
	valid := map[string][]string{"authorization": {"Bearer synthetic-token"}, "Cookie": {"subscription=synthetic-cookie"}, "User-Agent": {"synthetic-client"}, "Accept": {"text/plain"}, "X-API-Key": {"synthetic-vendor-key"}}
	normalized, err := WebAPIValidateCommittedSourceHeaders(valid)
	if err != nil || normalized["Authorization"][0] != "Bearer synthetic-token" {
		t.Fatal(err)
	}
	for name, input := range map[string]map[string][]string{
		"duplicate-case":   {"Cookie": {"a=b"}, "cookie": {"c=d"}},
		"duplicate-values": {"Cookie": {"a=b", "c=d"}},
		"empty-values":     {"Cookie": {}},
		"newline":          {"Authorization": {"Basic Zm9vOmJhcg==\r\nHost: local"}},
		"nul":              {"Cookie": {"a=b\x00"}},
		"control":          {"Accept": {"a\x7f"}},
		"proxy":            {"Proxy-Authorization": {"Basic Zm9vOmJhcg=="}},
		"host":             {"Host": {"127.0.0.1"}},
		"origin":           {"Origin": {"http://browser.test"}},
		"hop-field":        {"TE": {"trailers"}},
		"name-space":       {" Cookie": {"a=b"}},
		"name-token":       {"X Bad": {"synthetic"}},
		"vendor-newline":   {"X-API-Key": {"synthetic\r\nHost: local"}},
		"vendor-size":      {"X-API-Key": {strings.Repeat("x", 8193)}},
		"scheme":           {"Authorization": {"Digest synthetic"}},
		"empty-auth":       {"Authorization": {"Bearer "}},
		"size":             {"User-Agent": {strings.Repeat("x", 1025)}},
		"total":            {"Authorization": {"Bearer " + strings.Repeat("x", 8185)}, "Cookie": {strings.Repeat("x", 8192)}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := WebAPIValidateCommittedSourceHeaders(input)
			webBackupRequireCode(t, err, "invalid_source_headers")
		})
	}
	for _, name := range []string{"Host", "Origin", "Referer", "Proxy-Authorization", "Proxy-Authenticate", "Connection", "Proxy-Connection", "Keep-Alive", "Content-Length", "Transfer-Encoding", "Upgrade", "TE", "Trailer", "Expect"} {
		_, err := WebAPIValidateCommittedSourceHeaders(map[string][]string{name: {"synthetic"}})
		webBackupRequireCode(t, err, "invalid_source_headers")
	}
	vendorDigest, _ := webSourceIdentityDigest("http://source.test/path", map[string][]string{"X-API-Key": {"synthetic-one"}})
	changedVendorDigest, _ := webSourceIdentityDigest("http://source.test/path", map[string][]string{"x-api-key": {"synthetic-two"}})
	if vendorDigest == changedVendorDigest {
		t.Fatal("vendor credential did not participate in full identity")
	}
	tooMany := map[string][]string{}
	for i := 0; i < 33; i++ {
		tooMany[fmt.Sprintf("X-Entry-%d", i)] = []string{"synthetic"}
	}
	_, err = WebAPIValidateCommittedSourceHeaders(tooMany)
	webBackupRequireCode(t, err, "invalid_source_headers")
	_, canonical, _ := webSourceCanonical("http://synthetic-user:synthetic-password@SOURCE.test/path?token=synthetic")
	empty, err := webSourceIdentityDigest("http://synthetic-user:synthetic-password@source.test:80/path?token=synthetic", nil)
	if err != nil || canonical != empty {
		t.Fatal("nil headers changed legacy/DAV URL identity")
	}
	first, err := webSourceIdentityDigest("http://source.test/path?token=synthetic", valid)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := webSourceIdentityDigest("http://SOURCE.TEST:80/path?token=synthetic", normalized)
	if first != second {
		t.Fatal("equivalent origin/header casing changed identity")
	}
	for _, raw := range []string{"http://source.test/other?token=synthetic", "http://source.test/path?token=other"} {
		changed, _ := webSourceIdentityDigest(raw, valid)
		if changed == first {
			t.Fatal("grant bound only origin instead of full URL")
		}
	}
	normalized["Cookie"][0] = "subscription=changed"
	changed, _ := webSourceIdentityDigest("http://source.test/path?token=synthetic", normalized)
	if changed == first {
		t.Fatal("literal committed cookie was not in identity")
	}
	_, err = webSourceIdentityDigest("http://user:password@source.test/path", valid)
	webBackupRequireCode(t, err, "invalid_source_headers")
	encoded, err := json.Marshal(WebAPISourceRequest{CommittedHeaders: valid})
	if err != nil || strings.Contains(string(encoded), "synthetic-") || strings.Contains(string(encoded), "CommittedHeaders") {
		t.Fatal("Go-only headers serialized")
	}
}

func TestWebSourceCommittedCredentialsExactOriginAndNeverReturnAfterCrossOrigin(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:1")
	for _, test := range []struct {
		name, target string
		retain       bool
	}{
		{"same-canonical-origin", "http://SOURCE.TEST:80/next", true},
		{"host", "http://other.test/next", false},
		{"subdomain", "http://sub.source.test/next", false},
		{"port", "http://source.test:81/next", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := &webSourceTestWire{respond: func(r *http.Request, conn net.Conn) {
				switch r.URL.Path {
				case "/first":
					webSourceTestResponse(conn, 302, http.Header{"Location": {test.target}}, strings.NewReader(""), 0)
				case "/next":
					webSourceTestResponse(conn, 302, http.Header{"Location": {"http://source.test/final"}}, strings.NewReader(""), 0)
				default:
					webSourceTestResponse(conn, 200, nil, strings.NewReader("ok"), 2)
				}
			}}
			request := WebAPISourceRequest{Purpose: WebAPISourceExternalProviderRefresh, Key: &WebAPISourceResource{Kind: "external-provider", ID: "123456789012345678901234567890"}, URL: "http://source.test/first", CommittedHeaders: map[string][]string{"Authorization": {"Bearer synthetic-provider"}, "Cookie": {"literal=synthetic-provider"}, "User-Agent": {"stored-client"}, "Accept": {"application/yaml"}, "X-API-Key": {"synthetic-provider-key"}}}
			_, err := webFetchPurposeSource(context.Background(), request, nil, wire.options())
			if err != nil {
				t.Fatal(err)
			}
			dials, seen := wire.observed()
			if len(seen) != 3 || len(dials) != 3 {
				t.Fatalf("missing redirect observations %d/%d", len(seen), len(dials))
			}
			for i, r := range seen {
				shouldHave := i == 0 || test.retain
				if (r.Header.Get("Authorization") != "") != shouldHave || (r.Header.Get("Cookie") != "") != shouldHave {
					t.Fatalf("credential scope at hop %d: %s", i, r.Host)
				}
				if r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("Origin") != "" || r.Header.Get("Referer") != "" {
					t.Fatal("ambient header reached origin")
				}
				if shouldHave {
					if r.Header.Get("User-Agent") != "stored-client" || r.Header.Get("Accept") != "application/yaml" || r.Header.Get("X-API-Key") != "synthetic-provider-key" {
						t.Fatal("committed declaration was not sent")
					}
				} else if r.Header.Get("User-Agent") == "stored-client" || r.Header.Get("Accept") != "" || r.Header.Get("X-API-Key") != "" {
					t.Fatal("committed header leaked across origin or returned origin")
				}
			}
		})
	}
}

func TestWebSourceOriginalURLBasicIsPurposeBoundAndStrippedFromRequestURL(t *testing.T) {
	wire := &webSourceTestWire{respond: func(_ *http.Request, conn net.Conn) {
		webSourceTestResponse(conn, 200, nil, strings.NewReader("ok"), 2)
	}}
	_, err := webFetchPurposeSource(context.Background(), WebAPISourceRequest{Purpose: WebAPISourceProfileImport, URL: "http://synthetic-user:synthetic-password@source.test/path"}, nil, wire.options())
	if err != nil {
		t.Fatal(err)
	}
	_, requests := wire.observed()
	if len(requests) != 1 {
		t.Fatal("no wire request")
	}
	user, password, ok := requests[0].BasicAuth()
	if !ok || user != "synthetic-user" || password != "synthetic-password" || requests[0].URL.User != nil {
		t.Fatal("original Basic credentials lost or exposed in wire URL")
	}
}

func TestWebSourcePrivateGrantBindsCommittedHeaderAndFullURLIdentity(t *testing.T) {
	store := webSourceTestStore(t)
	identity := WebAPICommittedSource{URL: "http://lan.test/provider?token=synthetic", CommittedHeaders: map[string][]string{"Cookie": {"literal=synthetic"}}}
	wire := &webSourceTestWire{respond: func(_ *http.Request, conn net.Conn) {
		webSourceTestResponse(conn, 200, nil, strings.NewReader("ok"), 2)
	}}
	options := wire.options()
	options.LookupNetIP = webSourceTestDNS("10.1.2.3")
	clock := time.Now()
	options.Now = func() time.Time { return clock }
	options.ResolveRuntimeSourceIdentity = func(context.Context, *WebAPIReadTx, WebAPISourceResource) (WebAPICommittedSource, error) {
		return identity, nil
	}
	service := NewWebAPISourceService(store, WebAPISourceOptions{WebAPISourceFetchOptions: options, ResolveRuntimeSourceIdentity: func(context.Context, *WebAPIReadTx, WebAPISourceResource) (WebAPICommittedSource, error) {
		return identity, nil
	}})
	ctx := webSourceTestContext("source-session")
	key := &WebAPISourceResource{Kind: "external-provider", ID: "123456789012345678901234567890"}
	prepare := WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceExternalProviderRefresh, Key: key}
	_, grant := webSourceTestAuthorize(t, service, ctx, prepare, "header-grant")
	request := WebAPISourceRequest{Purpose: prepare.Purpose, Key: key, URL: identity.URL, CommittedHeaders: identity.CommittedHeaders, AuthorizationID: grant.ID}
	if _, err := WebAPIFetchSource(ctx, store, request, options); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"header", "path", "query", "userinfo", "purpose", "key", "session", "expiry"} {
		t.Run(mutation, func(t *testing.T) {
			changed := request
			callCtx := ctx
			switch mutation {
			case "header":
				changed.CommittedHeaders = map[string][]string{"Cookie": {"literal=changed"}}
			case "path":
				changed.URL = "http://lan.test/other?token=synthetic"
			case "query":
				changed.URL = "http://lan.test/provider?token=changed"
			case "userinfo":
				changed.URL = "http://user:changed@lan.test/provider?token=synthetic"
			case "purpose":
				changed.Purpose = WebAPISourceGeoRefresh
				changed.CommittedHeaders = nil
				changed.Key = &WebAPISourceResource{Kind: "geo", ID: "opaque"}
			case "key":
				changed.Key = &WebAPISourceResource{Kind: "external-provider", ID: "other"}
			case "session":
				callCtx = webSourceTestContext("other-session")
			case "expiry":
				clock = clock.Add(2 * time.Hour)
				defer func() { clock = clock.Add(-2 * time.Hour) }()
			}
			_, err := WebAPIFetchSource(callCtx, store, changed, options)
			if err == nil {
				t.Fatal("changed grant identity accepted")
			}
			dials, _ := wire.observed()
			if len(dials) != 1 {
				t.Fatalf("changed identity dialed: %v", dials)
			}
		})
	}
	_, staged, err := service.Prepare(ctx, prepare)
	if err != nil {
		t.Fatal(err)
	}
	identity.CommittedHeaders = map[string][]string{"Cookie": {"literal=changed-after-prepare"}}
	_, err = service.Authorize(ctx, WebAPISourceAuthorize{ExpectedRevision: "0", OperationID: "changed-after-prepare", PreparationID: staged.PreparationID, URLDigest: staged.URLDigest, Intent: "authorize-server-private-source"})
	webBackupRequireCode(t, err, "source_target_changed")
	wire.respond = func(_ *http.Request, conn net.Conn) {
		webSourceTestResponse(conn, 302, http.Header{"Location": {"/different-private-resource"}}, strings.NewReader(""), 0)
	}
	_, err = webFetchPurposeSource(ctx, request, &grant, options)
	webBackupRequireCode(t, err, "source_redirect_authorization_required")
	_, requests := wire.observed()
	if len(requests) != 2 {
		t.Fatal("private grant escaped its full URL")
	}
}

func TestWebSourceCommittedCookieClearedOnSchemeChange(t *testing.T) {
	certificate, roots := webSourceTestCertificate(t)
	wire := &webSourceTestWire{
		tlsForAddress: func(address string) *tls.Config {
			if strings.HasSuffix(address, ":443") {
				return &tls.Config{Certificates: []tls.Certificate{certificate}, MaxVersion: tls.VersionTLS12}
			}
			return nil
		},
		respond: func(r *http.Request, conn net.Conn) {
			if r.URL.Path == "/start" {
				webSourceTestResponse(conn, 302, http.Header{"Location": {"https://source.test/final"}}, strings.NewReader(""), 0)
			} else {
				webSourceTestResponse(conn, 200, nil, strings.NewReader("ok"), 2)
			}
		},
	}
	options := wire.options()
	options.TLSRootCAs = roots
	request := WebAPISourceRequest{Purpose: WebAPISourceExternalProviderRefresh, Key: &WebAPISourceResource{Kind: "external-provider", ID: "123456789012345678901234567890"}, URL: "http://source.test/start", CommittedHeaders: map[string][]string{"Cookie": {"literal=synthetic"}, "Authorization": {"Bearer synthetic"}, "X-API-Key": {"synthetic-vendor"}}}
	if _, err := webFetchPurposeSource(context.Background(), request, nil, options); err != nil {
		t.Fatal(err)
	}
	_, requests := wire.observed()
	if len(requests) != 2 || requests[0].Header.Get("Cookie") == "" || requests[1].Header.Get("Cookie") != "" || requests[1].Header.Get("Authorization") != "" || requests[1].Header.Get("X-API-Key") != "" {
		t.Fatal("scheme change retained committed credentials")
	}
}
