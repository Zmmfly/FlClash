//go:build webpanel && !android

package core

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWebSourceTypedResolversUseExactTransactionWithQueuedClose(t *testing.T) {
	for _, kind := range []string{"runtime-identity", "utility", "pending", "custom-pending"} {
		t.Run(kind, func(t *testing.T) {
			store := webSourceTestStore(t)
			entered, proceed := make(chan struct{}), make(chan struct{})
			read := func(tx *WebAPIReadTx) error { close(entered); <-proceed; _, err := tx.Profiles(); return err }
			options := WebAPISourceOptions{WebAPISourceFetchOptions: WebAPISourceFetchOptions{LookupNetIP: webSourceTestDNS("8.8.8.8")}}
			request := WebAPISourcePrepare{ExpectedRevision: "0"}
			switch kind {
			case "runtime-identity":
				request.Purpose, request.Key = WebAPISourceExternalProviderRefresh, &WebAPISourceResource{Kind: "external-provider", ID: "123456789012345678901234567890"}
				options.ResolveRuntimeSourceIdentity = func(_ context.Context, tx *WebAPIReadTx, _ WebAPISourceResource) (WebAPICommittedSource, error) {
					err := read(tx)
					return WebAPICommittedSource{URL: "http://public.test/source", CommittedHeaders: map[string][]string{"Cookie": {"literal=synthetic"}}}, err
				}
			case "utility":
				request.Purpose, request.Key = WebAPISourceIconImage, &WebAPISourceResource{Kind: "icon-image", ID: "123456789012345678901234567890"}
				options.ResolveUtilitySource = func(_ context.Context, tx *WebAPIReadTx, _ WebAPISourceResource) (string, error) {
					return "http://public.test/icon", read(tx)
				}
			case "custom-pending":
				profile := webSourceTestProfile(t, store, "custom")
				request.ExpectedRevision = "1"
				request.Purpose, request.ImportJobID, request.SourceReference = WebAPISourceCustomProfileDependency, "synthetic-job", "proxy-providers-0"
				options.ResolveCustomImportJobSource = func(_ context.Context, tx *WebAPIReadTx, job, reference string) (WebAPIProspectiveSource, error) {
					err := read(tx)
					return WebAPIProspectiveSource{URL: "http://public.test/dependency", Key: &WebAPISourceResource{Kind: "custom-import-dependency", ID: string(profile.ID), Name: reference}, ExpectedRevision: "1", ImportJobID: job, SourceReference: reference, RequestDigest: strings.Repeat("a", 64)}, err
				}
			case "pending":
				request.Purpose, request.ImportJobID, request.SourceReference = WebAPISourceMitmResourceImport, "synthetic-job", "synthetic-reference"
				options.ResolveImportJobSource = func(_ context.Context, tx *WebAPIReadTx, _, _ string) (WebAPIProspectiveSource, error) {
					err := read(tx)
					return WebAPIProspectiveSource{URL: "http://public.test/dependency", ExpectedRevision: "0", RequestDigest: strings.Repeat("a", 64)}, err
				}
			}
			service := NewWebAPISourceService(store, options)
			prepared := make(chan error, 1)
			go func() { _, _, err := service.Prepare(webSourceTestContext("source-session"), request); prepared <- err }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				close(proceed)
				t.Fatal("resolver did not enter")
			}
			closed := make(chan error, 1)
			go func() { closed <- store.Close() }()
			deadline := time.Now().Add(time.Second)
			for store.access.TryRLock() {
				store.access.RUnlock()
				if time.Now().After(deadline) {
					close(proceed)
					t.Fatal("close did not queue")
				}
				runtime.Gosched()
			}
			close(proceed)
			select {
			case err := <-prepared:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("nested View deadlocked queued Close")
			}
			select {
			case err := <-closed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Close remained blocked")
			}
		})
	}
}

func TestWebSourceFetchRuntimeResolverUsesExactTransactionWithQueuedClose(t *testing.T) {
	store := webSourceTestStore(t)
	entered, proceed := make(chan struct{}), make(chan struct{})
	options := WebAPISourceFetchOptions{LookupNetIP: webSourceTestDNS("8.8.8.8"), ResolveRuntimeSourceIdentity: func(_ context.Context, tx *WebAPIReadTx, _ WebAPISourceResource) (WebAPICommittedSource, error) {
		close(entered)
		<-proceed
		_, err := tx.Profiles()
		return WebAPICommittedSource{URL: "http://public.test/provider"}, err
	}, DialContext: func(context.Context, string, string) (net.Conn, error) {
		t.Error("closed store allowed source dial")
		return nil, fmt.Errorf("forbidden")
	}}
	done := make(chan error, 1)
	go func() {
		_, err := WebAPIFetchSource(webSourceTestContext("source-session"), store, WebAPISourceRequest{Purpose: WebAPISourceExternalProviderRefresh, Key: &WebAPISourceResource{Kind: "external-provider", ID: "123456789012345678901234567890"}, URL: "http://public.test/provider"}, options)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(proceed)
		t.Fatal("fetch resolver did not enter")
	}
	closed := make(chan error, 1)
	go func() { closed <- store.Close() }()
	deadline := time.Now().Add(time.Second)
	for store.access.TryRLock() {
		store.access.RUnlock()
		if time.Now().After(deadline) {
			close(proceed)
			t.Fatal("close did not queue")
		}
		runtime.Gosched()
	}
	close(proceed)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("fetch ignored closing authority")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fetch callback nested View deadlocked")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close remained blocked")
	}
}

