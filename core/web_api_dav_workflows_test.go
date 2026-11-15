//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"testing"
)

func TestWebDAVHTTPPrivateSourceAuthorizationIsCommittedTupleAndPurposeBound(t *testing.T) {
	wire := webDAVSuccessfulWire()
	f := newWebDAVHTTPFixture(t, wire)
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.20.30.40")}, nil
	}
	f.sources.options.LookupNetIP = lookup
	f.dav.options.LookupNetIP = lookup
	prepare := WebAPISourcePrepare{ExpectedRevision: "1", Purpose: "dav-probe", Key: &WebAPISourceResource{Kind: "dav", ID: "settings"}}
	status, body := f.request(t, "POST", "/api/v1/sources:prepare", prepare, f.cookie)
	if status != 200 {
		t.Fatalf("prepare DAV private source %d: %s", status, body)
	}
	var prepared WebAPIEnvelope[WebAPISourcePreparation]
	if err := json.Unmarshal(body, &prepared); err != nil {
		t.Fatal(err)
	}
	if !prepared.Data.RequiresPrivateNetworkConfirmation || len(wire.requests) != 0 {
		t.Fatal("source preparation fetched or concealed private server view")
	}
	authorize := WebAPISourceAuthorize{ExpectedRevision: "1", OperationID: "dav-private-authorization", PreparationID: prepared.Data.PreparationID, URLDigest: prepared.Data.URLDigest, Intent: "authorize-server-private-source"}
	status, body = f.request(t, "POST", "/api/v1/sources:authorize", authorize, f.cookie)
	if status != 200 {
		t.Fatalf("authorize DAV %d: %s", status, body)
	}
	var grant WebAPIEnvelope[WebAPISourceAuthorizationResult]
	if err := json.Unmarshal(body, &grant); err != nil {
		t.Fatal(err)
	}
	probe := WebAPIDAVProbe{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-private-probe"}, Intent: "probe-dav", AuthorizationID: grant.Data.AuthorizationID}
	if status, body := f.request(t, "POST", "/api/v1/dav:probe", probe, f.cookie); status != 200 {
		t.Fatalf("private probe %d: %s", status, body)
	}
	if f.job(t, probe.OperationID).Data.Phase != WebAPIDAVSucceeded || len(wire.requests) != 1 || wire.dials[0] != "10.20.30.40:80" {
		t.Fatal("private grant did not pin the exact target")
	}
	backup := WebAPIDAVBackup{WebAPIMutation: WebAPIMutation{ExpectedRevision: "2", OperationID: "dav-wrong-purpose"}, Intent: "backup-to-dav", Format: "flclash-v1", AuthorizationID: grant.Data.AuthorizationID}
	if status, body := f.request(t, "POST", "/api/v1/dav:backup", backup, f.cookie); status != 200 {
		t.Fatalf("wrong-purpose accepted intent %d: %s", status, body)
	}
	job := f.job(t, backup.OperationID)
	if job.Data.Phase != WebAPIDAVFailed || job.Data.Error.Code != "source_authorization_invalid" || len(wire.requests) != 1 {
		t.Fatalf("grant crossed purpose: %+v", job.Data)
	}
	bad := prepare
	bad.ExpectedRevision = "3"
	bad.URL = "http://arbitrary.test/"
	if status, _ := f.request(t, "POST", "/api/v1/sources:prepare", bad, f.cookie); status != 400 {
		t.Fatal("DAV source accepted arbitrary URL")
	}
}

