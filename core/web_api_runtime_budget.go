//go:build webpanel && !android

package core

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"errors"
	"io"

	"go.yaml.in/yaml/v3"
)

const webRuntimeSnapshotBytes = 256 << 20

type webRuntimeSnapshotBudget struct {
	used  int64
	items map[string]int64
}

func webRuntimeSnapshotTooLarge() *WebAPIError {
	return webAPIError(413, "runtime_snapshot_too_large", "runtime snapshot sources exceed 256 MiB")
}

func (budget *webRuntimeSnapshotBudget) reserve(key string, size int64) error {
	if budget == nil {
		return nil
	}
	if budget.items == nil {
		budget.items = map[string]int64{}
	}
	if size < 0 {
		return webRuntimeSnapshotTooLarge()
	}
	previous := budget.items[key]
	if size <= previous {
		return nil
	}
	growth := size - previous
	if growth > webRuntimeSnapshotBytes-budget.used {
		return webRuntimeSnapshotTooLarge()
	}
	budget.used += growth
	budget.items[key] = size
	return nil
}

// The store writes these records through json.Marshal with a canonical lower-case
// byte field. Inspect its encoded span before json.Unmarshal can allocate a copy.
func webRuntimeStoredByteSize(raw []byte) (int64, error) {
	marker := []byte(`"bytes":`)
	at := bytes.LastIndex(raw, marker)
	if at < 0 {
		return 0, webAPIError(409, "runtime_source_invalid", "stored source has no canonical byte field")
	}
	value := bytes.TrimLeft(raw[at+len(marker):], " \t\r\n")
	if bytes.HasPrefix(value, []byte("null")) {
		return 0, nil
	}
	if len(value) < 2 || value[0] != '"' {
		return 0, webAPIError(409, "runtime_source_invalid", "stored byte field is not canonical")
	}
	end := bytes.IndexByte(value[1:], '"')
	if end < 0 {
		return 0, webAPIError(409, "runtime_source_invalid", "stored byte field is incomplete")
	}
	encoded := value[1 : 1+end]
	if len(encoded)%4 != 0 || bytes.IndexByte(encoded, '\\') >= 0 {
		return 0, webAPIError(409, "runtime_source_invalid", "stored byte field is not canonical base64")
	}
	length := int64(len(encoded)/4) * 3
	if len(encoded) > 0 && encoded[len(encoded)-1] == '=' {
		length--
		if len(encoded) > 1 && encoded[len(encoded)-2] == '=' {
			length--
		}
	}
	return length, nil
}

func (budget *webRuntimeSnapshotBudget) content(tx *WebAPIReadTx, key WebAPIContentKey) (WebAPIContent, error) {
	if budget == nil {
		return tx.Content(key)
	}
	name, _, err := webAPIContentKey(key)
	if err != nil {
		return WebAPIContent{}, err
	}
	bucket := tx.tx.Bucket([]byte("content"))
	if bucket == nil || bucket.Get([]byte(name)) == nil {
		return WebAPIContent{}, webAPIError(404, "not_found", "content source does not exist")
	}
	size, err := webRuntimeStoredByteSize(bucket.Get([]byte(name)))
	if err != nil {
		return WebAPIContent{}, err
	}
	if err = budget.reserve("content/"+name, size); err != nil {
		return WebAPIContent{}, err
	}
	return tx.Content(key)
}

func (budget *webRuntimeSnapshotBudget) record(tx *WebAPIReadTx, bucketName, key string) error {
	if budget == nil {
		return nil
	}
	bucket := tx.tx.Bucket([]byte(bucketName))
	if bucket == nil {
		return nil
	}
	return budget.reserve("metadata/"+bucketName+"/"+key, int64(len(bucket.Get([]byte(key)))))
}

func (budget *webRuntimeSnapshotBudget) bucket(tx *WebAPIReadTx, name string) error {
	if budget == nil {
		return nil
	}
	bucket := tx.tx.Bucket([]byte(name))
	if bucket == nil {
		return nil
	}
	return bucket.ForEach(func(key, raw []byte) error { return budget.reserve("metadata/"+name+"/"+string(key), int64(len(raw))) })
}

