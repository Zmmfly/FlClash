//go:build webpanel && !android

package core

import (
	"encoding/json"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/metacubex/mihomo/component/easytier"
	ZT "github.com/metacubex/zerotier-go"
)

func webStatefulInvalidDefinition() *WebAPIError {
	return webAPIError(400, "invalid_stateful_resource", "stateful protocol options are invalid")
}

func webStatefulStrings(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	if values, ok := raw.([]string); ok {
		return values, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, webStatefulInvalidDefinition()
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, webStatefulInvalidDefinition()
		}
		out = append(out, text)
	}
	return out, nil
}

func webStatefulOptionalBool(definition map[string]any, field string) (*bool, error) {
	raw, exists := definition[field]
	if !exists {
		return nil, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return nil, webStatefulInvalidDefinition()
	}
	return &value, nil
}

func webStatefulInteger(raw any) (int64, bool) {
	switch value := raw.(type) {
	case nil:
		return 0, true
	case int:
		return int64(value), true
	case int64:
		return value, true
	case uint64:
		return int64(value), value <= math.MaxInt64
	case float64:
		return int64(value), !math.IsNaN(value) && !math.IsInf(value, 0) && value >= math.MinInt64 && value < float64(math.MaxInt64) && math.Trunc(value) == value
	case json.Number:
		number, err := strconv.ParseInt(string(value), 10, 64)
		return number, err == nil
	default:
		return 0, false
	}
}

func webStatefulValidateOptions(definition map[string]any) error {
	kind := definition["type"]
	switch kind {
	case "tailscale":
		for _, field := range []string{"accept-routes", "exit-node-allow-lan-access", "udp"} {
			if _, err := webStatefulOptionalBool(definition, field); err != nil {
				return err
			}
		}
	case "easytier":
		peers, err := webStatefulStrings(definition["peers"])
		if err != nil {
			return err
		}
		listeners, err := webStatefulStrings(definition["listeners"])
		if err != nil {
			return err
		}
		noListener, err := webStatefulOptionalBool(definition, "no-listener")
		if err != nil {
			return err
		}
		secure, err := webStatefulOptionalBool(definition, "secure-mode")
		if err != nil {
			return err
		}
		name, _ := definition["network-name"].(string)
		private, _ := definition["local-private-key"].(string)
		public, _ := definition["local-public-key"].(string)
		config := easytier.Config{NetworkName: name, Peers: peers, Listeners: listeners, NoListener: noListener, SecureMode: secure, LocalPrivateKey: private, LocalPublicKey: public}
		if config.ValidateStructured() != nil {
			return webStatefulInvalidDefinition()
		}
	case "zerotier":
		network, _ := definition["network"].(string)
		if _, err := ZT.ParseNetworkID(network); err != nil {
			return webStatefulInvalidDefinition()
		}
		secret, _ := definition["identity-secret"].(string)
		if secret != "" {
			identity, err := ZT.ParseIdentity(secret)
			if err != nil || !identity.HasPrivate() || identity.Validate() != nil {
				return webStatefulInvalidDefinition()
			}
		}
		for field, bounds := range map[string][2]int64{"mtu": {ZT.MinNetworkMTU, ZT.MaxNetworkMTU}, "physical-mtu": {ZT.MinPhysicalMTU, ZT.MaxPhysicalMTU}, "primary-port": {1, 65535}, "secondary-port": {1, 65535}, "remote-trace-level": {0, int64(ZT.TraceLevelInsane)}} {
			value, valid := webStatefulInteger(definition[field])
			if !valid || value != 0 && (value < bounds[0] || value > bounds[1]) {
				return webStatefulInvalidDefinition()
			}
		}
	}
	return nil
}

func webStatefulEasyTierPeerKeys(definition map[string]any) []string {
	peers, _ := webStatefulStrings(definition["peers"])
	keys := map[string]bool{}
	for _, peer := range peers {
		parsed, err := url.Parse(strings.TrimSpace(peer))
		if err != nil {
			continue
		}
		for _, pair := range strings.Split(parsed.RawQuery, "&") {
			name, value, _ := strings.Cut(pair, "=")
			name, _ = url.PathUnescape(name)
			if name == "peer-public-key" || name == "peer_public_key" {
				key, err := url.PathUnescape(value)
				if err == nil && key != "" {
					keys[key] = true
				}
			}
		}
	}
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
