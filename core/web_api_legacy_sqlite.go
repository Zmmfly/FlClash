//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
)

type webNativeRow map[string]any

type webNativeTable struct{ name, schema string }

var webNativeTables = []webNativeTable{
	{"profiles", `id INTEGER PRIMARY KEY, type TEXT NOT NULL, label TEXT NOT NULL, current_group_name TEXT, url TEXT NOT NULL, age_secret_key TEXT, last_update_date INTEGER, extend_type TEXT NOT NULL, script_id INTEGER, match_target TEXT, auto_update_duration_millis INTEGER NOT NULL, subscription_info TEXT, auto_update INTEGER NOT NULL CHECK(auto_update IN(0,1)), selected_map TEXT NOT NULL, unfold_set TEXT NOT NULL, "order" INTEGER, overrides TEXT`},
	{"scripts", `id INTEGER PRIMARY KEY, label TEXT NOT NULL, last_update_time INTEGER NOT NULL, url TEXT, "order" INTEGER`},
	{"rules", `id INTEGER PRIMARY KEY, profile_id INTEGER REFERENCES profiles(id) ON DELETE CASCADE, rule_action TEXT NOT NULL, content TEXT, rule_target TEXT, rule_provider TEXT, sub_rule TEXT, no_resolve INTEGER NOT NULL DEFAULT 0, src INTEGER NOT NULL DEFAULT 0, "order" TEXT`},
	{"disabled_rules", `profile_id INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE, rule_id INTEGER NOT NULL REFERENCES rules(id) ON DELETE CASCADE, PRIMARY KEY(profile_id,rule_id)`},
	{"proxy_groups", `id INTEGER PRIMARY KEY, profile_id INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE, name TEXT NOT NULL, type TEXT NOT NULL, proxies TEXT, "use" TEXT, definition TEXT NOT NULL DEFAULT '{}', "order" TEXT`},
	{"custom_proxies", `id INTEGER PRIMARY KEY, definition TEXT NOT NULL, "order" TEXT, empty_options TEXT`},
	{"clash_providers", `id INTEGER PRIMARY KEY, kind TEXT NOT NULL, label TEXT NOT NULL, url TEXT NOT NULL, behavior TEXT, format TEXT, "order" INTEGER`},
	{"proxy_dialers", `profile_id INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE, proxy_id INTEGER NOT NULL REFERENCES custom_proxies(id) ON DELETE CASCADE, target TEXT NOT NULL, PRIMARY KEY(profile_id,proxy_id)`},
	{"mitm_modules", `id INTEGER PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', url TEXT, enabled INTEGER NOT NULL DEFAULT 1, "order" INTEGER, arguments TEXT NOT NULL DEFAULT '{}', resources TEXT NOT NULL DEFAULT '{}', system TEXT, scripts INTEGER NOT NULL DEFAULT 0, last_update_time INTEGER NOT NULL`},
	{"icon_sets", `id INTEGER PRIMARY KEY, name TEXT NOT NULL, url TEXT NOT NULL, icons TEXT NOT NULL, last_update_time INTEGER, "order" INTEGER`},
	{"icon_records", `url TEXT PRIMARY KEY NOT NULL, last_accessed INTEGER NOT NULL`},
	{"ip_qualities", `ip TEXT PRIMARY KEY NOT NULL, quality TEXT NOT NULL, checked_at INTEGER NOT NULL`},
}

func webNativeTime(seconds int64) string { return time.Unix(seconds, 0).UTC().Format(time.RFC3339) }

type webNativeImageConnection interface{ Serialize() ([]byte, error) }

