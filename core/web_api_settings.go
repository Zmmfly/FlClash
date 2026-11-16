//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"reflect"
	"strings"
	"unicode/utf8"
)

func (c *WebAPISettingsRuntimeCommand) UnmarshalJSON(b []byte) error {
	type plain WebAPISettingsRuntimeCommand
	var v plain
	if err := webAPISettingsDecodeCommand(b, &v, "patch"); err != nil {
		return err
	}
	*c = WebAPISettingsRuntimeCommand(v)
	return nil
}

func (c *WebAPISettingsNetworkCommand) UnmarshalJSON(b []byte) error {
	type plain WebAPISettingsNetworkCommand
	var v plain
	if err := webAPISettingsDecodeCommand(b, &v, "patch"); err != nil {
		return err
	}
	*c = WebAPISettingsNetworkCommand(v)
	return nil
}

func (c *WebAPISettingsCurrentProfileCommand) UnmarshalJSON(b []byte) error {
	type plain WebAPISettingsCurrentProfileCommand
	var v plain
	if err := webAPISettingsDecodeCommand(b, &v, "currentProfileId"); err != nil {
		return err
	}
	*c = WebAPISettingsCurrentProfileCommand(v)
	return nil
}

func (c *WebAPISettingsDAVCommand) UnmarshalJSON(b []byte) error {
	type plain WebAPISettingsDAVCommand
	var v plain
	if err := webAPISettingsDecodeCommand(b, &v, "dav"); err != nil {
		return err
	}
	*c = WebAPISettingsDAVCommand(v)
	return nil
}

func (c *WebAPISettingsSelectedMapCommand) UnmarshalJSON(b []byte) error {
	type plain WebAPISettingsSelectedMapCommand
	var v plain
	if err := webAPISettingsDecodeCommand(b, &v, "profileId", "groupName", "proxyName"); err != nil {
		return err
	}
	*c = WebAPISettingsSelectedMapCommand(v)
	return nil
}

func webAPISettingsDecodeCommand(b []byte, out any, required ...string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(b, &object); err != nil || object == nil {
		return errors.New("expected settings command")
	}
	for _, name := range required {
		if _, ok := object[name]; !ok {
			return errors.New("required settings field is absent")
		}
	}
	if err := webAPISettingsNullFields(b, reflect.TypeOf(out).Elem(), false); err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func webAPISettingsNullFields(raw []byte, t reflect.Type, nullable bool) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if nullable {
			return nil
		}
		return errors.New("settings patch fields cannot be null")
	}
	if t.Kind() == reflect.Pointer {
		return webAPISettingsNullFields(raw, t.Elem(), false)
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return errors.New("expected settings object")
	}
	if t.NumField() == 1 && t.Field(0).Name == "Value" {
		if _, ok := object["value"]; !ok {
			return errors.New("nullable settings value must be explicit")
		}
	}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.Anonymous {
			if err := webAPISettingsNullFields(raw, field.Type, false); err != nil {
				return err
			}
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if value, ok := object[name]; ok {
			if err := webAPISettingsNullFields(value, field.Type, field.Tag.Get("settings") == "nullable"); err != nil {
				return err
			}
		}
	}
	return nil
}

func webAPISettingsPatchPresent(patch any) bool {
	v := reflect.ValueOf(patch)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return false
		}
		v = v.Elem()
	}
	if v.NumField() == 1 && v.Type().Field(0).Name == "Value" {
		return true
	}
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		if field.IsNil() {
			continue
		}
		if field.Elem().Kind() != reflect.Struct || webAPISettingsPatchPresent(field.Interface()) {
			return true
		}
	}
	return false
}

func webAPIApplySettingsPatch(destination, patch any) {
	webAPIMergeSettingsFields(reflect.ValueOf(destination).Elem(), reflect.ValueOf(patch))
}

func webAPIMergeSettingsFields(destination, patch reflect.Value) {
	if patch.Kind() == reflect.Pointer {
		patch = patch.Elem()
	}
	for i := 0; i < patch.NumField(); i++ {
		value := patch.Field(i)
		if value.IsNil() {
			continue
		}
		field := destination.FieldByName(patch.Type().Field(i).Name)
		value = value.Elem()
		if field.Kind() == reflect.Pointer {
			field.Set(value.FieldByName("Value"))
		} else if field.Kind() == reflect.Struct {
			webAPIMergeSettingsFields(field, value)
		} else {
			field.Set(value)
		}
	}
}

func webAPISettingsInvalid(path string) error {
	return &WebAPIError{Status: 400, Code: "invalid_settings", Message: "setting has an invalid value", Details: map[string]any{"field": path}}
}

