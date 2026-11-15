//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/metacubex/mihomo/component/geodata/router"
	"google.golang.org/protobuf/proto"
)

func TestWebAPISchedulerTypedGeoAllFourUseFakeInstallerAndPreserveDesiredSwitch(t *testing.T) {
	clock := newWebSchedulerFakeClock()
	store := newWebSchedulerStore(t, clock)
	mmdb, err := os.ReadFile("../assets/data/GEOIP.metadb")
	if err != nil {
		t.Fatal(err)
	}
	ip, err := proto.Marshal(&router.GeoIPList{Entry: []*router.GeoIP{{CountryCode: "cn", Cidr: []*router.CIDR{{Ip: []byte{1, 2, 3, 0}, Prefix: 24}}}}})
	if err != nil {
		t.Fatal(err)
	}
	site, err := proto.Marshal(&router.GeoSiteList{Entry: []*router.GeoSite{{CountryCode: "cn", Domain: []*router.Domain{{Value: "example.test"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	bodies := map[string][]byte{"MMDB": mmdb, "ASN": mmdb, "GEOIP": ip, "GEOSITE": site}
	var dials atomic.Int32
	options := webSchedulerSourceOptions(clock, false, func(_ context.Context, r *http.Request) string { return string(bodies[r.URL.Path[1:]]) }, &dials)
	var mu sync.Mutex
	installed := map[string]int{}
	backend := WebAPIRuntimeBackend{InstallGeo: func(ctx context.Context, kind string, data []byte) error {
		if !WebAPIInternalAuthorized(ctx) || WebAPIRequestSessionID(ctx) != "" {
			t.Error("runtime geo retained browser authority")
		}
		if !bytes.Equal(data, bodies[kind]) {
			t.Error("installer received wrong typed bytes")
		}
		mu.Lock()
		installed[kind]++
		mu.Unlock()
		return nil
	}}
	var resolverRuntime *WebAPIRuntime
	options.ResolveRuntimeSourceIdentity = func(ctx context.Context, tx *WebAPIReadTx, key WebAPISourceResource) (WebAPICommittedSource, error) {
		if resolverRuntime == nil {
			return WebAPICommittedSource{}, webRuntimeUnavailable()
		}
		return resolverRuntime.ResolveRuntimeSourceIdentity(ctx, tx, key)
	}
	runtime, err := NewWebAPIRuntimeWithOptions(store, backend, WebAPIRuntimeOptions{WorkspaceRoot: filepath.Join(t.TempDir(), "runtime"), SourceFetchOptions: options})
	if err != nil {
		t.Fatal(err)
	}
	resolverRuntime = runtime
	t.Cleanup(runtime.Close)
	webSchedulerMutate(t, store, "scheduler-four-geo-sources", func(tx *WebAPIWriteTx) (any, error) {
		settings := DefaultWebAPISettings()
		settings.Runtime.PatchClashConfig.GeoAutoUpdate = true
		settings.Runtime.PatchClashConfig.GeoXURL = WebAPISettingsGeoXURL{MMDB: "http://public.test/MMDB", ASN: "http://public.test/ASN", GEOIP: "http://public.test/GEOIP", GEOSITE: "http://public.test/GEOSITE"}
		return true, tx.SaveSettings(settings)
	})
	s := webSchedulerNew(t, store, clock, WebAPISchedulerServiceBackend(nil, runtime, options))
	if err = s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, status, err := s.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range status.Targets {
		if target.Kind == WebAPISchedulerGeo && (target.LastSuccess == nil || target.Phase != "idle" || target.RequiresApply) {
			t.Fatal("geo pending was misreported or callback did not install", target)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(installed) != 4 || dials.Load() != 4 {
		t.Fatal(installed, dials.Load())
	}
	for _, count := range installed {
		if count != 1 {
			t.Fatal("geo scheduler duplicated an effect")
		}
	}
	_, err = store.View(t.Context(), func(tx *WebAPIReadTx) error {
		settings, err := tx.Settings()
		if err != nil {
			return err
		}
		patch, err := webRuntimeSettingsPatch(settings)
		if err == nil && (!settings.Runtime.PatchClashConfig.GeoAutoUpdate || patch.GeoAutoUpdate == nil || *patch.GeoAutoUpdate) {
			t.Error("native updater was enabled or desired switch silently disabled")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebAPISchedulerTypedExternalProviderPreparesButNeverAutoApplies(t *testing.T) {
	clock := newWebSchedulerFakeClock()
	store := newWebSchedulerStore(t, clock)
	var applied, dials atomic.Int32
	options := webSchedulerSourceOptions(clock, false, func(context.Context, *http.Request) string {
		return "proxies:\n  - {name: Synthetic, type: socks5, server: 192.0.2.2, port: 1080}\n"
	}, &dials)
	var resolverRuntime *WebAPIRuntime
	options.ResolveRuntimeSourceIdentity = func(ctx context.Context, tx *WebAPIReadTx, key WebAPISourceResource) (WebAPICommittedSource, error) {
		if resolverRuntime == nil {
			return WebAPICommittedSource{}, webRuntimeUnavailable()
		}
		return resolverRuntime.ResolveRuntimeSourceIdentity(ctx, tx, key)
	}
	runtime, err := NewWebAPIRuntimeWithOptions(store, WebAPIRuntimeBackend{Apply: func(context.Context, WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
		applied.Add(1)
		return WebAPIRuntimeApplyResult{}, nil
	}}, WebAPIRuntimeOptions{WorkspaceRoot: filepath.Join(t.TempDir(), "runtime"), SourceFetchOptions: options})
	if err != nil {
		t.Fatal(err)
	}
	resolverRuntime = runtime
	t.Cleanup(runtime.Close)
	webSchedulerMutate(t, store, "scheduler-external-source", func(tx *WebAPIWriteTx) (any, error) {
		p, err := tx.CreateProfile(WebAPIProfileInput{Type: "file", Label: "Synthetic external", ExtendType: "standard", AutoUpdateDurationMillis: 3600000, SelectedMap: map[string]string{}, UnfoldSet: []string{}})
		if err != nil {
			return nil, err
		}
		plain := []byte("proxy-providers:\n  remote: {type: http, url: 'http://public.test/provider', interval: 30}\nproxy-groups:\n  - {name: Pick, type: select, proxies: [DIRECT], use: [remote]}\nrules: ['MATCH,Pick']\n")
		_, catalog, err := webContentProfile(plain, "")
		if err != nil {
			return nil, err
		}
		if err = tx.SetProfileCatalog(p.ID, catalog); err != nil {
			return nil, err
		}
		if err = tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: p.ID}, MediaType: "application/yaml", Bytes: plain}); err != nil {
			return nil, err
		}
		settings := DefaultWebAPISettings()
		settings.CurrentProfileID = &p.ID
		return p, tx.SaveSettings(settings)
	})
	s := webSchedulerNew(t, store, clock, WebAPISchedulerServiceBackend(nil, runtime, options))
	if err = s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, status, err := s.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, target := range status.Targets {
		if target.Kind == WebAPISchedulerExternalProvider {
			found = true
			if target.LastSuccess == nil || target.Phase != "idle" || !target.RequiresApply {
				t.Fatal("provider prepare lied about runtime apply", target)
			}
		}
	}
	if !found || dials.Load() != 1 || applied.Load() != 0 {
		t.Fatal("external provider did not use its typed background path")
	}
}
