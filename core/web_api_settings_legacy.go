//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

func NativeWebAPISettingsSnapshotForSchema(schema int, configJSON []byte) (WebAPISettingsSnapshot, WebAPISettingsMigrationPlan, error) {
	plan := WebAPISettingsMigrationPlan{SourceSchema: schema, SourceConfigVersion: 1, Steps: []WebAPISettingsMigrationStep{}, Changes: []WebAPISettingsMigrationChange{}}
	if schema == 0 {
		plan.SourceConfigVersion = 0
	}
	if schema != 0 && schema != 2 && schema != 3 && schema != 10 && schema != 20 && schema != 21 && schema != 23 {
		return WebAPISettingsSnapshot{}, plan, webAPIError(400, "unsupported_settings_schema", "native settings source schema is not supported")
	}
	if len(configJSON) > webAPIMaxJSONBytes {
		return WebAPISettingsSnapshot{}, plan, webAPIError(413, "settings_too_large", "native settings exceed the JSON limit")
	}
	if !webAPISettingsLegacyUnicodeExact(configJSON) {
		return WebAPISettingsSnapshot{}, plan, webAPIError(400, "invalid_native_settings", "native settings must contain well-formed Unicode strings")
	}
	seed, err := webAPISettingsNative(SnapshotWebAPISettings(DefaultWebAPISettings()))
	if err != nil {
		return WebAPISettingsSnapshot{}, plan, err
	}
	version := plan.SourceConfigVersion
	seed.Version = &version
	var document map[string]json.RawMessage
	if err = webAPIStrictJSON(configJSON, &document); err != nil {
		return WebAPISettingsSnapshot{}, plan, webAPIError(400, "invalid_native_settings", "native settings are not strict JSON")
	}
	if raw, ok := document["version"]; ok {
		var sourceVersion int32
		if json.Unmarshal(raw, &sourceVersion) == nil {
			plan.SourceConfigVersion = sourceVersion
		}
	}
	if plan.SourceConfigVersion == 0 {
		for _, section := range []string{"profiles", "scripts", "rules", "scriptProps", "overwrite"} {
			if webAPISettingsLegacyHas(document, section) {
				plan.RequiresProjection = true
				plan.add(WebAPISettingsMigrationResources, section)
				return WebAPISettingsSnapshot{}, plan, &WebAPIError{Status: 400, Code: "legacy_settings_resource_projection_required", Message: "the archive owner must decode non-settings resources before projecting legacy settings", Details: map[string]any{"field": section}}
			}
		}
	}
	if raw, ok := document["davProps"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		seed.DAVProps = &WebAPISettingsDAV{FileName: "backup.zip"}
	}
	legacy := webAPISettingsLegacyNative{
		WebAPISettingsLegacyNativeBase: WebAPISettingsLegacyNativeBase(seed),
		App:                            &webAPISettingsLegacyApp{WebAPISettingsApp: seed.AppSettingProps.WebAPISettingsApp, WebAPISettingsAppPresentation: seed.AppSettingProps.WebAPISettingsAppPresentation},
		Theme:                          &webAPISettingsLegacyTheme{WebAPISettingsTheme: seed.ThemeProps},
		Network:                        &webAPISettingsLegacyNetwork{WebAPISettingsNetworkProps: seed.NetworkProps},
		VPN:                            &webAPISettingsLegacyVPN{WebAPISettingsVPN: seed.VPNProps},
		Patch:                          &webAPISettingsLegacyClash{WebAPISettingsClash: seed.PatchClashConfig, LegacyTun: &webAPISettingsLegacyTun{WebAPISettingsTun: seed.PatchClashConfig.Tun}, LegacyGeo: &webAPISettingsLegacyGeo{WebAPISettingsGeoXURL: seed.PatchClashConfig.GeoXURL}},
	}
	if webAPISettingsLegacyHas(document, "appSetting") {
		legacy.LegacyApp = &webAPISettingsLegacyApp{WebAPISettingsApp: seed.AppSettingProps.WebAPISettingsApp, WebAPISettingsAppPresentation: seed.AppSettingProps.WebAPISettingsAppPresentation}
	}
	if webAPISettingsLegacyHas(document, "dav") {
		legacy.LegacyDAV = &WebAPISettingsDAV{FileName: "backup.zip"}
	}
	if err = webAPIStrictJSON(configJSON, &legacy); err != nil {
		return WebAPISettingsSnapshot{}, plan, webAPIError(400, "invalid_native_settings", "native settings have an unknown field or invalid typed value")
	}
	if err = webAPISettingsLegacyNulls(configJSON, ""); err != nil {
		return WebAPISettingsSnapshot{}, plan, err
	}
	if legacy.Version == nil || (*legacy.Version != 0 && *legacy.Version != 1) {
		return WebAPISettingsSnapshot{}, plan, webAPIError(400, "unsupported_native_settings", "native settings version is not supported")
	}
	plan.SourceConfigVersion = *legacy.Version
	appKey := "appSettingProps"
	if legacy.LegacyApp != nil {
		if webAPISettingsLegacyHas(document, "appSettingProps") && !webAPISettingsLegacySame(legacy.App, legacy.LegacyApp) {
			return WebAPISettingsSnapshot{}, plan, webAPISettingsLegacyConflict("appSettingProps")
		}
		legacy.App = legacy.LegacyApp
		appKey = "appSetting"
		plan.add(WebAPISettingsMigrationAlias, "appSetting")
	}
	if legacy.App == nil || legacy.Theme == nil || legacy.Network == nil || legacy.VPN == nil || legacy.Patch == nil || legacy.Patch.LegacyTun == nil || legacy.Patch.LegacyGeo == nil {
		return WebAPISettingsSnapshot{}, plan, webAPISettingsInvalid("config")
	}
	if legacy.LegacyWindow != nil {
		if webAPISettingsLegacyHas(document, "windowGeometry") && !webAPISettingsLegacySame(legacy.WindowGeometry, *legacy.LegacyWindow) {
			return WebAPISettingsSnapshot{}, plan, webAPISettingsLegacyConflict("windowGeometry")
		}
		legacy.WindowGeometry = *legacy.LegacyWindow
		before := webAPISettingsMigrationWindow(*legacy.LegacyWindow)
		for _, field := range []string{"width", "height", "top", "left", "scale"} {
			if webAPISettingsLegacyHas(document, "windowProps", field) {
				before.WindowFields = append(before.WindowFields, field)
			}
		}
		after := webAPISettingsMigrationWindow(legacy.WindowGeometry)
		after.WindowFields = []string{"width", "height", "top", "left", "scale"}
		plan.change("windowProps", "windowGeometry", before, &after, false)
		plan.add(WebAPISettingsMigrationAlias, "windowProps")
	}
	if legacy.LegacyStyle != nil {
		if webAPISettingsLegacyHas(document, "proxiesStyleProps") && !webAPISettingsLegacySame(legacy.ProxiesStyleProps, *legacy.LegacyStyle) {
			return WebAPISettingsSnapshot{}, plan, webAPISettingsLegacyConflict("proxiesStyleProps")
		}
		legacy.ProxiesStyleProps = *legacy.LegacyStyle
		plan.add(WebAPISettingsMigrationAlias, "proxiesStyle")
	}
	if webAPISettingsLegacyHas(document, "dav") {
		if webAPISettingsLegacyHas(document, "davProps") && !webAPISettingsLegacySame(legacy.DAVProps, legacy.LegacyDAV) {
			return WebAPISettingsSnapshot{}, plan, webAPISettingsLegacyConflict("davProps")
		}
		legacy.DAVProps = legacy.LegacyDAV
		plan.add(WebAPISettingsMigrationAlias, "dav")
	}
	if err = webAPISettingsLegacyAppFields(&legacy, document, appKey, &plan); err != nil {
		return WebAPISettingsSnapshot{}, plan, err
	}
	if err = webAPISettingsLegacyThemeFields(legacy.Theme, document, &plan); err != nil {
		return WebAPISettingsSnapshot{}, plan, err
	}
	if err = webAPISettingsLegacyStyle(&legacy.ProxiesStyleProps, &plan); err != nil {
		return WebAPISettingsSnapshot{}, plan, err
	}
	if err = webAPISettingsLegacyNetworkFields(&legacy, document, &plan); err != nil {
		return WebAPISettingsSnapshot{}, plan, err
	}
	if err = webAPISettingsLegacyClashFields(&legacy, document, &plan); err != nil {
		return WebAPISettingsSnapshot{}, plan, err
	}
	settings := WebAPISettings{Runtime: WebAPISettingsRuntime{PatchClashConfig: legacy.Patch.WebAPISettingsClash, AppSetting: legacy.App.WebAPISettingsApp}, Network: WebAPISettingsNetwork{NetworkProps: legacy.Network.WebAPISettingsNetworkProps, VPNProps: legacy.VPN.WebAPISettingsVPN, ExcludeSSIDs: legacy.ExcludeSSIDs},
		NativePresentation: &WebAPISettingsPresentation{AppSetting: legacy.App.WebAPISettingsAppPresentation, ThemeProps: legacy.Theme.WebAPISettingsTheme, WindowGeometry: legacy.WindowGeometry, HotKeyActions: legacy.HotKeyActions, ProxiesStyleProps: legacy.ProxiesStyleProps}}
	if err = webAPISettingsLegacyIdentity(legacy.CurrentProfile, &settings, &plan); err != nil {
		return WebAPISettingsSnapshot{}, plan, err
	}
	if err = ValidateWebAPISettings(settings); err != nil {
		return WebAPISettingsSnapshot{}, plan, err
	}
	if err = webAPISettingsLegacyPresentationValues(settings.NativePresentation); err != nil {
		return WebAPISettingsSnapshot{}, plan, err
	}
	snapshot := SnapshotWebAPISettings(settings)
	if legacy.DAVProps != nil {
		dav := *legacy.DAVProps
		dav.Password, err = webAPISettingsDecodeDAVPassword(dav.Password)
		if err != nil {
			return WebAPISettingsSnapshot{}, plan, err
		}
		snapshot.DAV = &dav
	}
	if plan.RequiresProjection {
		return snapshot, plan, webAPIError(400, "settings_conversion_required", "known legacy settings require an explicit typed conversion plan")
	}
	return snapshot, plan, nil
}

