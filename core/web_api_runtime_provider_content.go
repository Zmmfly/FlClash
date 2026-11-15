//go:build webpanel && !android

package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	bolt "github.com/metacubex/bbolt"
	"github.com/metacubex/mihomo/component/trie"
	cp "github.com/metacubex/mihomo/constant/provider"
	ruleCommon "github.com/metacubex/mihomo/rules/common"
	rp "github.com/metacubex/mihomo/rules/provider"
)

const webRuntimeWriteProvider WebAPIRuntimeAction = "write-provider-source"
const webRuntimeSideloadProvider WebAPIRuntimeAction = "sideload-provider-source"

type WebAPIRuntimeProviderHead struct {
	Source    WebAPIRuntimeProviderSource `json:"source"`
	Prepared  bool                        `json:"prepared"`
	MediaType string                      `json:"mediaType"`
	Size      int64                       `json:"size"`
	SHA256    string                      `json:"sha256"`
}
type WebAPIRuntimeProviderContent struct {
	WebAPIRuntimeProviderHead
	Bytes []byte `json:"bytes"`
}
type WebAPIRuntimeProviderPreview struct {
	SourceVersion string `json:"sourceVersion"`
	Format        string `json:"format"`
	Text          string `json:"text"`
	ReadOnly      bool   `json:"readOnly"`
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
}
type WebAPIRuntimeProviderWrite struct {
	WebAPIMutation
	SourceVersion string `json:"sourceVersion"`
	Bytes         string `json:"bytes"`
}
type webRuntimeDesiredProviderWrite struct {
	ID         WebAPIRuntimeProviderID      `json:"id"`
	Request    WebAPIRuntimeProviderWrite   `json:"request"`
	Descriptor webRuntimeProviderDescriptor `json:"descriptor"`
}

func webRuntimeProviderMedia(d webRuntimeProviderDescriptor) string {
	if d.Format == "mrs" {
		return "application/octet-stream"
	}
	if d.Source.Purpose == "proxy-providers" || d.Format == "" || d.Format == "yaml" {
		return "application/yaml"
	}
	return "text/plain; charset=utf-8"
}
func webRuntimeProviderHead(d webRuntimeProviderDescriptor, p webRuntimePreparedProvider) WebAPIRuntimeProviderHead {
	source := d.Source
	if p.Phase != "" {
		source.Phase = p.Phase
	}
	head := WebAPIRuntimeProviderHead{Source: source, Prepared: p.Phase == "ready", MediaType: webRuntimeProviderMedia(d)}
	if head.Prepared {
		hash := sha256.Sum256(p.Bytes)
		head.SHA256 = hex.EncodeToString(hash[:])
		head.Size = int64(len(p.Bytes))
	}
	return head
}

func (rt *WebAPIRuntime) providerContentRoute(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/runtime/provider-sources/")
	for _, suffix := range []string{":write", ":sideload"} {
		if strings.HasSuffix(path, suffix) {
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				WebAPIWriteError(w, webAPIError(405, "method_not_allowed", "POST is required"))
				return
			}
			rt.writeProviderHTTP(w, r, WebAPIRuntimeProviderID(strings.TrimSuffix(path, suffix)), suffix == ":sideload")
			return
		}
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		WebAPIWriteError(w, webAPIError(405, "method_not_allowed", "GET is required"))
		return
	}
	if _, err := WebAPIQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) > 2 {
		WebAPIWriteError(w, webAPIError(404, "not_found", "provider resource does not exist"))
		return
	}
	id := WebAPIRuntimeProviderID(parts[0])
	leaf := ""
	if len(parts) == 2 {
		leaf = parts[1]
	}
	if leaf != "" && leaf != "content" && leaf != "download" && leaf != "preview" {
		WebAPIWriteError(w, webAPIError(404, "not_found", "provider content resource does not exist"))
		return
	}
	var descriptor webRuntimeProviderDescriptor
	var prepared webRuntimePreparedProvider
	revision, err := rt.store.View(r.Context(), func(tx *WebAPIReadTx) error {
		var err error
		descriptor, err = rt.lookupProviderTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		prepared, _ = tx.runtimeProvider(id)
		return nil
	})
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	head := webRuntimeProviderHead(descriptor, prepared)
	if leaf == "" {
		rt.profileAnswer(w, r, revision, head)
		return
	}
	if !head.Prepared {
		WebAPIWriteError(w, webAPIError(409, "provider_source_unavailable", "prepare this source before reading its content"))
		return
	}
	switch leaf {
	case "content":
		rt.profileAnswer(w, r, revision, WebAPIRuntimeProviderContent{WebAPIRuntimeProviderHead: head, Bytes: prepared.Bytes})
	case "download":
		release, err := WebAPIAcquireCommitAuthorization(r.Context())
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		release()
		w.Header().Set("Content-Type", head.MediaType)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Disposition", `attachment; filename="provider-source.bin"`)
		w.Header().Set("X-FlClash-Revision", string(revision))
		w.Header().Set("X-FlClash-Store-Id", rt.Events.storeID)
		w.Header().Set("X-FlClash-Source-Version", descriptor.Source.SourceVersion)
		_, _ = w.Write(prepared.Bytes)
	case "preview":
		preview := WebAPIRuntimeProviderPreview{SourceVersion: descriptor.Source.SourceVersion, Format: "text", SHA256: head.SHA256, Size: head.Size}
		if descriptor.Format == "mrs" {
			text, err := webRuntimeMRSPreview(prepared.Bytes, descriptor.Behavior)
			if err != nil {
				WebAPIWriteError(w, err)
				return
			}
			preview.Format, preview.Text, preview.ReadOnly = "mrs", text, true
		} else {
			if !utf8.Valid(prepared.Bytes) {
				WebAPIWriteError(w, webAPIError(422, "invalid_provider_content", "text provider is not UTF-8"))
				return
			}
			preview.Text = string(prepared.Bytes)
		}
		rt.profileAnswer(w, r, revision, preview)
	default:
		WebAPIWriteError(w, webAPIError(404, "not_found", "provider content resource does not exist"))
	}
}

