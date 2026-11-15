//go:build webpanel && !android

package core

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

type webSourceTestWire struct {
	mu            sync.Mutex
	dials         []string
	requests      []*http.Request
	tls           *tls.Config
	tlsForAddress func(string) *tls.Config
	respond       func(*http.Request, net.Conn)
}

func (wire *webSourceTestWire) options() WebAPISourceFetchOptions {
	return WebAPISourceFetchOptions{LookupNetIP: webSourceTestDNS("8.8.8.8"), DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
		wire.mu.Lock()
		wire.dials = append(wire.dials, address)
		wire.mu.Unlock()
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				server.Close()
			case <-done:
			}
		}()
		go func() {
			defer close(done)
			defer server.Close()
			var conn net.Conn = server
			config := wire.tls
			if wire.tlsForAddress != nil {
				config = wire.tlsForAddress(address)
			}
			if config != nil {
				conn = tls.Server(conn, config)
			}
			request, err := http.ReadRequest(bufio.NewReader(conn))
			if err != nil {
				return
			}
			defer request.Body.Close()
			wire.mu.Lock()
			wire.requests = append(wire.requests, request.Clone(context.Background()))
			wire.mu.Unlock()
			wire.respond(request, conn)
		}()
		return client, nil
	}}
}

func (wire *webSourceTestWire) observed() ([]string, []*http.Request) {
	wire.mu.Lock()
	defer wire.mu.Unlock()
	return append([]string(nil), wire.dials...), append([]*http.Request(nil), wire.requests...)
}

func webSourceTestResponse(conn net.Conn, status int, header http.Header, reader io.Reader, length int64) {
	response := &http.Response{StatusCode: status, ProtoMajor: 1, ProtoMinor: 1, Header: header, ContentLength: length, Body: io.NopCloser(reader)}
	if length < 0 {
		response.TransferEncoding = []string{"chunked"}
	}
	_ = response.Write(conn)
}

type webSourceZeros struct{}

func (webSourceZeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestWebSourceFixedUtilityPolicyChecksEveryHopBeforeDial(t *testing.T) {
	cases := []struct {
		name     string
		purpose  WebAPISourcePurpose
		source   string
		redirect string
	}{
		{"release-host", WebAPISourceAppRelease, webUtilityReleaseMetadataURL, "https://untrusted.test/release"},
		{"release-path", WebAPISourceAppRelease, webUtilityReleaseMetadataURL, "https://api.github.com/arbitrary"},
		{"IP-provider", WebAPISourceIPQualityLookup, "http://ip-api.com/json/8.8.8.8?fields=status,isp,org,as,mobile,proxy,hosting", "https://ident.me/json"},
		{"IP-address", WebAPISourceIPQualityLookup, "http://ip-api.com/json/8.8.8.8?fields=status,isp,org,as,mobile,proxy,hosting", "http://ip-api.com/json/1.1.1.1?fields=status,isp,org,as,mobile,proxy,hosting"},
	}
	cert, roots := webSourceTestCertificate(t)
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			wire := &webSourceTestWire{respond: func(_ *http.Request, conn net.Conn) {
				webSourceTestResponse(conn, 302, http.Header{"Location": {test.redirect}}, strings.NewReader(""), 0)
			}}
			if strings.HasPrefix(test.source, "https:") {
				wire.tls = &tls.Config{Certificates: []tls.Certificate{cert}, MaxVersion: tls.VersionTLS12}
			}
			options := wire.options()
			options.TLSRootCAs = roots
			_, err := webFetchPurposeSource(context.Background(), WebAPISourceRequest{Purpose: test.purpose, URL: test.source}, nil, options)
			if err == nil {
				t.Fatal("changed fixed utility source accepted")
			}
			dials, _ := wire.observed()
			if len(dials) != 1 {
				t.Fatalf("redirect reached unapproved destination: %v; %v", dials, err)
			}
		})
	}
	for _, purpose := range []WebAPISourcePurpose{WebAPISourceAppRelease, WebAPISourceIPQualityLookup} {
		source := webUtilityReleaseMetadataURL
		if purpose == WebAPISourceIPQualityLookup {
			source = "https://ident.me/json"
		}
		options := WebAPISourceFetchOptions{LookupNetIP: webSourceTestDNS("8.8.8.8", "::ffff:10.1.2.3"), DialContext: func(context.Context, string, string) (net.Conn, error) {
			t.Error("utility dialed private target")
			return nil, errors.New("forbidden")
		}}
		request := WebAPISourceRequest{Purpose: purpose, URL: source}
		_, err := webFetchPurposeSource(context.Background(), request, nil, options)
		webBackupRequireCode(t, err, "source_address_forbidden")
		request.AuthorizationID = "not-permitted"
		_, err = webFetchPurposeSource(context.Background(), request, nil, options)
		webBackupRequireCode(t, err, "invalid_source_request")
	}
}

