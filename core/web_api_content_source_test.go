//go:build webpanel && !android

package core

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWebSourceAddressClassification(t *testing.T) {
	cases := map[string]string{"8.8.8.8": "public", "2001:4860:4860::8888": "public", "10.2.3.4": "private", "127.0.0.1": "private", "::1": "private", "fd00::1": "private", "100.64.0.1": "private", "100.100.100.200": "forbidden", "::ffff:100.100.100.200": "forbidden", "fd00:ec2::254": "forbidden", "::ffff:127.0.0.1": "private", "::ffff:100.64.0.1": "private", "169.254.169.254": "forbidden", "::ffff:169.254.169.254": "forbidden", "fe80::1": "forbidden", "0.0.0.0": "forbidden", "255.255.255.255": "forbidden", "192.0.2.1": "forbidden", "224.0.0.1": "forbidden", "64:ff9b::a00:1": "forbidden"}
	for text, want := range cases {
		if got := webSourceAddressClass(netip.MustParseAddr(text)); got != want {
			t.Errorf("%s = %s, want %s", text, got, want)
		}
	}
}

func TestWebSourcePinnedDialNoAmbientCredentialsAndRedirectPolicy(t *testing.T) {
	var mu sync.Mutex
	visited := []string{}
	authAtOther := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		visited = append(visited, r.Host+r.URL.Path)
		mu.Unlock()
		if r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("Referer") != "" {
			t.Error("ambient header forwarded")
		}
		switch r.URL.Path {
		case "/private":
			http.Redirect(w, r, "http://private.test:"+strings.Split(r.Host, ":")[1]+"/payload", 302)
		case "/other":
			http.Redirect(w, r, "http://other.test:"+strings.Split(r.Host, ":")[1]+"/payload", 302)
		default:
			if strings.HasPrefix(r.Host, "other.test") {
				authAtOther = r.Header.Get("Authorization")
			}
			io.WriteString(w, "proxies: []\nrules:\n - MATCH,DIRECT\n")
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	var dialed []string
	options := WebAPISourceFetchOptions{LookupNetIP: func(ctx context.Context, network, host string) ([]netip.Addr, error) {
		if host == "private.test" {
			return []netip.Addr{netip.MustParseAddr("10.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return (&net.Dialer{}).DialContext(ctx, network, u.Host)
	}}
	request := WebAPISourceRequest{Purpose: WebAPISourceProfileImport, URL: "http://synthetic:secret@public.test:" + u.Port() + "/private"}
	_, err := webFetchPurposeSource(context.Background(), request, nil, options)
	webBackupRequireCode(t, err, "source_redirect_authorization_required")
	if len(dialed) != 1 || dialed[0] != "8.8.8.8:"+u.Port() {
		t.Fatalf("unpinned/private dial: %v", dialed)
	}
	request.URL = "http://synthetic:secret@public.test:" + u.Port() + "/other"
	if _, err = webFetchPurposeSource(context.Background(), request, nil, options); err != nil {
		t.Fatal(err)
	}
	if authAtOther != "" {
		t.Fatal("cross-origin Basic authorization leaked")
	}
}

func TestWebSourceGrantPinsAllDNSAnswersAndExpires(t *testing.T) {
	ctx := webSourceTestContext("synthetic-session")
	request := WebAPISourceRequest{Purpose: WebAPISourceProfileRefresh, Key: &WebAPISourceResource{Kind: "profile", ID: "1"}, URL: "http://lan.test/sub", AuthorizationID: "grant-1"}
	u, digest, err := webSourceCanonical(request.URL)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	grant := &WebAPISourceGrant{ID: "grant-1", Purpose: request.Purpose, Key: request.Key, URLDigest: digest, Scheme: "http", Host: "lan.test", Port: 80, Addresses: []string{"10.0.0.1"}, PolicyVersion: WebAPISourcePolicyVersion, SessionID: "synthetic-session", ExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano)}
	if err := webSourceValidateGrant(ctx, request, u, digest, []netip.Addr{netip.MustParseAddr("10.0.0.1")}, grant, now); err != nil {
		t.Fatal(err)
	}
	webBackupRequireCode(t, webSourceValidateGrant(ctx, request, u, digest, []netip.Addr{netip.MustParseAddr("10.0.0.2")}, grant, now), "source_target_changed")
	webBackupRequireCode(t, webSourceValidateGrant(ctx, request, u, digest, []netip.Addr{netip.MustParseAddr("10.0.0.1")}, grant, now.Add(2*time.Minute)), "source_authorization_invalid")
	changed := request
	changed.Key = &WebAPISourceResource{Kind: "profile", ID: "2"}
	webBackupRequireCode(t, webSourceValidateGrant(ctx, changed, u, digest, []netip.Addr{netip.MustParseAddr("10.0.0.1")}, grant, now), "source_authorization_invalid")
	changed = request
	changed.URL = "http://lan.test/other"
	u2, d2, _ := webSourceCanonical(changed.URL)
	webBackupRequireCode(t, webSourceValidateGrant(ctx, changed, u2, d2, []netip.Addr{netip.MustParseAddr("10.0.0.1")}, grant, now), "source_authorization_invalid")
}

func TestWebSourceRejectsMixedAnswersMetadataBeforeAnyDial(t *testing.T) {
	calls := 0
	options := WebAPISourceFetchOptions{LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.1")}, nil
	}, DialContext: func(context.Context, string, string) (net.Conn, error) {
		calls++
		t.Fatal("unexpected dial")
		return nil, nil
	}}
	_, err := webFetchPurposeSource(context.Background(), WebAPISourceRequest{Purpose: WebAPISourceProfileImport, URL: "http://mixed.test/sub"}, nil, options)
	webBackupRequireCode(t, err, "source_authorization_required")
	for _, host := range []string{"metadata.google.internal", "instance-data.ec2.internal"} {
		_, err = webFetchPurposeSource(context.Background(), WebAPISourceRequest{Purpose: WebAPISourceProfileImport, URL: "http://" + host + "/token"}, nil, options)
		webBackupRequireCode(t, err, "source_address_forbidden")
	}
	if calls != 0 {
		t.Fatal("forbidden metadata fetched")
	}
	options.LookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("::ffff:169.254.169.254")}, nil
	}
	_, _, err = webSourceResolve(context.Background(), &url.URL{Scheme: "http", Host: "cloud.test"}, options)
	webBackupRequireCode(t, err, "source_address_forbidden")
}