func webNativeOpen(ctx context.Context, data []byte, readonly bool) (*sql.Conn, func(), error) {
	name := ":memory:"
	directory := ""
	if len(data) > 0 {
		var err error
		directory, err = os.MkdirTemp("", "flclash-native-archive-")
		if err != nil {
			return nil, nil, err
		}
		file := filepath.Join(directory, "archive.sqlite")
		handle, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			os.RemoveAll(directory)
			return nil, nil, err
		}
		_, writeErr := handle.Write(data)
		closeErr := handle.Close()
		if err = errors.Join(writeErr, closeErr); err != nil {
			os.RemoveAll(directory)
			return nil, nil, err
		}
		uri := url.URL{Scheme: "file", Path: file}
		query := uri.Query()
		if readonly {
			query.Set("mode", "ro")
			query.Set("immutable", "1")
		}
		uri.RawQuery = query.Encode()
		name = uri.String()
	}
	remove := func() {
		if directory != "" {
			_ = os.RemoveAll(directory)
		}
	}
	db, err := sql.Open("sqlite", name)
	if err != nil {
		remove()
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		remove()
		return nil, nil, err
	}
	cleanup := func() { conn.Close(); db.Close(); remove() }
	for _, limit := range [][2]int{{0, WebAPIBackupMetadataLimit}, {1, 1 << 20}, {2, 128}, {3, 32}, {4, 16}, {7, 0}, {9, 128}, {10, 0}} {
		if _, err = sqlite.Limit(conn, limit[0], limit[1]); err != nil {
			cleanup()
			return nil, nil, err
		}
	}
	if _, err = conn.ExecContext(ctx, "PRAGMA trusted_schema=OFF; PRAGMA temp_store=MEMORY; PRAGMA cache_size=-4096;"); err != nil {
		cleanup()
		return nil, nil, err
	}
	if readonly {
		if _, err = conn.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
			cleanup()
			return nil, nil, err
		}
	} else {
		if _, err = conn.ExecContext(ctx, "PRAGMA page_size=4096; PRAGMA max_page_count=16384;"); err != nil {
			cleanup()
			return nil, nil, err
		}
	}
	return conn, cleanup, nil
}