func (plan *WebAPISettingsMigrationPlan) add(kind WebAPISettingsMigrationKind, field string) {
	for _, step := range plan.Steps {
		if step.Kind == kind && step.Field == field {
			return
		}
	}
	plan.Steps = append(plan.Steps, WebAPISettingsMigrationStep{Kind: kind, Field: field})
}

func (plan *WebAPISettingsMigrationPlan) routing(field string) {
	plan.RequiresProjection = true
	plan.add(WebAPISettingsMigrationRouting, field)
}

func webAPISettingsLegacyHas(document map[string]json.RawMessage, paths ...string) bool {
	for i, path := range paths {
		raw, ok := document[path]
		if !ok {
			return false
		}
		if i == len(paths)-1 {
			return true
		}
		var nested map[string]json.RawMessage
		if json.Unmarshal(raw, &nested) != nil {
			return false
		}
		document = nested
	}
	return false
}

func webAPISettingsLegacySame(a, b any) bool {
	x, ex := json.Marshal(a)
	y, ey := json.Marshal(b)
	return ex == nil && ey == nil && bytes.Equal(x, y)
}

func webAPISettingsLegacyConflict(field string) error {
	return &WebAPIError{Status: 400, Code: "conflicting_legacy_settings", Message: "legacy and canonical settings disagree", Details: map[string]any{"field": field}}
}

