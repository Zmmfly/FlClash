//go:build webpanel && !android

package core

import (
	"reflect"
	"strings"
	"testing"
)

func TestWebAPISettingsContractTypedPartialFieldCoverage(t *testing.T) {
	pairs := [][2]reflect.Type{
		{reflect.TypeFor[WebAPISettingsRuntime](), reflect.TypeFor[WebAPISettingsRuntimePatch]()},
		{reflect.TypeFor[WebAPISettingsApp](), reflect.TypeFor[WebAPISettingsAppPatch]()},
		{reflect.TypeFor[WebAPISettingsNetwork](), reflect.TypeFor[WebAPISettingsNetworkPatch]()},
		{reflect.TypeFor[WebAPISettingsNetworkProps](), reflect.TypeFor[WebAPISettingsNetworkPropsPatch]()},
		{reflect.TypeFor[WebAPISettingsAuthentication](), reflect.TypeFor[WebAPISettingsAuthenticationPatch]()},
		{reflect.TypeFor[WebAPISettingsVPN](), reflect.TypeFor[WebAPISettingsVPNPatch]()},
		{reflect.TypeFor[WebAPISettingsAccessControl](), reflect.TypeFor[WebAPISettingsAccessControlPatch]()},
		{reflect.TypeFor[WebAPISettingsDAV](), reflect.TypeFor[WebAPISettingsDAVPatch]()},
		{reflect.TypeFor[WebAPISettingsClash](), reflect.TypeFor[WebAPISettingsClashPatch]()},
		{reflect.TypeFor[WebAPISettingsGeoXURL](), reflect.TypeFor[WebAPISettingsGeoXURLPatch]()},
		{reflect.TypeFor[WebAPISettingsTun](), reflect.TypeFor[WebAPISettingsTunPatch]()},
		{reflect.TypeFor[WebAPISettingsMitm](), reflect.TypeFor[WebAPISettingsMitmPatch]()},
		{reflect.TypeFor[WebAPIDNS](), reflect.TypeFor[WebAPISettingsDNSPatch]()},
		{reflect.TypeFor[WebAPIFallbackFilter](), reflect.TypeFor[WebAPISettingsFallbackFilterPatch]()},
		{reflect.TypeFor[WebAPIProfileTun](), reflect.TypeFor[WebAPISettingsProfileTunPatch]()},
	}
	for _, pair := range pairs {
		full, partial := pair[0], pair[1]
		t.Run(full.Name(), func(t *testing.T) {
			if full.NumField() != partial.NumField() {
				t.Fatal("a typed native setting field is missing from its explicit partial command")
			}
			for i := 0; i < full.NumField(); i++ {
				source := full.Field(i)
				patch, ok := partial.FieldByName(source.Name)
				if !ok || patch.Type.Kind() != reflect.Pointer || strings.Split(source.Tag.Get("json"), ",")[0] != strings.Split(patch.Tag.Get("json"), ",")[0] {
					t.Fatalf("field %s does not have an exact optional typed command field", source.Name)
				}
				if source.Type.Kind() != reflect.Struct && source.Type.Kind() != reflect.Pointer && patch.Type.Elem() != source.Type {
					t.Fatalf("field %s changed its native type", source.Name)
				}
			}
		})
	}
}