func webRuntimeProviderWriteBytes(p WebAPIRuntimeProviderWrite) ([]byte, error) {
	if len(p.Bytes) > ((WebAPIMaxContentBytes+2)/3)*4 {
		return nil, webAPIError(413, "content_too_large", "provider content exceeds 32 MiB")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(p.Bytes)
	if err != nil || base64.StdEncoding.EncodeToString(data) != p.Bytes {
		return nil, webAPIError(400, "invalid_base64", "provider bytes must be canonical base64")
	}
	if len(data) > WebAPIMaxContentBytes {
		return nil, webAPIError(413, "content_too_large", "provider content exceeds 32 MiB")
	}
	return data, nil
}

func (rt *WebAPIRuntime) writeProviderHTTP(w http.ResponseWriter, r *http.Request, id WebAPIRuntimeProviderID, sideload bool) {
	if _, err := WebAPIQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	var p WebAPIRuntimeProviderWrite
	if err := WebAPIDecodeJSONLimit(w, r, &p, 48<<20); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	if _, err := webRuntimeProviderWriteBytes(p); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	action := webRuntimeWriteProvider
	if sideload {
		action = webRuntimeSideloadProvider
	}
	command := "runtime/provider-source:" + string(action)
	request := struct {
		ID WebAPIRuntimeProviderID `json:"id"`
		WebAPIRuntimeProviderWrite
	}{id, p}
	if receipt, found, err := rt.preflightApply(r.Context(), p.WebAPIMutation, command, request); err != nil {
		WebAPIWriteError(w, err)
		return
	} else if found {
		WebAPIWriteReceipt(w, receipt)
		return
	}
	if rt.backend.SafeMode {
		WebAPIWriteError(w, webRuntimeSafeMode())
		return
	}
	_, existed := rt.providers.sessions.LoadOrStore(p.OperationID, context.WithoutCancel(r.Context()))
	receipt, err := rt.store.Transaction(r.Context(), p.WebAPIMutation, command, request, func(tx *WebAPIWriteTx) (any, error) {
		descriptor, err := rt.lookupProviderTx(r.Context(), tx.WebAPIReadTx, id)
		if err != nil {
			return nil, err
		}
		if descriptor.Source.SourceVersion != p.SourceVersion {
			return nil, webAPIError(409, "source_target_changed", "provider source changed since this edit began")
		}
		if !sideload && descriptor.Format == "mrs" {
			return nil, webAPIError(409, "read_only_provider", "MRS preview is read-only; upload an explicit typed binary replacement")
		}
		previous, _ := tx.runtimeProvider(id)
		previous.Descriptor, previous.Phase, previous.OperationID = descriptor, "pending", p.OperationID
		if err = tx.saveRuntimeProvider(previous); err != nil {
			return nil, err
		}
		return tx.RecordRuntimeIntent(p.OperationID, action, webRuntimeDesiredProviderWrite{ID: id, Request: p, Descriptor: descriptor})
	})
	if err != nil {
		if !existed {
			rt.providers.sessions.Delete(p.OperationID)
		}
		WebAPIWriteError(w, err)
		return
	}
	if !receipt.Replayed {
		select {
		case rt.wake <- struct{}{}:
		default:
		}
	} else if !existed {
		rt.providers.sessions.Delete(p.OperationID)
	}
	WebAPIWriteReceipt(w, receipt)
}

func (rt *WebAPIRuntime) prepareProviderBytes(ctx context.Context, d webRuntimeProviderDescriptor, data []byte) ([]byte, error) {
	if d.Source.Purpose == "proxy-providers" {
		plain, err := webContentDecompress(data)
		if err != nil {
			return nil, err
		}
		doc, err := webRuntimeDocument(plain)
		if err != nil {
			proxies, convertErr := webRuntimeConvert(string(plain))
			if convertErr != nil {
				return nil, err
			}
			plain, err = webRuntimeMarshalYAML(map[string]any{"proxies": proxies})
			if err != nil {
				return nil, err
			}
			doc, err = webRuntimeDocument(plain)
			if err != nil {
				return nil, err
			}
		}
		var root map[string]any
		if err = doc.Decode(&root); err != nil {
			return nil, err
		}
		if err = webRuntimePathPolicy(root, ""); err != nil {
			return nil, err
		}
		entries, ok := root["proxies"].([]any)
		if !ok {
			return nil, webAPIError(422, "invalid_provider_content", "proxy provider must contain a proxy list")
		}
		definitions := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			definition, ok := entry.(map[string]any)
			if !ok {
				return nil, webAPIError(422, "invalid_provider_content", "proxy definitions must be objects")
			}
			definitions = append(definitions, definition)
		}
		if err = webRuntimeDefinitions(definitions); err != nil {
			return nil, err
		}
		issues, err := webRuntimeValidateDefinitions(ctx, ProxiesParams{Proxies: definitions})
		if err != nil {
			return nil, err
		}
		if len(issues) > 0 {
			return nil, webAPIError(422, "invalid_provider_content", "provider contains invalid proxy definitions")
		}
		return webRuntimeMarshalYAML(map[string]any{"proxies": definitions})
	}
	if d.Format == "mrs" {
		if _, err := webRuntimeMRSPreview(data, d.Behavior); err != nil {
			return nil, err
		}
		return data, nil
	}
	if err := webRuntimeValidateRuleProvider(ctx, d, data); err != nil {
		return nil, err
	}
	return data, nil
}