func webAPISettingsLegacyNulls(raw []byte, path string) error {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		allowed := []string{"currentProfileId", "davProps", "dav", "themeProps.primaryColor", "themeProps.systemFontFamily", "appSettingProps.locale", "appSetting.locale", "appSettingProps.currentService", "appSetting.currentService", "windowProps.top", "windowProps.left", "windowProps.scale", "windowGeometry.top", "windowGeometry.left", "windowGeometry.scale", "patchClashConfig.global-ua", "patchClashConfig.mitm.trusted-fingerprint", "patchClashConfig.mitm.capture-entries", "hotKeyActions.key"}
		if webAPISettingsEnum(path, allowed...) {
			return nil
		}
		return webAPISettingsInvalid(path)
	}
	if path == "currentProfileId" || path == "themeProps.liquidGlass" {
		return nil
	}
	var object map[string]json.RawMessage
	if len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, &object) == nil {
		for name, value := range object {
			next := name
			if path != "" {
				next = path + "." + name
			}
			if err := webAPISettingsLegacyNulls(value, next); err != nil {
				return err
			}
		}
	}
	var items []json.RawMessage
	if len(raw) > 0 && raw[0] == '[' && json.Unmarshal(raw, &items) == nil {
		for _, value := range items {
			if err := webAPISettingsLegacyNulls(value, path); err != nil {
				return err
			}
		}
	}
	return nil
}

