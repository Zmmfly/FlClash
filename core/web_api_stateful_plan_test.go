//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ZT "github.com/metacubex/zerotier-go"
)

func webStatefulTestDefinition(kind string) map[string]any {
	definition := map[string]any{"name": "synthetic-node", "type": kind}
	switch kind {
	case "tailscale":
		definition["auth-key"] = "synthetic-enrollment-key"
	case "easytier":
		definition["network-name"], definition["network-secret"] = "synthetic-network", "synthetic-secret"
		definition["peers"] = []string{"tcp://192.0.2.1:11010"}
	case "zerotier":
		definition["network"] = "0123456789abcdef"
	}
	return definition
}

func webStatefulTestPlan(t *testing.T, id WebAPIID, definitions ...map[string]any) WebAPIStatefulPlan {
	t.Helper()
	plan, err := WebAPIPlanStatefulResources(id, []WebAPIStatefulInput{{Scope: WebAPIStatefulScope{Kind: WebAPIStatefulRoot}, Proxies: definitions}})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func webStatefulTestPlanet(t *testing.T, owner WebAPIID) WebAPIStatefulPlanetAsset {
	t.Helper()
	identity, err := ZT.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	world := ZT.World{Type: ZT.WorldTypePlanet, ID: 12345, Timestamp: 1, Roots: []ZT.WorldRoot{{Identity: identity, Endpoints: []netip.AddrPort{netip.MustParseAddrPort("192.0.2.1:9993")}}}}
	data, err := world.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return WebAPIStatefulPlanetAsset{ProfileID: owner, Reference: WebAPIStatefulPlanetReference{Purpose: WebAPIStatefulPlanetPurpose, SHA256: webStatefulDigest(data)}, Bytes: data}
}

func TestWebAPIStatefulContractPurePlanHasNoFilesystemOrNativeConstruction(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "not-created")
	manager, err := NewWebAPIStatefulManager(absent)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	for _, kind := range []string{"tailscale", "easytier", "zerotier"} {
		definition := webStatefulTestDefinition(kind)
		before, _ := json.Marshal(definition)
		plan := webStatefulTestPlan(t, "1", definition)
		after, _ := json.Marshal(definition)
		if !bytes.Equal(before, after) || len(plan.Resources) != 1 {
			t.Fatal("pure plan mutated input or lost the resource")
		}
		encoded, _ := json.Marshal(plan)
		for _, private := range []string{absent, ".api-proxy-state", "synthetic-enrollment-key", "synthetic-secret", "state-dir"} {
			if bytes.Contains(encoded, []byte(private)) {
				t.Fatalf("pure plan disclosed private value %q", private)
			}
		}
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Fatalf("constructor or plan created state: %v", err)
	}
}

func TestWebAPIStatefulContractStableSecurityDomains(t *testing.T) {
	for _, kind := range []string{"tailscale", "easytier", "zerotier"} {
		t.Run(kind, func(t *testing.T) {
			before := webStatefulTestDefinition(kind)
			after := maps.Clone(before)
			for key, value := range map[string]any{"hostname": "renamed-host", "udp": true, "mtu": 1300, "dialer-proxy": "new-dialer", "ipv4": "192.0.2.2", "peers": []string{"tcp://192.0.2.3:1"}} {
				after[key] = value
			}
			first, second := webStatefulTestPlan(t, "1", before), webStatefulTestPlan(t, "1", after)
			if !reflect.DeepEqual(first, second) {
				t.Fatal("mutable connection settings changed identity")
			}
			switch kind {
			case "tailscale":
				after["auth-key"] = "other-enrollment-key"
			case "easytier":
				after["network-secret"] = "other-network-secret"
			case "zerotier":
				after["network"] = "1234567890abcdef"
			}
			if first.Resources[0].SecurityDomain == webStatefulTestPlan(t, "1", after).Resources[0].SecurityDomain {
				t.Fatal("security domain change reused the identity domain")
			}
		})
	}
	one, err := WebAPIStatefulOriginDomain("https://EXAMPLE.test:443/provider", map[string][]string{"Authorization": {"synthetic"}, "User-Agent": {"old"}})
	if err != nil {
		t.Fatal(err)
	}
	two, err := WebAPIStatefulOriginDomain("https://example.test/provider", map[string][]string{"authorization": {"synthetic"}, "User-Agent": {"new"}})
	if err != nil || one != two {
		t.Fatal("canonical origin or nonidentity header caused identity churn")
	}
	three, _ := WebAPIStatefulOriginDomain("https://example.test/provider", map[string][]string{"Authorization": {"different"}})
	if one == three {
		t.Fatal("origin authorization change reused identity")
	}
}

