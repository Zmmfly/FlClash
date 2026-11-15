//go:build webpanel && !android

package core

import (
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const webAPIIconCapacity = 1000
const webAPIIPQualityCapacity = 4096
const webAPIIPQualityTTL = 72 * time.Hour

func webAPIIconURL(value string) bool {
	if len(value) == 0 || len(value) > 1<<18 || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	if strings.HasPrefix(value, "data:image/") {
		comma := strings.IndexByte(value, ',')
		return comma > len("data:image/") && comma < len(value)-1
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func webAPIIconTime(record WebAPIIconRecord) int64 {
	value, _ := strconv.ParseInt(string(record.LastAccessed), 10, 64)
	return value
}

func webAPIQueryIcons(tx *WebAPICacheTx, query string, exact *string) ([]WebAPIIconRecord, error) {
	records, err := tx.IconRecords()
	if err != nil {
		return nil, err
	}
	result := make([]WebAPIIconRecord, 0)
	for _, record := range records {
		if exact != nil && record.URL == *exact || exact == nil && strings.Contains(record.URL, query) {
			result = append(result, record)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := webAPIIconTime(result[i]), webAPIIconTime(result[j])
		if a != b {
			return a > b
		}
		return result[i].URL < result[j].URL
	})
	return result, nil
}

func webAPIPutIcon(tx *WebAPICacheTx, value string, now time.Time, touch bool) (WebAPIIconRecord, error) {
	var result WebAPIIconRecord
	if !webAPIIconURL(value) {
		return result, webAPIError(400, "invalid_icon_url", "expected an HTTP(S) URL or an image data URI")
	}
	records, err := tx.IconRecords()
	if err != nil {
		return result, err
	}
	found := false
	kept := make([]WebAPIIconRecord, 0, len(records)+1)
	for _, record := range records {
		if record.URL == value {
			found = true
		} else {
			kept = append(kept, record)
		}
	}
	if touch && !found {
		return result, webAPIError(404, "not_found", "icon cache entry does not exist")
	}
	result = WebAPIIconRecord{URL: value, LastAccessed: WebAPIInt64(strconv.FormatInt(now.UnixMilli(), 10))}
	if err = tx.PutIconRecord(result); err != nil {
		return result, err
	}
	sort.Slice(kept, func(i, j int) bool {
		a, b := webAPIIconTime(kept[i]), webAPIIconTime(kept[j])
		if a != b {
			return a < b
		}
		return kept[i].URL < kept[j].URL
	})
	for i := 0; i < len(kept)+1-webAPIIconCapacity; i++ {
		if err = tx.DeleteIconRecord(kept[i].URL); err != nil {
			return result, err
		}
	}
	return result, nil
}

func webAPIDeleteIcons(tx *WebAPICacheTx, values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, webAPIError(400, "empty_delete", "at least one icon URL is required")
	}
	records, err := tx.IconRecords()
	if err != nil {
		return nil, err
	}
	existing := make(map[string]bool, len(records))
	for _, record := range records {
		existing[record.URL] = true
	}
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] {
			return nil, webAPIError(409, "duplicate_url", "duplicate icon URL in command")
		}
		seen[value] = true
		if !existing[value] {
			return nil, webAPIError(404, "not_found", "icon cache entry does not exist")
		}
	}
	for _, value := range values {
		if err = tx.DeleteIconRecord(value); err != nil {
			return nil, err
		}
	}
	return append([]string{}, values...), nil
}

