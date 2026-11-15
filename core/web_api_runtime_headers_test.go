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
	"strings"
	"sync/atomic"
	"testing"
)

func TestWebRuntimeCommittedProviderHeadersShareStrongFetchAndExactOriginCredentials(t *testing.T) {
	var requests atomic.Int32
	var target string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("Origin") != "" || r.Header.Get("Referer") != "" {
			t.Error("source fetch forwarded a forbidden incoming/transport credential header")
		}
		switch r.URL.Path {
		case "/start", "/same":
			if r.Header.Get("Cookie") != "provider_session=synthetic" || r.Header.Get("Authorization") != "Bearer synthetic-provider-token" || r.UserAgent() != "SyntheticCommittedAgent" || r.Header.Get("Accept") != "text/plain" {
				t.Error("same-origin request lost its exact committed provider headers or added browser credentials")
			}
			if r.URL.Path == "/start" {
				http.Redirect(w, r, "/same", http.StatusTemporaryRedirect)
			} else {
				http.Redirect(w, r, target, http.StatusTemporaryRedirect)
			}
		case "/end":
			if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
				t.Error("committed Cookie/Authorization crossed an exact-origin boundary")
			}
			_, _ = w.Write([]byte("header.example.test\n"))
		default:
			t.Error("unknown source fixture path")
			w.WriteHeader(404)
		}
	}))
	defer remote.Close()
	_, port, _ := net.SplitHostPort(remote.Listener.Addr().String())
	target = "http://other-provider.test:" + port + "/end"
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	s.runtime.workspace.options.SourceFetchOptions = WebAPISourceFetchOptions{ResolveRuntimeSourceIdentity: s.runtime.ResolveRuntimeSourceIdentity, LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if !strings.HasPrefix(address, "1.1.1.1:") {
			t.Error("un-pinned committed header request")
		}
		return (&net.Dialer{}).DialContext(ctx, network, remote.Listener.Addr().String())
	}}
	profile := "rule-providers:\n  remote:\n    type: http\n    url: 'http://origin-provider.test:" + port + "/start'\n    behavior: domain\n    format: text\n    header:\n      Cookie: provider_session=synthetic\n      Authorization: Bearer synthetic-provider-token\n      User-Agent: SyntheticCommittedAgent\n      Accept: text/plain\nrules: ['RULE-SET,remote,DIRECT']\n"
	webRuntimeSeedProfile(t, s, "file", profile)
	source := webRuntimeTestSource(t, s)
	if _, err := s.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		key := WebAPISourceResource{Kind: "external-provider", ID: string(source.ID)}
		identity, err := s.runtime.ResolveRuntimeSourceIdentity(context.Background(), tx, key)
		if err != nil {
			return err
		}
		if identity.CommittedHeaders["Cookie"][0] != "provider_session=synthetic" {
			t.Error("exact-TX identity omitted committed headers")
		}
		identity.CommittedHeaders["Cookie"][0] = "mutated-local-copy"
		second, err := s.runtime.ResolveRuntimeSourceIdentity(context.Background(), tx, key)
		if err == nil && second.CommittedHeaders["Cookie"][0] != "provider_session=synthetic" {
			t.Error("source identity returned aliased mutable committed headers")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(WebAPIRuntimeProviderRefresh{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "refresh-committed-headers"}, ID: source.ID, Purpose: source.Purpose})
	injected := strings.TrimSuffix(string(request), "}") + `,"headers":{"Cookie":"browser-injected"}}`
	if code, body := s.request(t, "POST", "/api/v1/runtime/provider-sources:refresh", injected, s.cookie, ""); code != 400 || requests.Load() != 0 {
		t.Fatalf("browser supplied temporary source headers %d %s", code, body)
	}
	code, receipt := s.request(t, "POST", "/api/v1/runtime/provider-sources:refresh", string(request), s.cookie, "")
	if code != 200 {
		t.Fatalf("%d %s", code, receipt)
	}
	if state := s.phase(t, "refresh-committed-headers"); state.Phase != WebAPIRuntimeApplied || requests.Load() != 3 {
		t.Fatalf("headers refresh %+v requests=%d", state, requests.Load())
	}
	_, err := s.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "2", OperationID: "change-provider-header-domain"}, "test:source-header-change", profile, func(tx *WebAPIWriteTx) (any, error) {
		return nil, tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: webRuntimeTestProfileID}, MediaType: "application/yaml", Bytes: []byte(strings.Replace(profile, "provider_session=synthetic", "provider_session=new-synthetic", 1))})
	})
	if err != nil {
		t.Fatal(err)
	}
	code, replay := s.request(t, "POST", "/api/v1/runtime/provider-sources:refresh", string(request), s.cookie, "")
	if code != 200 || !bytes.Equal(receipt, replay) || requests.Load() != 3 {
		t.Fatal("header identity change lost receipt idempotency or repeated the fetch")
	}
}