func TestWebAPIStatefulContractRawPathsRejectedEvenEmpty(t *testing.T) {
	for _, kind := range []string{"tailscale", "easytier", "zerotier"} {
		for _, key := range []string{"state-dir", "STATE-DIR", "stateDir", "state_dir", "path", "config-file", "planet"} {
			for _, value := range []any{nil, "", "../outside", "/private/identity", "C:\\private\\identity"} {
				definition := webStatefulTestDefinition(kind)
				definition[key] = value
				_, err := WebAPIValidateStatefulDefinition(definition)
				webAPITestErrorCode(t, err, "stateful_path_forbidden")
			}
		}
	}
	_, err := WebAPIValidateStatefulDefinition(map[string]any{"type": "openvpn", "state-dir": ""})
	webAPITestErrorCode(t, err, "stateful_path_forbidden")
}

func TestWebAPIStatefulContractTypedPlanetValidationAndBudget(t *testing.T) {
	asset := webStatefulTestPlanet(t, "1")
	info, err := WebAPIValidateStatefulPlanet(asset.Bytes)
	if err != nil || info.SHA256 != asset.Reference.SHA256 || info.WorldID != "0000000000003039" {
		t.Fatalf("synthetic planet validation: %v %+v", err, info)
	}
	for _, data := range [][]byte{nil, []byte("/etc/passwd"), append(append([]byte{}, asset.Bytes...), 0), asset.Bytes[:len(asset.Bytes)-1]} {
		_, err := WebAPIValidateStatefulPlanet(data)
		webAPITestErrorCode(t, err, "invalid_planet_asset")
	}
	moon := append([]byte{}, asset.Bytes...)
	moon[0] = byte(ZT.WorldTypeMoon)
	_, err = WebAPIValidateStatefulPlanet(moon)
	webAPITestErrorCode(t, err, "invalid_planet_asset")
	_, err = WebAPIValidateStatefulPlanetAssets([]WebAPIStatefulPlanetAsset{asset, asset})
	webAPITestErrorCode(t, err, "duplicate_planet_asset")
	_, err = WebAPIValidateStatefulPlanetAssets(make([]WebAPIStatefulPlanetAsset, WebAPIStatefulPlanetSlots+1))
	webAPITestErrorCode(t, err, "planet_asset_slots_exceeded")
	large := make([]byte, WebAPIStatefulPlanetBytes/2+1)
	_, err = WebAPIValidateStatefulPlanetAssets([]WebAPIStatefulPlanetAsset{{ProfileID: "1", Bytes: large}, {ProfileID: "1", Bytes: large}})
	webAPITestErrorCode(t, err, "planet_asset_too_large")
	wrong := asset
	wrong.Reference.Purpose = "generic-file"
	_, err = WebAPIValidateStatefulPlanetAssets([]WebAPIStatefulPlanetAsset{wrong})
	webAPITestErrorCode(t, err, "invalid_planet_reference")
	wrong = asset
	wrong.Reference.SHA256 = strings.Repeat("0", 64)
	_, err = WebAPIValidateStatefulPlanetAssets([]WebAPIStatefulPlanetAsset{wrong})
	webAPITestErrorCode(t, err, "planet_asset_digest_mismatch")
}

func TestWebAPIStatefulContractPlanetOwnerAndTypedReference(t *testing.T) {
	asset := webStatefulTestPlanet(t, "1")
	definition := webStatefulTestDefinition("zerotier")
	definition["planet-asset"] = map[string]any{"purpose": asset.Reference.Purpose, "sha256": asset.Reference.SHA256}
	input := WebAPIStatefulInput{Scope: WebAPIStatefulScope{Kind: WebAPIStatefulRoot}, Proxies: []map[string]any{definition}, PlanetAssets: []WebAPIStatefulPlanetAsset{asset}}
	plan, err := WebAPIPlanStatefulResources("1", []WebAPIStatefulInput{input})
	if err != nil || plan.Resources[0].PlanetSHA256 != asset.Reference.SHA256 {
		t.Fatal(err)
	}
	_, err = WebAPIPlanStatefulResources("2", []WebAPIStatefulInput{input})
	webAPITestErrorCode(t, err, "planet_asset_owner_mismatch")
	input.Scope = WebAPIStatefulScope{Kind: WebAPIStatefulProfileProvider, ProviderName: "provider", SourceProfileID: "1"}
	if _, err = WebAPIPlanStatefulResources("2", []WebAPIStatefulInput{input}); err != nil {
		t.Fatal(err)
	}
	input.PlanetAssets = nil
	_, err = WebAPIPlanStatefulResources("2", []WebAPIStatefulInput{input})
	webAPITestErrorCode(t, err, "planet_asset_unavailable")
	for _, reference := range []any{asset.Reference.SHA256, "/tmp/planet", nil, map[string]any{"purpose": asset.Reference.Purpose, "sha256": asset.Reference.SHA256, "path": "/tmp/planet"}} {
		definition["planet-asset"] = reference
		_, err = WebAPIValidateStatefulDefinition(definition)
		webAPITestErrorCode(t, err, "invalid_planet_reference")
	}
}