func webNativeSerialize(conn *sql.Conn) ([]byte, error) {
	var data []byte
	err := conn.Raw(func(raw any) error {
		image, ok := raw.(webNativeImageConnection)
		if !ok {
			return errors.New("SQLite image interface unavailable")
		}
		var err error
		data, err = image.Serialize()
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(data) > WebAPIBackupDatabaseLimit {
		return nil, webBackupInvalid("backup_database_limit")
	}
	return data, nil
}

func webNativeReadDatabase(ctx context.Context, data []byte) (webBackupData, error) {
	return webNativeReadDatabasePlan(ctx, data, nil)
}

func webNativeReadDatabasePlan(ctx context.Context, data []byte, changes *[]WebAPIBackupChange) (webBackupData, error) {
	out := webBackupData{}
	if len(data) < 100 || len(data) > WebAPIBackupDatabaseLimit || !bytes.Equal(data[:16], []byte("SQLite format 3\x00")) {
		return out, webBackupInvalid("backup_invalid_database")
	}
	version := int(binary.BigEndian.Uint32(data[60:64]))
	switch version {
	case 2, 3, 10, 20, 21, 23:
	default:
		return out, webAPIError(400, "backup_unsupported_version", "native SQLite schema was not verified against a released archive")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, cleanup, err := webNativeOpen(ctx, data, true)
	if err != nil {
		return out, webBackupInvalid("backup_invalid_database")
	}
	defer cleanup()
	allowed := map[string]bool{"profile_rule_mapping": version < 17}
	for _, t := range webNativeTables {
		allowed[t.name] = true
	}
	rows, err := conn.QueryContext(ctx, "SELECT type,name,sql FROM sqlite_schema")
	if err != nil {
		return out, webBackupInvalid("backup_invalid_database")
	}
	tables := map[string]bool{}
	for rows.Next() {
		var kind, name string
		var definition sql.NullString
		if rows.Scan(&kind, &name, &definition) != nil {
			rows.Close()
			return out, webBackupInvalid("backup_invalid_database")
		}
		if len(definition.String) > 65536 {
			rows.Close()
			return out, webBackupInvalid("backup_database_schema")
		}
		switch kind {
		case "table":
			if !allowed[name] || strings.Contains(strings.ToUpper(definition.String), "VIRTUAL") {
				rows.Close()
				return out, webBackupInvalid("backup_database_schema")
			}
			tables[name] = true
		case "index":
		default:
			rows.Close()
			return out, webBackupInvalid("backup_database_schema")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || !tables["profiles"] || !tables["scripts"] || !tables["rules"] {
		return out, webBackupInvalid("backup_database_schema")
	}
	var integrity string
	if conn.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&integrity) != nil || integrity != "ok" {
		return out, webBackupInvalid("backup_invalid_database")
	}
	native := map[string][]webNativeRow{}
	var count int
	for _, t := range append(append([]webNativeTable{}, webNativeTables...), webNativeTable{name: "profile_rule_mapping"}) {
		if !tables[t.name] {
			continue
		}
		rows, err := conn.QueryContext(ctx, `PRAGMA table_xinfo("`+t.name+`")`)
		if err != nil {
			return out, webBackupInvalid("backup_database_schema")
		}
		for rows.Next() {
			var cid, notnull, pk, hidden int
			var name, typ string
			var def any
			if rows.Scan(&cid, &name, &typ, &notnull, &def, &pk, &hidden) != nil || hidden != 0 {
				rows.Close()
				return out, webBackupInvalid("backup_database_schema")
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, webBackupInvalid("backup_database_schema")
		}
		rows, err = conn.QueryContext(ctx, `SELECT * FROM "`+t.name+`" LIMIT 100001`)
		if err != nil {
			return out, webBackupInvalid("backup_database_schema")
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return out, err
		}
		for rows.Next() {
			count++
			if count > 100000 {
				rows.Close()
				return out, webBackupInvalid("backup_row_limit")
			}
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if rows.Scan(ptrs...) != nil {
				rows.Close()
				return out, webBackupInvalid("backup_invalid_database")
			}
			row := webNativeRow{}
			for i, col := range cols {
				row[col] = vals[i]
			}
			native[t.name] = append(native[t.name], row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, webBackupInvalid("backup_invalid_database")
		}
	}
	if version < 17 {
		var err error
		if changes == nil {
			err = webNativeTransformEarly(native)
		} else {
			err = webNativeCompatibilityTransform(native, changes)
		}
		if err != nil {
			return out, err
		}
	}
	for table, items := range native {
		if table == "icon_records" || table == "ip_qualities" || table == "profile_rule_mapping" {
			continue
		}
		for _, row := range items {
			if err = webNativeDecodeRow(&out, table, row); err != nil {
				return webBackupData{}, err
			}
		}
	}
	return out, webBackupValidateData(out, nil)
}

func webNativeCamel(name string) string {
	parts := strings.Split(name, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

func webNativeDecodeRow(out *webBackupData, table string, row webNativeRow) error {
	v := map[string]any{}
	for name, value := range row {
		key := webNativeCamel(name)
		if value == nil {
			v[key] = nil
			continue
		}
		switch name {
		case "id", "profile_id", "script_id", "rule_id", "proxy_id":
			s, err := webBackupDecimal(value)
			if err != nil {
				return err
			}
			value = s
		case "last_update_date", "last_update_time":
			s, err := webBackupTimestamp(value)
			if err != nil {
				return err
			}
			value = s
		case "auto_update", "no_resolve", "src", "enabled":
			n, ok := value.(int64)
			if !ok || (n != 0 && n != 1) {
				return webBackupInvalid("backup_invalid_boolean")
			}
			value = n == 1
		case "selected_map", "unfold_set", "arguments", "resources", "icons", "empty_options", "proxies", "use", "definition":
			s, ok := value.(string)
			if !ok {
				return webBackupInvalid("backup_invalid_json")
			}
			var target any
			d := json.NewDecoder(strings.NewReader(s))
			d.UseNumber()
			if d.Decode(&target) != nil {
				return webBackupInvalid("backup_invalid_json")
			}
			value = target
		}
		v[key] = value
	}
	decode := func(target any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		b, err = webContentNormalizeCollections(b, reflect.TypeOf(target))
		if err != nil {
			return err
		}
		if err = webContentStrictJSON(b, target); err != nil {
			return webBackupInvalid("backup_invalid_row")
		}
		return nil
	}
	switch table {
	case "profiles":
		if v["type"] == nil {
			v["type"] = "file"
			if webBackupString(v["url"]) != "" {
				v["type"] = "url"
			}
		}
		if v["extendType"] == nil {
			v["extendType"] = v["overwriteType"]
		}
		delete(v, "overwriteType")
		if v["ageSecretKey"] == nil {
			v["ageSecretKey"] = ""
		}
		if s, ok := v["subscriptionInfo"].(string); ok {
			var values map[string]json.Number
			if json.Unmarshal([]byte(s), &values) != nil {
				return webBackupInvalid("backup_invalid_json")
			}
			info := map[string]string{}
			for _, k := range []string{"upload", "download", "total", "expire"} {
				n := values[k]
				if n == "" {
					n = "0"
				}
				if _, e := n.Int64(); e != nil {
					return webBackupInvalid("backup_invalid_json")
				}
				info[k] = n.String()
			}
			v["subscriptionInfo"] = info
		}
		if s, ok := v["overrides"].(string); ok {
			var overrides WebAPIProfileOverrides
			if err := webContentStrictJSON([]byte(s), &overrides); err != nil || !strings.HasPrefix(strings.TrimSpace(s), "{") {
				v["overridesRawDocument"] = s
				v["overrides"] = nil
			} else {
				v["overrides"] = &overrides
			}
		}
		var p WebAPIProfile
		if err := decode(&p); err != nil {
			return err
		}
		out.Profiles = append(out.Profiles, p)
	case "scripts":
		var s WebAPIScript
		if err := decode(&s); err != nil {
			return err
		}
		out.Scripts = append(out.Scripts, s)
	case "rules":
		var r WebAPIRule
		if err := decode(&r); err != nil {
			return err
		}
		out.Rules = append(out.Rules, r)
	case "disabled_rules":
		var r WebAPIDisabledRule
		if err := decode(&r); err != nil {
			return err
		}
		out.DisabledRules = append(out.DisabledRules, r)
	case "proxy_groups":
		def, ok := v["definition"].(map[string]any)
		if !ok {
			return webBackupInvalid("backup_invalid_json")
		}
		for _, key := range []string{"name", "type", "proxies", "use"} {
			def[key] = v[key]
			delete(v, key)
		}
		if layout, ok := def["memberLayout"]; ok {
			v["memberLayout"] = layout
			delete(def, "memberLayout")
		}
		var g WebAPIProxyGroup
		if err := decode(&g); err != nil {
			return err
		}
		out.ProxyGroups = append(out.ProxyGroups, g)
	case "custom_proxies":
		var p WebAPICustomProxy
		if err := decode(&p); err != nil {
			return err
		}
		out.CustomProxies = append(out.CustomProxies, p)
	case "proxy_dialers":
		var d WebAPIProxyDialer
		if err := decode(&d); err != nil {
			return err
		}
		out.ProxyDialers = append(out.ProxyDialers, d)
	case "clash_providers":
		if v["kind"] != "rule" {
			return webBackupLegacyTransform("legacy_proxy_provider")
		}
		delete(v, "kind")
		if v["behavior"] == nil {
			v["behavior"] = "classical"
		}
		if v["format"] == nil {
			v["format"] = "yaml"
		}
		var p WebAPIClashProvider
		if err := decode(&p); err != nil {
			return err
		}
		out.RuleProviders = append(out.RuleProviders, p)
	case "mitm_modules":
		var m WebAPIMitmModule
		if err := decode(&m); err != nil {
			return err
		}
		out.MitmModules = append(out.MitmModules, m)
	case "icon_sets":
		var i WebAPIIconSet
		if err := decode(&i); err != nil {
			return err
		}
		out.IconSets = append(out.IconSets, i)
	default:
		return webBackupInvalid("backup_database_schema")
	}
	return nil
}

func webNativeTransformEarly(tables map[string][]webNativeRow) error {
	for _, p := range tables["profiles"] {
		if p["overwrite_type"] != "standard" && p["overwrite_type"] != "script" {
			return webBackupLegacyTransform("custom_overwrite")
		}
	}
	if len(tables["proxy_groups"])+len(tables["proxy_dialers"]) > 0 {
		return webBackupLegacyTransform("legacy_profile_groups")
	}
	for _, p := range tables["custom_proxies"] {
		if p["profile_id"] != nil {
			return webBackupLegacyTransform("profile_owned_proxy")
		}
		delete(p, "profile_id")
	}
	owners := map[int64]webNativeRow{}
	for _, link := range tables["profile_rule_mapping"] {
		rule, ok := link["rule_id"].(int64)
		if !ok {
			return webBackupInvalid("backup_invalid_reference")
		}
		profile := link["profile_id"]
		if profile == nil && link["scene"] == nil || profile != nil && link["scene"] == "added" {
			if _, exists := owners[rule]; exists {
				return webBackupLegacyTransform("multiply_owned_rule")
			}
			owners[rule] = link
			continue
		}
		if profile != nil && link["scene"] == "disabled" {
			tables["disabled_rules"] = append(tables["disabled_rules"], webNativeRow{"profile_id": profile, "rule_id": rule})
			continue
		}
		return webBackupLegacyTransform("legacy_rule_scene")
	}
	for _, rule := range tables["rules"] {
		id, ok := rule["id"].(int64)
		if !ok {
			return webBackupInvalid("backup_invalid_id")
		}
		link, ok := owners[id]
		if !ok {
			return webBackupLegacyTransform("unowned_rule")
		}
		rule["profile_id"] = link["profile_id"]
		rule["order"] = link["order"]
	}
	return nil
}

func webNativeProviderEntry(p WebAPIClashProvider) string {
	sum := md5.Sum([]byte(string(p.ID) + "@" + p.URL))
	return "providers/rules/" + hex.EncodeToString(sum[:])
}

func webNativeAttachFiles(candidate *webBackupCandidate, archive webBackupArchive) error {
	expected := map[string]bool{"config.json": true, "database.sqlite": true}
	add := func(name string, key WebAPIContentKey, media string) error {
		expected[name] = true
		data, exists := archive.Entries[name]
		if !exists {
			return nil
		}
		candidate.Contents = append(candidate.Contents, WebAPIContent{Key: key, Bytes: data, MediaType: media, SHA256: webContentDigest(data)})
		return nil
	}
	for _, p := range candidate.Data.Profiles {
		if p.Type != "custom" {
			_ = add("profiles/"+webBackupName(p.ID, "yaml"), WebAPIContentKey{Kind: WebAPIContentProfile, ID: p.ID}, "application/yaml")
		}
	}
	for _, s := range candidate.Data.Scripts {
		_ = add("scripts/"+webBackupName(s.ID, "js"), WebAPIContentKey{Kind: WebAPIContentScript, ID: s.ID}, "text/javascript")
	}
	for _, p := range candidate.Data.RuleProviders {
		if p.URL == "" {
			_ = add(webNativeProviderEntry(p), WebAPIContentKey{Kind: WebAPIContentRuleProvider, ID: p.ID}, "application/octet-stream")
		}
	}
	for _, m := range candidate.Data.MitmModules {
		if webBackupRemote(m.URL) {
			continue
		}
		_ = add("mitm/modules/"+webBackupName(m.ID, "txt"), WebAPIContentKey{Kind: WebAPIContentMitmModule, ID: m.ID}, "text/plain")
		for key, name := range m.Resources {
			if strings.HasPrefix(key, "http://") || strings.HasPrefix(key, "https://") {
				continue
			}
			if !webBackupSafeName(name) || strings.Contains(name, "/") {
				return webBackupInvalid("backup_invalid_resource_name")
			}
			_ = add("mitm/resources/"+name, WebAPIContentKey{Kind: WebAPIContentMitmResource, ID: m.ID, Name: name}, "application/octet-stream")
		}
	}
	if err := webBackupNoExtraEntries(archive, expected); err != nil {
		return err
	}
	return webBackupValidateData(candidate.Data, candidate.Contents)
}

func webNativeReadArchive(ctx context.Context, archive webBackupArchive) (webBackupCandidate, error) {
	return webNativeReadArchivePlan(ctx, archive, nil)
}

func webNativeReadArchivePlan(ctx context.Context, archive webBackupArchive, changes *[]WebAPIBackupChange) (webBackupCandidate, error) {
	candidate := webBackupCandidate{NativeConfig: archive.Entries["config.json"], ConfigurationOnly: true}
	var config map[string]json.RawMessage
	if err := webContentStrictJSON(candidate.NativeConfig, &config); err != nil {
		return candidate, webBackupInvalid("backup_invalid_config")
	}
	var version int
	if b, ok := config["version"]; ok {
		if json.Unmarshal(b, &version) != nil {
			return candidate, webBackupInvalid("backup_invalid_config")
		}
	}
	if version > 1 || version < 0 {
		return candidate, webAPIError(400, "backup_unsupported_version", "native configuration version is not supported")
	}
	if version == 0 {
		return candidate, webBackupLegacyTransform("string_id_configuration_v0")
	}
	if database, ok := archive.Entries["database.sqlite"]; ok {
		candidate.ConfigurationOnly = false
		var err error
		candidate.Data, err = webNativeReadDatabasePlan(ctx, database, changes)
		if err != nil {
			return candidate, err
		}
		candidate.NativeSchema = int(binary.BigEndian.Uint32(database[60:64]))
	}
	return candidate, webNativeAttachFiles(&candidate, archive)
}

func webNativeEncodeDatabase(ctx context.Context, data webBackupData) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, cleanup, err := webNativeOpen(ctx, nil, false)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if _, err = conn.ExecContext(ctx, "PRAGMA journal_mode=DELETE; PRAGMA foreign_keys=ON; PRAGMA user_version=23;"); err != nil {
		return nil, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, t := range webNativeTables {
		if _, err = tx.ExecContext(ctx, `CREATE TABLE "`+t.name+`" (`+t.schema+`)`); err != nil {
			return nil, err
		}
	}
	insert := func(table string, value any) error {
		row, err := webNativeEncodeRow(table, value)
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(row))
		for k := range row {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		names := make([]string, len(keys))
		marks := make([]string, len(keys))
		args := make([]any, len(keys))
		for i, k := range keys {
			names[i] = `"` + k + `"`
			marks[i] = "?"
			args[i] = row[k]
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO "`+table+`" (`+strings.Join(names, ",")+`) VALUES (`+strings.Join(marks, ",")+`)`, args...)
		return err
	}
	for _, v := range data.Profiles {
		if err = insert("profiles", v); err != nil {
			return nil, err
		}
	}
	for _, v := range data.Scripts {
		if err = insert("scripts", v); err != nil {
			return nil, err
		}
	}
	for _, v := range data.Rules {
		if err = insert("rules", v); err != nil {
			return nil, err
		}
	}
	for _, v := range data.DisabledRules {
		if err = insert("disabled_rules", v); err != nil {
			return nil, err
		}
	}
	for _, v := range data.ProxyGroups {
		if err = insert("proxy_groups", v); err != nil {
			return nil, err
		}
	}
	for _, v := range data.CustomProxies {
		if err = insert("custom_proxies", v); err != nil {
			return nil, err
		}
	}
	for _, v := range data.RuleProviders {
		if err = insert("clash_providers", v); err != nil {
			return nil, err
		}
	}
	for _, v := range data.ProxyDialers {
		if err = insert("proxy_dialers", v); err != nil {
			return nil, err
		}
	}
	for _, v := range data.MitmModules {
		if err = insert("mitm_modules", v); err != nil {
			return nil, err
		}
	}
	for _, v := range data.IconSets {
		if err = insert("icon_sets", v); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return webNativeSerialize(conn)
}

func webNativeSnake(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c >= 'A' && c <= 'Z' {
			b.WriteByte('_')
			c += 32
		}
		b.WriteRune(c)
	}
	return b.String()
}

func webNativeEncodeRow(table string, value any) (webNativeRow, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	v := map[string]any{}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err = d.Decode(&v); err != nil {
		return nil, err
	}
	if table == "proxy_groups" {
		def, ok := v["definition"].(map[string]any)
		if !ok {
			return nil, errors.New("invalid group definition")
		}
		for _, key := range []string{"name", "type", "proxies", "use"} {
			v[key] = def[key]
			delete(def, key)
		}
		if v["memberLayout"] != nil {
			def["memberLayout"] = v["memberLayout"]
		}
		delete(v, "memberLayout")
	}
	if table == "profiles" {
		if raw, ok := v["overridesRawDocument"]; ok {
			v["overrides"] = raw
			delete(v, "overridesRawDocument")
		} else if v["overrides"] != nil {
			raw, err := json.Marshal(v["overrides"])
			if err != nil {
				return nil, err
			}
			v["overrides"] = string(raw)
		}
		if info, ok := v["subscriptionInfo"].(map[string]any); ok {
			for _, key := range []string{"upload", "download", "total", "expire"} {
				s, _ := info[key].(string)
				n, err := strconv.ParseInt(s, 10, 64)
				if err != nil {
					return nil, err
				}
				info[key] = n
			}
		}
	}
	if table == "clash_providers" {
		v["kind"] = "rule"
	}
	row := webNativeRow{}
	for key, value := range v {
		if value == nil {
			row[webNativeSnake(key)] = nil
			continue
		}
		switch key {
		case "id", "profileId", "ruleId", "proxyId", "scriptId":
			s, ok := value.(string)
			if !ok {
				return nil, errors.New("invalid id")
			}
			n, err := webAPIInteger(s, true)
			if err != nil {
				return nil, err
			}
			value = n
		case "lastUpdateDate", "lastUpdateTime":
			s, ok := value.(string)
			if !ok {
				return nil, errors.New("invalid timestamp")
			}
			t, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				return nil, err
			}
			value = t.Unix()
		case "selectedMap", "unfoldSet", "arguments", "resources", "icons", "emptyOptions", "proxies", "use", "definition", "subscriptionInfo":
			b, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			value = string(b)
		default:
			if b, ok := value.(bool); ok {
				if b {
					value = int64(1)
				} else {
					value = int64(0)
				}
			}
			if n, ok := value.(json.Number); ok {
				i, err := n.Int64()
				if err != nil {
					return nil, err
				}
				value = i
			}
		}
		row[webNativeSnake(key)] = value
	}
	return row, nil
}

func webNativeEncodeArchive(ctx context.Context, candidate webBackupCandidate, config json.RawMessage) ([]byte, error) {
	if err := webBackupValidateData(candidate.Data, candidate.Contents); err != nil {
		return nil, err
	}
	var settings map[string]json.RawMessage
	if err := webContentStrictJSON(config, &settings); err != nil {
		return nil, webBackupInvalid("backup_invalid_config")
	}
	settings["version"] = json.RawMessage(`1`)
	config, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	database, err := webNativeEncodeDatabase(ctx, candidate.Data)
	if err != nil {
		return nil, err
	}
	entries := map[string][]byte{"config.json": config, "database.sqlite": database}
	providers := map[WebAPIID]WebAPIClashProvider{}
	for _, v := range candidate.Data.RuleProviders {
		providers[v.ID] = v
	}
	modules := map[WebAPIID]WebAPIMitmModule{}
	for _, v := range candidate.Data.MitmModules {
		modules[v.ID] = v
	}
	for _, v := range candidate.Contents {
		name := ""
		switch v.Key.Kind {
		case WebAPIContentProfile:
			name = "profiles/" + webBackupName(v.Key.ID, "yaml")
		case WebAPIContentScript:
			name = "scripts/" + webBackupName(v.Key.ID, "js")
		case WebAPIContentRuleProvider:
			p := providers[v.Key.ID]
			if p.URL == "" {
				name = webNativeProviderEntry(p)
			}
		case WebAPIContentMitmModule:
			if !webBackupRemote(modules[v.Key.ID].URL) {
				name = "mitm/modules/" + webBackupName(v.Key.ID, "txt")
			}
		case WebAPIContentMitmResource:
			m := modules[v.Key.ID]
			if !webBackupRemote(m.URL) {
				for source, logicalKey := range m.Resources {
					if logicalKey == v.Key.Name && !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
						name = "mitm/resources/" + logicalKey
						break
					}
				}
			}
		}
		if name == "" {
			continue
		}
		if prior, ok := entries[name]; ok && !bytes.Equal(prior, v.Bytes) {
			return nil, webBackupInvalid("backup_content_collision")
		}
		entries[name] = v.Bytes
	}
	return webWriteBackupArchive("native-v1", entries)
}
