//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func webSourceTestContext(session string) context.Context {
	return WithWebAPIRequestAuthorization(context.Background(), session, func(context.Context) (func(), error) { return func() {}, nil })
}

func webSourceTestStore(t *testing.T) *WebAPIStore {
	t.Helper()
	store, err := OpenWebAPIStore(WebAPIStoreOptions{DataDir: filepath.Join(t.TempDir(), "data")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func webSourceTestDNS(addresses ...string) func(context.Context, string, string) ([]netip.Addr, error) {
	return func(context.Context, string, string) ([]netip.Addr, error) {
		result := make([]netip.Addr, len(addresses))
		for i, address := range addresses {
			result[i] = netip.MustParseAddr(address)
		}
		return result, nil
	}
}

func webSourceTestAuthorize(t *testing.T, service *WebAPISourceService, ctx context.Context, request WebAPISourcePrepare, operation string) (WebAPISourcePreparation, WebAPISourceGrant) {
	t.Helper()
	_, preparation, err := service.Prepare(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := service.Authorize(ctx, WebAPISourceAuthorize{ExpectedRevision: request.ExpectedRevision, OperationID: operation, PreparationID: preparation.PreparationID, URLDigest: preparation.URLDigest, Intent: "authorize-server-private-source"})
	if err != nil {
		t.Fatal(err)
	}
	var result WebAPIEnvelope[WebAPISourceAuthorizationResult]
	if err := json.Unmarshal(receipt.Body, &result); err != nil {
		t.Fatal(err)
	}
	grant, err := service.store.SourceGrant(ctx, result.Data.AuthorizationID)
	if err != nil {
		t.Fatal(err)
	}
	return preparation, grant
}

// build/api-source-policy-contract.md keeps public decisions outside the private staging budget.
func TestWebSourcePublicPreparationDoesNotConsumePrivateStages(t *testing.T) {
	store := webSourceTestStore(t)
	now := time.Now()
	service := NewWebAPISourceService(store, WebAPISourceOptions{WebAPISourceFetchOptions: WebAPISourceFetchOptions{Now: func() time.Time { return now }, LookupNetIP: webSourceTestDNS("8.8.8.8")}})
	ctx := webSourceTestContext("source-session")
	var last WebAPISourcePreparation
	for i := 0; i < 1000; i++ {
		_, result, err := service.Prepare(ctx, WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceProfileImport, URL: fmt.Sprintf("http://public.test/sub/%d", i)})
		if err != nil || result.RequiresPrivateNetworkConfirmation || len(result.Addresses) != 1 {
			t.Fatalf("public preparation %d: %+v %v", i, result, err)
		}
		last = result
	}
	if len(service.stages) != 0 {
		t.Fatalf("public decisions retained %d stages", len(service.stages))
	}
	_, err := service.Authorize(ctx, WebAPISourceAuthorize{ExpectedRevision: "0", OperationID: "public-cannot-authorize", PreparationID: last.PreparationID, URLDigest: last.URLDigest, Intent: "authorize-server-private-source"})
	webBackupRequireCode(t, err, "source_preparation_invalid")
	service.options.LookupNetIP = webSourceTestDNS("10.1.2.3")
	for i := 0; i < 128; i++ {
		_, _, err := service.Prepare(ctx, WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceProfileImport, URL: fmt.Sprintf("http://lan.test/sub/%d", i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, _, err = service.Prepare(ctx, WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceProfileImport, URL: "http://lan.test/over"})
	webBackupRequireCode(t, err, "source_staging_full")
	service.options.LookupNetIP = webSourceTestDNS("8.8.8.8")
	if _, _, err := service.Prepare(ctx, WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceProfileImport, URL: "http://public.test/still-valid"}); err != nil {
		t.Fatal("private capacity blocked public preparation", err)
	}
	now = now.Add(6 * time.Minute)
	service.options.LookupNetIP = webSourceTestDNS("10.1.2.3")
	consumed, _ := webSourceTestAuthorize(t, service, ctx, WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceProfileImport, URL: "http://lan.test/new"}, "private-consumed")
	_, err = service.Authorize(ctx, WebAPISourceAuthorize{ExpectedRevision: "0", OperationID: "cannot-consume-twice", PreparationID: consumed.PreparationID, URLDigest: consumed.URLDigest, Intent: "authorize-server-private-source"})
	webBackupRequireCode(t, err, "source_preparation_invalid")
	if len(service.stages) != 0 {
		t.Fatalf("expired/consumed stages retained: %d", len(service.stages))
	}
	revision, err := store.View(ctx, func(*WebAPIReadTx) error { return nil })
	if err != nil || revision != "0" {
		t.Fatalf("source authorization changed config revision: %s %v", revision, err)
	}
}

func TestWebSourcePrepareMetadataAndMappedAddressesNeverDial(t *testing.T) {
	store := webSourceTestStore(t)
	dials := 0
	service := NewWebAPISourceService(store, WebAPISourceOptions{WebAPISourceFetchOptions: WebAPISourceFetchOptions{LookupNetIP: webSourceTestDNS("8.8.8.8"), DialContext: func(context.Context, string, string) (net.Conn, error) {
		dials++
		return nil, fmt.Errorf("forbidden dial")
	}}})
	ctx := webSourceTestContext("source-session")
	for _, host := range []string{"metadata.google.internal", "metadata", "nested.metadata.azure.internal", "100.100.100.200", "[::ffff:100.100.100.200]", "[fd00:ec2::254]", "169.254.169.254", "[::ffff:169.254.169.254]", "[fe80::1]"} {
		t.Run(host, func(t *testing.T) {
			_, _, err := service.Prepare(ctx, WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceProfileImport, URL: "http://" + host + "/token"})
			webBackupRequireCode(t, err, "source_address_forbidden")
		})
	}
	for _, address := range []string{"100.100.100.200", "::ffff:100.100.100.200", "fd00:ec2::254", "169.254.169.254", "::ffff:169.254.169.254", "fe80::1"} {
		service.options.LookupNetIP = webSourceTestDNS("8.8.8.8", address)
		_, _, err := service.Prepare(ctx, WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceProfileImport, URL: "http://synthetic.test/token"})
		webBackupRequireCode(t, err, "source_address_forbidden")
	}
	for _, address := range []string{"10.2.3.4", "fd00::1", "::ffff:100.64.0.1"} {
		service.options.LookupNetIP = webSourceTestDNS(address)
		_, prepared, err := service.Prepare(ctx, WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceProfileImport, URL: "http://synthetic.test/sub"})
		if err != nil || !prepared.RequiresPrivateNetworkConfirmation {
			t.Fatalf("ordinary LAN/ULA denied: %s %v", address, err)
		}
	}
	if dials != 0 {
		t.Fatal("preparation performed network I/O")
	}
}

