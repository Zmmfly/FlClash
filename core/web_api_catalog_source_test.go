//go:build webpanel && !android

package core

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func catalogSourceHTTPOptions(t *testing.T, handler http.Handler) *WebAPISourceFetchOptions {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &WebAPISourceFetchOptions{
		LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "8.8.8.8:80" {
				t.Errorf("source did not dial the pinned resolved IP: %s", address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	}
}

func TestWebAPICatalogContractIconRefreshPrivateSourceGrantHTTP(t *testing.T) {
	var dials atomic.Int64
	var rebound atomic.Bool
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != "GET" || request.URL.Path != "/gallery" {
			t.Error(request.Method, request.URL.String())
		}
		_, _ = w.Write([]byte("{\"icons\":[{\"name\":\"private-gallery-icon\",\"url\":\"https://unit.invalid/icon\"}]}"))
	}))
	defer source.Close()
	options := &WebAPISourceFetchOptions{
		LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
			if rebound.Load() {
				return []netip.Addr{netip.MustParseAddr("10.1.2.4")}, nil
			}
			return []netip.Addr{netip.MustParseAddr("10.1.2.3")}, nil
		},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dials.Add(1)
			if address != "10.1.2.3:80" {
				t.Error("not pinned", address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, source.Listener.Addr().String())
		},
	}
	r := newCatalogTestRig(t, options)
	r.session.Store("synthetic-catalog-session")
	icon := catalogData[WebAPIIconSet](t, r.call("POST", "/api/v1/icon-sets:create", catalogMutation("0", "private-gallery-create", map[string]any{"iconSet": map[string]any{"name": "Private", "url": "http://lan.test/gallery", "icons": []any{}}}), 200)).Data
	denied := r.call("POST", "/api/v1/icon-sets:refresh", catalogMutation("1", "private-without-grant", map[string]any{"id": icon.ID}), 403)
	if dials.Load() != 0 || !strings.Contains(string(denied), "source_") {
		t.Fatal("private source dialed without consent", string(denied), dials.Load())
	}
	prepared := catalogData[WebAPISourcePreparation](t, r.call("POST", "/api/v1/sources:prepare", map[string]any{"expectedRevision": "1", "purpose": "icon-set-refresh", "key": map[string]any{"kind": "icon-set", "id": icon.ID}}, 200)).Data
	if !prepared.RequiresPrivateNetworkConfirmation || prepared.NetworkView != "server" {
		t.Fatal(prepared)
	}
	grant := catalogData[WebAPISourceAuthorizationResult](t, r.call("POST", "/api/v1/sources:authorize", map[string]any{"expectedRevision": r.revision(), "operationId": "authorize-private-gallery", "preparationId": prepared.PreparationID, "urlDigest": prepared.URLDigest, "intent": "authorize-server-private-source"}, 200)).Data
	if grant.AuthorizationID == "" || r.revision() != "1" {
		t.Fatal(grant, r.revision())
	}
	r.call("POST", "/api/v1/icon-sets:refresh", catalogMutation("1", "refresh-private-gallery", map[string]any{"id": icon.ID, "authorizationId": grant.AuthorizationID}), 200)
	if dials.Load() != 1 {
		t.Fatal(dials.Load())
	}
	rebound.Store(true)
	if code := r.callIfAny("POST", "/api/v1/icon-sets:refresh", catalogMutation("2", "rebound-private-gallery", map[string]any{"id": icon.ID, "authorizationId": grant.AuthorizationID})); code < 400 {
		t.Fatal("DNS rebind reused a grant", code)
	}
	rebound.Store(false)
	r.session.Store("different-catalog-session")
	if code := r.callIfAny("POST", "/api/v1/icon-sets:refresh", catalogMutation("2", "cross-session-gallery", map[string]any{"id": icon.ID, "authorizationId": grant.AuthorizationID})); code < 400 {
		t.Fatal("cross-session grant was accepted", code)
	}
	r.session.Store("synthetic-catalog-session")
	other := catalogData[WebAPIIconSet](t, r.call("POST", "/api/v1/icon-sets:create", catalogMutation("2", "other-private-gallery", map[string]any{"iconSet": map[string]any{"name": "Other", "url": "http://lan.test/gallery", "icons": []any{}}}), 200)).Data
	if code := r.callIfAny("POST", "/api/v1/icon-sets:refresh", catalogMutation("3", "cross-resource-gallery", map[string]any{"id": other.ID, "authorizationId": grant.AuthorizationID})); code < 400 {
		t.Fatal("cross-resource grant was accepted", code)
	}
	r.now.Add(int64(time.Hour + time.Second))
	if code := r.callIfAny("POST", "/api/v1/icon-sets:refresh", catalogMutation("3", "expired-private-gallery", map[string]any{"id": icon.ID, "authorizationId": grant.AuthorizationID})); code < 400 {
		t.Fatal("expired grant was accepted", code)
	}
	if dials.Load() != 1 || r.revision() != "3" {
		t.Fatal("denied source request dialed or committed", dials.Load(), r.revision())
	}
}
