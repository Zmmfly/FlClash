//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"strconv"
)

var webAPIBackupMetadataBuckets = []string{"profiles", "scripts", "rules", "disabled-rules", "proxy-groups", "custom-proxies", "proxy-dialers", "rule-providers", "mitm-modules", "icon-sets", "settings"}

func webAPIBackupBudgetError(dimension string, limit int64) *WebAPIError {
	problem := webAPIError(413, "backup_budget_exceeded", "stored resources exceed the backup budget")
	problem.Details = map[string]any{"dimension": dimension, "limit": strconv.FormatInt(limit, 10)}
	return problem
}

func webAPIBackupInvalidContent() *WebAPIError {
	return webAPIError(409, "backup_invalid_store_content", "stored content does not have the canonical resource encoding")
}

// maxContentBytes bounds all expanded input: metadata plus decoded source bytes.
func (tx *WebAPIReadTx) CheckBackupBudget(maxRows int, maxMetadataBytes, maxContentBytes int64) error {
	if maxRows < 0 || maxMetadataBytes < 0 || maxContentBytes < 0 {
		return webAPIError(500, "invalid_backup_budget", "backup limits must be nonnegative")
	}
	rows := 0
	metadataRemaining := maxMetadataBytes
	expandedRemaining := maxContentBytes
	chargeMetadata := func(size int64) error {
		if size > metadataRemaining {
			return webAPIBackupBudgetError("metadata_bytes", maxMetadataBytes)
		}
		if size > expandedRemaining {
			return webAPIBackupBudgetError("expanded_bytes", maxContentBytes)
		}
		metadataRemaining -= size
		expandedRemaining -= size
		return nil
	}
	chargeRow := func() error {
		if rows >= maxRows {
			return webAPIBackupBudgetError("rows", int64(maxRows))
		}
		rows++
		return nil
	}
	for _, name := range webAPIBackupMetadataBuckets {
		bucket := tx.tx.Bucket([]byte(name))
		if bucket == nil {
			return webAPIError(409, "backup_invalid_store", "backup resource bucket is missing")
		}
		cursor := bucket.Cursor()
		for key, raw := cursor.First(); key != nil; key, raw = cursor.Next() {
			if err := chargeRow(); err != nil {
				return err
			}
			if raw == nil {
				return webAPIError(409, "backup_invalid_store", "backup resource bucket contains nested data")
			}
			if err := chargeMetadata(int64(len(key))); err != nil {
				return err
			}
			if err := chargeMetadata(int64(len(raw))); err != nil {
				return err
			}
		}
	}
	bucket := tx.tx.Bucket([]byte("content"))
	if bucket == nil {
		return webAPIError(409, "backup_invalid_store", "backup content bucket is missing")
	}
	cursor := bucket.Cursor()
	for key, raw := cursor.First(); key != nil; key, raw = cursor.Next() {
		if err := chargeRow(); err != nil {
			return err
		}
		start, end, err := webAPIBackupContentRange(raw)
		if err != nil {
			return err
		}
		if err = chargeMetadata(int64(len(key))); err != nil {
			return err
		}
		if err = chargeMetadata(int64(len(raw) - (end - start))); err != nil {
			return err
		}
		payload := raw[start:end]
		if len(payload)%4 != 0 {
			return webAPIBackupInvalidContent()
		}
		padding := 0
		if len(payload) > 0 && payload[len(payload)-1] == '=' {
			padding++
			if payload[len(payload)-2] == '=' {
				padding++
			}
		}
		decoded := int64(len(payload)/4)*3 - int64(padding)
		if decoded > expandedRemaining {
			return webAPIBackupBudgetError("expanded_bytes", maxContentBytes)
		}
		if !webAPIBackupBase64(payload, padding) {
			return webAPIBackupInvalidContent()
		}
		expandedRemaining -= decoded
	}
	return nil
}

func webAPIBackupBase64Value(c byte) (byte, bool) {
	switch {
	case c >= 'A' && c <= 'Z':
		return c - 'A', true
	case c >= 'a' && c <= 'z':
		return c - 'a' + 26, true
	case c >= '0' && c <= '9':
		return c - '0' + 52, true
	case c == '+':
		return 62, true
	case c == '/':
		return 63, true
	default:
		return 0, false
	}
}