func TestWebSourcePreparedIdentityIsImmutableAndPublicDNSCannotAutoAuthorize(t *testing.T) {
	store := webSourceTestStore(t)
	key := &WebAPISourceResource{Kind: "external-provider", ID: "opaque-provider"}
	service := NewWebAPISourceService(store, WebAPISourceOptions{WebAPISourceFetchOptions: WebAPISourceFetchOptions{LookupNetIP: webSourceTestDNS("10.1.2.3")}, ResolveRuntimeSourceIdentity: func(context.Context, *WebAPIReadTx, WebAPISourceResource) (WebAPICommittedSource, error) {
		return WebAPICommittedSource{URL: "http://lan.test/provider"}, nil
	}})
	ctx := webSourceTestContext("source-session")
	request := WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceExternalProviderRefresh, Key: key}
	_, result, err := service.Prepare(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	key.ID = "mutated-caller"
	result.Key.ID = "mutated-result"
	result.Addresses[0] = "10.8.8.8"
	stage := service.stages[result.PreparationID]
	if stage.request.Key.ID != "opaque-provider" || stage.preparation.Key.ID != "opaque-provider" || !reflect.DeepEqual(stage.preparation.Addresses, []string{"10.1.2.3"}) {
		t.Fatal("caller mutated private authorization stage")
	}
	service.options.LookupNetIP = webSourceTestDNS("8.8.8.8")
	_, public, err := service.Prepare(ctx, WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceProfileImport, URL: "http://rebind.test/sub"})
	if err != nil || public.RequiresPrivateNetworkConfirmation {
		t.Fatal(err)
	}
	options := WebAPISourceFetchOptions{LookupNetIP: webSourceTestDNS("10.1.2.3"), DialContext: func(context.Context, string, string) (net.Conn, error) {
		t.Error("rebound private source dialed")
		return nil, fmt.Errorf("forbidden")
	}}
	_, err = WebAPIFetchSource(ctx, store, WebAPISourceRequest{Purpose: WebAPISourceProfileImport, URL: "http://rebind.test/sub"}, options)
	webBackupRequireCode(t, err, "source_authorization_required")
}

func TestWebSourceOpaqueIconKeysAndFixedDAVKeys(t *testing.T) {
	maximum := "115792089237316195423570985008687907853269984665640564039457584007913129639935"
	for _, id := range []string{"0", "1", maximum} {
		if !webSourceResourcePurpose(&WebAPISourceResource{Kind: "icon-image", ID: id}, WebAPISourceIconImage) {
			t.Fatalf("uint256 icon identity rejected: %s", id)
		}
	}
	for _, id := range []string{"", "01", "-1", "+1", strings.Repeat("9", 78), maximum + "0"} {
		if webSourceResourcePurpose(&WebAPISourceResource{Kind: "icon-image", ID: id}, WebAPISourceIconImage) {
			t.Fatalf("invalid icon identity accepted: %s", id)
		}
	}
	for _, purpose := range []WebAPISourcePurpose{WebAPISourceDAVProbe, WebAPISourceDAVBackup, WebAPISourceDAVRestore} {
		if !webSourceResourcePurpose(&WebAPISourceResource{Kind: "dav", ID: "settings"}, purpose) || webSourceResourcePurpose(nil, purpose) || webSourceResourcePurpose(&WebAPISourceResource{Kind: "dav", ID: "settings", Name: "other"}, purpose) {
			t.Fatal("DAV key is not fixed")
		}
		_, err := webFetchPurposeSource(context.Background(), WebAPISourceRequest{Purpose: purpose, Key: &WebAPISourceResource{Kind: "dav", ID: "settings"}, URL: "http://public.test/"}, nil, WebAPISourceFetchOptions{})
		webBackupRequireCode(t, err, "invalid_source_purpose")
	}
}
