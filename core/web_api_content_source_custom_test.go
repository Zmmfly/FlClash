//go:build webpanel && !android

package core

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

func webSourceTestProfile(t *testing.T, store *WebAPIStore, kind string) WebAPIProfile {
	t.Helper()
	var profile WebAPIProfile
	source := ""
	if kind == "url" {
		source = "http://source.test/original"
	}
	_, err := store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "0", OperationID: "seed-source-profile"}, "test:profile", nil, func(tx *WebAPIWriteTx) (any, error) {
		var err error
		profile, err = tx.CreateProfile(WebAPIProfileInput{Type: kind, Label: "Source", URL: source, ExtendType: "standard", AutoUpdateDurationMillis: 86400000, SelectedMap: map[string]string{}, UnfoldSet: []string{}})
		return profile, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestWebSourceRuntimeIdentityRecheckedAfterDNS(t *testing.T) {
	for _, change := range []string{"URL", "headers"} {
		t.Run(change, func(t *testing.T) {
			store := webSourceTestStore(t)
			profile := webSourceTestProfile(t, store, "url")
			headers := map[string][]string{"Cookie": {"literal=synthetic"}}
			calls := 0
			options := WebAPISourceFetchOptions{ResolveRuntimeSourceIdentity: func(_ context.Context, tx *WebAPIReadTx, _ WebAPISourceResource) (WebAPICommittedSource, error) {
				calls++
				current, err := tx.Profile(profile.ID)
				return WebAPICommittedSource{URL: current.URL, CommittedHeaders: headers}, err
			}, LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
				if change == "URL" {
					_, err := store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "1", OperationID: "change-runtime-source"}, "test:source", nil, func(tx *WebAPIWriteTx) (any, error) {
						current, err := tx.Profile(profile.ID)
						if err != nil {
							return nil, err
						}
						current.URL = "http://source.test/changed"
						return nil, tx.SaveProfile(current)
					})
					if err != nil {
						return nil, err
					}
				} else {
					headers = map[string][]string{"Cookie": {"literal=changed"}}
				}
				return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
			}, DialContext: func(context.Context, string, string) (net.Conn, error) {
				t.Error("changed runtime identity reached dial")
				return nil, fmt.Errorf("forbidden")
			}}
			request := WebAPISourceRequest{Purpose: WebAPISourceExternalProviderRefresh, Key: &WebAPISourceResource{Kind: "external-provider", ID: "123456789012345678901234567890"}, URL: profile.URL, CommittedHeaders: headers}
			_, err := WebAPIFetchSource(webSourceTestContext("source-session"), store, request, options)
			webBackupRequireCode(t, err, "source_target_changed")
			if calls != 2 {
				t.Fatalf("runtime resolver was not checked both before and after DNS: %d", calls)
			}
		})
	}
}

func TestWebSourceCustomRootRequiresExistingCustomTargetAndCannotGrantDependencies(t *testing.T) {
	for _, kind := range []string{"file", "custom"} {
		t.Run(kind, func(t *testing.T) {
			store := webSourceTestStore(t)
			profile := webSourceTestProfile(t, store, kind)
			key := &WebAPISourceResource{Kind: "profile", ID: string(profile.ID)}
			wire := &webSourceTestWire{respond: func(_ *http.Request, conn net.Conn) {
				webSourceTestResponse(conn, 200, nil, strings.NewReader("proxies: []\n"), 12)
			}}
			options := wire.options()
			options.LookupNetIP = webSourceTestDNS("10.1.2.3")
			service := NewWebAPISourceService(store, WebAPISourceOptions{WebAPISourceFetchOptions: options})
			request := WebAPISourcePrepare{ExpectedRevision: "1", Purpose: WebAPISourceCustomProfileImport, Key: key, URL: "http://lan.test/root"}
			ctx := webSourceTestContext("source-session")
			if kind != "custom" {
				_, _, err := service.Prepare(ctx, request)
				webBackupRequireCode(t, err, "invalid_profile_state")
				_, err = WebAPIFetchSource(ctx, store, WebAPISourceRequest{Purpose: request.Purpose, Key: key, URL: request.URL}, options)
				webBackupRequireCode(t, err, "invalid_profile_state")
				if dials, _ := wire.observed(); len(dials) != 0 {
					t.Fatal("non-custom profile dialed")
				}
				return
			}
			preparation, grant := webSourceTestAuthorize(t, service, ctx, request, "custom-root-grant")
			fetch := WebAPISourceRequest{Purpose: request.Purpose, Key: key, URL: request.URL, AuthorizationID: grant.ID, ImportJobID: preparation.PreparationID}
			if _, err := WebAPIFetchSource(ctx, store, fetch, options); err != nil {
				t.Fatal(err)
			}
			fetch.Purpose = WebAPISourceCustomProfileDependency
			fetch.Key = &WebAPISourceResource{Kind: "custom-import-dependency", ID: string(profile.ID), Name: "proxy-providers-0"}
			fetch.SourceReference, fetch.RequestDigest = fetch.Key.Name, strings.Repeat("a", 64)
			_, err := WebAPIFetchSource(ctx, store, fetch, options)
			webBackupRequireCode(t, err, "source_authorization_invalid")
			if dials, _ := wire.observed(); len(dials) != 1 {
				t.Fatal("root grant authorized dependency")
			}
			for _, bad := range []*WebAPISourceResource{nil, {Kind: "script", ID: string(profile.ID)}, {Kind: "profile", ID: string(profile.ID), Name: "child"}} {
				request.Key = bad
				_, _, err := service.Prepare(ctx, request)
				webBackupRequireCode(t, err, "invalid_source_purpose")
			}
		})
	}
}

