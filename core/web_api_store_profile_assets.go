//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"strings"
)

const webAPIMaxProfileAssets = 32
const webAPIProfileAssetMetadataBytes = 4096

type webAPIProfileAssetRow struct{ key, raw []byte }

func webAPIProfileAssetName(name string) bool {
	const prefix = "zerotier-planet:"
	return strings.HasPrefix(name, prefix) && webAPISourceDigest(strings.TrimPrefix(name, prefix))
}

func webAPIProfileAssetsPrefix(id WebAPIID) []byte {
	return []byte(string(WebAPIContentProfileAsset) + ":" + string(id) + ":")
}

func (tx *WebAPIReadTx) profileAssetRows(id WebAPIID, except string) ([]webAPIProfileAssetRow, int64, error) {
	if _, err := webAPIInteger(string(id), true); err != nil {
		return nil, 0, webAPIError(400, "invalid_id", "profile assets require a decimal profile identity")
	}
	if tx.tx.Bucket([]byte("profiles")).Get([]byte(id)) == nil {
		return nil, 0, webAPIError(404, "not_found", "profile asset owner does not exist")
	}
	prefix := webAPIProfileAssetsPrefix(id)
	rows := make([]webAPIProfileAssetRow, 0, webAPIMaxProfileAssets)
	var total int64
	cursor := tx.tx.Bucket([]byte("content")).Cursor()
	for key, raw := cursor.Seek(prefix); key != nil && bytes.HasPrefix(key, prefix); key, raw = cursor.Next() {
		if string(key) == except {
			continue
		}
		if len(rows) >= webAPIMaxProfileAssets {
			return nil, 0, webAPIError(413, "profile_asset_limit", "profile asset count exceeds the limit")
		}
		if !webAPIProfileAssetName(string(key[len(prefix):])) {
			return nil, 0, webAPIError(409, "invalid_profile_asset", "stored profile asset key is invalid")
		}
		start, end, err := webAPIBackupContentRange(raw)
		if err != nil {
			return nil, 0, err
		}
		if len(raw)-(end-start) > webAPIProfileAssetMetadataBytes {
			return nil, 0, webAPIError(413, "profile_asset_limit", "profile asset metadata exceeds the fixed schema limit")
		}
		payload := raw[start:end]
		if len(payload)%4 != 0 {
			return nil, 0, webAPIError(409, "invalid_profile_asset", "stored profile asset bytes are not canonical base64")
		}
		padding := 0
		if len(payload) > 0 && payload[len(payload)-1] == '=' {
			padding++
			if payload[len(payload)-2] == '=' {
				padding++
			}
		}
		size := int64(len(payload)/4)*3 - int64(padding)
		if size > int64(WebAPIMaxContentBytes)-total {
			return nil, 0, webAPIError(413, "profile_asset_limit", "profile asset aggregate bytes exceed the limit")
		}
		if !webAPIBackupBase64(payload, padding) {
			return nil, 0, webAPIError(409, "invalid_profile_asset", "stored profile asset bytes are not canonical base64")
		}
		total += size
		rows = append(rows, webAPIProfileAssetRow{key: key, raw: raw})
	}
	return rows, total, nil
}

func (tx *WebAPIReadTx) ProfileAssets(id WebAPIID) ([]WebAPIContent, error) {
	rows, _, err := tx.profileAssetRows(id, "")
	if err != nil {
		return nil, err
	}
	assets := make([]WebAPIContent, 0, len(rows))
	for _, row := range rows {
		var content WebAPIContent
		if err := json.Unmarshal(row.raw, &content); err != nil {
			return nil, err
		}
		key, _, err := webAPIContentKey(content.Key)
		if err != nil {
			return nil, err
		}
		if content.Key.Kind != WebAPIContentProfileAsset || content.Key.ID != id || key != string(row.key) || content.Key.Name != "zerotier-planet:"+content.SHA256 {
			return nil, webAPIError(409, "invalid_profile_asset", "profile asset metadata does not match its stored identity")
		}
		assets = append(assets, content)
	}
	return assets, nil
}

func (tx *WebAPIWriteTx) deleteProfileAssets(id WebAPIID) error {
	prefix := webAPIProfileAssetsPrefix(id)
	cursor := tx.tx.Bucket([]byte("content")).Cursor()
	for key, _ := cursor.Seek(prefix); key != nil && bytes.HasPrefix(key, prefix); key, _ = cursor.Next() {
		if err := cursor.Delete(); err != nil {
			return err
		}
	}
	return nil
}
