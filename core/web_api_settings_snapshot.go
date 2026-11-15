//go:build webpanel && !android

package core

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

type WebAPISettingsSnapshotOptions struct{ IncludeDAVCredentials bool }
type WebAPISettingsRestoreOptions struct{ RestoreDAVCredentials bool }

func SnapshotWebAPISettings(settings WebAPISettings) WebAPISettingsSnapshot {
	return SnapshotWebAPISettingsWithOptions(settings, WebAPISettingsSnapshotOptions{})
}

func SnapshotWebAPISettingsWithOptions(settings WebAPISettings, options WebAPISettingsSnapshotOptions) WebAPISettingsSnapshot {
	snapshot := WebAPISettingsSnapshot{Version: 1, Runtime: settings.Runtime, Network: settings.Network, CurrentProfileID: settings.CurrentProfileID, Presentation: settings.NativePresentation}
	if options.IncludeDAVCredentials {
		snapshot.DAV = settings.DAV
	}
	return snapshot
}

func RestoreWebAPISettings(current WebAPISettings, snapshot WebAPISettingsSnapshot) (WebAPISettings, error) {
	return RestoreWebAPISettingsWithOptions(current, snapshot, WebAPISettingsRestoreOptions{})
}

func RestoreWebAPISettingsWithOptions(current WebAPISettings, snapshot WebAPISettingsSnapshot, options WebAPISettingsRestoreOptions) (WebAPISettings, error) {
	if snapshot.Version != 1 {
		return current, webAPIError(400, "unsupported_settings_snapshot", "settings snapshot version is not supported")
	}
	current.Runtime = snapshot.Runtime
	current.Network = snapshot.Network
	current.CurrentProfileID = snapshot.CurrentProfileID
	current.NativePresentation = snapshot.Presentation
	if options.RestoreDAVCredentials && snapshot.DAV != nil {
		current.DAV = snapshot.DAV
	}
	if err := ValidateWebAPISettings(current); err != nil {
		return current, err
	}
	return current, nil
}

type webAPISettingsNativeApp struct {
	WebAPISettingsApp
	WebAPISettingsAppPresentation
}

type webAPISettingsNativeConfig struct {
	Version           *int32                     `json:"version,omitempty"`
	CurrentProfileID  *json.Number               `json:"currentProfileId"`
	HotKeyActions     []WebAPISettingsHotKey     `json:"hotKeyActions"`
	AppSettingProps   webAPISettingsNativeApp    `json:"appSettingProps"`
	DAVProps          *WebAPISettingsDAV         `json:"davProps,omitempty"`
	NetworkProps      WebAPISettingsNetworkProps `json:"networkProps"`
	VPNProps          WebAPISettingsVPN          `json:"vpnProps"`
	ThemeProps        WebAPISettingsTheme        `json:"themeProps"`
	ProxiesStyleProps WebAPISettingsProxiesStyle `json:"proxiesStyleProps"`
	WindowGeometry    WebAPISettingsWindow       `json:"windowGeometry"`
	PatchClashConfig  WebAPISettingsClash        `json:"patchClashConfig"`
	ExcludeSSIDs      []string                   `json:"excludeSSIDs"`
}

func webAPISettingsDefaultPresentation() WebAPISettingsPresentation {
	color := uint32(0xff007aff)
	return WebAPISettingsPresentation{
		AppSetting:    WebAPISettingsAppPresentation{DashboardWidgets: []string{"currentOutbound", "networkSpeed", "outboundMode", "vpnButton", "networkTakeover", "intranetIp", "serviceStatus", "profiles", "proxyGroups", "connectionStats"}, TabAnimation: "fade", FloatingNavigationBar: true, SidebarExpanded: true, MinimizeOnExit: true, ShowTrayTitle: true, MonochromeTrayIcon: true, Filters: []WebAPISettingsFilter{}, EditorFontSize: "standard", ServiceOrder: []string{}, DisabledServices: []string{}, TableColumnWidths: map[string]map[string]float64{}, RecordGroupings: map[string]string{}},
		ThemeProps:    WebAPISettingsTheme{PrimaryColor: &color, PrimaryColors: []uint32{}, ThemeMode: "system", ColorStyle: "vivid", LiquidGlass: "none", TextScale: WebAPISettingsTextScale{Scale: 1}},
		HotKeyActions: []WebAPISettingsHotKey{}, ProxiesStyleProps: WebAPISettingsProxiesStyle{Type: "list", SortType: "none", Layout: "standard", IconStyle: "filled", CardType: "standard"},
	}
}

func webAPISettingsNative(snapshot WebAPISettingsSnapshot) (webAPISettingsNativeConfig, error) {
	settings, err := RestoreWebAPISettings(DefaultWebAPISettings(), snapshot)
	if err != nil {
		return webAPISettingsNativeConfig{}, err
	}
	presentation := webAPISettingsDefaultPresentation()
	if snapshot.Presentation != nil {
		presentation = *snapshot.Presentation
	}
	var profile *json.Number
	if snapshot.CurrentProfileID != nil {
		v := json.Number(*snapshot.CurrentProfileID)
		profile = &v
	}
	version := int32(1)
	return webAPISettingsNativeConfig{Version: &version, CurrentProfileID: profile, HotKeyActions: presentation.HotKeyActions,
		AppSettingProps: webAPISettingsNativeApp{WebAPISettingsApp: settings.Runtime.AppSetting, WebAPISettingsAppPresentation: presentation.AppSetting},
		NetworkProps:    settings.Network.NetworkProps, VPNProps: settings.Network.VPNProps, ThemeProps: presentation.ThemeProps, ProxiesStyleProps: presentation.ProxiesStyleProps, WindowGeometry: presentation.WindowGeometry, PatchClashConfig: settings.Runtime.PatchClashConfig, ExcludeSSIDs: settings.Network.ExcludeSSIDs}, nil
}

