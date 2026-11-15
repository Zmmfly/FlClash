//go:build webpanel && !android

package core

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

type webBackupTestEntry struct {
	name string
	data []byte
	mode fs.FileMode
}

func webBackupTestZIP(t *testing.T, entries []webBackupTestEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	for _, entry := range entries {
		h := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		mode := entry.mode
		if mode == 0 {
			mode = 0o600
		}
		h.SetMode(mode)
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func webBackupRequireCode(t *testing.T, err error, code string) {
	t.Helper()
	var api *WebAPIError
	if !errors.As(err, &api) || api.Code != code {
		t.Fatalf("error=%v, want %s", err, code)
	}
}

func webBackupFixture() webBackupCandidate {
	p := WebAPIProfile{ID: "9007199254740993", WebAPIProfileInput: WebAPIProfileInput{Type: "file", Label: "fixture", ExtendType: "standard", AutoUpdateDurationMillis: 86400000, AutoUpdate: true, SelectedMap: map[string]string{}, UnfoldSet: []string{}}, Order: new(int32)}
	data := []byte("proxies: []\nrules:\n  - MATCH,DIRECT\n")
	return webBackupCandidate{Data: webBackupData{Profiles: []WebAPIProfile{p}}, Contents: []WebAPIContent{{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: p.ID}, MediaType: "application/yaml", Bytes: data, SHA256: webContentDigest(data)}}}
}

func TestWebBackupTypedManifestRoundTripPreservesInt64AndCorruptOverrides(t *testing.T) {
	fixture := webBackupFixture()
	raw := "{broken overrides raw"
	fixture.Data.Profiles[0].OverridesRawDocument = &raw
	archive, err := webBackupEncodeTyped(fixture)
	if err != nil {
		t.Fatal(err)
	}
	read, err := webReadBackupArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	got, err := webBackupTypedCandidate(read)
	if err != nil {
		t.Fatal(err)
	}
	if got.Data.Profiles[0].ID != "9007199254740993" || got.Data.Profiles[0].OverridesRawDocument == nil || *got.Data.Profiles[0].OverridesRawDocument != raw {
		t.Fatalf("lost profile identity/raw state: %+v", got.Data.Profiles[0])
	}
	if !bytes.Equal(got.Contents[0].Bytes, fixture.Contents[0].Bytes) {
		t.Fatal("blob changed")
	}
}

func TestWebBackupArchiveRejectsMaliciousEntries(t *testing.T) {
	for _, name := range []string{"../escape", "profiles/../x", "/etc/passwd", "C:/x", "a\\b", "a//b", "a/./b", "profiles/1.yaml\x00", "auth/../config.json", "a. ", "a."} {
		t.Run(name, func(t *testing.T) {
			_, err := webReadBackupArchive(webBackupTestZIP(t, []webBackupTestEntry{{"config.json", []byte(`{"version":1}`), 0}, {name, []byte("x"), 0}}))
			if err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
	for _, mode := range []fs.FileMode{fs.ModeSymlink | 0o600, fs.ModeNamedPipe | 0o600, fs.ModeDir | 0o700} {
		t.Run(mode.String(), func(t *testing.T) {
			_, err := webReadBackupArchive(webBackupTestZIP(t, []webBackupTestEntry{{"config.json", []byte(`{"version":1}`), 0}, {"links", []byte("/etc/passwd"), mode}}))
			webBackupRequireCode(t, err, "backup_entry_type")
		})
	}
	_, err := webReadBackupArchive(webBackupTestZIP(t, []webBackupTestEntry{{"config.json", []byte(`{}`), 0}, {"config.json", []byte(`{}`), 0}}))
	webBackupRequireCode(t, err, "backup_entry_name")
	_, err = webReadBackupArchive([]byte("not zip"))
	webBackupRequireCode(t, err, "backup_invalid_zip")
}

func TestWebBackupArchiveRejectsBombsAndUnsupportedVersion(t *testing.T) {
	_, err := webReadBackupArchive(webBackupTestZIP(t, []webBackupTestEntry{{"config.json", []byte(`{"version":1}`), 0}, {"bomb", bytes.Repeat([]byte{0}, 2<<20), 0}}))
	webBackupRequireCode(t, err, "backup_compression_ratio")
	_, err = webReadBackupArchive(webBackupTestZIP(t, []webBackupTestEntry{{"manifest.json", []byte(`{"format":"flclash-v1","version":2,"entries":[]}`), 0}}))
	webBackupRequireCode(t, err, "backup_unsupported_version")
	_, err = webReadBackupArchive(webBackupTestZIP(t, []webBackupTestEntry{{"manifest.json", []byte(`{"format":"flclash-v1","version":1,"version":1,"entries":[]}`), 0}}))
	webBackupRequireCode(t, err, "backup_invalid_manifest")
}

func TestWebBackupRejectsUnmanifestedAndMisnamedBlobs(t *testing.T) {
	fixture := webBackupFixture()
	archive, err := webBackupEncodeTyped(fixture)
	if err != nil {
		t.Fatal(err)
	}
	read, err := webReadBackupArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	read.Entries["auth/credentials.json"] = []byte(`{}`)
	_, err = webBackupTypedCandidate(read)
	webBackupRequireCode(t, err, "backup_unexpected_entry")
	delete(read.Entries, "auth/credentials.json")
	var data webBackupData
	if err := json.Unmarshal(read.Entries["data.json"], &data); err != nil {
		t.Fatal(err)
	}
	data.Contents[0].Key.ID = "1"
	read.Entries["data.json"], _ = json.Marshal(data)
	_, err = webBackupTypedCandidate(read)
	webBackupRequireCode(t, err, "backup_content_mismatch")
}

func TestWebBackupGraphReferencesRejectOrphansAndDuplicates(t *testing.T) {
	fixture := webBackupFixture()
	fixture.Data.Profiles = append(fixture.Data.Profiles, fixture.Data.Profiles[0])
	webBackupRequireCode(t, webBackupValidateData(fixture.Data, fixture.Contents), "backup_duplicate_or_invalid_id")
	fixture = webBackupFixture()
	owner := WebAPIID("1")
	fixture.Data.Rules = []WebAPIRule{{ID: "2", WebAPIRuleInput: WebAPIRuleInput{ProfileID: &owner}}}
	webBackupRequireCode(t, webBackupValidateData(fixture.Data, fixture.Contents), "backup_invalid_reference")
	fixture = webBackupFixture()
	fixture.Contents[0].Key.ID = "3"
	webBackupRequireCode(t, webBackupValidateData(fixture.Data, fixture.Contents), "backup_invalid_content")
}

func TestWebBackupNativeSQLiteRoundTripSchema23(t *testing.T) {
	// Native archive paths and schema are from released v0.9.3 lib/common/backup.dart and database tables.
	fixture := webBackupFixture()
	raw := "null"
	fixture.Data.Profiles[0].OverridesRawDocument = &raw
	body, err := webNativeEncodeArchive(context.Background(), fixture, json.RawMessage(`{"version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := webReadBackupArchive(body)
	if err != nil {
		t.Fatal(err)
	}
	if archive.Format != "native-v1" || binary.BigEndian.Uint32(archive.Entries["database.sqlite"][60:64]) != 23 {
		t.Fatal("wrong native schema")
	}
	got, err := webNativeReadArchive(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	if got.Data.Profiles[0].ID != fixture.Data.Profiles[0].ID || got.Data.Profiles[0].OverridesRawDocument == nil || *got.Data.Profiles[0].OverridesRawDocument != raw {
		t.Fatalf("native conversion lost state: %+v", got.Data.Profiles[0])
	}
	if !bytes.Equal(got.Contents[0].Bytes, fixture.Contents[0].Bytes) {
		t.Fatal("native blob changed")
	}
	archive.Entries["profiles/111.yaml"] = []byte("x")
	_, err = webNativeReadArchive(context.Background(), archive)
	webBackupRequireCode(t, err, "backup_unexpected_entry")
}

func TestWebBackupNativeSQLiteRejectsFutureAndActiveSchema(t *testing.T) {
	fixture := webBackupFixture()
	body, err := webNativeEncodeDatabase(context.Background(), fixture.Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []uint32{0, 4, 17, 22, 24, 999} {
		changed := append([]byte{}, body...)
		binary.BigEndian.PutUint32(changed[60:64], version)
		_, err := webNativeReadDatabase(context.Background(), changed)
		webBackupRequireCode(t, err, "backup_unsupported_version")
	}
	conn, cleanup, err := webNativeOpen(context.Background(), body, false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err = conn.ExecContext(context.Background(), "CREATE VIEW attack AS SELECT 1"); err != nil {
		t.Fatal(err)
	}
	changed, err := webNativeSerialize(conn)
	if err != nil {
		t.Fatal(err)
	}
	_, err = webNativeReadDatabase(context.Background(), changed)
	webBackupRequireCode(t, err, "backup_database_schema")
}

func TestWebContentStrictJSON(t *testing.T) {
	type input struct {
		Name   string `json:"name"`
		Nested []struct {
			ID WebAPIID `json:"id"`
		} `json:"nested"`
	}
	for _, body := range []string{`{"name":"a","name":"b"}`, `{"Name":"a"}`, `{"name":null}`, `{"name":"a","unknown":1}`, `{"nested":[{"id":9007199254740993}]}`, `{"nested":[{"id":"01"}]}`, `{"nested":[{"id":"1","id":"2"}]}`, `{} {}`, strings.Repeat("[", 70) + strings.Repeat("]", 70), "{\"name\":\"\xff\"}"} {
		var v input
		if err := webContentStrictJSON([]byte(body), &v); err == nil {
			t.Fatalf("accepted invalid JSON %q", body)
		}
	}
	var v input
	if err := webContentStrictJSON([]byte(`{"name":"ok","nested":[{"id":"9007199254740993"}]}`), &v); err != nil {
		t.Fatal(err)
	}
}