func webAPISettingsLegacyAppFields(root *webAPISettingsLegacyNative, document map[string]json.RawMessage, appKey string, plan *WebAPISettingsMigrationPlan) error {
	app := root.App
	if app.IsAnimateToPage != nil {
		value := "fade"
		if *app.IsAnimateToPage {
			value = "slide"
		}
		if webAPISettingsLegacyHas(document, appKey, "tabAnimation") && app.TabAnimation != value {
			return webAPISettingsLegacyConflict("appSettingProps.tabAnimation")
		}
		app.TabAnimation = value
		after := webAPISettingsMigrationString(value)
		plan.change("appSettingProps.isAnimateToPage", "appSettingProps.tabAnimation", webAPISettingsMigrationBool(*app.IsAnimateToPage), &after, true)
		plan.add(WebAPISettingsMigrationAlias, "appSettingProps.isAnimateToPage")
	}
	if app.ShowLabel != nil {
		if webAPISettingsLegacyHas(document, appKey, "sidebarExpanded") && app.SidebarExpanded != *app.ShowLabel {
			return webAPISettingsLegacyConflict("appSettingProps.sidebarExpanded")
		}
		app.SidebarExpanded = *app.ShowLabel
		after := webAPISettingsMigrationBool(app.SidebarExpanded)
		plan.change("appSettingProps.showLabel", "appSettingProps.sidebarExpanded", webAPISettingsMigrationBool(*app.ShowLabel), &after, true)
		plan.add(WebAPISettingsMigrationAlias, "appSettingProps.showLabel")
	}
	if app.RecoveryStrategy != nil {
		if !webAPISettingsEnum(*app.RecoveryStrategy, "compatible", "override") {
			return webAPISettingsInvalid("appSetting.recoveryStrategy")
		}
		if webAPISettingsLegacyHas(document, appKey, "restoreStrategy") && app.RestoreStrategy != *app.RecoveryStrategy {
			return webAPISettingsLegacyConflict("appSettingProps.restoreStrategy")
		}
		app.RestoreStrategy = *app.RecoveryStrategy
		plan.add(WebAPISettingsMigrationAlias, "appSetting.recoveryStrategy")
	}
	if app.TestURL != nil {
		if !webAPISettingsURL(*app.TestURL) {
			return webAPISettingsInvalid("appSettingProps.testUrl")
		}
		if webAPISettingsLegacyHas(document, "patchClashConfig", "test-url") && root.Patch.TestURL != *app.TestURL {
			return webAPISettingsLegacyConflict("patchClashConfig.test-url")
		}
		root.Patch.TestURL = *app.TestURL
		plan.add(WebAPISettingsMigrationAlias, "appSettingProps.testUrl")
	}
	if app.CustomUserAgent != nil {
		custom := strings.TrimSpace(*app.CustomUserAgent)
		agents := []string{"clash-verge/v2.4.2", "ClashforWindows/0.19.23"}
		if custom != "" && !webAPISettingsEnum(custom, agents...) {
			agents = append(agents, custom)
		}
		if webAPISettingsLegacyHas(document, appKey, "userAgents") && !reflect.DeepEqual(app.UserAgents, agents) {
			return webAPISettingsLegacyConflict("appSettingProps.userAgents")
		}
		app.UserAgents = agents
		after := webAPISettingsMigrationStrings(agents)
		plan.change("appSettingProps.customUserAgent", "appSettingProps.userAgents", webAPISettingsMigrationString(*app.CustomUserAgent), &after, false)
		plan.add(WebAPISettingsMigrationAlias, "appSettingProps.customUserAgent")
	}
	if app.DisclaimerAccepted != nil {
		if webAPISettingsLegacyHas(document, appKey, "acceptedDisclaimerVersion") && app.AcceptedDisclaimerVersion != 0 {
			return webAPISettingsLegacyConflict("appSettingProps.acceptedDisclaimerVersion")
		}
		app.AcceptedDisclaimerVersion = 0
		after := webAPISettingsMigrationInteger(0)
		plan.change("appSettingProps.disclaimerAccepted", "appSettingProps.acceptedDisclaimerVersion", webAPISettingsMigrationBool(*app.DisclaimerAccepted), &after, !*app.DisclaimerAccepted)
		plan.add(WebAPISettingsMigrationConsent, "appSettingProps.disclaimerAccepted")
	}
	if app.CrashlyticsTip != nil || app.Crashlytics != nil || app.CurrentService != nil || app.DisclaimerAccepted != nil {
		plan.RetiredPresentation = &WebAPISettingsLegacyPresentation{CrashlyticsTip: app.CrashlyticsTip, Crashlytics: app.Crashlytics, CurrentService: app.CurrentService, DisclaimerAccepted: app.DisclaimerAccepted}
		if app.CrashlyticsTip != nil {
			plan.add(WebAPISettingsMigrationRetired, "appSettingProps.crashlyticsTip")
			plan.change("appSettingProps.crashlyticsTip", "", webAPISettingsMigrationBool(*app.CrashlyticsTip), nil, false)
		}
		if app.Crashlytics != nil {
			plan.add(WebAPISettingsMigrationRetired, "appSettingProps.crashlytics")
			if !*app.Crashlytics {
				plan.change("appSettingProps.crashlytics", "", webAPISettingsMigrationBool(false), nil, false)
			}
		}
		if app.CurrentService != nil {
			plan.add(WebAPISettingsMigrationRetired, "appSettingProps.currentService")
		}
		if app.Crashlytics != nil && *app.Crashlytics {
			plan.routing("appSettingProps.crashlytics")
		}
		if app.CurrentService != nil && *app.CurrentService != "" {
			plan.routing("appSettingProps.currentService")
		}
	}
	if webAPISettingsLegacyHas(document, appKey, "currentService") && (app.CurrentService == nil || *app.CurrentService == "") {
		before := webAPISettingsMigrationNull()
		if app.CurrentService != nil {
			before = webAPISettingsMigrationString(*app.CurrentService)
		}
		plan.change("appSettingProps.currentService", "", before, nil, false)
		plan.add(WebAPISettingsMigrationRetired, "appSettingProps.currentService")
	}
	oldWidgets := append([]string{}, app.DashboardWidgets...)
	widgets, changed, err := webAPISettingsLegacyDashboard(app.DashboardWidgets)
	if err != nil {
		return err
	}
	app.DashboardWidgets = widgets
	if changed {
		plan.add(WebAPISettingsMigrationDashboard, "appSettingProps.dashboardWidgets")
		after := webAPISettingsMigrationStrings(widgets)
		plan.change("appSettingProps.dashboardWidgets", "appSettingProps.dashboardWidgets", webAPISettingsMigrationStrings(oldWidgets), &after, false)
	}
	return nil
}