func NativeWebAPISettingsSnapshot(configJSON []byte) (WebAPISettingsSnapshot, error) {
	if len(configJSON) > webAPIMaxJSONBytes {
		return WebAPISettingsSnapshot{}, webAPIError(413, "settings_too_large", "native settings exceed the JSON limit")
	}
	native, err := webAPISettingsNative(SnapshotWebAPISettings(DefaultWebAPISettings()))
	if err != nil {
		return WebAPISettingsSnapshot{}, err
	}
	if err = webAPIStrictJSON(configJSON, &native); err != nil {
		return WebAPISettingsSnapshot{}, webAPIError(400, "invalid_native_settings", "native settings have an invalid schema")
	}
	if native.Version != nil && *native.Version != 1 {
		return WebAPISettingsSnapshot{}, webAPIError(400, "unsupported_native_settings", "native settings version is not supported")
	}
	var profile *WebAPIID
	if native.CurrentProfileID != nil {
		if _, err = webAPIInteger(native.CurrentProfileID.String(), true); err != nil {
			return WebAPISettingsSnapshot{}, webAPISettingsInvalid("currentProfileId")
		}
		v := WebAPIID(native.CurrentProfileID.String())
		profile = &v
	}
	settings := WebAPISettings{Runtime: WebAPISettingsRuntime{PatchClashConfig: native.PatchClashConfig, AppSetting: native.AppSettingProps.WebAPISettingsApp}, Network: WebAPISettingsNetwork{NetworkProps: native.NetworkProps, VPNProps: native.VPNProps, ExcludeSSIDs: native.ExcludeSSIDs}, CurrentProfileID: profile,
		NativePresentation: &WebAPISettingsPresentation{AppSetting: native.AppSettingProps.WebAPISettingsAppPresentation, ThemeProps: native.ThemeProps, WindowGeometry: native.WindowGeometry, HotKeyActions: native.HotKeyActions, ProxiesStyleProps: native.ProxiesStyleProps}}
	if err = ValidateWebAPISettings(settings); err != nil {
		return WebAPISettingsSnapshot{}, err
	}
	snapshot := SnapshotWebAPISettings(settings)
	if native.DAVProps != nil {
		dav := *native.DAVProps
		dav.Password, err = webAPISettingsDecodeDAVPassword(dav.Password)
		if err != nil {
			return WebAPISettingsSnapshot{}, err
		}
		snapshot.DAV = &dav
	}
	return snapshot, nil
}

func NativeWebAPISettingsConfig(snapshot WebAPISettingsSnapshot) ([]byte, error) {
	return NativeWebAPISettingsConfigWithOptions(snapshot, WebAPISettingsSnapshotOptions{})
}

func NativeWebAPISettingsConfigWithOptions(snapshot WebAPISettingsSnapshot, options WebAPISettingsSnapshotOptions) ([]byte, error) {
	native, err := webAPISettingsNative(snapshot)
	if err != nil {
		return nil, err
	}
	if options.IncludeDAVCredentials && snapshot.DAV != nil {
		dav := *snapshot.DAV
		dav.Password, err = webAPISettingsEncodeDAVPassword(dav.Password)
		if err != nil {
			return nil, err
		}
		native.DAVProps = &dav
	}
	b, err := json.Marshal(native)
	if err != nil {
		return nil, errors.New("native settings cannot be encoded")
	}
	return b, nil
}

var webAPISettingsDAVMask = []byte{0x9d, 0x42, 0xe7, 0x1b, 0x68, 0xb4, 0x35, 0xca, 0x7f, 0x20, 0xd1, 0x56, 0x83, 0xfa, 0x0c, 0xa9}

func webAPISettingsDecodeDAVPassword(value string) (string, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		return value, nil
	}
	nonce, err := base64.URLEncoding.DecodeString(parts[1])
	if err != nil || len(nonce) != 16 {
		return "", webAPISettingsInvalid("dav.password")
	}
	body, err := base64.URLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", webAPISettingsInvalid("dav.password")
	}
	for i := range body {
		body[i] ^= nonce[i%len(nonce)] ^ webAPISettingsDAVMask[i%len(webAPISettingsDAVMask)]
	}
	if !utf8.Valid(body) {
		return "", webAPISettingsInvalid("dav.password")
	}
	return string(body), nil
}

func webAPISettingsEncodeDAVPassword(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", errors.New("DAV password encoding failed")
	}
	body := []byte(value)
	for i := range body {
		body[i] ^= nonce[i%len(nonce)] ^ webAPISettingsDAVMask[i%len(webAPISettingsDAVMask)]
	}
	return "v1." + base64.URLEncoding.EncodeToString(nonce) + "." + base64.URLEncoding.EncodeToString(body), nil
}
