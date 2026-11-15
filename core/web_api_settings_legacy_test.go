//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func settingsLegacyObject(t *testing.T, document string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(document))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func settingsLegacyJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func settingsLegacyFixture(t *testing.T, tag string) webAPISettingsReleasedConfigFixture {
	t.Helper()
	for _, fixture := range webAPISettingsReleasedFixtures {
		if fixture.Tag == tag {
			return fixture
		}
	}
	t.Fatal("missing released config fixture")
	return webAPISettingsReleasedConfigFixture{}
}

func settingsLegacyError(t *testing.T, err error, code string) {
	t.Helper()
	var problem *WebAPIError
	if !errors.As(err, &problem) || problem.Code != code {
		t.Fatalf("error code=%v want=%s", err, code)
	}
}

func TestWebAPISettingsReleasedConfigFixturesMigrateAndNativeRoundTrip(t *testing.T) {
	for _, fixture := range webAPISettingsReleasedFixtures {
		t.Run(fixture.Tag, func(t *testing.T) {
			if len(fixture.GeneratedConfigSHA256) != 64 || len(fixture.GeneratedClashSHA256) != 64 || len(fixture.Config) < 4000 {
				t.Fatal("released evidence or complete synthetic config fixture missing")
			}
			snapshot, plan, err := NativeWebAPISettingsSnapshotForSchema(fixture.Schema, []byte(fixture.Config))
			if err != nil {
				t.Fatal(err)
			}
			if plan.SourceSchema != fixture.Schema || plan.SourceConfigVersion != 1 || plan.RequiresProjection {
				t.Fatal("unresolved legacy plan was silently accepted")
			}
			if snapshot.CurrentProfileID == nil || *snapshot.CurrentProfileID != "9007199254740993" {
				t.Fatal("native numeric identity was rounded")
			}
			if snapshot.Runtime.PatchClashConfig.MixedPort != 17890 || snapshot.Runtime.PatchClashConfig.Mode != "global" || snapshot.Runtime.PatchClashConfig.GlobalUA == nil || *snapshot.Runtime.PatchClashConfig.GlobalUA != "FixtureGlobal/4.2" {
				t.Fatal("runtime settings changed during migration")
			}
			if snapshot.Runtime.PatchClashConfig.DNS.Nameserver[0] != "https://resolver.fixture.invalid/query?token=fixture-dns-token" || snapshot.Runtime.PatchClashConfig.DNS.NameserverPolicy["fixture.invalid"] != "https://policy.fixture.invalid/query" {
				t.Fatal("typed DNS values were dropped")
			}
			if fixture.Schema >= 3 && snapshot.Network.NetworkProps.Authentication.Password != "fixture-inbound-password" {
				t.Fatal("application inbound authentication was lost")
			}
			if fixture.Tag >= "v0.9.0" && snapshot.Runtime.PatchClashConfig.Secret != "fixture-controller-secret" {
				t.Fatal("native application controller secret was lost")
			}
			if snapshot.DAV == nil || snapshot.DAV.URI != "https://dav.fixture.invalid/backup?token=fixture-dav-token" || snapshot.DAV.User != "fixture-dav-user" || snapshot.DAV.Password != "fixture-dav-password" || snapshot.DAV.FileName != "fixture-backup.zip" {
				t.Fatal("native DAV tuple was not decoded intact")
			}
			presentation := snapshot.Presentation
			if presentation == nil || presentation.AppSetting.Locale == nil || *presentation.AppSetting.Locale != "zh_CN" || presentation.ThemeProps.ColorStyle != "soft" || presentation.WindowGeometry.Width != 1040 || presentation.WindowGeometry.Top == nil || *presentation.WindowGeometry.Top != 13.5 {
				t.Fatal("known presentation mappings lost fields")
			}
			if !webAPISettingsEnum("currentOutbound", presentation.AppSetting.DashboardWidgets...) {
				t.Fatal("known legacy dashboard mapping was not applied")
			}
			if fixture.Schema == 2 && snapshot.Runtime.PatchClashConfig.InterfaceNameMode != "follow" {
				t.Fatal("legacy profile interface inheritance was weakened")
			}
			if fixture.Schema <= 3 && (presentation.AppSetting.TabAnimation != "fade" || !presentation.AppSetting.SidebarExpanded || snapshot.Runtime.AppSetting.UserAgents[len(snapshot.Runtime.AppSetting.UserAgents)-1] != "FixtureLegacy/7.0") {
				t.Fatal("known legacy app aliases lost intent")
			}
			native, err := NativeWebAPISettingsConfigWithOptions(snapshot, WebAPISettingsSnapshotOptions{IncludeDAVCredentials: true})
			if err != nil {
				t.Fatal(err)
			}
			canonical, canonicalPlan, err := NativeWebAPISettingsSnapshotForSchema(23, native)
			if err != nil {
				t.Fatal(err)
			}
			if canonicalPlan.RequiresProjection || !reflect.DeepEqual(snapshot, canonical) {
				t.Fatal("migrated settings did not survive a full canonical native round trip")
			}
			withoutDAV, err := NativeWebAPISettingsConfig(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			for _, part := range []string{"davProps", "fixture-dav-token", "fixture-dav-user", "fixture-dav-password", "fixture-backup.zip"} {
				if bytes.Contains(withoutDAV, []byte(part)) {
					t.Fatal("default export leaked a DAV tuple component")
				}
			}
		})
	}
}

func TestWebAPISettingsReleasedLegacyUnknownsAndAliasConflictsFailClosed(t *testing.T) {
	fixture := settingsLegacyFixture(t, "v0.8.95")
	mutations := []struct {
		name   string
		code   string
		mutate func(map[string]any)
	}{
		{"unknown-root", "invalid_native_settings", func(root map[string]any) { root["webAuth"] = map[string]any{"passwordHash": "synthetic-never-import"} }},
		{"unknown-app", "invalid_native_settings", func(root map[string]any) { root["appSettingProps"].(map[string]any)["unknownFutureAuthority"] = true }},
		{"unknown-dashboard-enum", "invalid_settings", func(root map[string]any) {
			root["appSettingProps"].(map[string]any)["dashboardWidgets"] = []string{"unknown-dashboard"}
		}},
		{"unknown-theme-enum", "invalid_settings", func(root map[string]any) { root["themeProps"].(map[string]any)["schemeVariant"] = "unknown-scheme" }},
		{"unknown-style-enum", "invalid_settings", func(root map[string]any) { root["proxiesStyleProps"].(map[string]any)["cardType"] = "future-card" }},
		{"conflicting-sidebar-alias", "conflicting_legacy_settings", func(root map[string]any) { root["appSettingProps"].(map[string]any)["sidebarExpanded"] = false }},
		{"conflicting-route-alias", "conflicting_legacy_settings", func(root map[string]any) { root["networkProps"].(map[string]any)["bypassPrivateRoute"] = true }},
		{"unknown-inactive-override", "invalid_settings", func(root map[string]any) {
			root["patchClashConfig"].(map[string]any)["dns-override-keys"] = []string{"unknown-routing-option"}
		}},
		{"invalid-null-legacy-bool", "invalid_settings", func(root map[string]any) { root["appSettingProps"].(map[string]any)["showLabel"] = nil }},
		{"quoted-v1-identity", "invalid_settings", func(root map[string]any) { root["currentProfileId"] = "9007199254740993" }},
		{"exponent-v1-identity", "invalid_settings", func(root map[string]any) { root["currentProfileId"] = json.Number("9e3") }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			root := settingsLegacyObject(t, fixture.Config)
			test.mutate(root)
			_, _, err := NativeWebAPISettingsSnapshotForSchema(fixture.Schema, settingsLegacyJSON(t, root))
			settingsLegacyError(t, err, test.code)
		})
	}
	_, _, err := NativeWebAPISettingsSnapshotForSchema(9, []byte(fixture.Config))
	settingsLegacyError(t, err, "unsupported_settings_schema")
	_, _, err = NativeWebAPISettingsSnapshotForSchema(2, []byte(`{"version":1,"version":0}`))
	settingsLegacyError(t, err, "invalid_native_settings")
}