func webAPISettingsLegacyDashboard(widgets []string) ([]string, bool, error) {
	aliases := map[string]string{"networkDetection": "intranetIp", "outboundModeV2": "outboundMode", "systemProxyButton": "networkTakeover", "tunButton": "networkTakeover", "connections": "connectionStats", "requests": "connectionStats", "dnsQueries": "connectionStats", "trafficUsage": "networkSpeed"}
	current := []string{"currentOutbound", "networkSpeed", "outboundMode", "vpnButton", "networkTakeover", "serviceStatus", "profiles", "proxyGroups", "connectionStats", "intranetIp", "memoryInfo"}
	retired := []string{"runTime", "overrideDnsButton", "overrideNtpButton"}
	legacyDefaults := []string{"networkSpeed", "systemProxyButton", "tunButton", "outboundMode", "networkDetection", "trafficUsage", "intranetIp"}
	seen := map[string]bool{}
	out := []string{}
	for _, name := range widgets {
		if !webAPISettingsEnum(name, current...) && aliases[name] == "" && !webAPISettingsEnum(name, retired...) {
			return nil, false, webAPISettingsInvalid("appSettingProps.dashboardWidgets")
		}
		if webAPISettingsEnum(name, retired...) {
			continue
		}
		value := name
		if alias := aliases[name]; alias != "" {
			value = alias
		}
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	old := append([]string{}, widgets...)
	for i, name := range old {
		if name == "outboundModeV2" {
			old[i] = "outboundMode"
		}
	}
	sort.Strings(old)
	sort.Strings(legacyDefaults)
	if reflect.DeepEqual(old, legacyDefaults) {
		out = webAPISettingsDefaultPresentation().AppSetting.DashboardWidgets
	} else if !seen["currentOutbound"] {
		out = append([]string{"currentOutbound"}, out...)
	}
	return out, !reflect.DeepEqual(widgets, out), nil
}

func webAPISettingsLegacyThemeFields(theme *webAPISettingsLegacyTheme, document map[string]json.RawMessage, plan *WebAPISettingsMigrationPlan) error {
	if theme.SchemeVariant != nil {
		if !webAPISettingsEnum(*theme.SchemeVariant, "tonalSpot", "fidelity", "monochrome", "neutral", "vibrant", "expressive", "content", "rainbow", "fruitSalad") {
			return webAPISettingsInvalid("themeProps.schemeVariant")
		}
		style := "vivid"
		if *theme.SchemeVariant == "monochrome" {
			style = "monochrome"
		}
		if *theme.SchemeVariant == "tonalSpot" || *theme.SchemeVariant == "neutral" {
			style = "soft"
		}
		if webAPISettingsLegacyHas(document, "themeProps", "colorStyle") && theme.ColorStyle != style {
			return webAPISettingsLegacyConflict("themeProps.colorStyle")
		}
		theme.ColorStyle = style
		plan.displayString("themeProps.schemeVariant", "themeProps.colorStyle", *theme.SchemeVariant, style, false)
		plan.add(WebAPISettingsMigrationEnum, "themeProps.schemeVariant")
		if plan.RetiredPresentation == nil {
			plan.RetiredPresentation = &WebAPISettingsLegacyPresentation{}
		}
		plan.RetiredPresentation.SchemeVariant = theme.SchemeVariant
	}
	if len(theme.Glass) > 0 {
		var flag bool
		if json.Unmarshal(theme.Glass, &flag) == nil {
			theme.LiquidGlass = "none"
			if flag {
				theme.LiquidGlass = "standard"
			}
			plan.add(WebAPISettingsMigrationEnum, "themeProps.liquidGlass")
			after := webAPISettingsMigrationString(theme.LiquidGlass)
			plan.change("themeProps.liquidGlass", "themeProps.liquidGlass", webAPISettingsMigrationBool(flag), &after, false)
		} else {
			var value string
			if json.Unmarshal(theme.Glass, &value) != nil || !webAPISettingsEnum(value, "none", "standard", "ultra") {
				return webAPISettingsInvalid("themeProps.liquidGlass")
			}
			theme.LiquidGlass = value
		}
	}
	return nil
}

func webAPISettingsLegacyStyle(style *WebAPISettingsProxiesStyle, plan *WebAPISettingsMigrationPlan) error {
	icon := map[string]string{"none": "hidden", "standard": "filled", "icon": "plain"}
	card := map[string]string{"expand": "loose", "shrink": "standard", "min": "compact"}
	if value := icon[style.IconStyle]; value != "" {
		before := style.IconStyle
		style.IconStyle = value
		plan.displayString("proxiesStyleProps.iconStyle", "proxiesStyleProps.iconStyle", before, value, false)
		plan.add(WebAPISettingsMigrationEnum, "proxiesStyleProps.iconStyle")
	}
	if value := card[style.CardType]; value != "" {
		before := style.CardType
		style.CardType = value
		plan.displayString("proxiesStyleProps.cardType", "proxiesStyleProps.cardType", before, value, false)
		plan.add(WebAPISettingsMigrationEnum, "proxiesStyleProps.cardType")
	}
	if !webAPISettingsEnum(style.Type, "list", "tab") || !webAPISettingsEnum(style.SortType, "none", "delay", "name") || !webAPISettingsEnum(style.Layout, "loose", "standard", "tight") || !webAPISettingsEnum(style.IconStyle, "filled", "plain", "hidden") || !webAPISettingsEnum(style.CardType, "loose", "standard", "compact") {
		return webAPISettingsInvalid("proxiesStyleProps")
	}
	return nil
}

func webAPISettingsLegacyNetworkFields(root *webAPISettingsLegacyNative, document map[string]json.RawMessage, plan *WebAPISettingsMigrationPlan) error {
	if mode := root.Network.RouteMode; mode != nil {
		if !webAPISettingsEnum(*mode, "config", "bypassPrivate") {
			return webAPISettingsInvalid("networkProps.routeMode")
		}
		bypass := *mode == "bypassPrivate"
		if webAPISettingsLegacyHas(document, "networkProps", "bypassPrivateRoute") && root.Network.BypassPrivateRoute != bypass {
			return webAPISettingsLegacyConflict("networkProps.bypassPrivateRoute")
		}
		root.Network.BypassPrivateRoute = bypass
		if bypass && !webAPISettingsLegacyHas(document, "networkProps", "bypassPrivateRoute") {
			plan.LegacyRouting = &WebAPISettingsLegacyRouting{RouteMode: mode, RouteAddress: append([]string{}, root.Patch.LegacyTun.RouteAddress...)}
			if len(root.Patch.LegacyTun.RouteAddress) > 0 {
				plan.routing("networkProps.routeMode.ignored-route-address")
			}
			root.Patch.LegacyTun.RouteAddress = []string{}
		}
		plan.add(WebAPISettingsMigrationAlias, "networkProps.routeMode")
	}
	var access *WebAPISettingsAccessControl
	if root.VPN.AccessControl != nil {
		access = root.VPN.AccessControl
		plan.add(WebAPISettingsMigrationAlias, "vpnProps.accessControl")
	}
	if root.LegacyAccessControl != nil {
		if root.LegacyIsAccessControl != nil {
			root.LegacyAccessControl.Enable = *root.LegacyIsAccessControl
		}
		if access != nil && !webAPISettingsLegacySame(access, root.LegacyAccessControl) {
			return webAPISettingsLegacyConflict("vpnProps.accessControlProps")
		}
		access = root.LegacyAccessControl
		plan.add(WebAPISettingsMigrationAlias, "accessControl")
	}
	if root.LegacyIsAccessControl != nil && root.LegacyAccessControl == nil {
		candidate := root.VPN.AccessControlProps
		if access != nil {
			candidate = *access
		}
		if candidate.Enable != *root.LegacyIsAccessControl {
			return webAPISettingsLegacyConflict("vpnProps.accessControlProps.enable")
		}
		plan.add(WebAPISettingsMigrationAlias, "isAccessControl")
	}
	if access != nil {
		if webAPISettingsLegacyHas(document, "vpnProps", "accessControlProps") && !webAPISettingsLegacySame(root.VPN.AccessControlProps, *access) {
			return webAPISettingsLegacyConflict("vpnProps.accessControlProps")
		}
		root.VPN.AccessControlProps = *access
	}
	return nil
}

var webAPISettingsLegacyDNSKeys = []string{"enable", "listen", "use-hosts", "use-system-hosts", "ipv6", "respect-rules", "prefer-h3", "enhanced-mode", "fake-ip-range", "fake-ip-filter", "default-nameserver", "nameserver-policy", "nameserver", "fallback", "proxy-server-nameserver", "fallback-filter.geoip", "fallback-filter.geoip-code", "fallback-filter.geosite", "fallback-filter.ipcidr", "fallback-filter.domain"}
var webAPISettingsNormalDNSKeys = []string{"listen", "ipv6", "prefer-h3", "enhanced-mode", "fake-ip-filter", "fake-ip-filter-mode", "default-nameserver", "nameserver", "fallback", "proxy-server-nameserver", "direct-nameserver"}

func webAPISettingsLegacyClashFields(root *webAPISettingsLegacyNative, document map[string]json.RawMessage, plan *WebAPISettingsMigrationPlan) error {
	clash, tun, geo := root.Patch, root.Patch.LegacyTun, root.Patch.LegacyGeo
	clash.Tun = tun.WebAPISettingsTun
	if !webAPISettingsLegacyHas(document, "patchClashConfig", "interface-name-mode") && root.App.IsAnimateToPage != nil && root.App.ShowLabel != nil {
		clash.InterfaceNameMode = "follow"
		plan.add(WebAPISettingsMigrationOverride, "patchClashConfig.interface-name-mode")
	}
	if geo.LegacyGEOIP != nil {
		if webAPISettingsLegacyHas(document, "patchClashConfig", "geox-url", "geoip") && geo.GEOIP != *geo.LegacyGEOIP {
			return webAPISettingsLegacyConflict("patchClashConfig.geox-url.geoip")
		}
		geo.GEOIP = *geo.LegacyGEOIP
		plan.add(WebAPISettingsMigrationAlias, "patchClashConfig.geox-url.geo-ip")
	}
	if geo.LegacyGEOSITE != nil {
		if webAPISettingsLegacyHas(document, "patchClashConfig", "geox-url", "geosite") && geo.GEOSITE != *geo.LegacyGEOSITE {
			return webAPISettingsLegacyConflict("patchClashConfig.geox-url.geosite")
		}
		geo.GEOSITE = *geo.LegacyGEOSITE
		plan.add(WebAPISettingsMigrationAlias, "patchClashConfig.geox-url.geo-site")
	}
	clash.GeoXURL = geo.WebAPISettingsGeoXURL
	if !webAPISettingsLegacyHas(document, "patchClashConfig", "general-override-keys") {
		clash.GeneralOverrideKeys = []string{"unified-delay", "tcp-concurrent", "ipv6", "geodata-loader"}
		if clash.TestURL != "https://www.gstatic.com/generate_204" {
			clash.GeneralOverrideKeys = append(clash.GeneralOverrideKeys, "test-url")
		}
		plan.add(WebAPISettingsMigrationOverride, "patchClashConfig.general-override-keys")
	}
	if !webAPISettingsKeys(clash.DNSOverrideKeys, webAPISettingsLegacyAllDNSKeys()...) {
		return webAPISettingsInvalid("patchClashConfig.dns-override-keys")
	}
	if !webAPISettingsLegacyHas(document, "patchClashConfig", "dns-override-keys") && webAPISettingsLegacyHas(document, "patchClashConfig", "dns") {
		clash.DNSOverrideKeys = append([]string{}, webAPISettingsLegacyDNSKeys...)
	}
	originalDNSKeys := append([]string{}, clash.DNSOverrideKeys...)
	if root.OverrideDNS != nil {
		if *root.OverrideDNS {
			for _, key := range clash.DNSOverrideKeys {
				if !webAPISettingsEnum(key, webAPISettingsNormalDNSKeys...) {
					plan.routing("patchClashConfig.dns-override-keys")
					break
				}
			}
		} else {
			if webAPISettingsLegacyHas(document, "patchClashConfig", "dns-override-keys") && len(originalDNSKeys) > 0 {
				plan.routing("overrideDns.disabled-selection")
			}
			clash.DNSOverrideKeys = []string{}
		}
		plan.add(WebAPISettingsMigrationOverride, "overrideDns")
	}
	if tun.AutoRoute != nil {
		plan.add(WebAPISettingsMigrationRetired, "patchClashConfig.tun.auto-route")
		if !*tun.AutoRoute {
			plan.routing("patchClashConfig.tun.auto-route")
		} else {
			after := webAPISettingsMigrationBool(true)
			plan.change("patchClashConfig.tun.auto-route", "current-runtime-policy.tun.auto-route", webAPISettingsMigrationBool(true), &after, true)
		}
	}
	if tun.Enable && (tun.AutoRoute != nil || tun.StrictRoute != nil) {
		if webAPISettingsLegacyHas(document, "patchClashConfig", "tun", "route-address") && len(tun.RouteAddress) == 0 {
			plan.routing("patchClashConfig.tun.route-address")
		}
		if webAPISettingsLegacyHas(document, "patchClashConfig", "tun", "route-exclude-address") && len(tun.RouteExcludeAddress) == 0 {
			plan.routing("patchClashConfig.tun.route-exclude-address")
		}
	}
	if tun.DNSHijack != nil {
		if len(*tun.DNSHijack) == 0 {
			plan.routing("patchClashConfig.tun.dns-hijack")
		}
		if webAPISettingsLegacyHas(document, "patchClashConfig", "tun-override", "dns-hijack") && !reflect.DeepEqual(clash.TunOverride.DNSHijack, *tun.DNSHijack) {
			return webAPISettingsLegacyConflict("patchClashConfig.tun-override.dns-hijack")
		}
		clash.TunOverride.DNSHijack = append([]string{}, (*tun.DNSHijack)...)
		clash.TunOverrideKeys = webAPISettingsLegacyPick(clash.TunOverrideKeys, "dns-hijack")
		plan.add(WebAPISettingsMigrationOverride, "patchClashConfig.tun.dns-hijack")
	}
	if !webAPISettingsLegacyHas(document, "patchClashConfig", "tun-override-keys") {
		if webAPISettingsLegacyHas(document, "patchClashConfig", "tun", "mtu") {
			clash.TunOverride.MTU = tun.MTU
			clash.TunOverrideKeys = webAPISettingsLegacyPick(clash.TunOverrideKeys, "mtu")
		}
		if webAPISettingsLegacyHas(document, "patchClashConfig", "tun", "congestion-controller") {
			clash.TunOverride.CongestionController = tun.CongestionController
			clash.TunOverrideKeys = webAPISettingsLegacyPick(clash.TunOverrideKeys, "congestion-controller")
		}
		if tun.StrictRoute != nil {
			clash.TunOverride.StrictRoute = *tun.StrictRoute
			clash.TunOverrideKeys = webAPISettingsLegacyPick(clash.TunOverrideKeys, "strict-route")
		}
		if len(tun.RouteAddress) > 0 {
			clash.TunOverride.RouteAddress = append([]string{}, tun.RouteAddress...)
			clash.TunOverrideKeys = webAPISettingsLegacyPick(clash.TunOverrideKeys, "route-address")
		}
		if len(tun.RouteExcludeAddress) > 0 {
			clash.TunOverride.RouteExcludeAddress = append([]string{}, tun.RouteExcludeAddress...)
			clash.TunOverrideKeys = webAPISettingsLegacyPick(clash.TunOverrideKeys, "route-exclude-address")
		}
	}
	if root.OverrideNTP != nil || clash.NTP != nil || len(clash.NTPOverrideKeys) > 0 {
		if !webAPISettingsKeys(clash.NTPOverrideKeys, "enable", "server", "port", "interval", "dialer-proxy", "write-to-system") {
			return webAPISettingsInvalid("patchClashConfig.ntp-override-keys")
		}
		defaultNTP := WebAPINTP{Server: "time.apple.com", Port: 123, Interval: 30}
		if root.OverrideNTP != nil && *root.OverrideNTP && len(clash.NTPOverrideKeys) > 0 || clash.NTP != nil && !reflect.DeepEqual(*clash.NTP, defaultNTP) || len(clash.NTPOverrideKeys) > 0 {
			plan.routing("patchClashConfig.ntp")
		}
		if !plan.RequiresProjection {
			if root.OverrideNTP != nil {
				plan.change("overrideNtp", "", webAPISettingsMigrationBool(*root.OverrideNTP), nil, false)
			}
			if clash.NTP != nil {
				plan.change("patchClashConfig.ntp", "", webAPISettingsMigrationNTP(*clash.NTP), nil, false)
			}
			if webAPISettingsLegacyHas(document, "patchClashConfig", "ntp-override-keys") {
				plan.change("patchClashConfig.ntp-override-keys", "", webAPISettingsMigrationStrings(clash.NTPOverrideKeys), nil, false)
			}
		}
		plan.add(WebAPISettingsMigrationRetired, "overrideNtp")
	}
	if tun.AutoRoute != nil || tun.DNSHijack != nil || root.OverrideDNS != nil || root.OverrideNTP != nil || clash.NTP != nil {
		var savedMode *string
		savedRoutes := tun.RouteAddress
		if plan.LegacyRouting != nil {
			savedMode = plan.LegacyRouting.RouteMode
			savedRoutes = plan.LegacyRouting.RouteAddress
		}
		plan.LegacyRouting = &WebAPISettingsLegacyRouting{RouteMode: savedMode, AutoRoute: tun.AutoRoute, DNSHijack: tun.DNSHijack, RouteAddress: savedRoutes, OverrideDNS: root.OverrideDNS, DNS: &clash.DNS, DNSOverrideKeys: originalDNSKeys, OverrideNTP: root.OverrideNTP, NTP: clash.NTP, NTPOverrideKeys: clash.NTPOverrideKeys}
	}
	return nil
}

func webAPISettingsLegacyPick(keys []string, key string) []string {
	if !webAPISettingsEnum(key, keys...) {
		return append(keys, key)
	}
	return keys
}

func webAPISettingsLegacyIdentity(raw json.RawMessage, settings *WebAPISettings, plan *WebAPISettingsMigrationPlan) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	if plan.SourceConfigVersion == 0 {
		var original string
		if json.Unmarshal(raw, &original) != nil || original == "" || len(original) > 256 {
			return webAPISettingsInvalid("currentProfileId")
		}
		plan.LegacyCurrentProfileID = &original
		plan.RequiresProjection = true
		plan.add(WebAPISettingsMigrationIdentity, "currentProfileId")
		return nil
	}
	value := string(bytes.TrimSpace(raw))
	if _, err := webAPIInteger(value, true); err != nil {
		return webAPISettingsInvalid("currentProfileId")
	}
	id := WebAPIID(value)
	settings.CurrentProfileID = &id
	return nil
}

