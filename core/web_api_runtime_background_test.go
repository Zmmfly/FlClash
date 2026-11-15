//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func webRuntimePublicFixtureOptions(t *testing.T, remote *httptest.Server, runtime *WebAPIRuntime) WebAPISourceFetchOptions {
	t.Helper()
	return WebAPISourceFetchOptions{ResolveRuntimeSourceIdentity: runtime.ResolveRuntimeSourceIdentity, LookupNetIP: func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
		if WebAPIRequestSessionID(ctx) != "" || !WebAPIInternalAuthorized(ctx) {
			t.Error("background source lookup manufactured a browser identity or lost service authority")
		}
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if !strings.HasPrefix(address, "1.1.1.1:") {
			t.Error("background source dial was not pinned")
		}
		return (&net.Dialer{}).DialContext(ctx, network, remote.Listener.Addr().String())
	}}
}

func TestWebRuntimeBackgroundExternalProviderUsesInternalAuthorityAndOriginalRevision(t *testing.T) {
	var fetched atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched.Add(1)
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("background fetch forwarded a browser credential")
		}
		_, _ = w.Write([]byte("background.example.test\n"))
	}))
	defer remote.Close()
	_, port, _ := net.SplitHostPort(remote.Listener.Addr().String())
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	s.runtime.workspace.options.SourceFetchOptions = webRuntimePublicFixtureOptions(t, remote, s.runtime)
	profile := strings.Replace(webRuntimeRuleSourceYAML, "https://provider.test/rules", "http://provider.test:"+port+"/rules", 1)
	webRuntimeSeedProfile(t, s, "file", profile)
	var source WebAPIRuntimeProviderSource
	if _, err := s.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		values, err := s.runtime.RuntimeProviderSources(context.Background(), tx, webRuntimeTestProfileID)
		if err == nil {
			source = values[0]
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	authority := newWebAPIServiceAuthority()
	defer authority.close()
	if _, err := s.runtime.BackgroundRefreshExternalProvider(context.Background(), "1", string(source.ID), "background-no-authority"); err == nil || webRuntimeFailure(err).Code != "internal_authority_required" {
		t.Fatal("background refresh accepted an unprivileged context")
	}
	if _, err := s.runtime.BackgroundRefreshExternalProvider(authority.ctx, "0", string(source.ID), "background-stale-revision"); err == nil || webRuntimeFailure(err).Status != 409 {
		t.Fatal("background refresh upgraded its original revision")
	}
	if fetched.Load() != 0 {
		t.Fatal("rejected background refresh issued a network request")
	}
	receipt, err := s.runtime.BackgroundRefreshExternalProvider(authority.ctx, "1", string(source.ID), "background-public-source")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(authority.ctx, 3*time.Second)
	defer cancel()
	state, err := s.runtime.WaitBackgroundOperation(ctx, "background-public-source")
	if err != nil || state.Phase != WebAPIRuntimeApplied || fetched.Load() != 1 || !bytes.Contains(state.Result, []byte(`"requiresApply":true`)) {
		t.Fatalf("background result %+v %v fetch=%d", state, err, fetched.Load())
	}
	replay, err := s.runtime.BackgroundRefreshExternalProvider(authority.ctx, "1", string(source.ID), "background-public-source")
	if err != nil || !bytes.Equal(replay.Body, receipt.Body) || fetched.Load() != 1 {
		t.Fatal("background receipt replay repeated a public fetch")
	}
}

func TestWebRuntimeBackgroundExternalProviderCannotAuthorizePrivateNetwork(t *testing.T) {
	var dialed atomic.Int32
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	s.runtime.workspace.options.SourceFetchOptions = WebAPISourceFetchOptions{ResolveRuntimeSourceIdentity: s.runtime.ResolveRuntimeSourceIdentity, LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.0.0.8")}, nil
	}, DialContext: func(context.Context, string, string) (net.Conn, error) { dialed.Add(1); return nil, nil }}
	webRuntimeSeedProfile(t, s, "file", webRuntimeRuleSourceYAML)
	source := webRuntimeTestSource(t, s)
	authority := newWebAPIServiceAuthority()
	defer authority.close()
	if _, err := s.runtime.BackgroundRefreshExternalProvider(authority.ctx, "1", string(source.ID), "background-private-denied"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(authority.ctx, time.Second)
	defer cancel()
	state, err := s.runtime.WaitBackgroundOperation(ctx, "background-private-denied")
	if err != nil || state.Phase != WebAPIRuntimeFailed || state.Error == nil || state.Error.Code != "source_authorization_required" || dialed.Load() != 0 {
		t.Fatalf("service autoauthorized private network: %+v %v dial=%d", state, err, dialed.Load())
	}
}

