//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func webBackupSettingsArchive(t *testing.T, config string) []byte {
	t.Helper()
	archive, err := webWriteBackupArchive("native-v1", map[string][]byte{"config.json": []byte(config)})
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func TestWebBackupHTTPSettingsConversionPreviewHasTypedValuesAndRequiresConfirm(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	archive := webBackupSettingsArchive(t, `{"version":1,"appSettingProps":{"crashlytics":false,"showLabel":true},"themeProps":{"schemeVariant":"neutral"}}`)
	restore := WebAPIBackupRestore{WebAPIMutation: WebAPIMutation{ExpectedRevision: "0", OperationID: "unconfirmed-settings"}, Intent: "restore-application-data", Strategy: "override", ArchiveBase64: archive}
	if status, body := f.request(t, "POST", "/api/v1/backups:restore", restore, f.cookie); status != 400 || !bytes.Contains(body, []byte("settings_conversion_required")) {
		t.Fatalf("unconfirmed display conversion=%d %s", status, body)
	}
	prepare := WebAPIBackupPrepare{ExpectedRevision: "0", ArchiveBase64: archive, Strategy: "override"}
	status, body := f.request(t, "POST", "/api/v1/backups:prepare", prepare, f.cookie)
	prepared := webContentHTTPDecode[WebAPIBackupPreparation](t, status, body)
	if prepared.Revision != "0" || !prepared.Data.RequiresConfirmation {
		t.Fatal("preparation committed or omitted confirmation")
	}
	seen := map[string]WebAPISettingsMigrationChange{}
	for _, change := range prepared.Data.Changes {
		if change.SettingsChange != nil {
			seen[change.SettingsChange.SourceField] = *change.SettingsChange
		}
	}
	theme, ok := seen["themeProps.schemeVariant"]
	if !ok || theme.Before.String == nil || *theme.Before.String != "neutral" || theme.After == nil || theme.After.String == nil || *theme.After.String != "soft" || !theme.RequiresConfirmation {
		t.Fatalf("theme original/proposed values missing: %s", body)
	}
	retired, ok := seen["appSettingProps.crashlytics"]
	if !ok || retired.Before.Bool == nil || *retired.Before.Bool || retired.Action != "delete" || retired.After != nil {
		t.Fatal("retired original value was silently discarded")
	}
	alias, ok := seen["appSettingProps.showLabel"]
	if !ok || !alias.Lossless || alias.RequiresConfirmation {
		t.Fatal("lossless alias was not visible separately from lossy rewrites")
	}
	confirm := WebAPIBackupConfirm{WebAPIMutation: WebAPIMutation{ExpectedRevision: "0", OperationID: "confirmed-settings"}, Intent: "confirm-backup-conversion", Token: prepared.Data.Token, ArchiveDigest: prepared.Data.ArchiveDigest, PlanDigest: prepared.Data.PlanDigest}
	changed := confirm
	changed.PlanDigest = strings.Repeat("a", 64)
	if status, _ := f.request(t, "POST", "/api/v1/backups:confirm", changed, f.cookie); status != 409 {
		t.Fatalf("changed settings plan digest=%d", status)
	}
	status, receipt := f.request(t, "POST", "/api/v1/backups:confirm", confirm, f.cookie)
	result := webContentHTTPDecode[WebAPIBackupResult](t, status, receipt)
	if result.Revision != "1" || !result.Data.Restored {
		t.Fatal("confirmed settings did not commit")
	}
	status, replay := f.request(t, "POST", "/api/v1/backups:confirm", confirm, f.cookie)
	if status != 200 || !bytes.Equal(receipt, replay) {
		t.Fatal("settings confirmation did not replay after stage removal")
	}
}

func TestWebBackupHTTPSettingsProjectionCannotBeForcedAndOnlyProfilesSkipsIt(t *testing.T) {
	for _, config := range []string{
		`{"version":1,"patchClashConfig":{"tun":{"enable":true,"auto-route":false}}}`,
		`{"version":1,"appSettingProps":{"crashlytics":true}}`,
		`{"version":1,"overrideNtp":true,"patchClashConfig":{"ntp":{"enable":true,"server":"time.example.test","port":123,"interval":30}}}`,
	} {
		t.Run(webContentDigest([]byte(config))[:8], func(t *testing.T) {
			f := newWebContentHTTPFixture(t, nil, nil)
			status, body := f.request(t, "POST", "/api/v1/profiles:import", webContentImportRequest("keep-profile", "0"), f.cookie)
			profile := webContentHTTPDecode[WebAPIProfile](t, status, body).Data
			archive := webBackupSettingsArchive(t, config)
			prepare := WebAPIBackupPrepare{ExpectedRevision: "1", ArchiveBase64: archive, Strategy: "override"}
			status, body = f.request(t, "POST", "/api/v1/backups:prepare", prepare, f.cookie)
			if status != 400 || !bytes.Contains(body, []byte("settings_conversion_required")) {
				t.Fatalf("projection entered confirmable stage=%d %s", status, body)
			}
			prepare.OnlyProfiles = true
			status, body = f.request(t, "POST", "/api/v1/backups:prepare", prepare, f.cookie)
			prepared := webContentHTTPDecode[WebAPIBackupPreparation](t, status, body)
			for _, change := range prepared.Data.Changes {
				if change.SettingsChange != nil {
					t.Fatal("unselected legacy settings entered the plan")
				}
			}
			confirm := WebAPIBackupConfirm{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "profiles-only"}, Intent: "confirm-backup-conversion", Token: prepared.Data.Token, ArchiveDigest: prepared.Data.ArchiveDigest, PlanDigest: prepared.Data.PlanDigest}
			status, body = f.request(t, "POST", "/api/v1/backups:confirm", confirm, f.cookie)
			webContentHTTPDecode[WebAPIBackupResult](t, status, body)
			_, err := f.store.View(context.Background(), func(tx *WebAPIReadTx) error {
				if _, err := tx.Profile(profile.ID); err != nil {
					return err
				}
				settings, err := tx.Settings()
				if err != nil {
					return err
				}
				actual, _ := json.Marshal(settings)
				expected, _ := json.Marshal(DefaultWebAPISettings())
				if !bytes.Equal(actual, expected) {
					t.Error("onlyProfiles changed application settings")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
