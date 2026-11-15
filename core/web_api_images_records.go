//go:build webpanel && !android

package core

import (
	"context"
	"math/big"
	"net/netip"
	"strings"
)

func webImageRemoteURL(raw string) error {
	if len(raw) > 8192 || strings.ContainsAny(raw, "\x00\r\n") {
		return webAPIError(400, "invalid_icon_url", "icon resource requires a bounded HTTP(S) URL")
	}
	u, err := webContentURL(raw)
	if err != nil {
		return webAPIError(400, "invalid_icon_url", "icon resource requires an HTTP(S) URL without a fragment")
	}
	if !webSourceHostAllowed(u.Hostname()) {
		return webAPIError(403, "source_address_forbidden", "machine metadata is not an icon resource")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && webSourceAddressClass(ip) == "forbidden" {
		return webAPIError(403, "source_address_forbidden", "the icon resource address is forbidden")
	}
	return nil
}

func WebAPIUtilityIconSourceID(raw string) string { return webImageSourceID(raw) }

func ResolveWebAPIUtilitySource(_ context.Context, tx *WebAPIReadTx, key WebAPISourceResource) (string, error) {
	if len(key.ID) == 0 || len(key.ID) > 78 {
		return "", webAPIError(400, "invalid_source_resource", "invalid icon source identity")
	}
	integer, ok := new(big.Int).SetString(key.ID, 10)
	if key.Kind != "icon-image" || key.Name != "" || !ok || integer.Sign() < 0 || integer.BitLen() > 256 || integer.String() != key.ID {
		return "", webAPIError(400, "invalid_source_resource", "invalid icon source identity")
	}
	matches := func(raw string) bool { return webImageSourceID(raw) == key.ID && webImageRemoteURL(raw) == nil }
	records, err := (&WebAPICacheTx{tx.tx}).IconRecords()
	if err != nil {
		return "", err
	}
	for _, record := range records {
		if matches(record.URL) {
			return record.URL, nil
		}
	}
	groups, err := tx.ProxyGroups()
	if err != nil {
		return "", err
	}
	for _, group := range groups {
		if group.Definition.Icon != nil && matches(*group.Definition.Icon) {
			return *group.Definition.Icon, nil
		}
	}
	sets, err := tx.IconSets()
	if err != nil {
		return "", err
	}
	for _, set := range sets {
		for _, icon := range set.Icons {
			if matches(icon.URL) {
				return icon.URL, nil
			}
		}
	}
	source, err := tx.ProfileIconSource(key.ID)
	if err != nil {
		return "", err
	}
	if err := webImageRemoteURL(source); err != nil {
		return "", err
	}
	if webImageSourceID(source) != key.ID {
		return "", webAPIError(409, "source_target_changed", "profile icon source no longer matches its identity")
	}
	return source, nil
}

func webImageResourceKey(raw string) WebAPISourceResource {
	return WebAPISourceResource{Kind: "icon-image", ID: webImageSourceID(raw)}
}
