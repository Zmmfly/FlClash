//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

func webBackupLegacyFixture(t *testing.T, version int, custom bool) []byte {
	t.Helper()
	ctx := context.Background()
	conn, cleanup, err := webNativeOpen(ctx, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	// Released v0.8.95/96 schema2 and v0.8.97/98 schema3 kept links separate from rules.
	schema := `CREATE TABLE profiles(id INTEGER PRIMARY KEY,label TEXT NOT NULL,current_group_name TEXT,url TEXT NOT NULL,last_update_date INTEGER,overwrite_type TEXT NOT NULL,script_id INTEGER,auto_update_duration_millis INTEGER NOT NULL,subscription_info TEXT,auto_update INTEGER NOT NULL,selected_map TEXT NOT NULL,unfold_set TEXT NOT NULL,"order" INTEGER);
CREATE TABLE scripts(id INTEGER PRIMARY KEY,label TEXT NOT NULL,last_update_time INTEGER NOT NULL);
CREATE TABLE rules(id INTEGER PRIMARY KEY,rule_action TEXT NOT NULL,content TEXT,rule_target TEXT,rule_provider TEXT,sub_rule TEXT,no_resolve INTEGER NOT NULL DEFAULT 0,src INTEGER NOT NULL DEFAULT 0);
CREATE TABLE profile_rule_mapping(id TEXT PRIMARY KEY,profile_id INTEGER,rule_id INTEGER NOT NULL,scene TEXT,"order" TEXT);
CREATE TABLE proxy_groups(id INTEGER PRIMARY KEY,profile_id INTEGER,name TEXT NOT NULL,type TEXT NOT NULL,proxies TEXT,"use" TEXT,url TEXT,interval INTEGER,timeout INTEGER,max_failed_times INTEGER,lazy INTEGER,disable_u_d_p INTEGER,filter TEXT,exclude_filter TEXT,exclude_type TEXT,expected_status TEXT,include_all INTEGER,include_all_proxies INTEGER,include_all_providers INTEGER,hidden INTEGER,icon TEXT,"order" TEXT);
CREATE TABLE icon_records(url TEXT PRIMARY KEY,last_accessed INTEGER NOT NULL);`
	if _, err = conn.ExecContext(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if version >= 3 {
		if _, err = conn.ExecContext(ctx, "ALTER TABLE profiles ADD COLUMN match_target TEXT"); err != nil {
			t.Fatal(err)
		}
	}
	if version == 10 {
		if _, err = conn.ExecContext(ctx, `ALTER TABLE scripts ADD COLUMN url TEXT; ALTER TABLE scripts ADD COLUMN "order" INTEGER; CREATE TABLE clash_providers(id INTEGER PRIMARY KEY,kind TEXT NOT NULL,label TEXT NOT NULL,url TEXT NOT NULL,behavior TEXT,format TEXT,"order" INTEGER); CREATE TABLE custom_proxies(id INTEGER PRIMARY KEY,profile_id INTEGER,definition TEXT NOT NULL,"order" TEXT);`); err != nil {
			t.Fatal(err)
		}
	}
	typeName := "standard"
	if custom {
		typeName = "custom"
	}
	if _, err = conn.ExecContext(ctx, `INSERT INTO profiles(id,label,url,last_update_date,overwrite_type,auto_update_duration_millis,auto_update,selected_map,unfold_set) VALUES(9007199254740993,'Legacy','',1700000000,?,86400000,1,'{}','[]'); INSERT INTO rules(id,rule_action,content,rule_target) VALUES(501,'DOMAIN','example.test','DIRECT'); INSERT INTO profile_rule_mapping(id,rule_id,"order") VALUES('global',501,'a0');`, typeName); err != nil {
		t.Fatal(err)
	}
	if custom {
		if _, err = conn.ExecContext(ctx, `INSERT INTO proxy_groups(id,profile_id,name,type,proxies) VALUES(601,9007199254740993,'old group','select','["DIRECT"]'); INSERT INTO rules(id,rule_action,content,rule_target) VALUES(502,'DOMAIN','custom.test','old group'); INSERT INTO profile_rule_mapping(id,profile_id,rule_id,scene,"order") VALUES('custom',9007199254740993,502,'custom','a1');`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = conn.ExecContext(ctx, "PRAGMA user_version="+strconv.Itoa(version)); err != nil {
		t.Fatal(err)
	}
	database, err := webNativeSerialize(conn)
	if err != nil {
		t.Fatal(err)
	}
	return webBackupTestZIP(t, []webBackupTestEntry{{"config.json", []byte(`{"version":1}`), 0}, {"database.sqlite", database, 0}, {"profiles/9007199254740993.yaml", []byte("proxies: []\nrules:\n - MATCH,DIRECT\n"), 0}})
}

func TestWebBackupReleasedLegacyStandardSchemasKeepFullWidthIDs(t *testing.T) {
	for _, version := range []int{2, 3, 10} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			data := webBackupLegacyFixture(t, version, false)
			archive, err := webReadBackupArchive(data)
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := webNativeReadArchive(context.Background(), archive)
			if err != nil {
				t.Fatal(err)
			}
			if len(candidate.Data.Profiles) != 1 || candidate.Data.Profiles[0].ID != "9007199254740993" || len(candidate.Data.Rules) != 1 || candidate.Data.Rules[0].ProfileID != nil {
				t.Fatalf("lossy legacy conversion: %+v", candidate.Data)
			}
		})
	}
}

func TestWebBackupReleasedNative20And21SchemaCompatibility(t *testing.T) {
	for _, version := range []uint32{20, 21} {
		t.Run(strconv.Itoa(int(version)), func(t *testing.T) {
			fixture := webBackupFixture()
			database, err := webNativeEncodeDatabase(context.Background(), fixture.Data)
			if err != nil {
				t.Fatal(err)
			}
			binary.BigEndian.PutUint32(database[60:64], version)
			result, err := webNativeReadDatabase(context.Background(), database)
			if err != nil {
				t.Fatal(err)
			}
			if result.Profiles[0].ID != fixture.Data.Profiles[0].ID {
				t.Fatal("ID changed")
			}
		})
	}
}

func TestWebBackupHTTPPreparedLegacyExplicitPlanSessionTTLAndReplay(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	archive := webBackupLegacyFixture(t, 2, true)
	restore := WebAPIBackupRestore{WebAPIMutation: WebAPIMutation{ExpectedRevision: "0", OperationID: "legacy-default"}, Intent: "restore-application-data", Strategy: "override", ArchiveBase64: archive}
	status, body := f.request(t, "POST", "/api/v1/backups:restore", restore, f.cookie)
	if status != 400 || !bytes.Contains(body, []byte("backup_legacy_transform_required")) {
		t.Fatalf("default lossy restore %d: %s", status, body)
	}
	prepare := WebAPIBackupPrepare{ExpectedRevision: "0", ArchiveBase64: archive, Strategy: "override"}
	status, body = f.request(t, "POST", "/api/v1/backups:prepare", prepare, f.cookie)
	if status != 200 {
		t.Fatalf("prepare %d: %s", status, body)
	}
	var prepared WebAPIEnvelope[WebAPIBackupPreparation]
	json.Unmarshal(body, &prepared)
	joined := string(body)
	if !strings.Contains(joined, "old group") || !strings.Contains(joined, "replace-custom-overwrite-with-standard") || !strings.Contains(joined, "remove-custom-overwrite-rule-link") {
		t.Fatalf("preview hid loss: %s", body)
	}
	revision, err := f.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		p, e := tx.Profiles()
		if len(p) != 0 {
			t.Fatal("prepare wrote metadata")
		}
		return e
	})
	if err != nil || revision != "0" {
		t.Fatalf("prepare changed store: %v %s", err, revision)
	}
	confirm := WebAPIBackupConfirm{WebAPIMutation: WebAPIMutation{ExpectedRevision: "0", OperationID: "legacy-confirm"}, Intent: "confirm-backup-conversion", Token: prepared.Data.Token, ArchiveDigest: prepared.Data.ArchiveDigest, PlanDigest: prepared.Data.PlanDigest}
	other := webLogin(t, f.auth)
	if status, _ := f.request(t, "POST", "/api/v1/backups:confirm", confirm, other); status != 409 {
		t.Fatalf("cross-session confirm %d", status)
	}
	wrong := confirm
	wrong.PlanDigest = "changed"
	if status, _ := f.request(t, "POST", "/api/v1/backups:confirm", wrong, f.cookie); status != 409 {
		t.Fatalf("plan tamper %d", status)
	}
	wrong = confirm
	wrong.RestoreDAVCredentials = true
	if status, _ := f.request(t, "POST", "/api/v1/backups:confirm", wrong, f.cookie); status != 409 {
		t.Fatalf("option tamper %d", status)
	}
	status, committed := f.request(t, "POST", "/api/v1/backups:confirm", confirm, f.cookie)
	if status != 200 {
		t.Fatalf("confirm %d: %s", status, committed)
	}
	status, replay := f.request(t, "POST", "/api/v1/backups:confirm", confirm, f.cookie)
	if status != 200 || !bytes.Equal(committed, replay) {
		t.Fatal("confirmation not replayable after token deletion")
	}
	_, err = f.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		profiles, e := tx.Profiles()
		if e != nil {
			return e
		}
		if len(profiles) != 1 || profiles[0].ID != "9007199254740993" || profiles[0].ExtendType != "standard" {
			t.Fatal("prepared conversion differs")
		}
		groups, e := tx.ProxyGroups()
		if len(groups) != 0 {
			t.Fatal("loss plan not applied")
		}
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	prepare.ExpectedRevision = "1"
	status, body = f.request(t, "POST", "/api/v1/backups:prepare", prepare, f.cookie)
	if status != 200 {
		t.Fatalf("second prepare %d %s", status, body)
	}
	json.Unmarshal(body, &prepared)
	f.backup.now = func() time.Time { return time.Now().Add(10 * time.Minute) }
	confirm.ExpectedRevision = "1"
	confirm.OperationID = "expired-confirm"
	confirm.Token = prepared.Data.Token
	confirm.PlanDigest = prepared.Data.PlanDigest
	confirm.ArchiveDigest = prepared.Data.ArchiveDigest
	if status, _ := f.request(t, "POST", "/api/v1/backups:confirm", confirm, f.cookie); status != 409 {
		t.Fatalf("expired plan %d", status)
	}
}
