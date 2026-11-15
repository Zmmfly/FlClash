//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
	"github.com/metacubex/mihomo/component/trie"
	cp "github.com/metacubex/mihomo/constant/provider"
	ruleCommon "github.com/metacubex/mihomo/rules/common"
	rp "github.com/metacubex/mihomo/rules/provider"
	"go.yaml.in/yaml/v3"
)

func webContentRuleSet(data []byte, allowEmpty bool) (*RuleSetInfo, []byte, error) {
	if len(data) > WebAPIMaxContentBytes {
		return nil, nil, webAPIError(413, "content_too_large", "rule provider exceeds 32 MiB")
	}
	if bytes.HasPrefix(data, zstdMagic) {
		if err := webContentCheckMRS(data); err != nil {
			return nil, nil, err
		}
	} else {
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			return nil, nil, webAPIError(400, "invalid_rule_provider_content", "rule provider must be text or MRS")
		}
		var document yaml.Node
		if yaml.Unmarshal(data, &document) == nil {
			if err := WebAPIValidateYAMLWork(&document); err != nil {
				return nil, nil, err
			}
		}
		_, entries := ruleSetEntries(data)
		for _, entry := range entries {
			if strings.Contains(entry, ",") {
				kind, payload, _, _ := ruleCommon.ParseRulePayload(entry, false)
				if !webAPIContains(webAPIRuleActions, strings.ReplaceAll(kind, "-", "_")) || kind == "MATCH" || kind == "RULE-SET" || kind == "SUB-RULE" || payload == "" {
					return nil, nil, webAPIError(400, "invalid_rule_provider_content", "invalid classical rule provider entry")
				}
			} else if _, ok := parseCIDR(entry); !ok {
				if strings.ContainsAny(entry, "/{}[]\"':;<>\\\\\t ") {
					return nil, nil, webAPIError(400, "invalid_rule_provider_content", "invalid domain or IP entry")
				}
				if _, err := trie.ValidAndSplitDomain(entry); err != nil {
					return nil, nil, webAPIError(400, "invalid_rule_provider_content", "invalid domain rule provider entry")
				}
			}
		}
	}
	info, compiled, err := compileRuleSet(data, allowEmpty)
	if err != nil {
		return nil, nil, webAPIError(400, "invalid_rule_provider_content", "rule provider content could not be compiled")
	}
	return info, compiled, nil
}

