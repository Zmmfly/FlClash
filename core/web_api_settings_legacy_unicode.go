//go:build webpanel && !android

package core

func webAPISettingsLegacyUnicodeExact(raw []byte) bool {
	inside := false
	for i := 0; i < len(raw); i++ {
		if !inside {
			if raw[i] == '"' {
				inside = true
			}
			continue
		}
		if raw[i] == '"' {
			inside = false
			continue
		}
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		value, ok := webAPISettingsLegacyHex16(raw, i+1)
		if !ok {
			return false
		}
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value >= 0xd800 && value <= 0xdbff {
			if i+6 >= len(raw) || raw[i+5] != '\\' || raw[i+6] != 'u' {
				return false
			}
			low, ok := webAPISettingsLegacyHex16(raw, i+7)
			if !ok || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 10
		} else {
			i += 4
		}
	}
	return true
}

func webAPISettingsLegacyHex16(raw []byte, start int) (uint16, bool) {
	if start < 0 || start+4 > len(raw) {
		return 0, false
	}
	var value uint16
	for _, c := range raw[start : start+4] {
		var part byte
		switch {
		case c >= '0' && c <= '9':
			part = c - '0'
		case c >= 'a' && c <= 'f':
			part = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			part = c - 'A' + 10
		default:
			return 0, false
		}
		value = value<<4 | uint16(part)
	}
	return value, true
}