func TestWebSourceCustomDependencyDerivedHeadersAndIndependentReferenceGrant(t *testing.T) {
	store := webSourceTestStore(t)
	profile := webSourceTestProfile(t, store, "custom")
	key := &WebAPISourceResource{Kind: "custom-import-dependency", ID: string(profile.ID), Name: "proxy-providers-0"}
	pending := WebAPIProspectiveSource{URL: "http://lan.test/provider", Key: key, ExpectedRevision: "1", ImportJobID: "custom-job", SourceReference: key.Name, RequestDigest: strings.Repeat("a", 64), CommittedHeaders: map[string][]string{"Authorization": {"Bearer synthetic"}, "Cookie": {"literal=synthetic"}}}
	wire := &webSourceTestWire{respond: func(_ *http.Request, conn net.Conn) {
		webSourceTestResponse(conn, 200, nil, strings.NewReader("ok"), 2)
	}}
	options := wire.options()
	options.LookupNetIP = webSourceTestDNS("10.1.2.3")
	moduleCalls := 0
	service := NewWebAPISourceService(store, WebAPISourceOptions{WebAPISourceFetchOptions: options, ResolveCustomImportJobSource: func(_ context.Context, tx *WebAPIReadTx, job, reference string) (WebAPIProspectiveSource, error) {
		if _, err := tx.Profile(profile.ID); err != nil {
			return WebAPIProspectiveSource{}, err
		}
		if job != pending.ImportJobID || reference != pending.SourceReference {
			return WebAPIProspectiveSource{}, webAPIError(404, "not_found", "unknown dependency")
		}
		return pending, nil
	}, ResolveImportJobSource: func(context.Context, *WebAPIReadTx, string, string) (WebAPIProspectiveSource, error) {
		moduleCalls++
		return WebAPIProspectiveSource{}, webAPIError(404, "not_found", "not a module job")
	}})
	ctx := webSourceTestContext("source-session")
	prepare := WebAPISourcePrepare{ExpectedRevision: "1", Purpose: WebAPISourceCustomProfileDependency, ImportJobID: pending.ImportJobID, SourceReference: pending.SourceReference}
	_, grant := webSourceTestAuthorize(t, service, ctx, prepare, "custom-dependency-grant")
	digest, _ := webSourceIdentityDigest(pending.URL, pending.CommittedHeaders)
	if grant.URLDigest != digest || grant.RequestDigest != pending.RequestDigest || grant.ImportJobID != pending.ImportJobID || grant.SourceReference != pending.SourceReference || moduleCalls != 0 {
		t.Fatal("custom source lost exact derived job/header identity")
	}
	request := WebAPISourceRequest{Purpose: prepare.Purpose, Key: key, URL: pending.URL, AuthorizationID: grant.ID, ImportJobID: pending.ImportJobID, SourceReference: pending.SourceReference, RequestDigest: pending.RequestDigest, CommittedHeaders: pending.CommittedHeaders}
	if _, err := WebAPIFetchSource(ctx, store, request, options); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"reference", "job", "digest", "headers", "URL"} {
		changed := request
		switch field {
		case "reference":
			changed.SourceReference = "rule-providers-0"
			changed.Key = &WebAPISourceResource{Kind: key.Kind, ID: key.ID, Name: changed.SourceReference}
		case "job":
			changed.ImportJobID = "other-job"
		case "digest":
			changed.RequestDigest = strings.Repeat("b", 64)
		case "headers":
			changed.CommittedHeaders = map[string][]string{"Cookie": {"literal=changed"}}
		case "URL":
			changed.URL = "http://lan.test/other"
		}
		if _, err := WebAPIFetchSource(ctx, store, changed, options); err == nil {
			t.Fatalf("changed dependency %s accepted", field)
		}
	}
	if dials, _ := wire.observed(); len(dials) != 1 {
		t.Fatal("one grant crossed dependency identity")
	}
	_, seen := wire.observed()
	if len(seen) != 1 || seen[0].Header.Get("Authorization") != "Bearer synthetic" || seen[0].Header.Get("Cookie") != "literal=synthetic" {
		t.Fatal("server-derived declaration not sent")
	}
	for _, field := range []string{"key", "URL", "missing-job"} {
		changed := prepare
		switch field {
		case "key":
			changed.Key = key
		case "URL":
			changed.URL = pending.URL
		case "missing-job":
			changed.ImportJobID = ""
		}
		if _, _, err := service.Prepare(ctx, changed); err == nil {
			t.Fatalf("caller supplied dependency %s accepted", field)
		}
	}
	for _, field := range []string{"kind", "name", "digest", "job"} {
		original := pending
		switch field {
		case "kind":
			pending.Key = &WebAPISourceResource{Kind: "profile", ID: key.ID, Name: key.Name}
		case "name":
			pending.Key = &WebAPISourceResource{Kind: key.Kind, ID: key.ID, Name: "other"}
		case "digest":
			pending.RequestDigest = strings.Repeat("A", 64)
		case "job":
			pending.ImportJobID = "other-job"
		}
		if _, _, err := service.Prepare(ctx, prepare); err == nil {
			t.Fatalf("malformed derived %s accepted", field)
		}
		pending = original
	}
}
