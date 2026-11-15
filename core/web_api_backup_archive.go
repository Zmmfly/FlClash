//go:build webpanel && !android

package core

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	WebAPIBackupArchiveLimit     = 32 << 20
	WebAPIBackupExpandedLimit    = 128 << 20
	WebAPIBackupEntryLimit       = 10000
	WebAPIBackupMetadataLimit    = 8 << 20
	WebAPIBackupDatabaseLimit    = 64 << 20
	WebAPIBackupCompressionRatio = 200
)

type webBackupEntry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type webBackupManifest struct {
	Format  string           `json:"format"`
	Version int              `json:"version"`
	Entries []webBackupEntry `json:"entries"`
}

type webBackupArchive struct {
	Format  string
	Entries map[string][]byte
}

func webBackupInvalid(code string) error {
	return webAPIError(400, code, "backup archive is invalid or exceeds a declared limit")
}

func webBackupSafeName(name string) bool {
	if name == "" || len(name) > 512 || !utf8.ValidString(name) || !fs.ValidPath(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\:\x00\r\n") || strings.HasPrefix(name, "/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.TrimSpace(part) != part || strings.HasSuffix(part, ".") {
			return false
		}
	}
	return true
}

func webBackupEntrySize(name string) uint64 {
	switch name {
	case "database.sqlite":
		return WebAPIBackupDatabaseLimit
	case "manifest.json", "data.json", "config.json":
		return WebAPIBackupMetadataLimit
	default:
		return WebAPIMaxContentBytes
	}
}

func webReadBackupArchive(data []byte) (webBackupArchive, error) {
	out := webBackupArchive{Entries: map[string][]byte{}}
	if len(data) == 0 || len(data) > WebAPIBackupArchiveLimit {
		return out, webBackupInvalid("backup_archive_limit")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(zr.File) == 0 {
		return out, webBackupInvalid("backup_invalid_zip")
	}
	if len(zr.File) > WebAPIBackupEntryLimit {
		return out, webBackupInvalid("backup_entry_limit")
	}
	seen := map[string]bool{}
	var expanded uint64
	for _, file := range zr.File {
		name := file.Name
		if !webBackupSafeName(name) || seen[name] {
			return out, webBackupInvalid("backup_entry_name")
		}
		seen[name] = true
		if !file.Mode().IsRegular() || file.Flags&1 != 0 || (file.Method != zip.Store && file.Method != zip.Deflate) {
			return out, webBackupInvalid("backup_entry_type")
		}
		if file.UncompressedSize64 > webBackupEntrySize(name) || file.UncompressedSize64 > WebAPIBackupExpandedLimit-expanded {
			return out, webBackupInvalid("backup_expanded_limit")
		}
		expanded += file.UncompressedSize64
		if file.UncompressedSize64 > (file.CompressedSize64+1024)*WebAPIBackupCompressionRatio {
			return out, webBackupInvalid("backup_compression_ratio")
		}
	}
	for _, file := range zr.File {
		r, err := file.Open()
		if err != nil {
			return out, webBackupInvalid("backup_invalid_zip")
		}
		body, readErr := io.ReadAll(io.LimitReader(r, int64(file.UncompressedSize64)+1))
		closeErr := r.Close()
		if readErr != nil || closeErr != nil || uint64(len(body)) != file.UncompressedSize64 {
			return out, webBackupInvalid("backup_invalid_zip")
		}
		out.Entries[file.Name] = body
	}
	if manifest, exists := out.Entries["manifest.json"]; exists {
		var m webBackupManifest
		if err := webContentStrictJSON(manifest, &m); err != nil {
			return out, webBackupInvalid("backup_invalid_manifest")
		}
		if m.Format != "flclash-v1" || m.Version != 1 {
			return out, webAPIError(400, "backup_unsupported_version", "backup format or version is not supported")
		}
		if len(m.Entries)+1 != len(out.Entries) || len(m.Entries) > WebAPIBackupEntryLimit-1 {
			return out, webBackupInvalid("backup_manifest_mismatch")
		}
		listed := map[string]bool{}
		for _, entry := range m.Entries {
			body, exists := out.Entries[entry.Name]
			if !exists || listed[entry.Name] || entry.Name == "manifest.json" || entry.Size != int64(len(body)) || entry.SHA256 != webContentDigest(body) {
				return out, webBackupInvalid("backup_manifest_mismatch")
			}
			listed[entry.Name] = true
		}
		if _, exists := out.Entries["data.json"]; !exists {
			return out, webBackupInvalid("backup_manifest_mismatch")
		}
		out.Format = "flclash-v1"
	} else {
		if _, exists := out.Entries["config.json"]; !exists {
			return out, webBackupInvalid("backup_missing_config")
		}
		out.Format = "native-v1"
	}
	return out, nil
}

func webWriteBackupArchive(format string, entries map[string][]byte) ([]byte, error) {
	if format != "flclash-v1" && format != "native-v1" {
		return nil, webAPIError(400, "backup_unsupported_version", "unsupported export format")
	}
	if len(entries) == 0 || len(entries) >= WebAPIBackupEntryLimit {
		return nil, webBackupInvalid("backup_entry_limit")
	}
	names := make([]string, 0, len(entries))
	manifest := webBackupManifest{Format: format, Version: 1, Entries: make([]webBackupEntry, 0, len(entries))}
	var expanded int64
	for name, data := range entries {
		if name == "manifest.json" || !webBackupSafeName(name) {
			return nil, webBackupInvalid("backup_entry_name")
		}
		if uint64(len(data)) > webBackupEntrySize(name) {
			return nil, webBackupInvalid("backup_expanded_limit")
		}
		expanded += int64(len(data))
		if expanded > WebAPIBackupExpandedLimit {
			return nil, webBackupInvalid("backup_expanded_limit")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		manifest.Entries = append(manifest.Entries, webBackupEntry{Name: name, Size: int64(len(entries[name])), SHA256: webContentDigest(entries[name])})
	}
	var buf bytes.Buffer
	writer := zip.NewWriter(&webBackupBoundedWriter{Writer: &buf, remaining: WebAPIBackupArchiveLimit})
	write := func(name string, data []byte) error {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0o600)
		w, err := writer.CreateHeader(h)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	if format == "flclash-v1" {
		data, err := json.Marshal(manifest)
		if err != nil {
			return nil, err
		}
		if err = write("manifest.json", data); err != nil {
			return nil, err
		}
	}
	for _, name := range names {
		if err := write(name, entries[name]); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if _, err := webReadBackupArchive(buf.Bytes()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type webBackupBoundedWriter struct {
	io.Writer
	remaining int
}

func (w *webBackupBoundedWriter) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, webAPIError(413, "backup_archive_limit", "archive exceeds download limit")
	}
	n, err := w.Writer.Write(p)
	w.remaining -= n
	return n, err
}

func webContentDigest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func webBackupNoExtraEntries(archive webBackupArchive, expected map[string]bool) error {
	for name := range archive.Entries {
		if !expected[name] {
			return webBackupInvalid("backup_unexpected_entry")
		}
	}
	return nil
}

func webContentReadLimit(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, errors.New("content could not be read")
	}
	if int64(len(b)) > max {
		return nil, webAPIError(413, "content_too_large", "content exceeds the declared limit")
	}
	return b, nil
}
