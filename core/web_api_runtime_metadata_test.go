//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
	ZT "github.com/metacubex/zerotier-go"
	"go.yaml.in/yaml/v3"
)

func TestWebRuntimeAppliedCountsRemainInstalledTopLevelNotUnappliedSourceOrProviderMembers(t *testing.T) {
	var fail atomic.Bool
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{Apply: func(_ context.Context, p WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
		if fail.Load() {
			return WebAPIRuntimeApplyResult{}, context.DeadlineExceeded
		}
		return WebAPIRuntimeApplyResult{ActualConfig: "candidate", ProfileID: p.ProfileID}, nil
	}})
	profile := webRuntimeTestYAML + "proxy-providers:\n  inline: {type: inline, payload: [{name: I1, type: socks5, server: 192.0.2.2, port: 1080}, {name: I2, type: socks5, server: 192.0.2.3, port: 1080}]}\n"
	webRuntimeSeedProfile(t, s, "file", profile)
	if code, body := s.request(t, "GET", "/api/v1/runtime/applied-counts", "", s.cookie, ""); code != 409 || !bytes.Contains(body, []byte("applied_state_unavailable")) {
		t.Fatalf("unapplied source became runtime counts %d %s", code, body)
	}
	if code, body := s.request(t, "POST", "/api/v1/runtime:apply", webRuntimeApplyBody(t, "counts-first-apply", nil), s.cookie, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if state := s.phase(t, "counts-first-apply"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	check := func() {
		code, body := s.request(t, "GET", "/api/v1/runtime/applied-counts", "", s.cookie, "")
		var result WebAPIEnvelope[WebAPIRuntimeAppliedCounts]
		if code != 200 || json.Unmarshal(body, &result) != nil || result.Data.ActualConfig != "candidate" || result.Data.RuntimeGeneration != "0" || result.Data.Groups != 1 || result.Data.Proxies != 1 || result.Data.Rules != 1 {
			t.Fatalf("installed top-level counts %d %s", code, body)
		}
	}
	check()
	_, err := s.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "2", OperationID: "edit-unapplied-count-source"}, "test:source-count-change", profile, func(tx *WebAPIWriteTx) (any, error) {
		return nil, tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: webRuntimeTestProfileID}, MediaType: "application/yaml", Bytes: []byte(strings.Replace(profile, "proxies:\n", "proxies:\n  - {name: New, type: socks5, server: 192.0.2.9, port: 1080}\n", 1))})
	})
	if err != nil {
		t.Fatal(err)
	}
	check()
	fail.Store(true)
	if code, body := s.request(t, "POST", "/api/v1/runtime:apply", webRuntimeApplyBody(t, "counts-unknown-apply", map[string]any{"expectedRevision": "3"}), s.cookie, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if state := s.phase(t, "counts-unknown-apply"); state.Phase != WebAPIRuntimeUnknown {
		t.Fatal(state)
	}
	if code, body := s.request(t, "GET", "/api/v1/runtime/applied-counts", "", s.cookie, ""); code != 409 || !bytes.Contains(body, []byte("applied_state_unknown")) {
		t.Fatalf("Unknown exposed last-good counts %d %s", code, body)
	}
}

func TestWebRuntimeAppliedCountsExplicitFallbackIsNotLastGood(t *testing.T) {
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{Apply: func(context.Context, WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
		return WebAPIRuntimeApplyResult{ActualConfig: "default-fallback"}, webAPIError(422, "config_rejected", "explicit synthetic fallback")
	}})
	webRuntimeSeedProfile(t, s, "file", webRuntimeTestYAML)
	if code, body := s.request(t, "POST", "/api/v1/runtime:apply", webRuntimeApplyBody(t, "counts-fallback-apply", nil), s.cookie, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if state := s.phase(t, "counts-fallback-apply"); state.Phase != WebAPIRuntimeFailed {
		t.Fatal(state)
	}
	code, body := s.request(t, "GET", "/api/v1/runtime/applied-counts", "", s.cookie, "")
	var result WebAPIEnvelope[WebAPIRuntimeAppliedCounts]
	if code != 200 || json.Unmarshal(body, &result) != nil || result.Data.ActualConfig != "default-fallback" || result.Data.Proxies != 0 || result.Data.Groups != 0 || result.Data.Rules != 0 {
		t.Fatalf("fallback was masked as source counts %d %s", code, body)
	}
}

