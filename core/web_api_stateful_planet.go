//go:build webpanel && !android

package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	ZT "github.com/metacubex/zerotier-go"
)

const WebAPIStatefulPlanetPurpose = "zerotier-planet"
const WebAPIStatefulPlanetSlots = 32
const WebAPIStatefulPlanetBytes = 32 << 20

type WebAPIStatefulPlanetReference struct {
	Purpose string `json:"purpose"`
	SHA256  string `json:"sha256"`
}

type WebAPIStatefulPlanetAsset struct {
	ProfileID WebAPIID                      `json:"profileId"`
	Reference WebAPIStatefulPlanetReference `json:"reference"`
	Bytes     []byte                        `json:"bytes"`
}

type WebAPIStatefulPlanetInfo struct {
	SHA256      string `json:"sha256"`
	Size        int    `json:"size"`
	WorldID     string `json:"worldId"`
	TrustDomain string `json:"trustDomain"`
}

func webStatefulDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func webStatefulHex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

func WebAPIValidateStatefulPlanet(data []byte) (WebAPIStatefulPlanetInfo, error) {
	var info WebAPIStatefulPlanetInfo
	if len(data) > WebAPIStatefulPlanetBytes {
		return info, webAPIError(413, "planet_asset_too_large", "planet asset exceeds the byte budget")
	}
	world, err := ZT.ParsePlanet(data)
	if err != nil {
		return info, webAPIError(400, "invalid_planet_asset", "planet asset must be one complete ZeroTier planet definition")
	}
	// The uploaded initial planet is a user-selected trust anchor, not an authenticated update.
	roots := make([]string, 0, len(world.Roots))
	for _, root := range world.Roots {
		roots = append(roots, root.Identity.PublicString())
	}
	sort.Strings(roots)
	trust, err := json.Marshal(struct {
		ID    uint64
		Key   [ZT.PublicKeySize]byte
		Roots []string
	}{world.ID, world.UpdatesMustBeSignedBy, roots})
	if err != nil {
		return info, err
	}
	info = WebAPIStatefulPlanetInfo{SHA256: webStatefulDigest(data), Size: len(data), WorldID: fmt.Sprintf("%016x", world.ID), TrustDomain: webStatefulDigest(trust)}
	return info, nil
}

func WebAPIValidateStatefulPlanetAssets(assets []WebAPIStatefulPlanetAsset) (map[string]WebAPIStatefulPlanetInfo, error) {
	if len(assets) > WebAPIStatefulPlanetSlots {
		return nil, webAPIError(413, "planet_asset_slots_exceeded", "profile planet asset slot budget exceeded")
	}
	remaining := WebAPIStatefulPlanetBytes
	var owner WebAPIID
	for _, asset := range assets {
		if _, err := webAPIInteger(string(asset.ProfileID), true); err != nil || owner != "" && owner != asset.ProfileID {
			return nil, webAPIError(409, "planet_asset_owner_mismatch", "planet assets must belong to one profile")
		}
		owner = asset.ProfileID
		if len(asset.Bytes) > remaining {
			return nil, webAPIError(413, "planet_asset_too_large", "profile planet assets exceed the aggregate byte budget")
		}
		remaining -= len(asset.Bytes)
	}
	result := make(map[string]WebAPIStatefulPlanetInfo, len(assets))
	for _, asset := range assets {
		if err := webStatefulPlanetReference(asset.Reference); err != nil {
			return nil, err
		}
		if _, exists := result[asset.Reference.SHA256]; exists {
			return nil, webAPIError(400, "duplicate_planet_asset", "profile planet asset references must be unique")
		}
		info, err := WebAPIValidateStatefulPlanet(asset.Bytes)
		if err != nil {
			return nil, err
		}
		if info.SHA256 != asset.Reference.SHA256 {
			return nil, webAPIError(409, "planet_asset_digest_mismatch", "planet bytes do not match their typed reference")
		}
		result[info.SHA256] = info
	}
	return result, nil
}

func webStatefulPlanetReference(reference WebAPIStatefulPlanetReference) error {
	if reference.Purpose != WebAPIStatefulPlanetPurpose || !webStatefulHex(reference.SHA256) {
		return webAPIError(400, "invalid_planet_reference", "planet reference requires zerotier-planet purpose and a lowercase SHA-256 digest")
	}
	return nil
}

func webStatefulParsePlanetReference(raw any) (WebAPIStatefulPlanetReference, error) {
	var reference WebAPIStatefulPlanetReference
	mapping, ok := raw.(map[string]any)
	if !ok || len(mapping) != 2 {
		return reference, webAPIError(400, "invalid_planet_reference", "planet-asset must be a typed purpose and digest reference")
	}
	reference.Purpose, _ = mapping["purpose"].(string)
	reference.SHA256, _ = mapping["sha256"].(string)
	return reference, webStatefulPlanetReference(reference)
}