func TestWebRuntimeBackgroundQueuedWorkRejectsRevokedServiceLifetime(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var dialed atomic.Int32
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{ResetTraffic: func(context.Context) error { close(entered); <-release; return nil }})
	s.runtime.workspace.options.SourceFetchOptions = WebAPISourceFetchOptions{ResolveRuntimeSourceIdentity: s.runtime.ResolveRuntimeSourceIdentity, LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) { dialed.Add(1); return nil, nil }}
	webRuntimeSeedProfile(t, s, "file", webRuntimeRuleSourceYAML)
	source := webRuntimeTestSource(t, s)
	if code, body := s.request(t, "POST", "/api/v1/runtime/traffic:reset", `{"expectedRevision":"1","operationId":"block-background-actor"}`, s.cookie, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("actor did not enter fake effect")
	}
	authority := newWebAPIServiceAuthority()
	defer authority.close()
	if _, err := s.runtime.BackgroundRefreshExternalProvider(authority.ctx, "2", string(source.ID), "background-revoked-queued"); err != nil {
		t.Fatal(err)
	}
	authority.close()
	once.Do(func() { close(release) })
	state := s.phase(t, "background-revoked-queued")
	if state.Phase != WebAPIRuntimeFailed || state.Error == nil || state.Error.Code != "service_unavailable" || dialed.Load() != 0 {
		t.Fatalf("queued background operation retained authority: %+v dial=%d", state, dialed.Load())
	}
	if _, err := s.runtime.WaitBackgroundOperation(context.Background(), "background-revoked-queued"); err == nil {
		t.Fatal("background status allowed a non-service caller")
	}
}

func TestWebRuntimeBackgroundSourceCannotPublishAfterServiceRevocation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(entered)
		<-release
		_, _ = w.Write([]byte("late.example.test\n"))
	}))
	defer remote.Close()
	defer once.Do(func() { close(release) })
	_, port, _ := net.SplitHostPort(remote.Listener.Addr().String())
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	s.runtime.workspace.options.SourceFetchOptions = webRuntimePublicFixtureOptions(t, remote, s.runtime)
	webRuntimeSeedProfile(t, s, "file", strings.Replace(webRuntimeRuleSourceYAML, "https://provider.test/rules", "http://provider.test:"+port+"/rules", 1))
	source := webRuntimeTestSource(t, s)
	authority := newWebAPIServiceAuthority()
	defer authority.close()
	if _, err := s.runtime.BackgroundRefreshExternalProvider(authority.ctx, "1", string(source.ID), "background-revoked-after-fetch"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("background fetch did not reach fixture")
	}
	authority.close()
	once.Do(func() { close(release) })
	state := s.phase(t, "background-revoked-after-fetch")
	if state.Phase != WebAPIRuntimeFailed || state.Error == nil || state.Error.Code != "service_unavailable" {
		t.Fatalf("revoked fetch published ready content: %+v", state)
	}
	if code, body := s.request(t, "GET", "/api/v1/runtime/provider-sources/"+string(source.ID), "", s.cookie, ""); code != 200 || bytes.Contains(body, []byte(`"prepared":true`)) {
		t.Fatalf("revoked content became readable %d %s", code, body)
	}
}