func webContentCheckMRS(data []byte) (err error) {
	defer func() {
		if recover() != nil {
			err = webAPIError(400, "invalid_mrs_content", "invalid MRS structure")
		}
	}()
	decoder, e := zstd.NewReader(bytes.NewReader(data), zstd.WithDecoderMaxMemory(WebAPIBackupExpandedLimit), zstd.WithDecoderConcurrency(1))
	if e != nil {
		return webAPIError(400, "invalid_mrs_content", "invalid MRS compression")
	}
	defer decoder.Close()
	raw, e := webContentReadLimit(decoder, WebAPIBackupExpandedLimit)
	if e != nil {
		return e
	}
	if len(raw) < 21 || !bytes.Equal(raw[:4], rp.MrsMagicBytes[:]) {
		return webAPIError(400, "invalid_mrs_content", "invalid MRS header")
	}
	count := binary.BigEndian.Uint64(raw[5:13])
	extra := binary.BigEndian.Uint64(raw[13:21])
	if count == 0 || count > 10000000 || extra > uint64(len(raw)-21) {
		return webAPIError(413, "mrs_complexity_limit", "MRS dimensions exceed the limit")
	}
	body := raw[21+int(extra):]
	if len(body) < 9 || body[0] != 1 {
		return webAPIError(400, "invalid_mrs_content", "invalid MRS body")
	}
	readArray := func(unit int) ([]byte, error) {
		if len(body) < 8 {
			return nil, io.ErrUnexpectedEOF
		}
		n := binary.BigEndian.Uint64(body[:8])
		body = body[8:]
		if n == 0 || n > uint64(len(body)/unit) {
			return nil, io.ErrUnexpectedEOF
		}
		array := body[:int(n)*unit]
		body = body[int(n)*unit:]
		return array, nil
	}
	body = body[1:]
	switch raw[4] {
	case cp.Domain.Byte():
		leaves, e := readArray(8)
		if e != nil {
			return webAPIError(400, "invalid_mrs_content", "invalid MRS leaf dimension")
		}
		bitmap, e := readArray(8)
		if e != nil {
			return webAPIError(400, "invalid_mrs_content", "invalid MRS bitmap dimension")
		}
		labels, e := readArray(1)
		if e != nil || len(body) != 0 {
			return webAPIError(400, "invalid_mrs_content", "invalid MRS label dimension")
		}
		nodes := len(labels) + 1
		if nodes > 8000000 || len(leaves)*8 < nodes || len(bitmap)*8 < 2*nodes-1 {
			return webAPIError(413, "mrs_complexity_limit", "MRS tree dimensions exceed the limit")
		}
		depth := make([]uint16, nodes)
		bit := 0
		next := 1
		edge := 0
		isSet := func(at int) bool { return binary.BigEndian.Uint64(bitmap[(at/64)*8:])&(uint64(1)<<uint(at%64)) != 0 }
		for node := 0; node < nodes; node++ {
			if node >= next {
				return webAPIError(400, "invalid_mrs_content", "disconnected MRS tree")
			}
			for bit < len(bitmap)*8 && !isSet(bit) {
				if next >= nodes || edge >= len(labels) || depth[node] >= 1024 {
					return webAPIError(413, "mrs_complexity_limit", "MRS tree depth exceeds the limit")
				}
				depth[next] = depth[node] + 1
				next++
				edge++
				bit++
			}
			if bit >= len(bitmap)*8 {
				return webAPIError(400, "invalid_mrs_content", "unterminated MRS node")
			}
			bit++
		}
		if next != nodes || edge != len(labels) {
			return webAPIError(400, "invalid_mrs_content", "invalid MRS tree")
		}
		for ; bit < len(bitmap)*8; bit++ {
			if isSet(bit) {
				return webAPIError(400, "invalid_mrs_content", "unexpected MRS tree bits")
			}
		}
	case cp.IPCIDR.Byte():
		if _, e := readArray(32); e != nil || len(body) != 0 {
			return webAPIError(400, "invalid_mrs_content", "invalid MRS CIDR dimensions")
		}
	default:
		return webAPIError(400, "invalid_mrs_content", "unknown MRS behavior")
	}
	return nil
}

type webContentPreviewWriter struct {
	bytes.Buffer
	overflow bool
}

func (w *webContentPreviewWriter) Write(p []byte) (int, error) {
	if len(p) > WebAPIMaxContentBytes-w.Len() {
		w.overflow = true
		return 0, errors.New("preview limit")
	}
	return w.Buffer.Write(p)
}

type WebAPIRuleProviderPreview struct {
	ID       WebAPIID `json:"id"`
	Behavior string   `json:"behavior"`
	Format   string   `json:"format"`
	Text     string   `json:"text"`
}

func (s *WebAPIContentService) previewRuleProvider(w http.ResponseWriter, r *http.Request) {
	if !webAPIGetMethod(w, r) {
		return
	}
	if _, err := WebAPIQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	id := WebAPIID(r.PathValue("id"))
	var provider WebAPIClashProvider
	var content WebAPIContent
	revision, err := s.store.View(r.Context(), func(tx *WebAPIReadTx) error {
		var err error
		provider, err = tx.ClashProvider(id)
		if err != nil {
			return err
		}
		content, err = tx.Content(WebAPIContentKey{Kind: WebAPIContentRuleProvider, ID: id})
		return err
	})
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	text := content.Bytes
	if provider.Format == "mrs" {
		if err = webContentCheckMRS(text); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		behavior := cp.Domain
		if provider.Behavior == "ipcidr" {
			behavior = cp.IPCIDR
		} else if provider.Behavior != "domain" {
			WebAPIWriteError(w, webAPIError(400, "invalid_mrs_content", "MRS behavior is invalid"))
			return
		}
		var writer webContentPreviewWriter
		err = rp.ConvertToMrs(text, behavior, cp.MrsRule, &writer)
		if writer.overflow {
			err = webAPIError(413, "content_too_large", "rule provider preview exceeds 32 MiB")
		}
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		text = writer.Bytes()
	}
	if !utf8.Valid(text) {
		WebAPIWriteError(w, webAPIError(400, "invalid_rule_provider_content", "provider preview is not UTF-8"))
		return
	}
	WebAPIWriteSuccess(w, revision, WebAPIRuleProviderPreview{ID: id, Behavior: provider.Behavior, Format: provider.Format, Text: string(text)})
}