func TestWebSourceApprovedLANFetchUsesOnlySyntheticDial(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "proxies: []\n") }))
	defer server.Close()
	local, _ := url.Parse(server.URL)
	request := WebAPISourceRequest{Purpose: WebAPISourceProfileImport, URL: "http://lan.test:" + local.Port() + "/sub", AuthorizationID: "grant-2"}
	u, digest, _ := webSourceCanonical(request.URL)
	_, _, port := webSourceOrigin(u)
	ctx := webSourceTestContext("synthetic-session")
	grant := &WebAPISourceGrant{ID: request.AuthorizationID, Purpose: request.Purpose, URLDigest: digest, Scheme: "http", Host: "lan.test", Port: port, Addresses: []string{"10.1.2.3"}, PolicyVersion: 1, SessionID: "synthetic-session", ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339Nano)}
	dialed := []string{}
	options := WebAPISourceFetchOptions{LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.1.2.3")}, nil
	}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return (&net.Dialer{}).DialContext(ctx, network, local.Host)
	}}
	result, err := webFetchPurposeSource(ctx, request, grant, options)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Bytes) != "proxies: []\n" || !reflect.DeepEqual(dialed, []string{"10.1.2.3:" + local.Port()}) {
		t.Fatalf("unexpected LAN fetch %+v %v", result, dialed)
	}
}
