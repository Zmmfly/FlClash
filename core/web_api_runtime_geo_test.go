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
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebRuntimeGeoStrongFetchValidatesBinaryAndUsesFakeInstaller(t *testing.T) {
	data, err := os.ReadFile("../assets/data/GEOIP.metadb")
	if err != nil {
		t.Fatal(err)
	}
	var fetches, installs atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		if r.URL.Path != "/geo" {
			t.Error("wrong purpose source")
		}
		_, _ = w.Write(data)
	}))
	defer remote.Close()
	_, port, _ := net.SplitHostPort(remote.Listener.Addr().String())
	url := "http://geo-source.test:" + port + "/geo"
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{InstallGeo: func(_ context.Context, kind string, body []byte) error {
		installs.Add(1)
		if kind != "MMDB" || !bytes.Equal(body, data) {
			t.Error("installer did not receive the validated fixed-purpose bytes")
		}
		return nil
	}, GeoMetadata: func(_ context.Context, kind string) (WebAPIRuntimeGeoMetadata, error) {
		if kind != "MMDB" || installs.Load() == 0 {
			return WebAPIRuntimeGeoMetadata{}, nil
		}
		size, modified := int64(len(data)), time.Unix(1700000000, 0).UTC()
		return WebAPIRuntimeGeoMetadata{Available: true, Size: &size, ModifiedAt: &modified}, nil
	}})
	s.runtime.workspace.options.SourceFetchOptions = WebAPISourceFetchOptions{ResolveRuntimeSourceIdentity: s.runtime.ResolveRuntimeSourceIdentity, LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if !strings.HasPrefix(address, "1.1.1.1:") {
			t.Error("geo target was not pinned")
		}
		return (&net.Dialer{}).DialContext(ctx, network, remote.Listener.Addr().String())
	}}
	_, err = s.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "0", OperationID: "seed-geo-source"}, "test:geo-source", url, func(tx *WebAPIWriteTx) (any, error) {
		settings := DefaultWebAPISettings()
		settings.Runtime.PatchClashConfig.GeoXURL.MMDB = url
		return nil, tx.SaveSettings(settings)
	})
	if err != nil {
		t.Fatal(err)
	}
	code, body := s.request(t, "GET", "/api/v1/runtime/geo", "", s.cookie, "")
	if code != 200 {
		t.Fatalf("geo %d %s", code, body)
	}
	var envelope WebAPIEnvelope[[]WebAPIRuntimeGeoResource]
	if err = json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	var sourceID WebAPIRuntimeProviderID
	for _, resource := range envelope.Data {
		if resource.Type == "MMDB" {
			sourceID = resource.SourceID
			if resource.Available || resource.Size != nil || resource.ModifiedAt != nil {
				t.Fatal("missing geo metadata fabricated an installation")
			}
		}
	}
	if !webRuntimeOpaqueValid(sourceID) || fetches.Load() != 0 {
		t.Fatal("geo metadata read fetched data or lost source identity")
	}
	request, _ := json.Marshal(WebAPIRuntimeGeoRequest{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "typed-geo-install"}, Type: "MMDB", SourceID: sourceID})
	code, receipt := s.request(t, "POST", "/api/v1/runtime/geo:update", string(request), s.cookie, "")
	if code != 200 {
		t.Fatalf("geo update %d %s", code, receipt)
	}
	state := s.phase(t, "typed-geo-install")
	if state.Phase != WebAPIRuntimeApplied || fetches.Load() != 1 || installs.Load() != 1 {
		t.Fatalf("geo state %+v fetch %d install %d", state, fetches.Load(), installs.Load())
	}
	if code, body = s.request(t, "GET", "/api/v1/runtime/geo", "", s.cookie, ""); code != 200 || json.Unmarshal(body, &envelope) != nil {
		t.Fatalf("geo metadata %d %s", code, body)
	}
	for _, resource := range envelope.Data {
		if resource.Type == "MMDB" && (!resource.Available || resource.Size == nil || *resource.Size != int64(len(data)) || resource.ModifiedAt == nil || !resource.ModifiedAt.Equal(time.Unix(1700000000, 0).UTC())) {
			t.Fatal("installed geo metadata lost the backend-observed size/mtime")
		}
	}
	code, replay := s.request(t, "POST", "/api/v1/runtime/geo:update", string(request), s.cookie, "")
	if code != 200 || !bytes.Equal(receipt, replay) || fetches.Load() != 1 || installs.Load() != 1 {
		t.Fatal("geo replay repeated the side effect")
	}
}

func TestWebRuntimeGeoInvalidBinaryNeverReachesInstaller(t *testing.T) {
	var installs atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("this is not a geo database")) }))
	defer remote.Close()
	_, port, _ := net.SplitHostPort(remote.Listener.Addr().String())
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{InstallGeo: func(context.Context, string, []byte) error { installs.Add(1); return nil }})
	s.runtime.workspace.options.SourceFetchOptions = WebAPISourceFetchOptions{ResolveRuntimeSourceIdentity: s.runtime.ResolveRuntimeSourceIdentity, LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, remote.Listener.Addr().String())
	}}
	_, err := s.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "0", OperationID: "invalid-geo-source"}, "test:geo-source", port, func(tx *WebAPIWriteTx) (any, error) {
		settings := DefaultWebAPISettings()
		settings.Runtime.PatchClashConfig.GeoXURL.MMDB = "http://geo-source.test:" + port + "/"
		return nil, tx.SaveSettings(settings)
	})
	if err != nil {
		t.Fatal(err)
	}
	code, body := s.request(t, "POST", "/api/v1/runtime/geo:update", `{"expectedRevision":"1","operationId":"invalid-geo-install","type":"MMDB"}`, s.cookie, "")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	state := s.phase(t, "invalid-geo-install")
	if state.Phase != WebAPIRuntimeFailed || state.Error == nil || state.Error.Code != "invalid_geo_content" || installs.Load() != 0 {
		t.Fatalf("invalid geo reached install: %+v", state)
	}
}

func TestWebRuntimeGeoDesiredAutoUpdateRemainsButNativePatchDisablesDirectFetcher(t *testing.T) {
	settings := DefaultWebAPISettings()
	settings.Runtime.PatchClashConfig.GeoAutoUpdate = true
	patch, err := webRuntimeSettingsPatch(settings)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Runtime.PatchClashConfig.GeoAutoUpdate || patch.GeoAutoUpdate == nil || *patch.GeoAutoUpdate {
		t.Fatal("desired auto-update was lost or native HTTP updater was enabled")
	}
}