func webAPISettingsEnum(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

func webAPISettingsKeys(keys []string, choices ...string) bool {
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if seen[key] || !webAPISettingsEnum(key, choices...) {
			return false
		}
		seen[key] = true
	}
	return true
}

func webAPISettingsURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" && parsed.User == nil && parsed.Fragment == "" && !strings.ContainsAny(value, "\x00\r\n")
}

func webAPISettingsDAVURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" && !strings.ContainsAny(value, "\x00\r\n")
}

func webAPISettingsBounded(v reflect.Value, depth int) bool {
	if depth > 32 {
		return false
	}
	if v.Kind() == reflect.Pointer {
		return v.IsNil() || webAPISettingsBounded(v.Elem(), depth+1)
	}
	switch v.Kind() {
	case reflect.String:
		return len(v.String()) <= 8192 && utf8.ValidString(v.String()) && !strings.ContainsRune(v.String(), '\x00')
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if !webAPISettingsBounded(v.Field(i), depth+1) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Len() > 10000 {
			return false
		}
		for i := 0; i < v.Len(); i++ {
			if !webAPISettingsBounded(v.Index(i), depth+1) {
				return false
			}
		}
	case reflect.Map:
		if v.Len() > 10000 {
			return false
		}
		iter := v.MapRange()
		for iter.Next() {
			if !webAPISettingsBounded(iter.Key(), depth+1) || !webAPISettingsBounded(iter.Value(), depth+1) {
				return false
			}
		}
	}
	return true
}

