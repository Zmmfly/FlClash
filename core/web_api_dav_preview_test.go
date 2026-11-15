//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestWebDAVHTTPSafeSummaryNeverTransfersAnotherSessionsPreviewToken(t *testing.T) {
	var archive []byte
	wire := &webDAVTestWire{handle: func(*http.Request, []byte) *http.Response {
		return webDAVTestResponse(200, nil, archive)
	}}
	f := newWebDAVHTTPFixture(t, wire)
	_, data, err := f.backup.Export(t.Context(), "flclash-v1", false)
	if err != nil {
		t.Fatal(err)
	}
	archive = data
	prepare := WebAPIDAVPrepareRestore{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-summary-preview"}, Intent: "prepare-dav-restore", Strategy: "merge"}
	if status, body := f.request(t, "POST", "/api/v1/dav:prepare-restore", prepare, f.cookie); status != 200 {
		t.Fatalf("prepare %d: %s", status, body)
	}
	job := f.job(t, prepare.OperationID)
	if job.Data.Phase != WebAPIDAVPrepared {
		t.Fatalf("preparation %+v", job.Data)
	}
	preview := job.Data.Result.Preparation
	other := webLogin(t, f.auth)
	status, body := f.request(t, "GET", "/api/v1/dav/jobs", nil, other)
	if status != 200 {
		t.Fatalf("summary %d: %s", status, body)
	}
	for _, secret := range []string{preview.Token, preview.ArchiveDigest, preview.PlanDigest, "preparation", "archiveDigest", "token", "authorizationId", "sessionId"} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatalf("cross-session summary leaked %s", secret)
		}
	}
	var summary WebAPIEnvelope[[]WebAPIDAVJobSummary]
	if err := json.Unmarshal(body, &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.Data) != 1 || !summary.Data[0].NeedsPrepare {
		t.Fatal("new session did not require fresh preparation")
	}
	if status, _ := f.request(t, "GET", "/api/v1/dav/jobs/"+prepare.OperationID, nil, other); status != 403 {
		t.Fatal("summary adopted full preview authority")
	}
}

func TestWebDAVHTTPOnlyProfilesPreservesRuntimeAndEntireDAVTuple(t *testing.T) {
	var archive []byte
	wire := &webDAVTestWire{handle: func(*http.Request, []byte) *http.Response {
		return webDAVTestResponse(200, nil, archive)
	}}
	f := newWebDAVHTTPFixture(t, wire)
	var candidate webBackupCandidate
	_, err := f.store.View(t.Context(), func(tx *WebAPIReadTx) error {
		var err error
		candidate, err = webBackupSnapshot(tx, true)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate.Data.Settings.Runtime.PatchClashConfig.Mode = "global"
	candidate.Data.Settings.DAV = &WebAPISettingsDAV{URI: "http://incoming.test/", User: "incoming-user", Password: "incoming-password", FileName: "incoming.zip"}
	archive, err = webBackupEncodeTyped(candidate)
	if err != nil {
		t.Fatal(err)
	}
	prepare := WebAPIDAVPrepareRestore{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-only-profiles"}, Intent: "prepare-dav-restore", Strategy: "override", OnlyProfiles: true}
	invalid := prepare
	invalid.RestoreDAVCredentials = true
	invalid.Intent = "prepare-dav-restore-with-credentials"
	if status, _ := f.request(t, "POST", "/api/v1/dav:prepare-restore", invalid, f.cookie); status != 400 {
		t.Fatal("onlyProfiles allowed credential replacement")
	}
	if status, body := f.request(t, "POST", "/api/v1/dav:prepare-restore", prepare, f.cookie); status != 200 {
		t.Fatalf("prepare %d: %s", status, body)
	}
	job := f.job(t, prepare.OperationID)
	if job.Data.Phase != WebAPIDAVPrepared {
		t.Fatalf("onlyProfiles preview %+v", job.Data)
	}
	preview := job.Data.Result.Preparation
	restore := WebAPIDAVRestore{WebAPIMutation: WebAPIMutation{ExpectedRevision: "2", OperationID: "dav-only-profiles-confirm"}, Intent: "restore-dav-backup", PreparationOperationID: prepare.OperationID, Token: preview.Token, ArchiveDigest: preview.ArchiveDigest, PlanDigest: preview.PlanDigest}
	if status, body := f.request(t, "POST", "/api/v1/dav:restore", restore, f.cookie); status != 200 {
		t.Fatalf("confirm %d: %s", status, body)
	}
	_, err = f.store.View(t.Context(), func(tx *WebAPIReadTx) error {
		settings, err := tx.Settings()
		if err != nil {
			return err
		}
		if settings.Runtime.PatchClashConfig.Mode != "rule" || settings.DAV == nil || settings.DAV.URI != "http://dav.test/base" || settings.DAV.User != "synthetic-user" || settings.DAV.Password != "synthetic-password" || settings.DAV.FileName != "backup.zip" {
			t.Error("onlyProfiles changed runtime or mixed the current DAV tuple")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(wire.requests) != 1 {
		t.Fatal("profile-only confirmation connected to archive DAV settings")
	}
}