func webRuntimeMRSPreview(data []byte, behaviorName string) (string, error) {
	if err := webContentCheckMRS(data); err != nil {
		return "", err
	}
	behavior, err := cp.ParseBehavior(behaviorName)
	if err != nil || (behavior != cp.Domain && behavior != cp.IPCIDR) {
		return "", webAPIError(422, "invalid_provider_content", "MRS requires domain or IP-CIDR behavior")
	}
	var output webContentPreviewWriter
	err = rp.ConvertToMrs(data, behavior, cp.MrsRule, &output)
	if output.overflow {
		return "", webAPIError(413, "content_too_large", "provider preview exceeds 32 MiB")
	}
	if err != nil {
		return "", webAPIError(422, "invalid_provider_content", "MRS content does not match the committed provider behavior")
	}
	return output.String(), nil
}

func webRuntimeValidateRuleProvider(ctx context.Context, d webRuntimeProviderDescriptor, data []byte) error {
	if len(data) > WebAPIMaxContentBytes {
		return webAPIError(413, "content_too_large", "provider content exceeds 32 MiB")
	}
	if !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
		return webAPIError(422, "invalid_provider_content", "rule provider text must be UTF-8 without NUL")
	}
	var entries []string
	switch d.Format {
	case "", "yaml":
		// Shared work accounting must precede any YAML Decode or native parser.
		doc, err := webRuntimeDocument(data)
		if err != nil {
			return err
		}
		var schema struct {
			Payload *[]string `yaml:"payload"`
			Rules   *[]string `yaml:"rules"`
		}
		if err = doc.Decode(&schema); err != nil || (schema.Payload == nil && schema.Rules == nil) {
			return webAPIError(422, "invalid_provider_content", "YAML rule provider requires a payload or rules list")
		}
		for _, list := range []*[]string{schema.Payload, schema.Rules} {
			if list != nil {
				entries = append(entries, (*list)...)
			}
		}
	case "text":
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "//") {
				entries = append(entries, line)
			}
		}
	default:
		return webAPIError(422, "invalid_provider_content", "unsupported rule provider format")
	}
	behavior, err := cp.ParseBehavior(d.Behavior)
	if err != nil {
		return webAPIError(422, "invalid_provider_content", "unsupported rule provider behavior")
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		switch behavior {
		case cp.Domain:
			if _, err := trie.ValidAndSplitDomain(entry); err != nil {
				return webAPIError(422, "invalid_provider_content", "invalid domain rule provider entry")
			}
		case cp.IPCIDR:
			if _, ok := parseCIDR(entry); !ok {
				return webAPIError(422, "invalid_provider_content", "invalid IP-CIDR rule provider entry")
			}
		case cp.Classical:
			kind, payload, target, parameters := ruleCommon.ParseRulePayload(entry, false)
			if kind == "MATCH" || kind == "RULE-SET" || kind == "SUB-RULE" || len(entry) > 65536 {
				return webAPIError(422, "invalid_provider_content", "invalid classical rule provider entry")
			}
			syntax := &webAPIRuleSyntax{providers: map[string]bool{}}
			if _, err := syntax.parse(kind, payload, target, parameters, nil); err != nil {
				return webAPIError(422, "invalid_provider_content", "invalid classical rule provider syntax")
			}
		}
	}
	return nil
}