func (budget *webRuntimeSnapshotBudget) metadata(tx *WebAPIReadTx) error {
	if budget == nil {
		return nil
	}
	// These are exactly the global metadata collections the custom candidate
	// builder reads; reserve their stored bytes before decoding the collections.
	for _, name := range []string{"profiles", "custom-proxies", "proxy-groups", "rules", "disabled-rules", "proxy-dialers", "rule-providers"} {
		bucket := tx.tx.Bucket([]byte(name))
		if bucket == nil {
			continue
		}
		if err := bucket.ForEach(func(key, raw []byte) error { return budget.reserve("metadata/"+name+"/"+string(key), int64(len(raw))) }); err != nil {
			return err
		}
	}
	return nil
}

func (budget *webRuntimeSnapshotBudget) decompress(data []byte, key WebAPIContentKey) ([]byte, error) {
	if budget == nil {
		return webContentDecompress(data)
	}
	name, _, err := webAPIContentKey(key)
	if err != nil {
		return nil, err
	}
	identity := "content/" + name
	limit := int64(WebAPIMaxContentBytes)
	available := budget.items[identity] + webRuntimeSnapshotBytes - budget.used
	if available < limit {
		limit = available
	}
	var reader io.ReadCloser
	switch {
	case bytes.HasPrefix(data, []byte{0x1f, 0x8b}):
		reader, err = gzip.NewReader(bytes.NewReader(data))
	case len(data) > 1 && data[0] == 0x78 && (uint16(data[0])<<8|uint16(data[1]))%31 == 0:
		reader, err = zlib.NewReader(bytes.NewReader(data))
	default:
		return data, nil
	}
	if err != nil {
		return nil, webAPIError(400, "invalid_compressed_content", "profile source has invalid compression")
	}
	defer reader.Close()
	plain, err := webContentReadLimit(reader, limit)
	if err != nil {
		if limit < WebAPIMaxContentBytes {
			return nil, webRuntimeSnapshotTooLarge()
		}
		return nil, err
	}
	if err = budget.reserve(identity, int64(len(plain))); err != nil {
		return nil, err
	}
	return plain, nil
}

func (budget *webRuntimeSnapshotBudget) provider(tx *WebAPIReadTx, id WebAPIRuntimeProviderID) (webRuntimePreparedProvider, error) {
	if budget == nil {
		return tx.runtimeProvider(id)
	}
	bucket := tx.tx.Bucket([]byte(webRuntimeProviderBucket))
	if bucket == nil || bucket.Get([]byte(id)) == nil {
		return webRuntimePreparedProvider{}, webAPIError(404, "provider_source_unavailable", "provider source is not prepared")
	}
	raw := bucket.Get([]byte(id))
	var head struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return webRuntimePreparedProvider{}, err
	}
	if head.Phase != "ready" {
		return webRuntimePreparedProvider{Phase: head.Phase}, nil
	}
	size, err := webRuntimeStoredByteSize(raw)
	if err != nil {
		return webRuntimePreparedProvider{}, err
	}
	if err = budget.reserve("provider/"+string(id), size); err != nil {
		return webRuntimePreparedProvider{}, err
	}
	return tx.runtimeProvider(id)
}

type webRuntimeYAMLBuffer struct {
	bytes.Buffer
	limit    int64
	overflow bool
}

func (buffer *webRuntimeYAMLBuffer) Write(data []byte) (int, error) {
	if int64(len(data)) > buffer.limit-int64(buffer.Len()) {
		buffer.overflow = true
		return 0, errors.New("runtime YAML limit")
	}
	return buffer.Buffer.Write(data)
}
func webRuntimeMarshalYAML(value any) ([]byte, error) {
	buffer := &webRuntimeYAMLBuffer{limit: WebAPIMaxContentBytes}
	encoder := yaml.NewEncoder(buffer)
	err := errors.Join(encoder.Encode(value), encoder.Close())
	if buffer.overflow {
		return nil, webAPIError(413, "content_too_large", "rendered runtime YAML exceeds 32 MiB")
	}
	if err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
