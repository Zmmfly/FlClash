//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"path/filepath"
	"testing"
)

func (f *webDAVHTTPFixture) authorizeDAV(t *testing.T, purpose WebAPISourcePurpose, revision WebAPIRevision, id string) string {
	t.Helper()
	prepare := WebAPISourcePrepare{ExpectedRevision: revision, Purpose: purpose, Key: &WebAPISourceResource{Kind: "dav", ID: "settings"}}
	status, body := f.request(t, "POST", "/api/v1/sources:prepare", prepare, f.cookie)
	if status != 200 {
		t.Fatalf("private DAV prepare %d: %s", status, body)
	}
	var prepared WebAPIEnvelope[WebAPISourcePreparation]
	if err := json.Unmarshal(body, &prepared); err != nil {
		t.Fatal(err)
	}
	authorize := WebAPISourceAuthorize{ExpectedRevision: revision, OperationID: id, PreparationID: prepared.Data.PreparationID, URLDigest: prepared.Data.URLDigest, Intent: "authorize-server-private-source"}
	status, body = f.request(t, "POST", "/api/v1/sources:authorize", authorize, f.cookie)
	if status != 200 {
		t.Fatalf("private DAV authorization %d: %s", status, body)
	}
	var granted WebAPIEnvelope[WebAPISourceAuthorizationResult]
	if err := json.Unmarshal(body, &granted); err != nil {
		t.Fatal(err)
	}
	return granted.Data.AuthorizationID
}

func webDAVPrivateFixtureDNS(f *webDAVHTTPFixture) {
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.20.30.40")}, nil
	}
	f.sources.options.LookupNetIP = lookup
	f.dav.options.LookupNetIP = lookup
}

