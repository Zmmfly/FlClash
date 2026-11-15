//go:build webpanel && !android

package core

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

type webDAVTestWire struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   [][]byte
	dials    []string
	handle   func(*http.Request, []byte) *http.Response
}

func webDAVTestResponse(status int, header http.Header, body []byte) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: status, Header: header, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}
}

func (f *webDAVTestWire) options() WebAPIDAVOptions {
	return WebAPIDAVOptions{LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}, DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
		client, server := net.Pipe()
		f.mu.Lock()
		f.dials = append(f.dials, address)
		f.mu.Unlock()
		go func() {
			defer server.Close()
			request, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				return
			}
			body, err := io.ReadAll(request.Body)
			if err != nil {
				return
			}
			request.Body.Close()
			f.mu.Lock()
			f.requests = append(f.requests, request)
			f.bodies = append(f.bodies, body)
			f.mu.Unlock()
			response := f.handle(request, body)
			if response == nil {
				return
			}
			response.Request = request
			_ = response.Write(server)
		}()
		return client, nil
	}}
}

func webDAVTestTransport(t *testing.T, wire *webDAVTestWire, action WebAPIDAVAction) *webDAVTransport {
	t.Helper()
	transport, err := newWebDAVTransport(WebAPISettingsDAV{URI: "http://dav.test/base", User: "synthetic-user", Password: "synthetic-password", FileName: "backup name.zip"}, action, "", nil, wire.options(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return transport
}

func webDAVErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("wanted %s, got nil", want)
	}
	problem, ok := err.(*WebAPIError)
	if !ok || problem.Code != want {
		t.Fatalf("wanted %s, got %v", want, err)
	}
	if strings.Contains(err.Error(), "synthetic-password") || strings.Contains(err.Error(), "dav.test") {
		t.Fatal("DAV error leaked credentials or endpoint")
	}
}

func TestWebDAVTransportNativeMethodsBasicConditionalAndPaths(t *testing.T) {
	wire := &webDAVTestWire{handle: func(r *http.Request, body []byte) *http.Response {
		user, password, ok := r.BasicAuth()
		if !ok || user != "synthetic-user" || password != "synthetic-password" {
			t.Error("missing purpose-bound Basic credentials")
		}
		for _, key := range []string{"Cookie", "Proxy-Authorization", "Referer", "Origin"} {
			if r.Header.Get(key) != "" {
				t.Error("forwarded browser header")
			}
		}
		switch r.Method {
		case "OPTIONS":
			if r.URL.Path != "/base/" {
				t.Error("probe path")
			}
			return webDAVTestResponse(204, nil, nil)
		case "MKCOL":
			if r.URL.Path != "/base/FlClash/" {
				t.Error("collection path")
			}
			return webDAVTestResponse(405, nil, nil)
		case "HEAD":
			return webDAVTestResponse(200, http.Header{"Etag": {`"prior"`}}, nil)
		case "PUT":
			if r.RequestURI != "/base/FlClash/backup%20name.zip" || r.Header.Get("If-Match") != `"prior"` || string(body) != "synthetic archive" {
				t.Error("upload boundary")
			}
			return webDAVTestResponse(204, http.Header{"Etag": {`"next"`}}, nil)
		case "GET":
			return webDAVTestResponse(200, http.Header{"Etag": {`"next"`}}, []byte("synthetic archive"))
		default:
			t.Error("non-DAV method")
			return webDAVTestResponse(500, nil, nil)
		}
	}}
	transport := webDAVTestTransport(t, wire, WebAPIDAVBackupAction)
	if result, err := transport.probe(t.Context()); err != nil || !result.Reachable {
		t.Fatalf("probe: %v", err)
	}
	if result, err := transport.backup(t.Context(), []byte("synthetic archive")); err != nil || !result.Conditional || result.ETag != `"next"` {
		t.Fatalf("backup: %+v %v", result, err)
	}
	if body, _, err := transport.download(t.Context()); err != nil || string(body) != "synthetic archive" {
		t.Fatalf("download: %v", err)
	}
	if len(wire.requests) != 5 {
		t.Fatalf("methods: %d", len(wire.requests))
	}
	for _, address := range wire.dials {
		if address != "8.8.8.8:80" {
			t.Fatalf("non-pinned dial %s", address)
		}
	}
}