func webAPIBackupBase64(payload []byte, padding int) bool {
	var last byte
	for _, c := range payload[:len(payload)-padding] {
		value, valid := webAPIBackupBase64Value(c)
		if !valid {
			return false
		}
		last = value
	}
	return padding == 0 || padding == 1 && last&3 == 0 || padding == 2 && last&15 == 0
}

func webAPIBackupWhitespace(raw []byte, i int) int {
	for i < len(raw) {
		switch raw[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}

func webAPIBackupStringEnd(raw []byte, start int) int {
	for i := start + 1; i < len(raw); i++ {
		if raw[i] == '\\' {
			i++
			continue
		}
		if raw[i] == '"' {
			return i + 1
		}
	}
	return -1
}

func webAPIBackupValueEnd(raw []byte, start int) int {
	if start >= len(raw) {
		return -1
	}
	if raw[start] == '"' {
		return webAPIBackupStringEnd(raw, start)
	}
	if raw[start] == '{' || raw[start] == '[' {
		depth := 0
		for i := start; i < len(raw); i++ {
			switch raw[i] {
			case '"':
				i = webAPIBackupStringEnd(raw, i) - 1
				if i < 0 {
					return -1
				}
			case '{', '[':
				depth++
				if depth > 64 {
					return -1
				}
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
		return -1
	}
	for i := start; i < len(raw); i++ {
		switch raw[i] {
		case ',', ']', '}', ' ', '\t', '\r', '\n':
			return i
		}
	}
	return len(raw)
}

func webAPIBackupContentRange(raw []byte) (int, int, error) {
	if !json.Valid(raw) {
		return 0, 0, webAPIBackupInvalidContent()
	}
	i := webAPIBackupWhitespace(raw, 0)
	if i >= len(raw) || raw[i] != '{' {
		return 0, 0, webAPIBackupInvalidContent()
	}
	i++
	seen := uint8(0)
	payloadStart, payloadEnd := 0, 0
	for {
		i = webAPIBackupWhitespace(raw, i)
		if i >= len(raw) || raw[i] != '"' {
			return 0, 0, webAPIBackupInvalidContent()
		}
		keyEnd := webAPIBackupStringEnd(raw, i)
		if keyEnd < 0 {
			return 0, 0, webAPIBackupInvalidContent()
		}
		key := raw[i:keyEnd]
		bit := uint8(0)
		switch {
		case bytes.Equal(key, []byte(`"key"`)):
			bit = 1
		case bytes.Equal(key, []byte(`"mediaType"`)):
			bit = 2
		case bytes.Equal(key, []byte(`"sha256"`)):
			bit = 4
		case bytes.Equal(key, []byte(`"bytes"`)):
			bit = 8
		case bytes.Equal(key, []byte(`"modifiedAt"`)):
			bit = 16
		default:
			return 0, 0, webAPIBackupInvalidContent()
		}
		if seen&bit != 0 {
			return 0, 0, webAPIBackupInvalidContent()
		}
		seen |= bit
		i = webAPIBackupWhitespace(raw, keyEnd)
		if i >= len(raw) || raw[i] != ':' {
			return 0, 0, webAPIBackupInvalidContent()
		}
		i = webAPIBackupWhitespace(raw, i+1)
		end := webAPIBackupValueEnd(raw, i)
		if end < 0 {
			return 0, 0, webAPIBackupInvalidContent()
		}
		if bit == 8 {
			if raw[i] == '"' {
				payloadStart = i + 1
				payloadEnd = end - 1
			} else if !bytes.Equal(raw[i:end], []byte("null")) {
				return 0, 0, webAPIBackupInvalidContent()
			}
		}
		i = webAPIBackupWhitespace(raw, end)
		if i >= len(raw) {
			return 0, 0, webAPIBackupInvalidContent()
		}
		if raw[i] == '}' {
			if seen&15 != 15 || webAPIBackupWhitespace(raw, i+1) != len(raw) {
				return 0, 0, webAPIBackupInvalidContent()
			}
			return payloadStart, payloadEnd, nil
		}
		if raw[i] != ',' {
			return 0, 0, webAPIBackupInvalidContent()
		}
		i++
	}
}
