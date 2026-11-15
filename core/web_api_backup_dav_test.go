//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestWebBackupHTTPDAVWholeTupleRequiresPreparedExplicitConfirmation(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	current := DefaultWebAPISettings()
	current.DAV = &WebAPISettingsDAV{URI: "https://current.invalid/storage?token=keep", User: "current", Password: "keep-secret", FileName: "current.zip"}
	_, err := f.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "0", OperationID: "dav-seed"}, "fixture", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return nil, tx.SaveSettings(current) })
	if err != nil {
		t.Fatal(err)
	}
	_, exported, err := f.backup.Export(context.Background(), "flclash-v1", false)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := webReadBackupArchive(exported)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(archive.Entries["data.json"], []byte("current.invalid")) || bytes.Contains(archive.Entries["data.json"], []byte("keep-secret")) || bytes.Contains(archive.Entries["data.json"], []byte("current.zip")) {
		t.Fatal("default export leaked part of DAV tuple")
	}
	incoming := DefaultWebAPISettings()
	incoming.DAV = &WebAPISettingsDAV{URI: "http://169.254.169.254/metadata?token=archive-secret", User: "archive", Password: "archive-password", FileName: "archive.zip"}
	snapshot := SnapshotWebAPISettingsWithOptions(incoming, WebAPISettingsSnapshotOptions{IncludeDAVCredentials: true})
	payload, err := webBackupEncodeTyped(webBackupCandidate{Data: webBackupData{Settings: &snapshot}})
	if err != nil {
		t.Fatal(err)
	}
	restore := WebAPIBackupRestore{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-default-restore"}, Intent: "restore-application-data", Strategy: "override", ArchiveBase64: payload}
	status, body := f.request(t, "POST", "/api/v1/backups:restore", restore, f.cookie)
	if status != 200 {
		t.Fatalf("default restore %d: %s", status, body)
	}
	_, err = f.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		got, e := tx.Settings()
		if !reflect.DeepEqual(got.DAV, current.DAV) {
			t.Fatalf("default restore mixed DAV tuple %+v", got.DAV)
		}
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	prepare := WebAPIBackupPrepare{ExpectedRevision: "2", ArchiveBase64: payload, Strategy: "override", RestoreDAVCredentials: true}
	status, body = f.request(t, "POST", "/api/v1/backups:prepare", prepare, f.cookie)
	if status != 200 {
		t.Fatalf("DAV prepare %d: %s", status, body)
	}
	if bytes.Contains(body, []byte("archive-password")) || bytes.Contains(body, []byte("archive-secret")) {
		t.Fatal("preview disclosed DAV credentials")
	}
	var prepared WebAPIEnvelope[WebAPIBackupPreparation]
	json.Unmarshal(body, &prepared)
	confirm := WebAPIBackupConfirm{WebAPIMutation: WebAPIMutation{ExpectedRevision: "2", OperationID: "dav-confirm-restore"}, Intent: "confirm-backup-conversion", Token: prepared.Data.Token, ArchiveDigest: prepared.Data.ArchiveDigest, PlanDigest: prepared.Data.PlanDigest, RestoreDAVCredentials: true}
	if status, body := f.request(t, "POST", "/api/v1/backups:confirm", confirm, f.cookie); status != 200 {
		t.Fatalf("DAV confirmation %d: %s", status, body)
	}
	_, err = f.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		got, e := tx.Settings()
		if !reflect.DeepEqual(got.DAV, incoming.DAV) {
			t.Fatalf("confirmed restore did not replace whole tuple %+v", got.DAV)
		}
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	_, native, err := f.backup.Export(context.Background(), "native-v1", true)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := webReadBackupArchive(native)
	if err != nil {
		t.Fatal(err)
	}
	back, err := NativeWebAPISettingsSnapshot(parsed.Entries["config.json"])
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.DAV, incoming.DAV) {
		t.Fatalf("native DAV tuple did not roundtrip %+v", back.DAV)
	}
}