func webDAVParseAuthorization(t *testing.T, header string) map[string]string {
	t.Helper()
	parts, ok := webDAVChallengeParts(strings.TrimPrefix(header, "Digest "))
	if !ok {
		t.Fatal("bad digest header")
	}
	result := map[string]string{}
	for _, part := range parts {
		key, raw, ok := strings.Cut(part, "=")
		value, valid := webDAVUnquote(raw)
		if !ok || !valid {
			t.Fatal("bad digest field")
		}
		result[key] = value
	}
	return result
}

func TestWebDAVTransportDigestAlgorithmsNonceAndQuotedFields(t *testing.T) {
	for _, algorithm := range []string{"MD5", "MD5-sess", "SHA-256", "SHA-256-sess"} {
		t.Run(algorithm, func(t *testing.T) {
			var count int
			wire := &webDAVTestWire{handle: func(r *http.Request, _ []byte) *http.Response {
				if !strings.HasPrefix(r.Header.Get("Authorization"), "Digest ") {
					return webDAVTestResponse(401, http.Header{"Www-Authenticate": {`Basic realm="fallback", Digest realm="realm,quoted\"", nonce="nonce", qop="auth-int, auth", algorithm=` + algorithm}}, nil)
				}
				count++
				v := webDAVParseAuthorization(t, r.Header.Get("Authorization"))
				if v["qop"] != "auth" || v["nc"] != []string{"00000001", "00000002"}[count-1] || v["uri"] != r.RequestURI {
					t.Error("digest counters or URI")
				}
				hash := func(value string) string {
					if strings.HasPrefix(algorithm, "SHA-256") {
						s := sha256.Sum256([]byte(value))
						return hex.EncodeToString(s[:])
					}
					s := md5.Sum([]byte(value))
					return hex.EncodeToString(s[:])
				}
				ha1 := hash("synthetic-user:" + v["realm"] + ":synthetic-password")
				if strings.HasSuffix(algorithm, "-sess") {
					ha1 = hash(ha1 + ":nonce:" + v["cnonce"])
				}
				want := hash(ha1 + ":nonce:" + v["nc"] + ":" + v["cnonce"] + ":auth:" + hash(r.Method+":"+r.RequestURI))
				if v["response"] != want {
					t.Error("wrong digest response")
				}
				return webDAVTestResponse(204, nil, nil)
			}}
			transport := webDAVTestTransport(t, wire, WebAPIDAVProbeAction)
			for range 2 {
				if _, err := transport.probe(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if count != 2 || len(wire.requests) != 3 {
				t.Fatal("authentication retried unexpectedly")
			}
		})
	}
}

func TestWebDAVDigestRejectsUnsupportedMalformedAndUnboundedChallenges(t *testing.T) {
	for _, header := range []string{`Digest nonce="n", qop="auth-int"`, `Digest nonce="n", algorithm=SHA-999`, `Digest nonce="n", nonce="duplicate"`, `NotDigest nonce="n"`, `Basic realm="Digest"`, `Digest nonce="unterminated`, `Digest nonce="n", qop=""`, `Digest nonce="n", userhash=true`, "Digest nonce=\"n\"\r\nInjected: value", `Digest nonce="` + strings.Repeat("n", webDAVChallengeLimit) + `"`} {
		if webDAVDigestChallenge([]string{header}) != nil {
			t.Errorf("accepted malformed challenge %q", header[:min(len(header), 120)])
		}
	}
	if d := webDAVDigestChallenge([]string{`Digest nonce="n", realm="legacy"`}); d == nil || d.qop != "" {
		t.Fatal("removed native no-qop Digest")
	}
	wire := &webDAVTestWire{handle: func(r *http.Request, _ []byte) *http.Response {
		return webDAVTestResponse(401, http.Header{"Www-Authenticate": {`Digest nonce="same", qop="auth"`}}, nil)
	}}
	_, err := webDAVTestTransport(t, wire, WebAPIDAVProbeAction).probe(t.Context())
	webDAVErrorCode(t, err, "dav_auth_failed")
	if len(wire.requests) != 2 {
		t.Fatal("unbounded authentication retries")
	}
}

func TestWebDAVRedirectCredentialsNeverCrossOriginOrReturn(t *testing.T) {
	wire := &webDAVTestWire{handle: func(r *http.Request, _ []byte) *http.Response {
		switch r.URL.Path {
		case "/base/":
			if r.Header.Get("Authorization") == "" {
				t.Error("initial auth")
			}
			return webDAVTestResponse(307, http.Header{"Location": {"http://other.test/jump"}}, nil)
		case "/jump":
			if r.Header.Get("Authorization") != "" {
				t.Error("cross-origin credentials")
			}
			return webDAVTestResponse(302, http.Header{"Location": {"http://dav.test/back"}}, nil)
		default:
			if r.Header.Get("Authorization") != "" {
				t.Error("credentials resurrected after crossing origin")
			}
			return webDAVTestResponse(204, nil, nil)
		}
	}}
	if _, err := webDAVTestTransport(t, wire, WebAPIDAVProbeAction).probe(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(wire.requests) != 3 {
		t.Fatal("redirect path not exercised")
	}
}

func TestWebDAVRedirectPUTKeepsMethodAndConditions(t *testing.T) {
	wire := &webDAVTestWire{handle: func(r *http.Request, body []byte) *http.Response {
		switch r.Method {
		case "MKCOL":
			return webDAVTestResponse(201, nil, nil)
		case "HEAD":
			return webDAVTestResponse(404, nil, nil)
		}
		if r.Method != "PUT" || r.Header.Get("If-None-Match") != "*" || string(body) != "archive" {
			t.Error("redirect changed upload intent")
		}
		if r.URL.Path != "/moved" {
			return webDAVTestResponse(303, http.Header{"Location": {"/moved"}}, nil)
		}
		return webDAVTestResponse(201, nil, nil)
	}}
	result, err := webDAVTestTransport(t, wire, WebAPIDAVBackupAction).backup(t.Context(), []byte("archive"))
	if err != nil || !result.Conditional {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestWebDAVDNSAllCandidatesPrivateMappedAndHardDenials(t *testing.T) {
	cases := []struct {
		name string
		ips  []string
		code string
	}{
		{"mixed", []string{"8.8.8.8", "10.0.0.1"}, "source_authorization_required"},
		{"mapped", []string{"::ffff:10.0.0.1"}, "source_authorization_required"},
		{"cgnat", []string{"100.64.0.1"}, "source_authorization_required"},
		{"linklocal", []string{"169.254.169.254"}, "source_address_forbidden"},
		{"v6link", []string{"fe80::1"}, "source_address_forbidden"},
		{"special", []string{"8.8.8.8", "198.18.0.1"}, "source_address_forbidden"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			wire := &webDAVTestWire{handle: func(*http.Request, []byte) *http.Response {
				t.Error("forbidden server reached")
				return webDAVTestResponse(204, nil, nil)
			}}
			transport := webDAVTestTransport(t, wire, WebAPIDAVProbeAction)
			transport.options.LookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
				var ips []netip.Addr
				for _, ip := range test.ips {
					ips = append(ips, netip.MustParseAddr(ip))
				}
				return ips, nil
			}
			_, err := transport.probe(t.Context())
			webDAVErrorCode(t, err, test.code)
			if len(wire.dials) != 0 {
				t.Fatal("dial before checking every DNS candidate")
			}
		})
	}
	wire := &webDAVTestWire{}
	transport := webDAVTestTransport(t, wire, WebAPIDAVProbeAction)
	transport.origin.Host = "metadata.google.internal:80"
	_, err := transport.probe(t.Context())
	webDAVErrorCode(t, err, "source_address_forbidden")
}

func TestWebDAVPrivateGrantTupleSessionDNSAndRedirectBinding(t *testing.T) {
	wire := &webDAVTestWire{handle: func(*http.Request, []byte) *http.Response { return webDAVTestResponse(204, nil, nil) }}
	transport := webDAVTestTransport(t, wire, WebAPIDAVProbeAction)
	transport.options.LookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("::ffff:10.0.0.1")}, nil
	}
	_, digest, _ := webSourceCanonical(transport.dav.URI)
	transport.request.AuthorizationID = "synthetic-grant"
	transport.grant = &WebAPISourceGrant{ID: "synthetic-grant", Purpose: transport.request.Purpose, Key: transport.request.Key, URLDigest: digest, Scheme: "http", Host: "dav.test", Port: 80, Addresses: []string{"10.0.0.1"}, PolicyVersion: WebAPISourcePolicyVersion, SessionID: "session-a", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), RequestDigest: transport.request.RequestDigest}
	ctx := WithWebAPIRequestAuthorization(t.Context(), "session-a", func(context.Context) (func(), error) { return func() {}, nil })
	if _, err := transport.probe(ctx); err != nil {
		t.Fatal(err)
	}
	if wire.dials[0] != "10.0.0.1:80" {
		t.Fatal("mapped address was not unwrapped")
	}
	transport.grant.RequestDigest = "changed"
	_, err := transport.probe(ctx)
	webDAVErrorCode(t, err, "source_authorization_invalid")
	transport.grant.RequestDigest = transport.request.RequestDigest
	transport.grant.Addresses = []string{"10.0.0.2"}
	_, err = transport.probe(ctx)
	webDAVErrorCode(t, err, "source_target_changed")
	transport.grant.Addresses = []string{"10.0.0.1"}
	transport.grant.SessionID = "session-b"
	_, err = transport.probe(ctx)
	webDAVErrorCode(t, err, "source_authorization_invalid")
}

