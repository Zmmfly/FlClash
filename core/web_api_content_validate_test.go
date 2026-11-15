//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/metacubex/age"
)

func TestWebContentHTTPProfileValidateIsReadOnlyPureAndRevisionBound(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	f.content.sourceOptions.LookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		t.Error("validation resolved a source")
		return nil, nil
	}
	f.content.sourceOptions.DialContext = func(context.Context, string, string) (net.Conn, error) {
		t.Error("validation dialed a source")
		return nil, nil
	}
	status, body := f.request(t, "POST", "/api/v1/profiles:import", webContentImportRequest("validation-owner", "0"), f.cookie)
	profile := webContentHTTPDecode[WebAPIProfile](t, status, body).Data
	request := WebAPIProfileValidate{ExpectedRevision: "1", ProfileID: &profile.ID, Bytes: []byte("proxies: []\nproxy-providers:\n  remote:\n    type: http\n    url: https://synthetic.invalid/proxies.yaml\n    path: ./synthetic.yaml\nproxy-groups:\n  - {name: 'group, exact', type: select, use: [remote]}\n")}
	status, body = f.request(t, "POST", "/api/v1/profiles:validate", request, f.cookie)
	result := webContentHTTPDecode[WebAPIProfileValidation](t, status, body)
	if !result.Data.Valid || result.Revision != "1" || result.Data.SHA256 != webContentDigest(request.Bytes) || result.Data.Counts.Outbounds != 1 {
		t.Fatalf("invalid validation result: %s", body)
	}
	status, body = f.request(t, "GET", "/api/v1/content/profile/"+string(profile.ID), nil, f.cookie)
	stored := webContentHTTPDecode[WebAPIContentResult](t, status, body)
	if stored.Revision != "1" || bytes.Equal(stored.Data.Bytes, request.Bytes) {
		t.Fatal("pure validation wrote content or revision")
	}
	request.ExpectedRevision = "0"
	if status, _ = f.request(t, "POST", "/api/v1/profiles:validate", request, f.cookie); status != 409 {
		t.Fatalf("stale validate=%d", status)
	}
	request.ExpectedRevision = "1"
	for _, data := range [][]byte{[]byte("proxies: [unterminated"), []byte("proxies:\n - {name: A, type: direct, dialer-proxy: absent}\n"), []byte("proxies:\n - {name: stateful, type: tailscale, state-dir: /host/private}\n")} {
		request.Bytes = data
		if status, body = f.request(t, "POST", "/api/v1/profiles:validate", request, f.cookie); status == 200 || bytes.Contains(body, []byte(`"valid":true`)) {
			t.Fatalf("invalid content validated: %d %s", status, body)
		}
	}
	request.ProfileID = nil
	request.Bytes = []byte("proxies: []\n")
	status, body = f.request(t, "POST", "/api/v1/profiles:validate", request, f.cookie)
	webContentHTTPDecode[WebAPIProfileValidation](t, status, body)
	unknown := WebAPIID("99999")
	request.ProfileID = &unknown
	if status, _ = f.request(t, "POST", "/api/v1/profiles:validate", request, f.cookie); status != 404 {
		t.Fatalf("unknown owner=%d", status)
	}
	if status, _ = f.request(t, "POST", "/api/v1/profiles:validate", request, nil); status != 401 {
		t.Fatalf("unauthenticated validate=%d", status)
	}
}

func TestWebContentHTTPProfileValidateUsesOnlyExplicitOrOwnerAgeIdentity(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	writer, err := age.Encrypt(&encrypted, identity.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write([]byte("proxies: []\n")); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	imported := webContentImportRequest("encrypted-owner", "0")
	imported.Source.Bytes = encrypted.Bytes()
	imported.AgeSecretKey = identity.String()
	status, body := f.request(t, "POST", "/api/v1/profiles:import", imported, f.cookie)
	profile := webContentHTTPDecode[WebAPIProfile](t, status, body).Data
	request := WebAPIProfileValidate{ExpectedRevision: "1", ProfileID: &profile.ID, Bytes: encrypted.Bytes()}
	status, body = f.request(t, "POST", "/api/v1/profiles:validate", request, f.cookie)
	result := webContentHTTPDecode[WebAPIProfileValidation](t, status, body)
	if !result.Data.Valid || result.Revision != "1" {
		t.Fatal("stored owner age identity was not used")
	}
	empty := ""
	request.AgeSecretKey = &empty
	if status, body = f.request(t, "POST", "/api/v1/profiles:validate", request, f.cookie); status != 400 || !bytes.Contains(body, []byte("age_secret_key_required")) {
		t.Fatalf("explicit empty identity fell back to owner: %d %s", status, body)
	}
	request.ProfileID = nil
	request.AgeSecretKey = nil
	if status, _ = f.request(t, "POST", "/api/v1/profiles:validate", request, f.cookie); status != 400 {
		t.Fatalf("validation used ambient key=%d", status)
	}
	key := identity.String()
	request.AgeSecretKey = &key
	status, body = f.request(t, "POST", "/api/v1/profiles:validate", request, f.cookie)
	webContentHTTPDecode[WebAPIProfileValidation](t, status, body)
}