func webAPISettingsLegacyPresentationValues(value *WebAPISettingsPresentation) error {
	app, theme := value.AppSetting, value.ThemeProps
	if !webAPISettingsEnum(app.TabAnimation, "slide", "fade") || !webAPISettingsEnum(app.EditorFontSize, "standard", "large", "extraLarge") || app.AcceptedDisclaimerVersion < 0 {
		return webAPISettingsInvalid("appSettingProps")
	}
	if !webAPISettingsEnum(theme.ThemeMode, "system", "light", "dark") || !webAPISettingsEnum(theme.ColorStyle, "vivid", "tinted", "soft", "monochrome") || !webAPISettingsEnum(theme.LiquidGlass, "none", "standard", "ultra") || theme.TextScale.Scale <= 0 || theme.TextScale.Scale > 10 {
		return webAPISettingsInvalid("themeProps")
	}
	for _, key := range value.HotKeyActions {
		if !webAPISettingsEnum(key.Action, "start", "view", "mode", "proxy", "tun", "ruleMode", "globalMode", "directMode", "delayTest", "updateProfiles", "copyEnv", "exit") || !webAPISettingsKeys(key.Modifiers, "alt", "capsLock", "control", "fn", "meta", "shift") {
			return webAPISettingsInvalid("hotKeyActions")
		}
	}
	return nil
}

func webAPISettingsLegacyAllDNSKeys() []string {
	t := reflect.TypeFor[WebAPIDNS]()
	keys := []string{}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if field.Type.Kind() != reflect.Struct {
			keys = append(keys, name)
			continue
		}
		for j := 0; j < field.Type.NumField(); j++ {
			keys = append(keys, name+"."+strings.Split(field.Type.Field(j).Tag.Get("json"), ",")[0])
		}
	}
	return keys
}