func TestWebSourcePurposeReadBudgetsIncludeStreamingAndEarlyLength(t *testing.T) {
	for _, test := range []struct {
		name    string
		request WebAPISourceRequest
		limit   int64
	}{
		{"release", WebAPISourceRequest{Purpose: WebAPISourceAppRelease, URL: webUtilityReleaseMetadataURL}, 1 << 20},
		{"IP", WebAPISourceRequest{Purpose: WebAPISourceIPQualityLookup, URL: "http://ip-api.com/json/8.8.8.8?fields=status,isp,org,as,mobile,proxy,hosting"}, 256 << 10},
		{"image", WebAPISourceRequest{Purpose: WebAPISourceIconImage, Key: &WebAPISourceResource{Kind: "icon-image", ID: "115792089237316195423570985008687907853269984665640564039457584007913129639935"}, URL: "http://image.test/image"}, 32 << 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			cert, roots := webSourceTestCertificate(t)
			for _, mode := range []string{"exact", "stream-over", "length-over"} {
				t.Run(mode, func(t *testing.T) {
					wire := &webSourceTestWire{respond: func(_ *http.Request, conn net.Conn) {
						if mode == "length-over" {
							fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", test.limit+1)
							_, _ = io.Copy(io.Discard, conn)
							return
						}
						size := test.limit
						if mode == "stream-over" {
							size++
						}
						webSourceTestResponse(conn, 200, http.Header{"Content-Type": {"application/octet-stream"}}, io.LimitReader(webSourceZeros{}, size), -1)
					}}
					if strings.HasPrefix(test.request.URL, "https:") {
						wire.tls = &tls.Config{Certificates: []tls.Certificate{cert}, MaxVersion: tls.VersionTLS12}
					}
					options := wire.options()
					options.TLSRootCAs = roots
					result, err := webFetchPurposeSource(context.Background(), test.request, nil, options)
					if mode == "exact" {
						if err != nil || int64(len(result.Bytes)) != test.limit {
							t.Fatalf("exact body rejected: %d %v", len(result.Bytes), err)
						}
					} else {
						webBackupRequireCode(t, err, "content_too_large")
						if len(result.Bytes) != 0 {
							t.Fatal("oversized partial result escaped")
						}
					}
				})
			}
		})
	}
}

