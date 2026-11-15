//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"net/netip"
	"testing"
)

func TestWebAPISchedulerSourceChangeRestoreResetRevokeGrantsAndEpoch(t *testing.T) {
	fixture := newWebContentHTTPFixture(t, nil, nil)
	store, clock := fixture.store, newWebSchedulerFakeClock()
	id := webSchedulerSeedProfile(t, store, clock, true)
	sources := NewWebAPISourceService(store, WebAPISourceOptions{WebAPISourceFetchOptions: WebAPISourceFetchOptions{Now: clock.Now, LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.23.45.67")}, nil
	}}})
	browser := WithWebAPIRequestAuthorization(t.Context(), "synthetic-source-browser", func(context.Context) (func(), error) { return func() {}, nil })
	authorize := func(operation string) {
		t.Helper()
		rev := webSchedulerRevision(t, store)
		_, p, err := sources.Prepare(browser, WebAPISourcePrepare{ExpectedRevision: rev, Purpose: WebAPISourceProfileRefresh, Key: &WebAPISourceResource{Kind: "profile", ID: string(id)}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = sources.Authorize(browser, WebAPISourceAuthorize{ExpectedRevision: rev, OperationID: operation, PreparationID: p.PreparationID, URLDigest: p.URLDigest, Intent: "authorize-server-private-source"})
		if err != nil {
			t.Fatal(err)
		}
	}
	grants := func() int {
		t.Helper()
		count := 0
		_, err := store.View(t.Context(), func(tx *WebAPIReadTx) error {
			rows, err := webAPIList[WebAPISourceGrant](tx.tx, "source-grants")
			count = len(rows)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return count
	}
	epoch := func() string {
		t.Helper()
		var value string
		_, err := store.View(t.Context(), func(tx *WebAPIReadTx) error { value = tx.SchedulerEpoch(); return nil })
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	var profile WebAPIProfile
	_, err := store.View(t.Context(), func(tx *WebAPIReadTx) error { var err error; profile, err = tx.Profile(id); return err })
	if err != nil {
		t.Fatal(err)
	}
	authorize("scheduler-grant-before-change")
	profile.URL = "http://changed.test/profile"
	command := WebAPIProfileUpdate{WebAPIMutation: WebAPIMutation{ExpectedRevision: webSchedulerRevision(t, store), OperationID: "scheduler-change-source-http"}, ID: id, Profile: profile.WebAPIProfileInput}
	if status, body := fixture.request(t, "POST", "/api/v1/profiles:update", command, fixture.cookie); status != 200 {
		t.Fatalf("source update %d %s", status, body)
	}
	if grants() != 0 {
		t.Fatal("source change retained private grants")
	}
	status, archive := fixture.request(t, "GET", "/api/v1/backups?format=flclash-v1", nil, fixture.cookie)
	if status != 200 {
		t.Fatalf("synthetic backup %d %s", status, archive)
	}
	authorize("scheduler-grant-before-restore")
	before := epoch()
	restore := WebAPIBackupRestore{WebAPIMutation: WebAPIMutation{ExpectedRevision: webSchedulerRevision(t, store), OperationID: "scheduler-restore-source-http"}, Intent: "restore-application-data", Strategy: "override", ArchiveBase64: archive}
	if status, body := fixture.request(t, "POST", "/api/v1/backups:restore", restore, fixture.cookie); status != 200 {
		t.Fatalf("synthetic restore %d %s", status, body)
	}
	if grants() != 0 || epoch() == before || epoch() == "" {
		t.Fatal("restore retained source grants or scheduler generation")
	}
	authorize("scheduler-grant-before-reset")
	before = epoch()
	reset := WebAPIReset{WebAPIMutation: WebAPIMutation{ExpectedRevision: webSchedulerRevision(t, store), OperationID: "scheduler-reset-source-http"}, Intent: "reset-application-data"}
	status, body := fixture.request(t, "POST", "/api/v1/app:reset", reset, fixture.cookie)
	if status != 200 {
		t.Fatalf("synthetic reset %d %s", status, body)
	}
	if grants() != 0 || epoch() == before {
		t.Fatal("reset retained source grants or scheduler generation")
	}
	var reply WebAPIEnvelope[map[string]any]
	if err = json.Unmarshal(body, &reply); err != nil || reply.OperationID != reset.OperationID {
		t.Fatal("reset has no immutable typed receipt", err)
	}
}