func TestWebAPISettingsReleasedRoutingNeedsExplicitProjection(t *testing.T) {
	for _, test := range []struct {
		name   string
		tag    string
		mutate func(map[string]any)
		field  string
	}{
		{"active-manual-routing", "v0.8.95", func(root map[string]any) {
			tun := root["patchClashConfig"].(map[string]any)["tun"].(map[string]any)
			tun["enable"] = true
			tun["auto-route"] = false
		}, "patchClashConfig.tun.auto-route"},
		{"empty-dns-hijack", "v0.8.95", func(root map[string]any) {
			root["patchClashConfig"].(map[string]any)["tun"].(map[string]any)["dns-hijack"] = []string{}
		}, "patchClashConfig.tun.dns-hijack"},
		{"active-full-dns-overwrite", "v0.8.95", func(root map[string]any) { root["overrideDns"] = true }, "patchClashConfig.dns-override-keys"},
		{"appwide-ntp", "v0.8.99", func(root map[string]any) {
			root["overrideNtp"] = true
			patch := root["patchClashConfig"].(map[string]any)
			patch["ntp-override-keys"] = []string{"server"}
			patch["ntp"].(map[string]any)["server"] = "ntp.fixture.invalid"
		}, "patchClashConfig.ntp"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := settingsLegacyFixture(t, test.tag)
			root := settingsLegacyObject(t, fixture.Config)
			test.mutate(root)
			snapshot, plan, err := NativeWebAPISettingsSnapshotForSchema(fixture.Schema, settingsLegacyJSON(t, root))
			settingsLegacyError(t, err, "settings_conversion_required")
			if !plan.RequiresProjection || plan.LegacyRouting == nil {
				t.Fatal("unrepresentable routing values were discarded")
			}
			found := false
			for _, step := range plan.Steps {
				if step.Kind == WebAPISettingsMigrationRouting && step.Field == test.field {
					found = true
				}
			}
			if !found || snapshot.Runtime.PatchClashConfig.Mode != "global" {
				t.Fatal("conversion did not return a typed candidate and exact routing obligation")
			}
		})
	}
}