func TestWebDAVConflictAndResponseLossNeverAutomaticallyRetryUpload(t *testing.T) {
	for _, status := range []int{409, 412, 500, 0} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			puts := 0
			wire := &webDAVTestWire{handle: func(r *http.Request, _ []byte) *http.Response {
				switch r.Method {
				case "MKCOL":
					return webDAVTestResponse(405, nil, nil)
				case "HEAD":
					return webDAVTestResponse(200, http.Header{"Etag": {`"observed"`}}, nil)
				}
				puts++
				if status == 0 {
					return nil
				}
				return webDAVTestResponse(status, nil, nil)
			}}
			_, err := webDAVTestTransport(t, wire, WebAPIDAVBackupAction).backup(t.Context(), []byte("archive"))
			code := "dav_remote_conflict"
			if status == 0 || status == 500 {
				code = "dav_effect_unknown"
			}
			webDAVErrorCode(t, err, code)
			if puts != 1 {
				t.Fatalf("upload attempts %d", puts)
			}
		})
	}
}

func TestWebDAVNoETagWeakETagRangesAndTransferBudget(t *testing.T) {
	for _, etag := range []string{"", `W/"weak"`} {
		puts := 0
		wire := &webDAVTestWire{handle: func(r *http.Request, _ []byte) *http.Response {
			if r.Method == "HEAD" {
				header := http.Header{}
				if etag != "" {
					header.Set("ETag", etag)
				}
				return webDAVTestResponse(200, header, nil)
			}
			if r.Method == "PUT" {
				puts++
				if r.Header.Get("If-Match") != "" {
					t.Error("invented entity tag")
				}
			}
			return webDAVTestResponse(204, nil, nil)
		}}
		result, err := webDAVTestTransport(t, wire, WebAPIDAVBackupAction).backup(t.Context(), []byte("archive"))
		if etag != "" {
			webDAVErrorCode(t, err, "dav_weak_etag")
			if puts != 0 {
				t.Fatal("weak ETag overwrite")
			}
		} else if err != nil || result.Conditional {
			t.Fatal("no-ETag request claimed conditional safety")
		}
	}
	for _, span := range []string{"bytes 0-2/3", "bytes 1-3/4", "bytes 0-2/*", "bytes 0-0/0"} {
		wire := &webDAVTestWire{handle: func(*http.Request, []byte) *http.Response {
			return webDAVTestResponse(206, http.Header{"Content-Range": {span}}, []byte("zip"))
		}}
		_, _, err := webDAVTestTransport(t, wire, WebAPIDAVPrepareRestoreAction).download(t.Context())
		if span == "bytes 0-2/3" {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			webDAVErrorCode(t, err, "dav_partial_archive")
		}
	}
	wire := &webDAVTestWire{handle: func(*http.Request, []byte) *http.Response {
		r := webDAVTestResponse(200, nil, nil)
		r.ContentLength = WebAPIDAVMaxArchiveBytes + 1
		return r
	}}
	_, _, err := webDAVTestTransport(t, wire, WebAPIDAVPrepareRestoreAction).download(t.Context())
	webDAVErrorCode(t, err, "dav_archive_too_large")
}