func ValidateWebAPISettings(settings WebAPISettings) error {
	if !webAPISettingsBounded(reflect.ValueOf(settings), 0) {
		return webAPISettingsInvalid("settings")
	}
	clash, app, network := settings.Runtime.PatchClashConfig, settings.Runtime.AppSetting, settings.Network
	if !webAPISettingsEnum(clash.Mode, "rule", "global", "direct") {
		return webAPISettingsInvalid("runtime.patchClashConfig.mode")
	}
	if !webAPISettingsEnum(clash.LogLevel, "debug", "info", "warning", "error", "silent") {
		return webAPISettingsInvalid("runtime.patchClashConfig.log-level")
	}
	if !webAPISettingsEnum(clash.FindProcessMode, "off", "always") || !webAPISettingsEnum(clash.InterfaceNameMode, "clear", "follow", "custom") || !webAPISettingsEnum(clash.GeodataLoader, "standard", "memconservative") {
		return webAPISettingsInvalid("runtime.patchClashConfig")
	}
	if !webAPISettingsEnum(clash.ExternalController, "", "127.0.0.1:9090") {
		return webAPISettingsInvalid("runtime.patchClashConfig.external-controller")
	}
	if clash.KeepAliveInterval < 0 || clash.KeepAliveIdle < 0 || clash.GeoUpdateInterval < 1 || clash.GeoUpdateInterval > 87600 {
		return webAPISettingsInvalid("runtime.patchClashConfig.interval")
	}
	if !webAPISettingsEnum(clash.Tun.Stack, "gvisor", "system", "mixed", "mips") || clash.Tun.Device == "" || clash.Tun.MTU < 1280 || clash.Tun.MTU > 65535 || !webAPISettingsEnum(clash.Tun.CongestionController, "cubic", "reno", "bbr", "bbr3") {
		return webAPISettingsInvalid("runtime.patchClashConfig.tun")
	}
	if clash.TunOverride.MTU < 1280 || clash.TunOverride.MTU > 65535 || !webAPISettingsEnum(clash.TunOverride.CongestionController, "cubic", "reno", "bbr", "bbr3") || clash.TunOverride.UDPTimeout < 0 {
		return webAPISettingsInvalid("runtime.patchClashConfig.tun-override")
	}
	for _, list := range [][]string{clash.Tun.RouteAddress, clash.Tun.RouteExcludeAddress, clash.TunOverride.RouteAddress, clash.TunOverride.RouteExcludeAddress} {
		for _, prefix := range list {
			if _, err := netip.ParsePrefix(prefix); err != nil {
				return webAPISettingsInvalid("runtime.patchClashConfig.route-address")
			}
		}
	}
	dns := clash.DNS
	if !webAPISettingsEnum(dns.EnhancedMode, "redir-host", "fake-ip") || !webAPISettingsEnum(dns.CacheAlgorithm, "lru", "arc") || !webAPISettingsEnum(dns.FakeIPFilterMode, "blacklist", "whitelist", "rule") || dns.IPv6Timeout < 0 || dns.CacheMaxSize < 0 || dns.FakeIPTTL < 0 {
		return webAPISettingsInvalid("runtime.patchClashConfig.dns")
	}
	for _, prefix := range []string{dns.FakeIPRange, dns.FakeIPRange6} {
		if prefix != "" {
			if _, err := netip.ParsePrefix(prefix); err != nil {
				return webAPISettingsInvalid("runtime.patchClashConfig.dns.fake-ip-range")
			}
		}
	}
	if !webAPISettingsKeys(clash.DNSOverrideKeys, "enable", "listen", "listen-routing-mark", "use-hosts", "use-system-hosts", "ipv6", "ipv6-timeout", "respect-rules", "prefer-h3", "cache-algorithm", "cache-max-size", "enhanced-mode", "fake-ip-range", "fake-ip-range6", "fake-ip-filter", "fake-ip-filter-mode", "fake-ip-ttl", "default-nameserver", "nameserver-policy", "nameserver", "fallback", "fallback-lazy-query", "proxy-server-nameserver", "proxy-server-nameserver-policy", "direct-nameserver", "direct-nameserver-follow-policy", "fallback-filter.geoip", "fallback-filter.geoip-code", "fallback-filter.geosite", "fallback-filter.ipcidr", "fallback-filter.domain") {
		return webAPISettingsInvalid("runtime.patchClashConfig.dns-override-keys")
	}
	if !webAPISettingsKeys(clash.TunOverrideKeys, "mtu", "congestion-controller", "strict-route", "route-address", "route-exclude-address", "dns-hijack", "udp-timeout", "disable-icmp-forwarding", "gso", "include-interface", "exclude-interface", "include-uid", "exclude-uid", "exclude-src-port", "exclude-dst-port") || !webAPISettingsKeys(clash.GeneralOverrideKeys, "test-url", "unified-delay", "tcp-concurrent", "ipv6", "geodata-loader") {
		return webAPISettingsInvalid("runtime.patchClashConfig.override-keys")
	}
	if !webAPISettingsURL(clash.TestURL) {
		return webAPISettingsInvalid("runtime.patchClashConfig.test-url")
	}
	for _, value := range []string{clash.GeoXURL.MMDB, clash.GeoXURL.ASN, clash.GeoXURL.GEOIP, clash.GeoXURL.GEOSITE} {
		if !webAPISettingsURL(value) {
			return webAPISettingsInvalid("runtime.patchClashConfig.geox-url")
		}
	}
	if clash.Mitm.CaptureEntries != nil && (*clash.Mitm.CaptureEntries < 1 || *clash.Mitm.CaptureEntries > 20000) {
		return webAPISettingsInvalid("runtime.patchClashConfig.mitm.capture-entries")
	}
	if !webAPISettingsEnum(app.RequestFeed, "recent", "live") || !webAPISettingsEnum(app.RestoreStrategy, "compatible", "override") || app.DelayTestTimeout < 1 || app.DelayTestTimeout > 300000 {
		return webAPISettingsInvalid("runtime.appSetting")
	}
	access := network.VPNProps.AccessControlProps
	if !webAPISettingsEnum(access.Mode, "acceptSelected", "rejectSelected") || !webAPISettingsEnum(access.Sort, "none", "name", "time") {
		return webAPISettingsInvalid("network.vpnProps.accessControlProps")
	}
	auth := network.NetworkProps.Authentication
	if auth.Enable && (auth.Username == "" || strings.ContainsAny(auth.Username, ":\r\n") || strings.ContainsAny(auth.Password, "\r\n")) {
		return webAPISettingsInvalid("network.networkProps.authentication")
	}
	if settings.CurrentProfileID != nil {
		if _, err := webAPIInteger(string(*settings.CurrentProfileID), true); err != nil {
			return webAPISettingsInvalid("currentProfileId")
		}
	}
	if settings.DAV != nil {
		dav := settings.DAV
		if !webAPISettingsDAVURL(dav.URI) || dav.FileName == "" || len(dav.FileName) > 256 || dav.FileName == "." || dav.FileName == ".." || strings.ContainsAny(dav.FileName, "/\\\r\n") {
			return webAPISettingsInvalid("dav")
		}
	}
	return nil
}

func ValidateWebAPISettingsReferences(tx *WebAPIReadTx, settings WebAPISettings) error {
	if settings.CurrentProfileID == nil {
		return nil
	}
	if _, err := tx.Profile(*settings.CurrentProfileID); err != nil {
		return webAPIError(409, "invalid_reference", "current profile does not exist")
	}
	return nil
}