func TestWebSourcePreparationExpiresAndConcurrentAuthorizationConsumesOnce(t *testing.T) {
	store := webSourceTestStore(t)
	clock := time.Now()
	service := NewWebAPISourceService(store, WebAPISourceOptions{WebAPISourceFetchOptions: WebAPISourceFetchOptions{Now: func() time.Time { return clock }, LookupNetIP: webSourceTestDNS("10.1.2.3")}})
	ctx := webSourceTestContext("source-session")
	request := WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceProfileImport, URL: "http://lan.test/sub"}
	_, preparation, err := service.Prepare(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(5 * time.Minute)
	command := WebAPISourceAuthorize{ExpectedRevision: "0", OperationID: "expired-stage", PreparationID: preparation.PreparationID, URLDigest: preparation.URLDigest, Intent: "authorize-server-private-source"}
	_, err = service.Authorize(ctx, command)
	webBackupRequireCode(t, err, "source_preparation_invalid")
	_, preparation, err = service.Prepare(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	command.PreparationID, command.URLDigest, command.OperationID = preparation.PreparationID, preparation.URLDigest, "concurrent-first"
	entered, proceed := make(chan struct{}), make(chan struct{})
	service.options.LookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		close(entered)
		<-proceed
		return []netip.Addr{netip.MustParseAddr("10.1.2.3")}, nil
	}
	done := make(chan error, 1)
	go func() { _, err := service.Authorize(ctx, command); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(proceed)
		t.Fatal("authorization did not reach DNS")
	}
	second := command
	second.OperationID = "concurrent-second"
	_, err = service.Authorize(ctx, second)
	webBackupRequireCode(t, err, "source_preparation_invalid")
	close(proceed)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("authorization remained blocked")
	}
	_, err = store.View(ctx, func(tx *WebAPIReadTx) error {
		if tx.tx.Bucket([]byte("source-grants")).Stats().KeyN != 1 {
			t.Error("one preparation produced multiple grants")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebSourceActualUtilityResolverUsesUint256AndRevokesBeforeDial(t *testing.T) {
	store := webSourceTestStore(t)
	ctx := webSourceTestContext("source-session")
	raw := "http://lan.test/icon?synthetic=source"
	_, err := store.CacheTransaction(ctx, WebAPIMutation{ExpectedRevision: "0", OperationID: "register-icon"}, "test:icon", nil, func(tx *WebAPICacheTx) (any, error) { return webAPIPutIcon(tx, raw, time.Now(), false) })
	if err != nil {
		t.Fatal(err)
	}
	key := &WebAPISourceResource{Kind: "icon-image", ID: WebAPIUtilityIconSourceID(raw)}
	if _, err := webAPIInteger(key.ID, true); err == nil {
		t.Fatal("fixture did not exercise a uint256 identity beyond int64")
	}
	wire := &webSourceTestWire{respond: func(_ *http.Request, conn net.Conn) {
		webSourceTestResponse(conn, 200, nil, strings.NewReader("ok"), 2)
	}}
	options := wire.options()
	options.LookupNetIP = webSourceTestDNS("10.1.2.3")
	service := NewWebAPISourceService(store, WebAPISourceOptions{WebAPISourceFetchOptions: options, ResolveUtilitySource: ResolveWebAPIUtilitySource})
	_, grant := webSourceTestAuthorize(t, service, ctx, WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceIconImage, Key: key}, "icon-source-grant")
	request := WebAPISourceRequest{Purpose: WebAPISourceIconImage, Key: key, URL: raw, AuthorizationID: grant.ID}
	if result, err := WebAPIFetchSource(ctx, store, request, options); err != nil || string(result.Bytes) != "ok" {
		t.Fatalf("registered private icon failed: %v", err)
	}
	options.LookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		_, err := store.CacheTransaction(ctx, WebAPIMutation{ExpectedRevision: "1", OperationID: "delete-icon-during-dns"}, "test:icon-delete", nil, func(tx *WebAPICacheTx) (any, error) { return webAPIDeleteIcons(tx, []string{raw}) })
		if err != nil {
			return nil, err
		}
		return []netip.Addr{netip.MustParseAddr("10.1.2.3")}, nil
	}
	_, err = WebAPIFetchSource(ctx, store, request, options)
	webBackupRequireCode(t, err, "icon_image_unregistered")
	dials, _ := wire.observed()
	if len(dials) != 1 {
		t.Fatal("deleted icon source dialed after DNS")
	}
}

func TestWebSourceResetDuringDNSRevokesGrantBeforeDial(t *testing.T) {
	store := webSourceTestStore(t)
	service := NewWebAPISourceService(store, WebAPISourceOptions{WebAPISourceFetchOptions: WebAPISourceFetchOptions{LookupNetIP: webSourceTestDNS("10.1.2.3")}})
	ctx := webSourceTestContext("source-session")
	preparation, grant := webSourceTestAuthorize(t, service, ctx, WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceProfileImport, URL: "http://lan.test/sub"}, "reset-race-grant")
	options := WebAPISourceFetchOptions{LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
		_, err := store.Transaction(ctx, WebAPIMutation{ExpectedRevision: "0", OperationID: "reset-during-dns"}, "test:reset", nil, func(tx *WebAPIWriteTx) (any, error) { return nil, tx.ResetAllSourceGrants() })
		if err != nil {
			return nil, err
		}
		return []netip.Addr{netip.MustParseAddr("10.1.2.3")}, nil
	}, DialContext: func(context.Context, string, string) (net.Conn, error) {
		t.Error("revoked grant reached dial")
		return nil, fmt.Errorf("forbidden")
	}}
	_, err := WebAPIFetchSource(ctx, store, WebAPISourceRequest{Purpose: WebAPISourceProfileImport, URL: "http://lan.test/sub", AuthorizationID: grant.ID, ImportJobID: preparation.PreparationID}, options)
	webBackupRequireCode(t, err, "not_found")
}