func TestWebAPISettingsReleasedRetiredNondefaultPreferencesAreNotDropped(t *testing.T) {
	fixture := settingsLegacyFixture(t, "v0.8.99")
	root := settingsLegacyObject(t, fixture.Config)
	root["appSettingProps"].(map[string]any)["crashlytics"] = true
	root["appSettingProps"].(map[string]any)["currentService"] = "custom-fixture-service"
	_, plan, err := NativeWebAPISettingsSnapshotForSchema(fixture.Schema, settingsLegacyJSON(t, root))
	settingsLegacyError(t, err, "settings_conversion_required")
	if plan.RetiredPresentation == nil || plan.RetiredPresentation.Crashlytics == nil || !*plan.RetiredPresentation.Crashlytics || plan.RetiredPresentation.CurrentService == nil || *plan.RetiredPresentation.CurrentService != "custom-fixture-service" {
		t.Fatal("known retired preferences were silently lost")
	}
}

func TestWebAPISettingsLegacyVersionZeroIdentityIsOpaqueUntilBound(t *testing.T) {
	fixture := settingsLegacyFixture(t, "v0.8.95")
	for _, legacyID := range []string{"opaque-profile-fixture", "9007199254740993", "9223372036854775807"} {
		t.Run(legacyID, func(t *testing.T) {
			root := settingsLegacyObject(t, fixture.Config)
			root["version"] = json.Number("0")
			root["currentProfileId"] = legacyID
			root["appSetting"] = root["appSettingProps"]
			delete(root, "appSettingProps")
			root["proxiesStyle"] = root["proxiesStyleProps"]
			delete(root, "proxiesStyleProps")
			root["dav"] = root["davProps"]
			delete(root, "davProps")
			snapshot, plan, err := NativeWebAPISettingsSnapshotForSchema(0, settingsLegacyJSON(t, root))
			settingsLegacyError(t, err, "settings_conversion_required")
			if plan.SourceConfigVersion != 0 || plan.LegacyCurrentProfileID == nil || *plan.LegacyCurrentProfileID != legacyID || snapshot.CurrentProfileID != nil || !plan.RequiresProjection {
				t.Fatal("v0 string identity was coerced, rounded, guessed or silently cleared")
			}
			if snapshot.DAV == nil || snapshot.DAV.Password != "fixture-dav-password" {
				t.Fatal("v0 alias mapping lost the full DAV tuple")
			}
		})
	}
}

