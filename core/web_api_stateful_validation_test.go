//go:build webpanel && !android

package core

import (
	"fmt"
	"net/netip"
	"testing"

	ZT "github.com/metacubex/zerotier-go"
)

func TestWebAPIStatefulContractNativeFreeOptionValidation(t *testing.T) {
	for _, definition := range []map[string]any{
		{"type": "easytier", "name": "invalid", "network-name": "synthetic"},
		{"type": "easytier", "name": "invalid", "network-name": "synthetic", "peers": []any{42}},
		{"type": "easytier", "name": "invalid", "network-name": "synthetic", "no-listener": true, "listeners": []string{"tcp://192.0.2.1:1"}},
		{"type": "zerotier", "name": "invalid", "network": "not-a-network-id"},
		{"type": "zerotier", "name": "invalid", "network": "0123456789abcdef", "identity-secret": "not-a-private-identity"},
		{"type": "zerotier", "name": "invalid", "network": "0123456789abcdef", "mtu": -1},
		{"type": "zerotier", "name": "invalid", "network": "0123456789abcdef", "primary-port": 65536},
		{"type": "tailscale", "name": "invalid", "accept-routes": "not-a-boolean"},
	} {
		stateful, err := WebAPIValidateStatefulDefinition(definition)
		if !stateful {
			t.Fatal("invalid stateful definition was passed to the native validator")
		}
		webAPITestErrorCode(t, err, "invalid_stateful_resource")
	}
}

func TestWebAPIStatefulContractPlanetTrustDomainNotSnapshotDigest(t *testing.T) {
	asset := webStatefulTestPlanet(t, "1")
	before, err := WebAPIValidateStatefulPlanet(asset.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	world, err := ZT.ParsePlanet(asset.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	world.Timestamp++
	world.Roots[0].Endpoints = []netip.AddrPort{netip.MustParseAddrPort("192.0.2.2:9993")}
	data, _ := world.Serialize()
	after, err := WebAPIValidateStatefulPlanet(data)
	if err != nil || before.SHA256 == after.SHA256 || before.TrustDomain != after.TrustDomain {
		t.Fatal("planet snapshot refresh changed the same trust domain")
	}
	world.UpdatesMustBeSignedBy[0] = 1
	data, _ = world.Serialize()
	changed, err := WebAPIValidateStatefulPlanet(data)
	if err != nil || changed.TrustDomain == before.TrustDomain {
		t.Fatal("new planet signing authority reused the old domain")
	}
}

func TestWebAPIStatefulContractPlanetBudgetAggregatesAcrossScopes(t *testing.T) {
	assets := make([]WebAPIStatefulPlanetAsset, WebAPIStatefulPlanetSlots+1)
	for i := range assets {
		assets[i] = WebAPIStatefulPlanetAsset{ProfileID: "1", Reference: WebAPIStatefulPlanetReference{Purpose: WebAPIStatefulPlanetPurpose, SHA256: fmt.Sprintf("%064x", i)}}
	}
	_, err := WebAPIPlanStatefulResources("1", []WebAPIStatefulInput{
		{Scope: WebAPIStatefulScope{Kind: WebAPIStatefulRoot}, PlanetAssets: assets[:16]},
		{Scope: WebAPIStatefulScope{Kind: WebAPIStatefulProfileProvider, ProviderName: "inline", SourceProfileID: "1", Inline: true}, PlanetAssets: assets[16:]},
	})
	webAPITestErrorCode(t, err, "planet_asset_slots_exceeded")
}