func TestWebRuntimeBackgroundGeoUsesServiceAuthorityAndTypedInstaller(t *testing.T) {
	data, err := os.ReadFile("../assets/data/GEOIP.metadb")
	if err != nil {
		t.Fatal(err)
	}
	var installed atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))
	defer remote.Close()
	_, port, _ := net.SplitHostPort(remote.Listener.Addr().String())
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{InstallGeo: func(_ context.Context, kind string, input []byte) error {
		installed.Add(1)
		if kind != "MMDB" || !bytes.Equal(input, data) {
			t.Error("typed background geo installer received another resource")
		}
		return nil
	}})
	s.runtime.workspace.options.SourceFetchOptions = webRuntimePublicFixtureOptions(t, remote, s.runtime)
	_, err = s.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "0", OperationID: "seed-background-geo"}, "test:geo", port, func(tx *WebAPIWriteTx) (any, error) {
		settings := DefaultWebAPISettings()
		settings.Runtime.PatchClashConfig.GeoXURL.MMDB = "http://geo.test:" + port + "/geo"
		return nil, tx.SaveSettings(settings)
	})
	if err != nil {
		t.Fatal(err)
	}
	var id WebAPIRuntimeProviderID
	if _, err = s.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		values, err := webRuntimeGeoDescriptors(tx, s.runtime.Events.storeID)
		if err == nil {
			for _, value := range values {
				if value.Type == "MMDB" {
					id = value.ID
				}
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	authority := newWebAPIServiceAuthority()
	defer authority.close()
	if _, err = s.runtime.BackgroundRefreshGeo(authority.ctx, "1", string(id), "background-geo-install"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(authority.ctx, 3*time.Second)
	defer cancel()
	state, err := s.runtime.WaitBackgroundOperation(ctx, "background-geo-install")
	if err != nil || state.Phase != WebAPIRuntimeApplied || installed.Load() != 1 {
		t.Fatalf("background geo %+v %v installed=%d", state, err, installed.Load())
	}
}

func TestWebRuntimeBackgroundEpochRejectsLateSameTargetAfterResetOrRestore(t *testing.T) {
	for _, kind := range []string{"provider", "geo"} {
		t.Run(kind, func(t *testing.T) {
			data := []byte("epoch.example.test\n")
			if kind == "geo" {
				var err error
				data, err = os.ReadFile("../assets/data/GEOIP.metadb")
				if err != nil {
					t.Fatal(err)
				}
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				close(entered)
				<-release
				_, _ = w.Write(data)
			}))
			defer remote.Close()
			defer once.Do(func() { close(release) })
			_, port, _ := net.SplitHostPort(remote.Listener.Addr().String())
			var installed atomic.Int32
			s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{InstallGeo: func(context.Context, string, []byte) error { installed.Add(1); return nil }})
			s.runtime.workspace.options.SourceFetchOptions = webRuntimePublicFixtureOptions(t, remote, s.runtime)
			var sourceID WebAPIRuntimeProviderID
			if kind == "provider" {
				webRuntimeSeedProfile(t, s, "file", strings.Replace(webRuntimeRuleSourceYAML, "https://provider.test/rules", "http://provider.test:"+port+"/same-source", 1))
				sourceID = webRuntimeTestSource(t, s).ID
			} else {
				_, err := s.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "0", OperationID: "seed-background-epoch-geo"}, "test:seed-epoch", port, func(tx *WebAPIWriteTx) (any, error) {
					settings := DefaultWebAPISettings()
					settings.Runtime.PatchClashConfig.GeoXURL.MMDB = "http://geo.test:" + port + "/same-source"
					return nil, tx.SaveSettings(settings)
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.store.View(context.Background(), func(tx *WebAPIReadTx) error {
					values, err := webRuntimeGeoDescriptors(tx, s.runtime.Events.storeID)
					if err == nil {
						for _, value := range values {
							if value.Type == "MMDB" {
								sourceID = value.ID
							}
						}
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			authority := newWebAPIServiceAuthority()
			defer authority.close()
			op := "background-old-epoch-" + kind
			var err error
			if kind == "provider" {
				_, err = s.runtime.BackgroundRefreshExternalProvider(authority.ctx, "1", string(sourceID), op)
			} else {
				_, err = s.runtime.BackgroundRefreshGeo(authority.ctx, "1", string(sourceID), op)
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("background source did not start")
			}
			_, err = s.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "2", OperationID: "rotate-background-epoch-" + kind}, "test:restore-same-target", sourceID, func(tx *WebAPIWriteTx) (any, error) { return nil, tx.ResetSchedulerAttempts() })
			if err != nil {
				t.Fatal(err)
			}
			once.Do(func() { close(release) })
			ctx, cancel := context.WithTimeout(authority.ctx, 3*time.Second)
			defer cancel()
			state, err := s.runtime.WaitBackgroundOperation(ctx, op)
			if err != nil || state.Phase != WebAPIRuntimeFailed || state.Error == nil || state.Error.Code != "background_scope_changed" || installed.Load() != 0 {
				t.Fatalf("late same-target operation crossed reset/restore authority epoch: %+v %v installed=%d", state, err, installed.Load())
			}
			if kind == "provider" {
				if code, body := s.request(t, "GET", "/api/v1/runtime/provider-sources/"+string(sourceID), "", s.cookie, ""); code != 200 || bytes.Contains(body, []byte(`"prepared":true`)) {
					t.Fatalf("old epoch published ready cache %d %s", code, body)
				}
			}
		})
	}
}