func TestWebAPISettingsConfigShapeIsNotGuessedFromDatabaseSchema(t *testing.T) {
	for _, fixture := range webAPISettingsReleasedFixtures {
		t.Run(fixture.Tag, func(t *testing.T) {
			original, _, err := NativeWebAPISettingsSnapshotForSchema(fixture.Schema, []byte(fixture.Config))
			if err != nil {
				t.Fatal(err)
			}
			migratedDB, _, err := NativeWebAPISettingsSnapshotForSchema(23, []byte(fixture.Config))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(original, migratedDB) {
				t.Fatal("the same explicit config intent changed when only the SQLite schema metadata changed")
			}
		})
	}
}

func TestWebAPISettingsLegacyDAVSensitiveURIWholeTupleIntent(t *testing.T) {
	fixture := settingsLegacyFixture(t, "v0.8.95")
	root := settingsLegacyObject(t, fixture.Config)
	uri := "https://uri-user:uri-password@dav.fixture.invalid/path?token=uri-token#private-fragment"
	root["davProps"].(map[string]any)["uri"] = uri
	snapshot, _, err := NativeWebAPISettingsSnapshotForSchema(fixture.Schema, settingsLegacyJSON(t, root))
	if err != nil {
		t.Fatal(err)
	}
	current := DefaultWebAPISettings()
	current.DAV = &WebAPISettingsDAV{URI: "https://current.fixture.invalid/", User: "current", Password: "current-password", FileName: "current.zip"}
	defaultRestore, err := RestoreWebAPISettings(current, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(defaultRestore.DAV, current.DAV) {
		t.Fatal("archive URI was combined with current credentials without explicit intent")
	}
	explicit, err := RestoreWebAPISettingsWithOptions(current, snapshot, WebAPISettingsRestoreOptions{RestoreDAVCredentials: true})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.DAV == nil || explicit.DAV.URI != uri || explicit.DAV.Password != "fixture-dav-password" || explicit.DAV.User != "fixture-dav-user" {
		t.Fatal("explicit native DAV tuple restore lost a URI component")
	}
	native, err := NativeWebAPISettingsConfig(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"uri-user", "uri-password", "uri-token", "private-fragment", "fixture-dav-user"} {
		if bytes.Contains(native, []byte(secret)) {
			t.Fatal("default native export leaked a sensitive DAV URI component")
		}
	}
}

func TestWebAPISettingsLegacyV0ResourcesRequireArchiveOwnerProjection(t *testing.T) {
	fixture := settingsLegacyFixture(t, "v0.8.95")
	root := settingsLegacyObject(t, fixture.Config)
	root["version"] = json.Number("0")
	root["profiles"] = []any{}
	_, plan, err := NativeWebAPISettingsSnapshotForSchema(0, settingsLegacyJSON(t, root))
	settingsLegacyError(t, err, "legacy_settings_resource_projection_required")
	if !plan.RequiresProjection || len(plan.Steps) != 1 || plan.Steps[0].Kind != WebAPISettingsMigrationResources || plan.Steps[0].Field != "profiles" {
		t.Fatal("non-settings v0 resources were silently ignored")
	}
}

func TestWebAPISettingsLegacyPresenceDoesNotBorrowOrMutateSiblingFields(t *testing.T) {
	var document map[string]json.RawMessage
	raw := []byte(`{"patchClashConfig":{"tun":{"mtu":8192},"tun-override":{}},"appSettingProps":{"sidebarExpanded":true}}`)
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	before := settingsLegacyJSON(t, document)
	if !webAPISettingsLegacyHas(document, "patchClashConfig", "tun", "mtu") {
		t.Fatal("present field was not found")
	}
	if webAPISettingsLegacyHas(document, "patchClashConfig", "tun-override", "mtu") {
		t.Fatal("a sibling's value supplied an absent field")
	}
	if webAPISettingsLegacyHas(document, "patchClashConfig", "sidebarExpanded") {
		t.Fatal("a field from another section supplied a baseline")
	}
	if !bytes.Equal(before, settingsLegacyJSON(t, document)) {
		t.Fatal("read-only presence lookup mutated the source document")
	}
}

func TestWebAPISettingsLegacyMalformedRawScalarsDoNotEchoObjectKeys(t *testing.T) {
	fixture := settingsLegacyFixture(t, "v0.9.3")
	for _, field := range []string{"currentProfileId", "liquidGlass"} {
		root := settingsLegacyObject(t, fixture.Config)
		invalid := map[string]any{"synthetic-do-not-echo-private-value": nil}
		if field == "currentProfileId" {
			root[field] = invalid
		} else {
			root["themeProps"].(map[string]any)[field] = invalid
		}
		_, _, err := NativeWebAPISettingsSnapshotForSchema(fixture.Schema, settingsLegacyJSON(t, root))
		settingsLegacyError(t, err, "invalid_settings")
		if bytes.Contains(settingsLegacyJSON(t, err), []byte("synthetic-do-not-echo-private-value")) {
			t.Fatal("malformed primitive error echoed a request object's key")
		}
	}
}

func TestWebAPISettingsLegacyUnicodeDoesNotCoerceOpaqueIdentityOrCredentials(t *testing.T) {
	fixture := settingsLegacyFixture(t, "v0.8.95")
	for _, rawID := range []json.RawMessage{json.RawMessage(`"opaque-\ud800"`), json.RawMessage(`"opaque-\udfff"`)} {
		root := settingsLegacyObject(t, fixture.Config)
		root["version"] = json.Number("0")
		root["currentProfileId"] = rawID
		_, _, err := NativeWebAPISettingsSnapshotForSchema(0, settingsLegacyJSON(t, root))
		settingsLegacyError(t, err, "invalid_native_settings")
	}
	root := settingsLegacyObject(t, fixture.Config)
	root["version"] = json.Number("0")
	root["currentProfileId"] = json.RawMessage(`"opaque-\uD83D\uDE00"`)
	_, plan, err := NativeWebAPISettingsSnapshotForSchema(0, settingsLegacyJSON(t, root))
	settingsLegacyError(t, err, "settings_conversion_required")
	if plan.LegacyCurrentProfileID == nil || *plan.LegacyCurrentProfileID != "opaque-😀" {
		t.Fatal("a valid Unicode identity did not remain exact")
	}
	root = settingsLegacyObject(t, fixture.Config)
	root["davProps"].(map[string]any)["password"] = json.RawMessage(`"password-\ud800"`)
	_, _, err = NativeWebAPISettingsSnapshotForSchema(2, settingsLegacyJSON(t, root))
	settingsLegacyError(t, err, "invalid_native_settings")
	if !webAPISettingsLegacyUnicodeExact([]byte(`{"value":"literal-\\ud800"}`)) {
		t.Fatal("an escaped backslash was mistaken for a surrogate")
	}
}
