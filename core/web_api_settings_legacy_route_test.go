//go:build webpanel && !android

package core

import (
	"reflect"
	"testing"
)

func TestWebAPISettingsLegacyDisabledDNSSelectionIsNotErased(t *testing.T) {
	fixture := settingsLegacyFixture(t, "v0.8.99")
	root := settingsLegacyObject(t, fixture.Config)
	root["overrideDns"] = false
	root["patchClashConfig"].(map[string]any)["dns-override-keys"] = []string{"nameserver"}
	snapshot, plan, err := NativeWebAPISettingsSnapshotForSchema(fixture.Schema, settingsLegacyJSON(t, root))
	settingsLegacyError(t, err, "settings_conversion_required")
	if len(snapshot.Runtime.PatchClashConfig.DNSOverrideKeys) != 0 || plan.LegacyRouting == nil || !reflect.DeepEqual(plan.LegacyRouting.DNSOverrideKeys, []string{"nameserver"}) || plan.LegacyRouting.OverrideDNS == nil || *plan.LegacyRouting.OverrideDNS {
		t.Fatal("disabled legacy selection was activated or its configured keys were silently erased")
	}
}

func TestWebAPISettingsLegacyActiveEmptyRoutesNeedProjection(t *testing.T) {
	fixture := settingsLegacyFixture(t, "v0.9.0")
	for _, key := range []string{"route-address", "route-exclude-address"} {
		t.Run(key, func(t *testing.T) {
			root := settingsLegacyObject(t, fixture.Config)
			tun := root["patchClashConfig"].(map[string]any)["tun"].(map[string]any)
			tun["enable"] = true
			tun[key] = []string{}
			_, plan, err := NativeWebAPISettingsSnapshotForSchema(fixture.Schema, settingsLegacyJSON(t, root))
			settingsLegacyError(t, err, "settings_conversion_required")
			found := false
			for _, step := range plan.Steps {
				if step.Kind == WebAPISettingsMigrationRouting && step.Field == "patchClashConfig.tun."+key {
					found = true
				}
			}
			if !found {
				t.Fatal("an explicit empty forced route field became an inherited profile field")
			}
		})
	}
}

func TestWebAPISettingsLegacyBypassModeRetainsIgnoredRouteInputInPlan(t *testing.T) {
	fixture := settingsLegacyFixture(t, "v0.8.95")
	root := settingsLegacyObject(t, fixture.Config)
	root["networkProps"].(map[string]any)["routeMode"] = "bypassPrivate"
	snapshot, plan, err := NativeWebAPISettingsSnapshotForSchema(fixture.Schema, settingsLegacyJSON(t, root))
	settingsLegacyError(t, err, "settings_conversion_required")
	if !snapshot.Network.NetworkProps.BypassPrivateRoute || len(snapshot.Runtime.PatchClashConfig.Tun.RouteAddress) != 0 || plan.LegacyRouting == nil || !reflect.DeepEqual(plan.LegacyRouting.RouteAddress, []string{"198.18.0.0/15"}) || plan.LegacyRouting.RouteMode == nil || *plan.LegacyRouting.RouteMode != "bypassPrivate" {
		t.Fatal("the old bypass mode lost its saved but ignored route list without a typed conversion obligation")
	}
}
