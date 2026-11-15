//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding"
	"encoding/json"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/common/structure"
)

func webAPICatalogName(value string) bool {
	return value != "" && len(value) <= 256 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func webAPICatalogURL(value string, optional bool) bool {
	if value == "" {
		return optional
	}
	if len(value) > 8192 || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func webAPIProxyMap(definition WebAPIProxyDefinition) (map[string]any, error) {
	data, err := json.Marshal(definition)
	if err != nil {
		return nil, webAPIError(400, "invalid_proxy_definition", "proxy definition cannot be encoded")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var result map[string]any
	if err = decoder.Decode(&result); err != nil || result == nil {
		return nil, webAPIError(400, "invalid_proxy_definition", "expected a proxy definition object")
	}
	return result, nil
}

func webAPIProxyString(definition WebAPIProxyDefinition, key string) string {
	var value string
	_ = json.Unmarshal(definition[key], &value)
	return value
}

func webAPIProtocolOption(kind string) any {
	switch kind {
	case "ss":
		return &outbound.ShadowSocksOption{}
	case "ssr":
		return &outbound.ShadowSocksROption{}
	case "socks5":
		return &outbound.Socks5Option{}
	case "http":
		return &outbound.HttpOption{}
	case "vmess":
		return &outbound.VmessOption{}
	case "vless":
		return &outbound.VlessOption{}
	case "snell":
		return &outbound.SnellOption{}
	case "trojan":
		return &outbound.TrojanOption{}
	case "hysteria":
		return &outbound.HysteriaOption{}
	case "hysteria2":
		return &outbound.Hysteria2Option{}
	case "wireguard":
		return &outbound.WireGuardOption{}
	case "tuic":
		return &outbound.TuicOption{}
	case "shadowquic":
		return &outbound.ShadowQuicOption{}
	case "gost-relay":
		return &outbound.GostRelayOption{}
	case "direct":
		return &outbound.DirectOption{}
	case "dns":
		return &outbound.DnsOption{}
	case "reject":
		return &outbound.RejectOption{}
	case "rematch":
		return &outbound.RematchOption{}
	case "ssh":
		return &outbound.SshOption{}
	case "mieru":
		return &outbound.MieruOption{}
	case "anytls":
		return &outbound.AnyTLSOption{}
	case "sudoku":
		return &outbound.SudokuOption{}
	case "masque":
		return &outbound.MasqueOption{}
	case "trusttunnel":
		return &outbound.TrustTunnelOption{}
	case "openvpn":
		return &outbound.OpenVPNOption{}
	case "tailscale":
		return &outbound.TailscaleOption{}
	case "zerotier":
		return &outbound.ZeroTierOption{}
	case "easytier":
		return &outbound.EasyTierOption{}
	default:
		return nil
	}
}

var webAPIProxyRequired = map[string][]string{
	"ss":     {"server", "port", "password", "cipher"},
	"ssr":    {"server", "port", "password", "cipher", "obfs", "protocol"},
	"socks5": {"server", "port"}, "http": {"server", "port"},
	"vmess": {"server", "port", "uuid", "alterId", "cipher"},
	"vless": {"server", "port", "uuid"}, "snell": {"server", "port", "psk"},
	"trojan": {"server", "port", "password"}, "anytls": {"server", "port", "password"},
	"hysteria": {"server", "up", "down"}, "hysteria2": {"server"},
	"wireguard": {"private-key"}, "ssh": {"server", "port", "username"},
	"mieru":  {"server", "transport", "username", "password"},
	"sudoku": {"server", "port", "key"}, "masque": {"server", "port", "private-key", "public-key"},
	"openvpn": {"server", "port", "ca"}, "zerotier": {"network"},
	"tuic": {"server", "port"}, "shadowquic": {"server", "port"},
	"gost-relay": {"server", "port"}, "trusttunnel": {"server", "port"},
}

func webAPIProxyFields(typ reflect.Type) map[string]reflect.Type {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	result := map[string]reflect.Type{}
	if typ.Kind() != reflect.Struct {
		return result
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := strings.Split(field.Tag.Get("proxy"), ",")[0]
		if tag == "-" || field.PkgPath != "" {
			continue
		}
		if field.Anonymous && tag == "" {
			for key, child := range webAPIProxyFields(field.Type) {
				result[key] = child
			}
			continue
		}
		if tag == "" {
			tag = field.Name
		}
		result[tag] = field.Type
	}
	return result
}

var webAPITextUnmarshaller = reflect.TypeFor[encoding.TextUnmarshaler]()

func webAPIProxyShape(value any, typ reflect.Type) bool {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if reflect.PointerTo(typ).Implements(webAPITextUnmarshaller) {
		text, ok := value.(string)
		return ok && reflect.New(typ).Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(text)) == nil
	}
	switch typ.Kind() {
	case reflect.Interface:
		return value != nil
	case reflect.String:
		_, ok := value.(string)
		return ok
	case reflect.Bool:
		_, ok := value.(bool)
		return ok
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var text string
		switch number := value.(type) {
		case json.Number:
			text = string(number)
		case string:
			text = number
		default:
			return false
		}
		_, err := strconv.ParseInt(text, 10, typ.Bits())
		return err == nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		var text string
		switch number := value.(type) {
		case json.Number:
			text = string(number)
		case string:
			text = number
		default:
			return false
		}
		_, err := strconv.ParseUint(text, 10, typ.Bits())
		return err == nil
	case reflect.Float32, reflect.Float64:
		value, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := strconv.ParseFloat(string(value), typ.Bits())
		return err == nil
	case reflect.Map:
		mapping, ok := value.(map[string]any)
		if !ok || typ.Key().Kind() != reflect.String {
			return false
		}
		for _, item := range mapping {
			if !webAPIProxyShape(item, typ.Elem()) {
				return false
			}
		}
		return true
	case reflect.Slice, reflect.Array:
		list, ok := value.([]any)
		if !ok {
			return false
		}
		for _, item := range list {
			if !webAPIProxyShape(item, typ.Elem()) {
				return false
			}
		}
		return true
	case reflect.Struct:
		mapping, ok := value.(map[string]any)
		if !ok {
			return false
		}
		fields := webAPIProxyFields(typ)
		for key, item := range mapping {
			child, exists := fields[key]
			if !exists || !webAPIProxyShape(item, child) {
				return false
			}
		}
		return true
	}
	return false
}

func webAPIValidateProxy(definition WebAPIProxyDefinition, emptyOptions []string) error {
	mapping, err := webAPIProxyMap(definition)
	if err != nil {
		return err
	}
	name, ok := mapping["name"].(string)
	if !ok || !webAPICatalogName(name) {
		return webAPIError(400, "invalid_name", "proxy name is required and must be an unambiguous nonempty string")
	}
	if webAPIContains([]string{"DIRECT", "REJECT", "REJECT-DROP", "COMPATIBLE", "PASS", "PASS-RULE", "GLOBAL"}, name) {
		return webAPIError(409, "reserved_name", "proxy name is reserved")
	}
	kind, ok := mapping["type"].(string)
	if !ok {
		return webAPIError(400, "invalid_proxy_type", "proxy type is required")
	}
	option := webAPIProtocolOption(kind)
	if option == nil {
		return webAPIError(400, "invalid_proxy_type", "unknown proxy protocol")
	}
	fields := webAPIProxyFields(reflect.TypeOf(option))
	fields["smux"] = reflect.TypeFor[outbound.SingMuxOption]()
	for key, value := range mapping {
		if key == "type" {
			continue
		}
		typ, exists := fields[key]
		if !exists || !webAPIProxyShape(value, typ) {
			return webAPIError(400, "invalid_proxy_option", "unknown proxy option or invalid option type")
		}
	}
	for _, key := range webAPIProxyRequired[kind] {
		value, exists := mapping[key]
		if !exists || value == nil || value == "" {
			return webAPIError(400, "missing_proxy_option", "a required proxy option is missing")
		}
	}
	if value, exists := mapping["port"]; exists {
		text := ""
		switch number := value.(type) {
		case json.Number:
			text = string(number)
		case string:
			text = number
		}
		port, e := strconv.ParseUint(text, 10, 16)
		if e != nil || port == 0 {
			return webAPIError(400, "invalid_proxy_port", "proxy port must be between 1 and 65535")
		}
	}
	if target, exists := mapping["dialer-proxy"]; exists {
		text, ok := target.(string)
		if !ok || text != "" && (!webAPICatalogName(text) || !dependencySupportsDialer(kind)) {
			return webAPIError(400, "invalid_proxy_dialer", "invalid dialer-proxy option")
		}
	}
	seen := map[string]bool{}
	for _, option := range emptyOptions {
		if seen[option] || !webAPIContains(webAPIProxyEmptyOptions, option) {
			return webAPIError(400, "invalid_empty_options", "unknown or duplicate empty proxy option")
		}
		seen[option] = true
	}
	data, _ := json.Marshal(mapping)
	var normalized map[string]any
	_ = json.Unmarshal(data, &normalized)
	decoder := structure.NewDecoder(structure.Option{TagName: "proxy", WeaklyTypedInput: true, KeyReplacer: structure.DefaultKeyReplacer})
	if err := decoder.Decode(normalized, option); err != nil {
		return webAPIError(400, "invalid_proxy_definition", "proxy definition does not match its protocol options")
	}
	return nil
}

var webAPIProxyEmptyOptions = strings.Fields("username password uuid psk key privateKey publicKey privateKeyPassphrase preSharedKey authStr cipher alterId protocol obfs transport up down network flow wsPath wsHost wsHeaders wsMaxEarlyData wsEarlyDataHeaderName wsHttpUpgrade wsHttpUpgradeFastOpen httpMethod httpPath httpHost httpHeaders h2Host h2Path grpcServiceName xhttpPath xhttpHost mkcpSeed mkcpHeader mekyaUrl headers tls sni servername alpn skipCertVerify clientFingerprint realityPublicKey realityShortId plugin pluginMode pluginHost pluginPath pluginUsername pluginPassword pluginVersion pluginVersionHint pluginRestlsScript pluginKey pluginCrypt pluginTls pluginMux pluginSkipCertVerify udpOverTcp protocolParam obfsParam obfsPassword obfsSecret obfsMode obfsHost version ports hopInterval congestionController udpRelayMode reduceRtt ip ipv6 allowedIps dns remoteDnsResolve mtu tfo mptcp interfaceName routingMark ipVersion")