func webAPISettingsSave(tx *WebAPIWriteTx, settings WebAPISettings) (WebAPISettings, error) {
	if err := ValidateWebAPISettings(settings); err != nil {
		return settings, err
	}
	if err := ValidateWebAPISettingsReferences(tx.WebAPIReadTx, settings); err != nil {
		return settings, err
	}
	return settings, tx.SaveSettings(settings)
}

func ApplyWebAPISettingsRuntime(tx *WebAPIWriteTx, command WebAPISettingsRuntimeCommand) (WebAPISettings, error) {
	settings, err := tx.Settings()
	if err != nil {
		return settings, err
	}
	if !webAPISettingsPatchPresent(command.Patch) {
		return settings, webAPIError(400, "empty_patch", "settings patch must change an explicit field")
	}
	if patch := command.Patch.PatchClashConfig; patch != nil && patch.ExternalController != nil && *patch.ExternalController != "" {
		return settings, webAPIError(403, "unsafe_controller", "Web settings cannot open an external controller")
	}
	webAPIApplySettingsPatch(&settings.Runtime, command.Patch)
	return webAPISettingsSave(tx, settings)
}

func ApplyWebAPISettingsNetwork(tx *WebAPIWriteTx, command WebAPISettingsNetworkCommand) (WebAPISettings, error) {
	settings, err := tx.Settings()
	if err != nil {
		return settings, err
	}
	if !webAPISettingsPatchPresent(command.Patch) {
		return settings, webAPIError(400, "empty_patch", "settings patch must change an explicit field")
	}
	webAPIApplySettingsPatch(&settings.Network, command.Patch)
	return webAPISettingsSave(tx, settings)
}

func ApplyWebAPISettingsCurrentProfile(tx *WebAPIWriteTx, command WebAPISettingsCurrentProfileCommand) (WebAPISettings, error) {
	settings, err := tx.Settings()
	if err != nil {
		return settings, err
	}
	settings.CurrentProfileID = command.CurrentProfileID
	return webAPISettingsSave(tx, settings)
}

func ApplyWebAPISettingsDAV(tx *WebAPIWriteTx, command WebAPISettingsDAVCommand) (WebAPISettings, error) {
	settings, err := tx.Settings()
	if err != nil {
		return settings, err
	}
	if command.DAV == nil {
		settings.DAV = nil
		return webAPISettingsSave(tx, settings)
	}
	if !webAPISettingsPatchPresent(command.DAV) {
		return settings, webAPIError(400, "empty_patch", "DAV patch must change an explicit field")
	}
	if settings.DAV == nil {
		settings.DAV = &WebAPISettingsDAV{FileName: "backup.zip"}
	}
	webAPIApplySettingsPatch(settings.DAV, command.DAV)
	return webAPISettingsSave(tx, settings)
}

func ApplyWebAPISettingsSelectedMap(tx *WebAPIWriteTx, command WebAPISettingsSelectedMapCommand) (WebAPISettingsSelectedMap, error) {
	result := WebAPISettingsSelectedMap{ProfileID: command.ProfileID, GroupName: command.GroupName, ProxyName: command.ProxyName}
	if _, err := webAPIInteger(string(command.ProfileID), true); err != nil || !webAPIName(command.GroupName) {
		return result, webAPISettingsInvalid("selected-map")
	}
	profile, err := tx.Profile(command.ProfileID)
	if err != nil {
		return result, webAPIError(409, "invalid_reference", "selected profile does not exist")
	}
	if _, err = tx.ProfileCatalog(command.ProfileID); err != nil {
		return result, webAPIError(409, "catalog_unavailable", "profile selector membership is not ready")
	}
	// Members come from the profile source graph, the authority the runtime selection also uses.
	members, ready, err := WebAPICatalogGroupMemberNames(tx.WebAPIReadTx, command.ProfileID, command.GroupName)
	if err != nil {
		var problem *WebAPIError
		if errors.As(err, &problem) && problem.Status == 404 {
			return result, webAPIError(409, "invalid_group", "selector group does not exist in this profile")
		}
		return result, webAPIError(409, "catalog_unavailable", "profile selector membership is not ready")
	}
	if command.ProxyName != nil {
		if !webAPIName(*command.ProxyName) || !webAPIContains(members, *command.ProxyName) {
			if !ready && webAPIName(*command.ProxyName) {
				return result, webAPIError(409, "catalog_unavailable", "provider members are not prepared")
			}
			return result, webAPIError(409, "invalid_selection", "proxy is not a member of the selected profile group")
		}
		if profile.SelectedMap == nil {
			profile.SelectedMap = map[string]string{}
		}
		profile.SelectedMap[command.GroupName] = *command.ProxyName
	} else {
		delete(profile.SelectedMap, command.GroupName)
	}
	return result, tx.SaveProfile(profile)
}