func TestWebSourceStructuredStatusContentTypeAndRedactedTimeouts(t *testing.T) {
	request := WebAPISourceRequest{Purpose: WebAPISourceProfileImport, URL: "http://synthetic-user:synthetic-password@public.test/sub?token=synthetic-query"}
	for _, status := range []int{200, 429, 503} {
		wire := &webSourceTestWire{respond: func(_ *http.Request, conn net.Conn) {
			webSourceTestResponse(conn, status, http.Header{"Content-Type": {"IMAGE/PNG; charset=utf-8"}}, strings.NewReader("ok"), 2)
		}}
		result, err := webFetchPurposeSource(context.Background(), request, nil, wire.options())
		if result.StatusCode != status || result.ContentType != "image/png" {
			t.Fatalf("structured metadata lost: %+v", result)
		}
		if status == 200 && (err != nil || string(result.Bytes) != "ok") {
			t.Fatal(err)
		}
		if status == 429 {
			webBackupRequireCode(t, err, "source_rate_limited")
		}
		if status == 503 {
			webBackupRequireCode(t, err, "source_fetch_failed")
		}
		if err != nil && (strings.Contains(err.Error(), "synthetic-") || strings.Contains(err.Error(), "public.test")) {
			t.Fatal("upstream error exposed source identity")
		}
	}
	for _, phase := range []string{"DNS", "headers", "body"} {
		t.Run(phase, func(t *testing.T) {
			wire := &webSourceTestWire{respond: func(_ *http.Request, conn net.Conn) {
				if phase == "body" {
					fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Length: 3\r\n\r\n")
				}
				_, _ = io.Copy(io.Discard, conn)
			}}
			options := wire.options()
			if phase == "DNS" {
				options.LookupNetIP = func(ctx context.Context, _, _ string) ([]netip.Addr, error) { <-ctx.Done(); return nil, ctx.Err() }
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			_, err := webFetchPurposeSource(ctx, request, nil, options)
			webBackupRequireCode(t, err, "source_timeout")
			var problem *WebAPIError
			if !errors.As(err, &problem) || problem.Status != 504 || strings.Contains(err.Error(), "synthetic-") {
				t.Fatalf("timeout not safe/typed: %v", err)
			}
		})
	}
}

func webSourceTestCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "source.test"}, DNSNames: []string{"source.test", "api.github.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}, roots
}

func TestWebSourceTLSHostVerificationAndHTTPSDowngrade(t *testing.T) {
	certificate, roots := webSourceTestCertificate(t)
	for _, test := range []struct {
		name, host string
		trust      bool
		location   string
		want       string
	}{
		{"trusted", "source.test", true, "", ""},
		{"untrusted", "source.test", false, "", "source_fetch_failed"},
		{"wrong-host", "other.test", true, "", "source_fetch_failed"},
		{"downgrade", "source.test", true, "http://source.test/payload", "source_downgrade_forbidden"},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := &webSourceTestWire{tls: &tls.Config{Certificates: []tls.Certificate{certificate}, MaxVersion: tls.VersionTLS12}, respond: func(_ *http.Request, conn net.Conn) {
				if test.location != "" {
					webSourceTestResponse(conn, 302, http.Header{"Location": {test.location}}, bytes.NewReader(nil), 0)
				} else {
					webSourceTestResponse(conn, 200, nil, strings.NewReader("ok"), 2)
				}
			}}
			options := wire.options()
			if test.trust {
				options.TLSRootCAs = roots
			}
			result, err := webFetchPurposeSource(context.Background(), WebAPISourceRequest{Purpose: WebAPISourceProfileImport, URL: "https://" + test.host + "/sub"}, nil, options)
			if test.want == "" {
				if err != nil || string(result.Bytes) != "ok" {
					t.Fatal(err)
				}
			} else {
				webBackupRequireCode(t, err, test.want)
			}
			dials, _ := wire.observed()
			if len(dials) != 1 || dials[0] != "8.8.8.8:443" {
				t.Fatalf("TLS lost pin or followed downgrade: %v", dials)
			}
		})
	}
}

func TestWebSourceGeoBudgetRemainsSeparateFromContent(t *testing.T) {
	request := WebAPISourceRequest{Purpose: WebAPISourceGeoRefresh, Key: &WebAPISourceResource{Kind: "geo", ID: "123456789012345678901234567890"}, URL: "http://geo.test/database"}
	wire := &webSourceTestWire{respond: func(_ *http.Request, conn net.Conn) {
		webSourceTestResponse(conn, 200, nil, io.LimitReader(webSourceZeros{}, (32<<20)+1), -1)
	}}
	result, err := webFetchPurposeSource(context.Background(), request, nil, wire.options())
	if err != nil || len(result.Bytes) != (32<<20)+1 {
		t.Fatalf("geo retained ordinary content cap: %d %v", len(result.Bytes), err)
	}
	wire.respond = func(_ *http.Request, conn net.Conn) {
		fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", WebAPIMaxGeoSourceBytes+1)
		_, _ = io.Copy(io.Discard, conn)
	}
	result, err = webFetchPurposeSource(context.Background(), request, nil, wire.options())
	webBackupRequireCode(t, err, "content_too_large")
	if len(result.Bytes) != 0 || webSourceReadLimit(WebAPISourceGeoRefresh) != 128<<20 || webSourceReadLimit(WebAPISourceProfileImport) != 32<<20 {
		t.Fatal("geo enlarged an ordinary body budget or returned partial data")
	}
}

