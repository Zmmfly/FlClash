//go:build webpanel && !android

package core

import (
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

func TestWebRuntimeProviderSourceStrongFetchPreparedApplyAndVersionInvalidation(t *testing.T) {
	var fetched, applied atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/provider" {
			t.Error("provider fetch is not the fixed source GET")
		}
		fetched.Add(1)
		_, _ = w.Write([]byte("proxies:\n  - {name: P2, type: socks5, server: 192.0.2.2, port: 1080}\n"))
	}))
	defer remote.Close()
	_, port, _ := net.SplitHostPort(remote.Listener.Addr().String())
	sourceURL := "http://provider.test:" + port + "/provider"
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{Apply: func(_ context.Context, p WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
		applied.Add(1)
		return WebAPIRuntimeApplyResult{ActualConfig: "candidate", ProfileID: p.ProfileID, Digest: p.Digest}, nil
	}})
	s.runtime.workspace.options.SourceFetchOptions = WebAPISourceFetchOptions{ResolveRuntimeSourceIdentity: s.runtime.ResolveRuntimeSourceIdentity, LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if !strings.HasPrefix(address, "1.1.1.1:") {
			t.Errorf("unpinned dial %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, remote.Listener.Addr().String())
	}}
	profile := "proxy-providers:\n  remote: {type: http, url: '" + sourceURL + "'}\nproxy-groups:\n  - {name: G, type: select, proxies: [DIRECT], use: [remote]}\nrules: ['MATCH,G']\n"
	webRuntimeSeedProfile(t, s, "file", profile)
	code, body := s.request(t, "GET", "/api/v1/runtime/provider-sources?profileId="+string(webRuntimeTestProfileID), "", s.cookie, "")
	if code != 200 {
		t.Fatalf("sources %d %s", code, body)
	}
	var envelope WebAPIEnvelope[[]WebAPIRuntimeProviderSource]
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data) != 1 {
		t.Fatal(string(body))
	}
	descriptor := envelope.Data[0]
	if len(descriptor.ID) < 20 || descriptor.Phase != "unprepared" || fetched.Load() != 0 {
		t.Fatalf("GET source allocated/fetched or rounded its identity: %+v", descriptor)
	}
	if strings.Contains(string(body), sourceURL) || strings.Contains(string(body), "/tmp/") {
		t.Fatal("source descriptor leaked an authority URL or server path")
	}
	before := webRuntimeApplyBody(t, "apply-unprepared-source", nil)
	if code, _ := s.request(t, "POST", "/api/v1/runtime:apply", before, s.cookie, ""); code != 409 {
		t.Fatal("unprepared remote provider reached Core")
	}
	request, _ := json.Marshal(WebAPIRuntimeProviderRefresh{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "refresh-strong-source"}, ID: descriptor.ID, Purpose: descriptor.Purpose})
	code, receipt := s.request(t, "POST", "/api/v1/runtime/provider-sources:refresh", string(request), s.cookie, "")
	if code != 200 {
		t.Fatalf("refresh %d %s", code, receipt)
	}
	state := s.phase(t, "refresh-strong-source")
	if state.Phase != WebAPIRuntimeApplied || fetched.Load() != 1 || applied.Load() != 0 || !strings.Contains(string(state.Result), `"requiresApply":true`) {
		t.Fatalf("refresh misrepresented Core state: %+v fetch=%d apply=%d", state, fetched.Load(), applied.Load())
	}
	apply := strings.Replace(webRuntimeApplyBody(t, "apply-prepared-source", nil), `"expectedRevision":"1"`, `"expectedRevision":"2"`, 1)
	if code, body := s.request(t, "POST", "/api/v1/runtime:apply", apply, s.cookie, ""); code != 200 {
		t.Fatalf("prepared apply %d %s", code, body)
	}
	if state := s.phase(t, "apply-prepared-source"); state.Phase != WebAPIRuntimeApplied || applied.Load() != 1 {
		t.Fatal(state)
	}
	_, err := s.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "3", OperationID: "change-source-version"}, "test:change-source", profile, func(tx *WebAPIWriteTx) (any, error) {
		return nil, tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: webRuntimeTestProfileID}, MediaType: "application/yaml", Bytes: []byte(strings.Replace(profile, "/provider'", "/changed'", 1))})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.runtime.lookupProvider(context.Background(), descriptor.ID); err == nil {
		t.Fatal("old provider authorization identity survived source change")
	}
	code, replay := s.request(t, "POST", "/api/v1/runtime/provider-sources:refresh", string(request), s.cookie, "")
	if code != 200 || string(replay) != string(receipt) || fetched.Load() != 1 {
		t.Fatalf("changed-source replay repeated fetch or lost receipt: %d %s", code, replay)
	}
	if err := s.runtime.sweepProviderSources(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWebRuntimeProviderSourcePrivateNetworkAndSafeModeAreNotBypassed(t *testing.T) {
	var dialed atomic.Int32
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	s.runtime.workspace.options.SourceFetchOptions = WebAPISourceFetchOptions{ResolveRuntimeSourceIdentity: s.runtime.ResolveRuntimeSourceIdentity, LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.0.0.8")}, nil
	}, DialContext: func(context.Context, string, string) (net.Conn, error) { dialed.Add(1); return nil, nil }}
	profile := "proxy-providers:\n  private: {type: http, url: 'http://private.test/provider'}\nproxy-groups:\n  - {name: G, type: select, proxies: [DIRECT], use: [private]}\n"
	webRuntimeSeedProfile(t, s, "file", profile)
	code, body := s.request(t, "GET", "/api/v1/runtime/provider-sources?profileId="+string(webRuntimeTestProfileID), "", s.cookie, "")
	if code != 200 {
		t.Fatal(string(body))
	}
	var envelope WebAPIEnvelope[[]WebAPIRuntimeProviderSource]
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(WebAPIRuntimeProviderRefresh{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "private-without-grant"}, ID: envelope.Data[0].ID, Purpose: envelope.Data[0].Purpose})
	if code, body := s.request(t, "POST", "/api/v1/runtime/provider-sources:refresh", string(request), s.cookie, ""); code != 200 {
		t.Fatalf("private intent %d %s", code, body)
	}
	state := s.phase(t, "private-without-grant")
	if state.Phase != WebAPIRuntimeFailed || state.Error == nil || state.Error.Code != "source_authorization_required" || dialed.Load() != 0 {
		t.Fatalf("private source bypass %+v dial=%d", state, dialed.Load())
	}
}