func TestWebDAVHTTPNativeArchiveAndMissingDAVPreserveCurrentWholeTuple(t *testing.T) {
	var archive []byte
	wire := &webDAVTestWire{handle: func(r *http.Request, body []byte) *http.Response {
		switch r.Method {
		case "HEAD":
			return webDAVTestResponse(404, nil, nil)
		case "PUT":
			archive = append([]byte{}, body...)
			return webDAVTestResponse(201, nil, nil)
		case "GET":
			return webDAVTestResponse(200, nil, archive)
		default:
			return webDAVTestResponse(204, nil, nil)
		}
	}}
	f := newWebDAVHTTPFixture(t, wire)
	backup := WebAPIDAVBackup{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-native-export"}, Intent: "backup-to-dav", Format: "native-v1"}
	if status, body := f.request(t, "POST", "/api/v1/dav:backup", backup, f.cookie); status != 200 {
		t.Fatalf("native export %d: %s", status, body)
	}
	if job := f.job(t, backup.OperationID); job.Data.Phase != WebAPIDAVSucceeded {
		t.Fatalf("native backup %+v", job.Data)
	}
	parsed, err := webReadBackupArchive(archive)
	if err != nil || parsed.Format != "native-v1" {
		t.Fatalf("native archive: %v", err)
	}
	if bytes.Contains(parsed.Entries["config.json"], []byte(`"davProps"`)) {
		t.Fatal("native export false retained DAV tuple")
	}
	prepare := WebAPIDAVPrepareRestore{WebAPIMutation: WebAPIMutation{ExpectedRevision: "2", OperationID: "dav-native-prepare"}, Intent: "prepare-dav-restore-with-credentials", Strategy: "override", RestoreDAVCredentials: true}
	if status, body := f.request(t, "POST", "/api/v1/dav:prepare-restore", prepare, f.cookie); status != 200 {
		t.Fatalf("native prepare %d: %s", status, body)
	}
	job := f.job(t, prepare.OperationID)
	if job.Data.Phase != WebAPIDAVPrepared {
		t.Fatalf("native preview %+v", job.Data)
	}
	preview := job.Data.Result.Preparation
	restore := WebAPIDAVRestore{WebAPIMutation: WebAPIMutation{ExpectedRevision: "3", OperationID: "dav-native-restore"}, Intent: "restore-dav-backup-with-credentials", PreparationOperationID: prepare.OperationID, Token: preview.Token, ArchiveDigest: preview.ArchiveDigest, PlanDigest: preview.PlanDigest, RestoreDAVCredentials: true}
	if status, body := f.request(t, "POST", "/api/v1/dav:restore", restore, f.cookie); status != 200 {
		t.Fatalf("native restore %d: %s", status, body)
	}
	_, err = f.store.View(t.Context(), func(tx *WebAPIReadTx) error {
		settings, err := tx.Settings()
		if err != nil {
			return err
		}
		want := WebAPISettingsDAV{URI: "http://dav.test/base", User: "synthetic-user", Password: "synthetic-password", FileName: "backup.zip"}
		if settings.DAV == nil || *settings.DAV != want {
			t.Error("missing archive tuple cleared or mixed current tuple")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebDAVHTTPResolutionTombstoneBlocksDelayedEffect(t *testing.T) {
	wire := webDAVSuccessfulWire()
	f := newWebDAVHTTPFixture(t, wire)
	operation := "dav-resolved-before-arrival"
	if status, body := f.request(t, "POST", "/api/v1/operations/"+operation+":resolve", WebAPIOperationResolve{Intent: "resolve-unknown-result"}, f.cookie); status != 409 {
		t.Fatalf("resolve %d: %s", status, body)
	}
	request := WebAPIDAVProbe{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: operation}, Intent: "probe-dav"}
	if status, body := f.request(t, "POST", "/api/v1/dav:probe", request, f.cookie); status != 409 || !bytes.Contains(body, []byte("operation_not_committed")) {
		t.Fatalf("delayed effect %d: %s", status, body)
	}
	if len(wire.requests) != 0 {
		t.Fatal("tombstoned operation performed a network effect")
	}
}

func TestWebDAVHTTPRemoteConditionalConflictPersistsFailedJob(t *testing.T) {
	wire := &webDAVTestWire{handle: func(r *http.Request, _ []byte) *http.Response {
		switch r.Method {
		case "HEAD":
			return webDAVTestResponse(200, http.Header{"Etag": {`"prior"`}}, nil)
		case "PUT":
			if r.Header.Get("If-Match") != `"prior"` {
				t.Error("missing ETag CAS")
			}
			return webDAVTestResponse(412, nil, nil)
		default:
			return webDAVTestResponse(204, nil, nil)
		}
	}}
	f := newWebDAVHTTPFixture(t, wire)
	request := WebAPIDAVBackup{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-conditional-conflict"}, Intent: "backup-to-dav", Format: "flclash-v1"}
	if status, body := f.request(t, "POST", "/api/v1/dav:backup", request, f.cookie); status != 200 {
		t.Fatalf("admission %d: %s", status, body)
	}
	job := f.job(t, request.OperationID)
	if job.Data.Phase != WebAPIDAVFailed || job.Data.Error.Code != "dav_remote_conflict" || !job.Data.Result.Conditional {
		t.Fatalf("remote conflict %+v", job.Data)
	}
}