func TestWebDAVHTTPRestartNewLoginSafeSummaryFreshGrantAndExplicitReadOnlyRecovery(t *testing.T) {
	var uploaded []byte
	puts := 0
	wire := &webDAVTestWire{handle: func(r *http.Request, body []byte) *http.Response {
		switch r.Method {
		case "HEAD":
			if puts == 0 {
				return webDAVTestResponse(404, nil, nil)
			}
			return webDAVTestResponse(200, http.Header{"Etag": {`"observed-now"`}}, nil)
		case "PUT":
			puts++
			if puts == 1 {
				uploaded = append([]byte{}, body...)
				return nil
			}
			if r.Header.Get("If-Match") != `"observed-now"` {
				t.Error("new explicit upload skipped fresh ETag")
			}
			return webDAVTestResponse(204, http.Header{"Etag": {`"new"`}}, nil)
		case "GET":
			return webDAVTestResponse(200, nil, uploaded)
		default:
			return webDAVTestResponse(204, nil, nil)
		}
	}}
	old := newWebDAVHTTPFixture(t, wire)
	webDAVPrivateFixtureDNS(old)
	oldGrant := old.authorizeDAV(t, "dav-backup", "1", "dav-old-private-grant")
	backup := WebAPIDAVBackup{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-before-restart"}, Intent: "backup-to-dav", Format: "flclash-v1", AuthorizationID: oldGrant}
	if status, body := old.request(t, "POST", "/api/v1/dav:backup", backup, old.cookie); status != 200 {
		t.Fatalf("old backup %d: %s", status, body)
	}
	if old.job(t, backup.OperationID).Data.Phase != WebAPIDAVUnknown {
		t.Fatal("fixture did not retain uncertain PUT")
	}
	old.server.Close()
	if err := old.dav.Close(); err != nil {
		t.Fatal(err)
	}
	if err := old.store.Close(); err != nil {
		t.Fatal(err)
	}
	old.auth.Close()
	auth, err := NewWebServer(WebServerOptions{DataDir: filepath.Join(t.TempDir(), "auth-restart"), Username: "admin", Password: "correct horse", SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { auth.Close() })
	store, err := OpenWebAPIStore(old.storeOptions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	fresh := mountWebDAVHTTPFixture(t, wire, store, old.storeOptions, auth)
	webDAVPrivateFixtureDNS(fresh)
	status, body := fresh.request(t, "GET", "/api/v1/dav/jobs", nil, fresh.cookie)
	if status != 200 {
		t.Fatalf("restart summary %d: %s", status, body)
	}
	var summaries WebAPIEnvelope[[]WebAPIDAVJobSummary]
	if err = json.Unmarshal(body, &summaries); err != nil {
		t.Fatal(err)
	}
	if len(summaries.Data) != 1 || summaries.Data[0].OperationID != backup.OperationID || !summaries.Data[0].CurrentTupleMatches || !summaries.Data[0].NeedsPrepare {
		t.Fatal("restart summary is not recoverable")
	}
	for _, forbidden := range []string{"token", "archiveDigest", "archiveSize", "tupleDigest", "authorizationId", "sessionId", "dav.test", "synthetic-password", oldGrant} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("restart summary leaked %s", forbidden)
		}
	}
	if status, _ := fresh.request(t, "GET", "/api/v1/dav/jobs/"+backup.OperationID, nil, fresh.cookie); status != 403 {
		t.Fatal("new session adopted old full job")
	}
	reconcile := WebAPIDAVReconcile{WebAPIMutation: WebAPIMutation{ExpectedRevision: "2", OperationID: "dav-restart-reconcile"}, Intent: "reconcile-dav-backup", AuthorizationID: oldGrant}
	if status, _ := fresh.request(t, "POST", "/api/v1/dav/jobs/"+backup.OperationID+":reconcile", reconcile, fresh.cookie); status != 403 {
		t.Fatal("old private grant survived new session")
	}
	if puts != 1 || len(wire.requests) != 3 {
		t.Fatal("restart or summary retried remote effect")
	}
	reconcile.AuthorizationID = fresh.authorizeDAV(t, "dav-restore", "2", "dav-new-read-grant")
	if status, body := fresh.request(t, "POST", "/api/v1/dav/jobs/"+backup.OperationID+":reconcile", reconcile, fresh.cookie); status != 200 {
		t.Fatalf("new session reconcile %d: %s", status, body)
	}
	proof := fresh.job(t, reconcile.OperationID)
	if proof.Data.Phase != WebAPIDAVSucceeded || !proof.Data.Result.RemoteMatches || puts != 1 {
		t.Fatal("reconciliation did not remain read-only")
	}
	backup.ExpectedRevision = "3"
	backup.OperationID = "dav-after-explicit-recovery"
	backup.AuthorizationID = fresh.authorizeDAV(t, "dav-backup", "3", "dav-new-write-grant")
	backup.ResolutionOperationID = reconcile.OperationID
	if status, body := fresh.request(t, "POST", "/api/v1/dav:backup", backup, fresh.cookie); status != 200 {
		t.Fatalf("explicit resumed backup %d: %s", status, body)
	}
	if fresh.job(t, backup.OperationID).Data.Phase != WebAPIDAVSucceeded || puts != 2 {
		t.Fatal("explicit recovery did not perform exactly one new attempt")
	}
}

func TestWebDAVHTTPReadOnlyReconcileCannotOverlapLocallyActiveUnknownEffect(t *testing.T) {
	wire := &webDAVTestWire{handle: func(r *http.Request, _ []byte) *http.Response {
		switch r.Method {
		case "HEAD":
			return webDAVTestResponse(404, nil, nil)
		case "PUT":
			return nil
		default:
			return webDAVTestResponse(204, nil, nil)
		}
	}}
	f := newWebDAVHTTPFixture(t, wire)
	backup := WebAPIDAVBackup{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-local-unknown"}, Intent: "backup-to-dav", Format: "flclash-v1"}
	if status, body := f.request(t, "POST", "/api/v1/dav:backup", backup, f.cookie); status != 200 {
		t.Fatalf("backup %d: %s", status, body)
	}
	// Model the interval between durable unknown status and actor cleanup.
	f.dav.mu.Lock()
	f.dav.active[backup.OperationID] = func() {}
	f.dav.mu.Unlock()
	defer func() { f.dav.mu.Lock(); delete(f.dav.active, backup.OperationID); f.dav.mu.Unlock() }()
	reconcile := WebAPIDAVReconcile{WebAPIMutation: WebAPIMutation{ExpectedRevision: "2", OperationID: "dav-too-early-reconcile"}, Intent: "reconcile-dav-backup"}
	before := len(wire.requests)
	if status, body := f.request(t, "POST", "/api/v1/dav/jobs/"+backup.OperationID+":reconcile", reconcile, f.cookie); status != 409 || !bytes.Contains(body, []byte("dav_operation_active")) {
		t.Fatalf("active actor bypass %d: %s", status, body)
	}
	if len(wire.requests) != before {
		t.Fatal("reconcile raced locally active old effect")
	}
}
