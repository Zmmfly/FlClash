//go:build webpanel && !android

package core

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func webSchedulerSourceOptions(clock *webSchedulerFakeClock, private bool, body func(context.Context, *http.Request) string, dials *atomic.Int32) WebAPISourceFetchOptions {
	return WebAPISourceFetchOptions{Now: clock.Now, LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
		if private {
			return []netip.Addr{netip.MustParseAddr("10.23.45.67")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		dials.Add(1)
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			request, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				return
			}
			data := body(ctx, request)
			fmt.Fprintf(server, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nContent-Type: text/plain\r\nConnection: close\r\n\r\n%s", len(data), data)
		}()
		return client, nil
	}}
}

func TestWebAPISchedulerContentNetworkThenOriginalCASPreservesEditAndDelete(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "later_edit", true: "later_delete"}[remove], func(t *testing.T) {
			clock := newWebSchedulerFakeClock()
			store := newWebSchedulerStore(t, clock)
			id := webSchedulerSeedProfile(t, store, clock, true)
			entered, release := make(chan struct{}), make(chan struct{})
			var dials atomic.Int32
			options := webSchedulerSourceOptions(clock, false, func(ctx context.Context, r *http.Request) string {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
				return "proxies: []\nrules: [MATCH,DIRECT]\n"
			}, &dials)
			content := NewWebAPIContentService(store, WebAPIContentOptions{SourceOptions: options, Now: clock.Now})
			s := webSchedulerNew(t, store, clock, WebAPISchedulerContentBackend(content, options))
			done := make(chan error, 1)
			go func() { done <- s.RunOnce(t.Context()) }()
			webSchedulerSignal(t, entered)
			webSchedulerMutate(t, store, "scheduler-user-edit", func(tx *WebAPIWriteTx) (any, error) {
				if remove {
					return tx.DeleteProfile(id)
				}
				p, err := tx.Profile(id)
				if err != nil {
					return nil, err
				}
				p.Label = "User edit wins"
				return p, tx.SaveProfile(p)
			})
			close(release)
			err := webSchedulerDone(t, done)
			api, ok := err.(*WebAPIError)
			if !ok || api.Status != 409 || api.Code != "revision_conflict" {
				t.Fatalf("network result was not rejected with CAS409: %v", err)
			}
			if dials.Load() != 1 {
				t.Fatal("conflict automatically refetched a source")
			}
			_, err = store.View(t.Context(), func(tx *WebAPIReadTx) error {
				p, err := tx.Profile(id)
				if remove {
					if !webSchedulerNotFound(err) {
						t.Error("deleted profile was resurrected")
					}
					return nil
				}
				if err != nil {
					return err
				}
				if p.Label != "User edit wins" || p.LastUpdateDate == nil || webSchedulerTime(*p.LastUpdateDate).Equal(clock.Now()) {
					t.Error("refresh overwrote newer edit")
				}
				if _, err = tx.Content(WebAPIContentKey{Kind: WebAPIContentProfile, ID: id}); !webSchedulerNotFound(err) {
					t.Error("stale download content was committed")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWebAPISchedulerPrivateSourceNeverAuthorizesOrReusesExpiredBrowserGrant(t *testing.T) {
	clock := newWebSchedulerFakeClock()
	store := newWebSchedulerStore(t, clock)
	id := webSchedulerSeedProfile(t, store, clock, true)
	webSchedulerMutate(t, store, "scheduler-private-source", func(tx *WebAPIWriteTx) (any, error) {
		p, err := tx.Profile(id)
		if err != nil {
			return nil, err
		}
		p.URL = "http://private.test/profile"
		return p, tx.SaveProfile(p)
	})
	var dials atomic.Int32
	options := webSchedulerSourceOptions(clock, true, func(context.Context, *http.Request) string {
		t.Error("private background dial was attempted")
		return ""
	}, &dials)
	sources := NewWebAPISourceService(store, WebAPISourceOptions{WebAPISourceFetchOptions: options})
	browser := WithWebAPIRequestAuthorization(t.Context(), "synthetic-browser", func(context.Context) (func(), error) { return func() {}, nil })
	rev := webSchedulerRevision(t, store)
	_, preparation, err := sources.Prepare(browser, WebAPISourcePrepare{ExpectedRevision: rev, Purpose: WebAPISourceProfileRefresh, Key: &WebAPISourceResource{Kind: "profile", ID: string(id)}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = sources.Authorize(browser, WebAPISourceAuthorize{ExpectedRevision: rev, OperationID: "scheduler-browser-authorize", PreparationID: preparation.PreparationID, URLDigest: preparation.URLDigest, Intent: "authorize-server-private-source"})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Hour)
	content := NewWebAPIContentService(store, WebAPIContentOptions{SourceOptions: options, Now: clock.Now})
	s := webSchedulerNew(t, store, clock, WebAPISchedulerContentBackend(content, options))
	if err = s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	state := webSchedulerTargetStatus(t, s, WebAPISchedulerProfile, string(id))
	if state.Phase != "blocked" || state.ErrorCode != "source_authorization_required" || state.LastSuccess != nil {
		t.Fatal(state)
	}
	clock.Advance(365 * 24 * time.Hour)
	if err = s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if dials.Load() != 0 || webSchedulerRevision(t, store) != rev {
		t.Fatal("private block changed authority or performed network effects")
	}
	_, err = store.View(t.Context(), func(tx *WebAPIReadTx) error {
		grants, err := webAPIList[WebAPISourceGrant](tx.tx, "source-grants")
		if len(grants) != 1 || grants[0].SessionID != "synthetic-browser" {
			t.Error("background created or modified private grants")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	f := newWebSchedulerHTTPFixture(t, store, s)
	status, body := f.request(t, "GET", "/api/v1/scheduler", nil, f.cookie)
	if status != 200 || strings.Contains(string(body), "private.test") || strings.Contains(string(body), "synthetic-browser") {
		t.Fatalf("status leaked source/session details: %d", status)
	}
}

func TestWebAPISchedulerMITMRestoreMissingContentKeepsConfirmationAndDesiredSettings(t *testing.T) {
	clock := newWebSchedulerFakeClock()
	store := newWebSchedulerStore(t, clock)
	var id WebAPIID
	moduleURL := "http://public.test/module"
	webSchedulerMutate(t, store, "scheduler-module-restore", func(tx *WebAPIWriteTx) (any, error) {
		settings := DefaultWebAPISettings()
		settings.Runtime.PatchClashConfig.Mitm.Enable = true
		settings.Runtime.PatchClashConfig.GeoAutoUpdate = false
		if err := tx.SaveSettings(settings); err != nil {
			return nil, err
		}
		var err error
		id, err = tx.AllocateID()
		if err != nil {
			return nil, err
		}
		module := WebAPIMitmModule{ID: id, Name: "Restore module", URL: &moduleURL, Enabled: true, Arguments: map[string]string{}, Resources: map[string]string{}, LastUpdateTime: clock.Now().Format(time.RFC3339Nano)}
		return module, tx.InsertMitmModule(module)
	})
	var dials atomic.Int32
	options := webSchedulerSourceOptions(clock, false, func(_ context.Context, r *http.Request) string {
		if r.URL.Path == "/module" {
			return "#!name=Synthetic module\n[Script]\nmain = type=http-request,pattern=.*,script-path=http://public.test/dependency.js\n"
		}
		return "function main() { return {}; }"
	}, &dials)
	content := NewWebAPIContentService(store, WebAPIContentOptions{SourceOptions: options, Now: clock.Now})
	s := webSchedulerNew(t, store, clock, WebAPISchedulerContentBackend(content, options))
	before := webSchedulerTargetStatus(t, s, WebAPISchedulerModule, string(id))
	if before.Phase != "due" || !before.MissingContent {
		t.Fatal(before)
	}
	if err := s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	after := webSchedulerTargetStatus(t, s, WebAPISchedulerModule, string(id))
	if after.MissingContent || !after.ConfirmationRequired || !after.RequiresApply || after.LastSuccess == nil || dials.Load() != 2 {
		t.Fatal(after, dials.Load())
	}
	_, err := store.View(t.Context(), func(tx *WebAPIReadTx) error {
		module, err := tx.MitmModule(id)
		if err != nil {
			return err
		}
		if module.Enabled || module.Scripts != 1 || len(module.Resources) != 1 {
			t.Error("script change did not disable module pending confirmation")
		}
		settings, err := tx.Settings()
		if !settings.Runtime.PatchClashConfig.Mitm.Enable || settings.Runtime.PatchClashConfig.GeoAutoUpdate {
			t.Error("scheduler disabled desired auto-update/MITM settings")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if dials.Load() != 2 {
		t.Fatal("MITM recovery repeated a successful download")
	}
}

func TestWebAPISchedulerResourcesUseGeoSettingsAndExternalProviderOwnPeriod(t *testing.T) {
	clock := newWebSchedulerFakeClock()
	store := newWebSchedulerStore(t, clock)
	var profileID WebAPIID
	remote := "http://public.test/script"
	webSchedulerMutate(t, store, "scheduler-native-periods", func(tx *WebAPIWriteTx) (any, error) {
		settings := DefaultWebAPISettings()
		settings.Runtime.PatchClashConfig.GeoAutoUpdate = true
		settings.Runtime.PatchClashConfig.GeoUpdateInterval = 6
		p, err := tx.CreateProfile(WebAPIProfileInput{Type: "file", Label: "Synthetic providers", ExtendType: "standard", AutoUpdateDurationMillis: 3600000, SelectedMap: map[string]string{}, UnfoldSet: []string{}})
		if err != nil {
			return nil, err
		}
		profileID = p.ID
		plain := []byte("proxy-providers:\n  remote: {type: http, url: 'http://public.test/proxies', interval: 30}\nproxies: []\nproxy-groups:\n  - {name: Pick, type: select, proxies: [DIRECT], use: [remote]}\nrules: [MATCH,DIRECT]\n")
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
		settings.CurrentProfileID = &profileID
		if err = tx.SaveSettings(settings); err != nil {
			return nil, err
		}
		id, err := tx.AllocateID()
		if err != nil {
			return nil, err
		}
		if err = tx.InsertScript(WebAPIScript{ID: id, Label: "Synthetic script", URL: &remote, LastUpdateTime: clock.Now().Add(-5 * time.Hour).Format(time.RFC3339Nano)}); err != nil {
			return nil, err
		}
		id, err = tx.AllocateID()
		if err != nil {
			return nil, err
		}
		if err = tx.InsertIconSet(WebAPIIconSet{ID: id, Name: "Synthetic icons", URL: "http://public.test/icons", Icons: []WebAPIIconSetIcon{}}); err != nil {
			return nil, err
		}
		id, err = tx.AllocateID()
		if err != nil {
			return nil, err
		}
		if err = tx.InsertClashProvider(WebAPIClashProvider{ID: id, Label: "Synthetic rules", URL: "http://public.test/rules", Behavior: "domain", Format: "text"}); err != nil {
			return nil, err
		}
		return p, nil
	})
	s := webSchedulerNew(t, store, clock, WebAPISchedulerBackend{})
	snapshot, err := s.snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[WebAPISchedulerKind]int{}
	for _, target := range snapshot.targets {
		kinds[target.status.Kind]++
		if target.status.Kind == WebAPISchedulerScript && target.status.Phase != "idle" {
			t.Fatal("script did not use geo interval")
		}
		if target.status.Kind == WebAPISchedulerExternalProvider && (target.interval != 30*time.Second || target.status.Phase != "due") {
			t.Fatal("provider interval was replaced by geo period")
		}
	}
	if kinds[WebAPISchedulerGeo] != 4 || kinds[WebAPISchedulerExternalProvider] != 1 || kinds[WebAPISchedulerScript] != 1 || kinds[WebAPISchedulerIconSet] != 1 || kinds[WebAPISchedulerRuleProvider] != 1 {
		t.Fatal(kinds)
	}
}
