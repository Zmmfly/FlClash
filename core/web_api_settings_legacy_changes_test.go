//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func settingsMigrationChange(t *testing.T, plan WebAPISettingsMigrationPlan, field string) WebAPISettingsMigrationChange {
	t.Helper()
	for _, change := range plan.Changes {
		if change.SourceField == field {
			return change
		}
	}
	t.Fatalf("missing explicit before/after migration for %s", field)
	return WebAPISettingsMigrationChange{}
}

func TestWebAPISettingsLegacyDisplayPlanHasExactBeforeAfterAndDeletionValues(t *testing.T) {
	fixture := settingsLegacyFixture(t, "v0.8.95")
	snapshot, plan, err := NativeWebAPISettingsSnapshotForSchema(fixture.Schema, []byte(fixture.Config))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RequiresConfirmation || plan.RequiresProjection {
		t.Fatal("approximate display conversion was not distinguished from an unconfirmable projection")
	}
	scheme := settingsMigrationChange(t, plan, "themeProps.schemeVariant")
	if scheme.Before.Kind != "string" || scheme.Before.String == nil || *scheme.Before.String != "neutral" || scheme.After == nil || scheme.After.String == nil || *scheme.After.String != "soft" || !scheme.RequiresConfirmation || scheme.Lossless {
		t.Fatal("theme approximation did not preserve the original and proposed value")
	}
	window := settingsMigrationChange(t, plan, "windowProps")
	if window.Before.Window == nil || window.Before.Window.Width != 1040 || window.Before.Window.Top == nil || *window.Before.Window.Top != 13.5 || !reflect.DeepEqual(window.Before.WindowFields, []string{"width", "height", "top", "left"}) || window.After == nil || window.After.Window == nil || window.After.Window.Width != 1040 || window.TargetField != "windowGeometry" {
		t.Fatal("window migration hid source values or source field presence")
	}
	widgets := settingsMigrationChange(t, plan, "appSettingProps.dashboardWidgets")
	if !reflect.DeepEqual(widgets.Before.Strings, []string{"networkDetection", "outboundModeV2", "memoryInfo"}) || widgets.After == nil || !reflect.DeepEqual(widgets.After.Strings, snapshot.Presentation.AppSetting.DashboardWidgets) || !widgets.RequiresConfirmation {
		t.Fatal("dashboard rewrite did not expose the complete original and proposed lists")
	}
	retired := settingsMigrationChange(t, plan, "appSettingProps.crashlytics")
	if retired.Before.Bool == nil || *retired.Before.Bool || retired.Action != "delete" || retired.After != nil || !retired.RequiresConfirmation {
		t.Fatal("retired disabled metadata was silently deleted")
	}
	alias := settingsMigrationChange(t, plan, "appSettingProps.showLabel")
	if !alias.Lossless || alias.RequiresConfirmation || alias.Before.Bool == nil || alias.After == nil || alias.After.Bool == nil || *alias.Before.Bool != *alias.After.Bool {
		t.Fatal("lossless display alias changed its value")
	}
	before := *window.Before.Window.Top
	*snapshot.Presentation.WindowGeometry.Top = 999
	if *window.Before.Window.Top != before {
		t.Fatal("candidate mutation changed the recorded original value")
	}
}

func TestWebAPISettingsLegacyDisplayPlanIsTypedAndRoundTripsForTokenBinding(t *testing.T) {
	fixture := settingsLegacyFixture(t, "v0.8.99")
	_, plan, err := NativeWebAPISettingsSnapshotForSchema(fixture.Schema, []byte(fixture.Config))
	if err != nil {
		t.Fatal(err)
	}
	selected := settingsMigrationChange(t, plan, "appSettingProps.currentService")
	if selected.Before.Kind != "null" || selected.Action != "delete" || selected.After != nil || !selected.RequiresConfirmation {
		t.Fatal("a present null retired field disappeared from the conversion list")
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var restored WebAPISettingsMigrationPlan
	if err = json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan, restored) {
		t.Fatal("typed before/after plan changed during serialization")
	}
	for _, change := range plan.Changes {
		if change.SourceField == "" || change.Before.Kind == "" || change.RequiresConfirmation == change.Lossless {
			t.Fatal("migration change has an ambiguous consent requirement")
		}
		if change.Action == "delete" && change.After != nil {
			t.Fatal("deletion retained an ambiguous proposed value")
		}
	}
}

func TestWebAPISettingsLegacyNetworkEffectsCannotBecomeDisplayConfirmation(t *testing.T) {
	fixture := settingsLegacyFixture(t, "v0.8.99")
	for _, field := range []string{"crashlytics", "currentService"} {
		root := settingsLegacyObject(t, fixture.Config)
		app := root["appSettingProps"].(map[string]any)
		if field == "crashlytics" {
			app[field] = true
		} else {
			app[field] = "youtube"
		}
		_, plan, err := NativeWebAPISettingsSnapshotForSchema(fixture.Schema, settingsLegacyJSON(t, root))
		settingsLegacyError(t, err, "settings_conversion_required")
		if !plan.RequiresProjection {
			t.Fatal("network-effect preference was accepted as a display rewrite")
		}
		for _, change := range plan.Changes {
			if change.SourceField == "appSettingProps."+field {
				t.Fatal("a blocked network effect was offered as a confirmable deletion")
			}
		}
	}
}

func TestWebAPISettingsLegacyPlanningAndDiscardDoNotWriteStore(t *testing.T) {
	fixture := newSettingsHTTPFixture(t)
	settings := settingsLegacyFixture(t, "v0.8.95")
	before, err := fixture.store.View(context.Background(), func(tx *WebAPIReadTx) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	_, plan, err := NativeWebAPISettingsSnapshotForSchema(settings.Schema, []byte(settings.Config))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RequiresConfirmation {
		t.Fatal("fixture no longer exercises a confirmation-requiring plan")
	}
	after, err := fixture.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		value, err := tx.Settings()
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(value, DefaultWebAPISettings()) {
			t.Fatal("planning or discarding a candidate wrote live settings")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if before != after || fixture.commits.Load() != 0 {
		t.Fatal("unconfirmed settings planning advanced the committed revision")
	}
}