func TestWebSourceContextDeadlineFromDialIsStructured(t *testing.T) {
	options := WebAPISourceFetchOptions{LookupNetIP: webSourceTestDNS("8.8.8.8"), DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, context.DeadlineExceeded }}
	_, err := webFetchPurposeSource(context.Background(), WebAPISourceRequest{Purpose: WebAPISourceProfileImport, URL: "http://synthetic-user:synthetic-password@source.test/sub"}, nil, options)
	webBackupRequireCode(t, err, "source_timeout")
	var problem *WebAPIError
	if !errors.As(err, &problem) || problem.Status != 504 || strings.Contains(err.Error(), "synthetic") || strings.Contains(err.Error(), "source.test") {
		t.Fatal("dial timeout lost safe structured classification")
	}
}

func TestWebSourceDialTimeoutAndDNSAnswerBudget(t *testing.T) {
	options := WebAPISourceFetchOptions{LookupNetIP: webSourceTestDNS("8.8.8.8"), DialContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.DNSError{Err: "synthetic-secret", IsTimeout: true}
	}}
	request := WebAPISourceRequest{Purpose: WebAPISourceProfileImport, URL: "http://synthetic-user:synthetic-password@source.test/sub"}
	_, err := webFetchPurposeSource(context.Background(), request, nil, options)
	webBackupRequireCode(t, err, "source_timeout")
	if strings.Contains(err.Error(), "synthetic-") {
		t.Fatal("dial error leaked source identity")
	}
	options.LookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		result := make([]netip.Addr, 65)
		for i := range result {
			result[i] = netip.MustParseAddr("8.8.8.8")
		}
		return result, nil
	}
	options.DialContext = func(context.Context, string, string) (net.Conn, error) {
		t.Error("oversized DNS candidate set reached dial")
		return nil, errors.New("forbidden")
	}
	_, err = webFetchPurposeSource(context.Background(), request, nil, options)
	webBackupRequireCode(t, err, "source_address_limit")
}

func TestWebSourceRedirectDNSRebindingAndFixedGet(t *testing.T) {
	lookups := 0
	wire := &webSourceTestWire{respond: func(r *http.Request, conn net.Conn) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected source method %s", r.Method)
		}
		webSourceTestResponse(conn, 307, http.Header{"Location": {"/redirect"}}, strings.NewReader(""), 0)
	}}
	options := wire.options()
	options.LookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		lookups++
		if lookups == 1 {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("::ffff:10.2.3.4")}, nil
	}
	_, err := webFetchPurposeSource(context.Background(), WebAPISourceRequest{Purpose: WebAPISourceProfileImport, URL: "http://rebind.test/start"}, nil, options)
	webBackupRequireCode(t, err, "source_redirect_authorization_required")
	dials, _ := wire.observed()
	if len(dials) != 1 || lookups != 2 {
		t.Fatalf("per-hop DNS pin failed: dials=%v lookups=%d", dials, lookups)
	}
	options.LookupNetIP = webSourceTestDNS("8.8.8.8")
	_, err = webFetchPurposeSource(context.Background(), WebAPISourceRequest{Purpose: WebAPISourceProfileImport, URL: "http://rebind.test/start"}, nil, options)
	webBackupRequireCode(t, err, "source_redirect_limit")
}