func (rt *WebAPIRuntime) writeProvider(ctx context.Context, p webRuntimeDesiredProviderWrite) (any, error) {
	defer rt.providers.sessions.Delete(p.Request.OperationID)
	if original, ok := rt.providers.sessions.Load(p.Request.OperationID); ok {
		ctx = webRuntimeRestoreAuthorization(ctx, original.(context.Context))
	}
	ctx = context.WithValue(ctx, webRuntimeAcceptedEvaluationKey{}, p.Request.OperationID)
	data, err := webRuntimeProviderWriteBytes(p.Request)
	if err != nil {
		return nil, err
	}
	data, err = rt.prepareProviderBytes(ctx, p.Descriptor, data)
	if err != nil {
		return nil, err
	}
	err = rt.store.updateRuntime(ctx, func(tx *bolt.Tx) error {
		read := &WebAPIReadTx{tx}
		current, err := rt.lookupProviderTx(ctx, read, p.ID)
		if err != nil {
			return err
		}
		if current.Source.SourceVersion != p.Request.SourceVersion {
			return webAPIError(409, "source_target_changed", "provider source changed during this edit")
		}
		prior, err := read.runtimeProvider(p.ID)
		if err != nil {
			return err
		}
		if prior.OperationID != p.Request.OperationID || prior.Phase != "pending" {
			return webAPIError(409, "operation_superseded", "a newer provider edit or reconciliation superseded this write")
		}
		return (&WebAPIWriteTx{WebAPIReadTx: read}).saveRuntimeProvider(webRuntimePreparedProvider{Descriptor: current, Bytes: data, Phase: "ready", OperationID: p.Request.OperationID})
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": p.ID, "sourceVersion": p.Request.SourceVersion, "prepared": true, "requiresApply": true}, nil
}

func (rt *WebAPIRuntime) markProviderOperationFailure(record webRuntimeRecord, phase WebAPIRuntimePhase) {
	_ = rt.store.updateRuntime(context.Background(), func(tx *bolt.Tx) error {
		return webRuntimeMarkProviderPhase(&WebAPIWriteTx{WebAPIReadTx: &WebAPIReadTx{tx}}, record, phase)
	})
}

func webRuntimeMarkProviderPhase(tx *WebAPIWriteTx, record webRuntimeRecord, phase WebAPIRuntimePhase) error {
	var id WebAPIRuntimeProviderID
	switch record.State.Action {
	case webRuntimeRefreshProvider:
		var desired webRuntimeDesiredProviderRefresh
		if json.Unmarshal(record.Desired, &desired) != nil {
			return nil
		}
		id = desired.Request.ID
	case webRuntimeWriteProvider, webRuntimeSideloadProvider:
		var desired webRuntimeDesiredProviderWrite
		if json.Unmarshal(record.Desired, &desired) != nil {
			return nil
		}
		id = desired.ID
	default:
		return nil
	}
	value, err := tx.runtimeProvider(id)
	if err != nil || value.OperationID != record.State.OperationID {
		return nil
	}
	value.Phase = string(phase)
	return tx.saveRuntimeProvider(value)
}