func webAPIValidateIPQuality(quality WebAPIIPQuality) error {
	ip, err := netip.ParseAddr(quality.IP)
	if err != nil || ip.Zone() != "" || ip.String() != quality.IP {
		return webAPIError(400, "invalid_ip", "expected a canonical IPv4 or IPv6 address without zone")
	}
	if !webAPIContains([]string{"identMe", "ipApiCom", "ipQuery", "ipLocate", "proxyCheck", "ipApiIs"}, quality.Source) {
		return webAPIError(400, "invalid_quality_source", "unknown IP quality source")
	}
	if !webAPIContains([]string{"residential", "mobile", "business", "hosting"}, quality.Type) {
		return webAPIError(400, "invalid_ip_type", "unknown IP classification")
	}
	if quality.Organization != nil && (len(*quality.Organization) > 4096 || !utf8.ValidString(*quality.Organization) || strings.ContainsAny(*quality.Organization, "\x00\r\n")) {
		return webAPIError(400, "invalid_organization", "invalid IP organization")
	}
	return nil
}

func webAPIIPRecordFresh(record WebAPIIPQualityRecord, now time.Time) bool {
	checked, err := time.Parse(time.RFC3339Nano, record.CheckedAt)
	return err == nil && !checked.After(now) && !checked.Before(now.Add(-webAPIIPQualityTTL))
}

func webAPIQueryIPQuality(tx *WebAPICacheTx, ip string, now time.Time) ([]WebAPIIPQualityRecord, error) {
	parsed, err := netip.ParseAddr(ip)
	if err != nil || parsed.Zone() != "" || parsed.String() != ip {
		return nil, webAPIError(400, "invalid_ip", "expected a canonical IPv4 or IPv6 address without zone")
	}
	records, err := tx.IPQualities()
	if err != nil {
		return nil, err
	}
	result := make([]WebAPIIPQualityRecord, 0, 1)
	for _, record := range records {
		if record.IP == ip && webAPIIPRecordFresh(record, now) {
			result = append(result, record)
		}
	}
	return result, nil
}

func webAPIPutIPQuality(tx *WebAPICacheTx, quality WebAPIIPQuality, now time.Time) (WebAPIIPQualityRecord, error) {
	var result WebAPIIPQualityRecord
	if err := webAPIValidateIPQuality(quality); err != nil {
		return result, err
	}
	records, err := tx.IPQualities()
	if err != nil {
		return result, err
	}
	var kept []WebAPIIPQualityRecord
	for _, record := range records {
		if record.IP == quality.IP {
			continue
		}
		if !webAPIIPRecordFresh(record, now) {
			if err = tx.DeleteIPQuality(record.IP); err != nil {
				return result, err
			}
		} else {
			kept = append(kept, record)
		}
	}
	result = WebAPIIPQualityRecord{IP: quality.IP, Quality: quality, CheckedAt: now.UTC().Format(time.RFC3339Nano)}
	if err = tx.PutIPQuality(result); err != nil {
		return result, err
	}
	sort.Slice(kept, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, kept[i].CheckedAt)
		b, _ := time.Parse(time.RFC3339Nano, kept[j].CheckedAt)
		if !a.Equal(b) {
			return a.Before(b)
		}
		return kept[i].IP < kept[j].IP
	})
	for i := 0; i < len(kept)+1-webAPIIPQualityCapacity; i++ {
		if err = tx.DeleteIPQuality(kept[i].IP); err != nil {
			return result, err
		}
	}
	return result, nil
}

func webAPIDeleteIPQualities(tx *WebAPICacheTx, ips []string) ([]string, error) {
	if len(ips) == 0 {
		return nil, webAPIError(400, "empty_delete", "at least one IP address is required")
	}
	records, err := tx.IPQualities()
	if err != nil {
		return nil, err
	}
	existing := make(map[string]bool, len(records))
	for _, record := range records {
		existing[record.IP] = true
	}
	seen := map[string]bool{}
	for _, ip := range ips {
		if seen[ip] {
			return nil, webAPIError(409, "duplicate_ip", "duplicate IP address in command")
		}
		seen[ip] = true
		if !existing[ip] {
			return nil, webAPIError(404, "not_found", "IP cache entry does not exist")
		}
	}
	for _, ip := range ips {
		if err = tx.DeleteIPQuality(ip); err != nil {
			return nil, err
		}
	}
	return append([]string{}, ips...), nil
}
