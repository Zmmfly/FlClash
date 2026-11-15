//go:build webpanel && !android

package core

import "encoding/json"

type WebAPISettingsMigrationKind string

const WebAPISettingsMigrationResources WebAPISettingsMigrationKind = "resource_projection_required"

const (
	WebAPISettingsMigrationAlias     WebAPISettingsMigrationKind = "known_field_alias"
	WebAPISettingsMigrationEnum      WebAPISettingsMigrationKind = "known_enum_alias"
	WebAPISettingsMigrationDashboard WebAPISettingsMigrationKind = "known_dashboard_layout"
	WebAPISettingsMigrationOverride  WebAPISettingsMigrationKind = "explicit_override_migration"
	WebAPISettingsMigrationRetired   WebAPISettingsMigrationKind = "retired_preference"
	WebAPISettingsMigrationConsent   WebAPISettingsMigrationKind = "consent_reconfirmation"
	WebAPISettingsMigrationRouting   WebAPISettingsMigrationKind = "routing_projection_required"
	WebAPISettingsMigrationIdentity  WebAPISettingsMigrationKind = "legacy_identity_mapping_required"
)

type WebAPISettingsMigrationStep struct {
	Kind  WebAPISettingsMigrationKind `json:"kind"`
	Field string                      `json:"field"`
}

type WebAPISettingsMigrationPlan struct {
	SourceSchema           int                               `json:"sourceSchema"`
	SourceConfigVersion    int32                             `json:"sourceConfigVersion"`
	Steps                  []WebAPISettingsMigrationStep     `json:"steps"`
	RequiresProjection     bool                              `json:"requiresProjection"`
	RequiresConfirmation   bool                              `json:"requiresConfirmation"`
	Changes                []WebAPISettingsMigrationChange   `json:"changes"`
	LegacyCurrentProfileID *string                           `json:"legacyCurrentProfileId,omitempty"`
	LegacyRouting          *WebAPISettingsLegacyRouting      `json:"legacyRouting,omitempty"`
	RetiredPresentation    *WebAPISettingsLegacyPresentation `json:"retiredPresentation,omitempty"`
}

type WebAPISettingsLegacyPresentation struct {
	CrashlyticsTip     *bool   `json:"crashlyticsTip,omitempty"`
	Crashlytics        *bool   `json:"crashlytics,omitempty"`
	CurrentService     *string `json:"currentService,omitempty"`
	SchemeVariant      *string `json:"schemeVariant,omitempty"`
	DisclaimerAccepted *bool   `json:"disclaimerAccepted,omitempty"`
}

type WebAPISettingsLegacyRouting struct {
	RouteMode       *string    `json:"routeMode,omitempty"`
	AutoRoute       *bool      `json:"auto-route,omitempty"`
	DNSHijack       *[]string  `json:"dns-hijack,omitempty"`
	RouteAddress    []string   `json:"route-address"`
	OverrideDNS     *bool      `json:"overrideDns,omitempty"`
	DNS             *WebAPIDNS `json:"dns,omitempty"`
	DNSOverrideKeys []string   `json:"dns-override-keys"`
	OverrideNTP     *bool      `json:"overrideNtp,omitempty"`
	NTP             *WebAPINTP `json:"ntp,omitempty"`
	NTPOverrideKeys []string   `json:"ntp-override-keys"`
}

type WebAPISettingsLegacyNativeBase webAPISettingsNativeConfig

type webAPISettingsLegacyNative struct {
	WebAPISettingsLegacyNativeBase
	CurrentProfile        json.RawMessage              `json:"currentProfileId"`
	App                   *webAPISettingsLegacyApp     `json:"appSettingProps"`
	LegacyApp             *webAPISettingsLegacyApp     `json:"appSetting"`
	Theme                 *webAPISettingsLegacyTheme   `json:"themeProps"`
	Network               *webAPISettingsLegacyNetwork `json:"networkProps"`
	VPN                   *webAPISettingsLegacyVPN     `json:"vpnProps"`
	Patch                 *webAPISettingsLegacyClash   `json:"patchClashConfig"`
	LegacyWindow          *WebAPISettingsWindow        `json:"windowProps"`
	LegacyStyle           *WebAPISettingsProxiesStyle  `json:"proxiesStyle"`
	LegacyDAV             *WebAPISettingsDAV           `json:"dav"`
	OverrideDNS           *bool                        `json:"overrideDns"`
	OverrideNTP           *bool                        `json:"overrideNtp"`
	LegacyAccessControl   *WebAPISettingsAccessControl `json:"accessControl"`
	LegacyIsAccessControl *bool                        `json:"isAccessControl"`
}

type webAPISettingsLegacyApp struct {
	WebAPISettingsApp
	WebAPISettingsAppPresentation
	TestURL            *string `json:"testUrl"`
	IsAnimateToPage    *bool   `json:"isAnimateToPage"`
	ShowLabel          *bool   `json:"showLabel"`
	DisclaimerAccepted *bool   `json:"disclaimerAccepted"`
	CrashlyticsTip     *bool   `json:"crashlyticsTip"`
	Crashlytics        *bool   `json:"crashlytics"`
	CustomUserAgent    *string `json:"customUserAgent"`
	CurrentService     *string `json:"currentService"`
	RecoveryStrategy   *string `json:"recoveryStrategy"`
}

type webAPISettingsLegacyTheme struct {
	WebAPISettingsTheme
	SchemeVariant *string         `json:"schemeVariant"`
	Glass         json.RawMessage `json:"liquidGlass"`
}

type webAPISettingsLegacyNetwork struct {
	WebAPISettingsNetworkProps
	RouteMode *string `json:"routeMode"`
}

type webAPISettingsLegacyVPN struct {
	WebAPISettingsVPN
	AccessControl *WebAPISettingsAccessControl `json:"accessControl"`
}

type webAPISettingsLegacyClash struct {
	WebAPISettingsClash
	LegacyTun       *webAPISettingsLegacyTun `json:"tun"`
	LegacyGeo       *webAPISettingsLegacyGeo `json:"geox-url"`
	NTP             *WebAPINTP               `json:"ntp"`
	NTPOverrideKeys []string                 `json:"ntp-override-keys"`
}

type webAPISettingsLegacyTun struct {
	WebAPISettingsTun
	AutoRoute   *bool     `json:"auto-route"`
	DNSHijack   *[]string `json:"dns-hijack"`
	StrictRoute *bool     `json:"strict-route"`
}

type webAPISettingsLegacyGeo struct {
	WebAPISettingsGeoXURL
	LegacyGEOIP   *string `json:"geo-ip"`
	LegacyGEOSITE *string `json:"geo-site"`
}
