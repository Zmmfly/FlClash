//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
)

func TestWebSourceHTTPPrepareCancelAuthorizeReplayAndImportBinding(t *testing.T) {
	fetches := 0
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches++
		w.Write([]byte("proxies: []\nrules:\n - MATCH,DIRECT\n"))
	}))
	defer origin.Close()
	local, _ := url.Parse(origin.URL)
	f := newWebContentHTTPFixture(t, nil, nil)
	options := WebAPISourceFetchOptions{LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.20.30.40")}, nil
	}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "10.20.30.40:"+local.Port() {
			t.Errorf("unapproved dial %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, local.Host)
	}}
	f.sources.options.WebAPISourceFetchOptions = options
	f.content.sourceOptions = options
	request := WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceProfileImport, URL: "http://private.test:" + local.Port() + "/subscription"}
	status, body := f.request(t, "POST", "/api/v1/sources:prepare", request, f.cookie)
	if status != 200 {
		t.Fatalf("prepare %d: %s", status, body)
	}
	var prepared WebAPIEnvelope[WebAPISourcePreparation]
	if err := json.Unmarshal(body, &prepared); err != nil {
		t.Fatal(err)
	}
	if fetches != 0 || !prepared.Data.RequiresPrivateNetworkConfirmation || prepared.Data.NetworkView != "server" {
		t.Fatal("prepare fetched content or hid network authority")
	}
	status, _ = f.request(t, "GET", "/api/v1/profiles", nil, f.cookie)
	if status != 200 {
		t.Fatal("cancel changed business state")
	}
	authorize := WebAPISourceAuthorize{ExpectedRevision: "0", OperationID: "authorize-private", PreparationID: prepared.Data.PreparationID, URLDigest: prepared.Data.URLDigest, Intent: "authorize-server-private-source"}
	status, first := f.request(t, "POST", "/api/v1/sources:authorize", authorize, f.cookie)
	if status != 200 {
		t.Fatalf("authorize %d: %s", status, first)
	}
	status, replay := f.request(t, "POST", "/api/v1/sources:authorize", authorize, f.cookie)
	if status != 200 || !bytes.Equal(first, replay) {
		t.Fatal("authorization receipt not replayable")
	}
	var granted struct {
		Data WebAPISourceAuthorizationResult `json:"data"`
	}
	json.Unmarshal(first, &granted)
	if granted.Data.ImportOperationID != prepared.Data.PreparationID {
		t.Fatal("import grant not bound to job")
	}
	other := webLogin(t, f.auth)
	if status, _ := f.request(t, "POST", "/api/v1/sources:authorize", authorize, other); status != 403 {
		t.Fatalf("cross-session authorize replay %d", status)
	}
	importRequest := webContentImportRequest("wrong-import-op", "0")
	importRequest.Source = WebAPIContentImportSource{Kind: "url", URL: request.URL, AuthorizationID: granted.Data.AuthorizationID}
	if status, _ := f.request(t, "POST", "/api/v1/profiles:import", importRequest, f.cookie); status != 409 || fetches != 0 {
		t.Fatal("grant crossed import-job boundary")
	}
	importRequest.OperationID = granted.Data.ImportOperationID
	status, imported := f.request(t, "POST", "/api/v1/profiles:import", importRequest, f.cookie)
	if status != 200 {
		t.Fatalf("private import %d: %s", status, imported)
	}
	if fetches != 1 {
		t.Fatalf("private fetch count %d", fetches)
	}
	if status, replay := f.request(t, "POST", "/api/v1/profiles:import", importRequest, f.cookie); status != 200 || !bytes.Equal(imported, replay) || fetches != 1 {
		t.Fatal("import replay fetched again")
	}
	_, archive, err := f.backup.Export(context.Background(), "flclash-v1", false)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := webReadBackupArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(parsed.Entries["data.json"], []byte(granted.Data.AuthorizationID)) {
		t.Fatal("backup exported private grant")
	}
	reset := WebAPIReset{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "reset-grants"}, Intent: "reset-application-data"}
	if status, body := f.request(t, "POST", "/api/v1/app:reset", reset, f.cookie); status != 200 {
		t.Fatalf("reset %d: %s", status, body)
	}
	_, err = f.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		_, err := tx.SourceGrant(granted.Data.AuthorizationID)
		webBackupRequireCode(t, err, "not_found")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebSourceHTTPPreparationTargetRevisionAndSessionBinding(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	f.sources.options.LookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.0.0.1")}, nil
	}
	request := WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceProfileImport, URL: "http://private.test/sub"}
	status, body := f.request(t, "POST", "/api/v1/sources:prepare", request, f.cookie)
	if status != 200 {
		t.Fatalf("prepare %d: %s", status, body)
	}
	var prepared WebAPIEnvelope[WebAPISourcePreparation]
	json.Unmarshal(body, &prepared)
	authorize := WebAPISourceAuthorize{ExpectedRevision: "0", OperationID: "stale-source-grant", PreparationID: prepared.Data.PreparationID, URLDigest: prepared.Data.URLDigest, Intent: "authorize-server-private-source"}
	other := webLogin(t, f.auth)
	if status, _ := f.request(t, "POST", "/api/v1/sources:authorize", authorize, other); status != 409 {
		t.Fatalf("cross-session prepare %d", status)
	}
	if status, body := f.request(t, "POST", "/api/v1/profiles:import", webContentImportRequest("change-target-rev", "0"), f.cookie); status != 200 {
		t.Fatalf("seed %d: %s", status, body)
	}
	if status, _ := f.request(t, "POST", "/api/v1/sources:authorize", authorize, f.cookie); status != 409 {
		t.Fatalf("stale revision grant %d", status)
	}
}