func TestWebRuntimeStatefulDefinitionUsesPublicTypedPlanetProvenanceAfterApply(t *testing.T) {
	identity, err := ZT.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	world := ZT.World{Type: ZT.WorldTypePlanet, ID: 12345, Timestamp: 1, Roots: []ZT.WorldRoot{{Identity: identity, Endpoints: []netip.AddrPort{netip.MustParseAddrPort("192.0.2.1:9993")}}}}
	planet, err := world.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	digest := webContentDigest(planet)
	profile := "proxies:\n  - name: Z\n    type: zerotier\n    network: '0123456789abcdef'\n    planet-asset: {purpose: zerotier-planet, sha256: '" + digest + "'}\nproxy-groups:\n  - {name: G, type: select, proxies: [Z]}\nrules: ['MATCH,G']\n"
	var nativeQueries atomic.Int32
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{
		ProxyDefinition: func(context.Context, string) (map[string]any, error) {
			nativeQueries.Add(1)
			return map[string]any{"name": "Z", "type": "zerotier", "network": "0123456789abcdef", "state-dir": "/private/state", "planet": "/private/planet"}, nil
		},
		Apply: func(_ context.Context, p WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
			data, err := os.ReadFile(p.ConfigPath)
			if err != nil {
				t.Error(err)
			}
			var root map[string]any
			if err = yaml.Unmarshal(data, &root); err != nil {
				t.Error(err)
			}
			proxy := root["proxies"].([]any)[0].(map[string]any)
			if proxy["planet-asset"] != nil || proxy["state-dir"] == nil {
				t.Error("worker did not bind the accepted typed planet/state")
			}
			path, _ := proxy["planet"].(string)
			content, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(content, planet) {
				t.Error("worker did not materialize the committed planet bytes")
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Error("planet snapshot is not private")
			}
			return WebAPIRuntimeApplyResult{ActualConfig: "candidate", ProfileID: p.ProfileID}, nil
		},
	})
	webRuntimeStatefulTestManager(t, s)
	webRuntimeSeedProfile(t, s, "file", profile)
	_, err = s.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "1", OperationID: "commit-runtime-planet-asset"}, "test:planet", digest, func(tx *WebAPIWriteTx) (any, error) {
		return nil, tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentKind("profile-asset"), ID: webRuntimeTestProfileID, Name: "zerotier-planet:" + digest}, MediaType: "application/octet-stream", Bytes: planet})
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, body := s.request(t, "POST", "/api/v1/runtime:apply", webRuntimeApplyBody(t, "stateful-public-definition-apply", map[string]any{"expectedRevision": "2"}), s.cookie, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if state := s.phase(t, "stateful-public-definition-apply"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	code, body := s.request(t, "GET", "/api/v1/runtime/proxies/definition?name=Z", "", s.cookie, "")
	if code != 200 || !bytes.Contains(body, []byte(`"planet-asset"`)) || !bytes.Contains(body, []byte(digest)) || bytes.Contains(body, []byte(`"state-dir"`)) || bytes.Contains(body, []byte(`"planet":`)) || bytes.Contains(body, []byte("/private/")) || nativeQueries.Load() != 0 {
		t.Fatalf("stateful public definition failed or leaked private paths %d %s nativeQueries=%d", code, body, nativeQueries.Load())
	}
}

func TestWebRuntimeNativeGeoMetadataUsesFixedFilesAndRealStat(t *testing.T) {
	root := filepath.Join(t.TempDir(), "geo-home")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	old := C.Path.HomeDir()
	C.SetHomeDir(root)
	defer C.SetHomeDir(old)
	missing, err := webRuntimeReadGeoMetadata(context.Background(), "MMDB")
	if err != nil || missing.Available || missing.Size != nil || missing.ModifiedAt != nil {
		t.Fatalf("missing metadata fabricated an installation: %+v %v", missing, err)
	}
	path := C.Path.MMDB()
	data := []byte("metadata-only fixed resource fixture")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	modified := time.Unix(1700000000, 0).UTC()
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	metadata, err := webRuntimeReadGeoMetadata(context.Background(), "MMDB")
	if err != nil || !metadata.Available || metadata.Size == nil || *metadata.Size != int64(len(data)) || metadata.ModifiedAt == nil || !metadata.ModifiedAt.Equal(modified) {
		t.Fatalf("fixed geo file metadata is not the observed stat: %+v %v", metadata, err)
	}
}
