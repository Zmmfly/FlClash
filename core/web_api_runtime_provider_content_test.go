//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cp "github.com/metacubex/mihomo/constant/provider"
	rp "github.com/metacubex/mihomo/rules/provider"
	"go.yaml.in/yaml/v3"
)

const webRuntimeRuleSourceYAML = "rule-providers:\n  remote: {type: http, url: 'https://provider.test/rules', behavior: domain, format: text}\nrules: ['RULE-SET,remote,DIRECT']\n"

func webRuntimeTestSource(t *testing.T, s *webRuntimeTestRig) WebAPIRuntimeProviderSource {
	t.Helper()
	code, body := s.request(t, "GET", "/api/v1/runtime/provider-sources?profileId="+string(webRuntimeTestProfileID), "", s.cookie, "")
	var result WebAPIEnvelope[[]WebAPIRuntimeProviderSource]
	if code != 200 || json.Unmarshal(body, &result) != nil || len(result.Data) != 1 {
		t.Fatalf("provider source %d %s", code, body)
	}
	return result.Data[0]
}

func webRuntimeProviderWriteBody(t *testing.T, source WebAPIRuntimeProviderSource, revision WebAPIRevision, operationID string, data []byte) string {
	t.Helper()
	body, err := json.Marshal(WebAPIRuntimeProviderWrite{WebAPIMutation: WebAPIMutation{ExpectedRevision: revision, OperationID: operationID}, SourceVersion: source.SourceVersion, Bytes: base64.StdEncoding.EncodeToString(data)})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestWebRuntimeProviderContentReadWritePreservesEditAndRequiresFullApply(t *testing.T) {
	// build/api-runtime-contract.md: side-load prepares immutable content, not an instant native SideUpdate.
	data := []byte("+.edited.example.test\n# preserve editor comment\n")
	var applies atomic.Int32
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{Apply: func(_ context.Context, p WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
		applies.Add(1)
		config, err := os.ReadFile(p.ConfigPath)
		if err != nil {
			t.Error(err)
		}
		var root map[string]any
		if err = yaml.Unmarshal(config, &root); err != nil {
			t.Error(err)
		}
		provider := root["rule-providers"].(map[string]any)["remote"].(map[string]any)
		if provider["type"] != "file" || provider["url"] != nil {
			t.Error("prepared source left a native fetcher enabled")
		}
		prepared, err := os.ReadFile(provider["path"].(string))
		if err != nil || !bytes.Equal(prepared, data) {
			t.Error("full apply silently discarded the edited content")
		}
		return WebAPIRuntimeApplyResult{ActualConfig: "candidate", ProfileID: p.ProfileID, Digest: p.Digest}, nil
	}})
	webRuntimeSeedProfile(t, s, "file", webRuntimeRuleSourceYAML)
	source := webRuntimeTestSource(t, s)
	path := "/api/v1/runtime/provider-sources/" + string(source.ID)
	if code, body := s.request(t, "GET", path, "", s.cookie, ""); code != 200 || !bytes.Contains(body, []byte(`"prepared":false`)) {
		t.Fatalf("unprepared head %d %s", code, body)
	}
	if code, _ := s.request(t, "GET", path+"/content", "", s.cookie, ""); code != 409 {
		t.Fatal("unprepared content was exposed")
	}
	body := webRuntimeProviderWriteBody(t, source, "1", "provider-write-content", data)
	code, receipt := s.request(t, "POST", path+":write", body, s.cookie, "")
	if code != 200 {
		t.Fatalf("write %d %s", code, receipt)
	}
	operation := s.phase(t, "provider-write-content")
	if operation.Phase != WebAPIRuntimeApplied || !bytes.Contains(operation.Result, []byte(`"prepared":true`)) || !bytes.Contains(operation.Result, []byte(`"requiresApply":true`)) || applies.Load() != 0 {
		t.Fatalf("write claimed immediate native application: %+v applies=%d", operation, applies.Load())
	}
	code, content := s.request(t, "GET", path+"/content", "", s.cookie, "")
	var envelope WebAPIEnvelope[WebAPIRuntimeProviderContent]
	if code != 200 || json.Unmarshal(content, &envelope) != nil || envelope.Revision != "2" || !bytes.Equal(envelope.Data.Bytes, data) {
		t.Fatalf("content read %d %s", code, content)
	}
	hash := sha256.Sum256(data)
	if !envelope.Data.Prepared || envelope.Data.SHA256 != hex.EncodeToString(hash[:]) || envelope.Data.Size != int64(len(data)) || envelope.Data.Source.SourceVersion != source.SourceVersion {
		t.Fatal("content head lost revision-independent byte identity")
	}
	code, preview := s.request(t, "GET", path+"/preview", "", s.cookie, "")
	var previewEnvelope WebAPIEnvelope[WebAPIRuntimeProviderPreview]
	if code != 200 || json.Unmarshal(preview, &previewEnvelope) != nil || previewEnvelope.Data.Text != string(data) || previewEnvelope.Data.ReadOnly || previewEnvelope.Data.Format != "text" {
		t.Fatalf("editable preview %d %s", code, preview)
	}
	request, err := http.NewRequest("GET", s.server.URL+path+"/download", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(s.cookie)
	response, err := s.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	download, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !bytes.Equal(download, data) || response.Header.Get("Content-Disposition") != `attachment; filename="provider-source.bin"` || response.Header.Get("X-FlClash-Revision") != "2" || response.Header.Get("X-FlClash-Store-Id") != s.runtime.Events.storeID || response.Header.Get("X-FlClash-Source-Version") != source.SourceVersion {
		t.Fatal("fixed download lost content, revision, store identity, or fixed filename")
	}
	for _, document := range [][]byte{receipt, content, preview} {
		if bytes.Contains(document, []byte("/tmp/")) || bytes.Contains(document, []byte("https://provider.test")) {
			t.Fatal("provider resource leaked server path or fetch authority")
		}
	}
	if code, result := s.request(t, "POST", "/api/v1/runtime:apply", webRuntimeApplyBody(t, "apply-edited-provider", map[string]any{"expectedRevision": "2"}), s.cookie, ""); code != 200 {
		t.Fatalf("apply %d %s", code, result)
	}
	if state := s.phase(t, "apply-edited-provider"); state.Phase != WebAPIRuntimeApplied || applies.Load() != 1 {
		t.Fatal(state)
	}
}

func TestWebRuntimeProviderWritesCASSourceVersionAndLostReceipt(t *testing.T) {
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	webRuntimeSeedProfile(t, s, "file", webRuntimeRuleSourceYAML)
	source := webRuntimeTestSource(t, s)
	path := "/api/v1/runtime/provider-sources/" + string(source.ID)
	second := s.login(t)
	body := webRuntimeProviderWriteBody(t, source, "1", "provider-edit-original", []byte("example.test\n"))
	code, receipt := s.request(t, "POST", path+":sideload", body, s.cookie, "")
	if code != 200 {
		t.Fatalf("sideload %d %s", code, receipt)
	}
	if state := s.phase(t, "provider-edit-original"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	if code, result := s.request(t, "POST", path+":sideload", webRuntimeProviderWriteBody(t, source, "1", "provider-edit-other-session", []byte("other.test\n")), second, ""); code != 409 {
		t.Fatalf("second session overwrote the original revision %d %s", code, result)
	}
	changed := source
	changed.SourceVersion = strings.Repeat("0", 64)
	if code, result := s.request(t, "POST", path+":write", webRuntimeProviderWriteBody(t, changed, "2", "provider-stale-source-version", []byte("new.test\n")), second, ""); code != 409 || !bytes.Contains(result, []byte("source_target_changed")) {
		t.Fatalf("source version upgrade accepted %d %s", code, result)
	}
	_, err := s.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "2", OperationID: "provider-source-changed"}, "test:source-change", source.ID, func(tx *WebAPIWriteTx) (any, error) {
		return nil, tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: webRuntimeTestProfileID}, MediaType: "application/yaml", Bytes: []byte(strings.Replace(webRuntimeRuleSourceYAML, "/rules'", "/new-rules'", 1))})
	})
	if err != nil {
		t.Fatal(err)
	}
	code, replay := s.request(t, "POST", path+":sideload", body, second, "")
	if code != 200 || !bytes.Equal(replay, receipt) {
		t.Fatalf("lost-response receipt changed after source edit %d %s", code, replay)
	}
	if code, _ := s.request(t, "GET", path+"/content", "", second, ""); code != 409 && code != 404 {
		t.Fatal("old source content survived source change")
	}
}

func TestWebRuntimeMRSProviderPreviewIsReadOnlyPureBytes(t *testing.T) {
	var mrs bytes.Buffer
	if err := rp.ConvertToMrs([]byte("+.example.test\n"), cp.Domain, cp.TextRule, &mrs); err != nil {
		t.Fatal(err)
	}
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	profile := strings.Replace(webRuntimeRuleSourceYAML, "format: text", "format: mrs", 1)
	webRuntimeSeedProfile(t, s, "file", profile)
	source := webRuntimeTestSource(t, s)
	path := "/api/v1/runtime/provider-sources/" + string(source.ID)
	if code, _ := s.request(t, "POST", path+":write", webRuntimeProviderWriteBody(t, source, "1", "mrs-denied-text-edit", mrs.Bytes()), s.cookie, ""); code != 409 {
		t.Fatal("MRS accepted an editable-text write")
	}
	if code, body := s.request(t, "POST", path+":sideload", webRuntimeProviderWriteBody(t, source, "1", "mrs-explicit-sideload", mrs.Bytes()), s.cookie, ""); code != 200 {
		t.Fatalf("MRS sideload %d %s", code, body)
	}
	if state := s.phase(t, "mrs-explicit-sideload"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	code, body := s.request(t, "GET", path+"/preview", "", s.cookie, "")
	var result WebAPIEnvelope[WebAPIRuntimeProviderPreview]
	if code != 200 || json.Unmarshal(body, &result) != nil || !result.Data.ReadOnly || result.Data.Format != "mrs" || !strings.Contains(result.Data.Text, "example.test") {
		t.Fatalf("MRS preview %d %s", code, body)
	}
	if s.runtime.workspace.root != nil {
		t.Fatal("MRS metadata/preview/write created a private filesystem artifact")
	}
	if _, err := webRuntimeMRSPreview(mrs.Bytes(), "ipcidr"); err == nil {
		t.Fatal("MRS behavior mismatch was ignored")
	}
}

func TestWebRuntimeProviderWriteStrictEncodingAndBudgets(t *testing.T) {
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	webRuntimeSeedProfile(t, s, "file", webRuntimeRuleSourceYAML)
	source := webRuntimeTestSource(t, s)
	path := "/api/v1/runtime/provider-sources/" + string(source.ID) + ":write"
	for _, encoded := range []string{"YQ", "YR==", "YQ==\n", "a!?!"} {
		body, _ := json.Marshal(WebAPIRuntimeProviderWrite{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "invalid-encoding-write"}, SourceVersion: source.SourceVersion, Bytes: encoded})
		if code, result := s.request(t, "POST", path, string(body), s.cookie, ""); code != 400 || !bytes.Contains(result, []byte("invalid_base64")) {
			t.Fatalf("noncanonical base64 accepted %d %s", code, result)
		}
	}
	if code, _ := s.request(t, "POST", path, strings.Repeat(" ", (48<<20)+1), s.cookie, ""); code != 413 {
		t.Fatal("48 MiB JSON request cap not enforced")
	}
	data := bytes.Repeat([]byte{' '}, WebAPIMaxContentBytes)
	data[0] = '#'
	body := webRuntimeProviderWriteBody(t, source, "1", "exact32mib-provider-write", data)
	if code, result := s.request(t, "POST", path, body, s.cookie, ""); code != 200 {
		t.Fatalf("32 MiB provider bytes lost the supported body budget %d %s", code, result)
	}
	// Race instrumentation of the full 32 MiB journal/cache round trip is
	// intentionally allowed longer than tiny commands; the actor deadline
	// itself remains 75 seconds and is tested independently at 20 ms.
	if state := s.phaseWithin(t, "exact32mib-provider-write", 45*time.Second); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	if _, err := webRuntimeProviderWriteBytes(WebAPIRuntimeProviderWrite{Bytes: base64.StdEncoding.EncodeToString(append(data, ' '))}); err == nil || webRuntimeFailure(err).Status != 413 {
		t.Fatal("decoded byte cap was not enforced")
	}
}

func TestWebRuntimeRuleProviderYAMLWorkBudgetPrecedesDecode(t *testing.T) {
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	var yamlText strings.Builder
	yamlText.WriteString("unused: &wide {\n")
	for i := 0; i < 9000; i++ {
		yamlText.WriteString("  ")
		yamlText.WriteString("key" + strconv.Itoa(i))
		yamlText.WriteString(": 1,\n")
	}
	yamlText.WriteString("}\npayload: ['example.test']\n")
	_, err := s.runtime.prepareProviderBytes(context.Background(), webRuntimeProviderDescriptor{Source: WebAPIRuntimeProviderSource{Purpose: "rule-providers"}, Format: "yaml", Behavior: "domain"}, []byte(yamlText.String()))
	if err == nil || webRuntimeFailure(err).Code != "yaml_too_complex" {
		t.Fatalf("rule provider reached a quadratic Decode without shared accounting: %v", err)
	}
}

func TestWebRuntimeNewProviderWriteCannotBeOverwrittenByEarlierRefresh(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		_, _ = w.Write([]byte("old.example.test\n"))
	}))
	defer remote.Close()
	defer releaseOnce.Do(func() { close(release) })
	_, port, _ := net.SplitHostPort(remote.Listener.Addr().String())
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	s.runtime.workspace.options.SourceFetchOptions = WebAPISourceFetchOptions{ResolveRuntimeSourceIdentity: s.runtime.ResolveRuntimeSourceIdentity, LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, remote.Listener.Addr().String())
	}}
	profile := strings.Replace(webRuntimeRuleSourceYAML, "https://provider.test/rules", "http://provider.test:"+port+"/rules", 1)
	webRuntimeSeedProfile(t, s, "file", profile)
	source := webRuntimeTestSource(t, s)
	refresh, _ := json.Marshal(WebAPIRuntimeProviderRefresh{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "refresh-before-local-edit"}, ID: source.ID, Purpose: source.Purpose})
	if code, body := s.request(t, "POST", "/api/v1/runtime/provider-sources:refresh", string(refresh), s.cookie, ""); code != 200 {
		releaseOnce.Do(func() { close(release) })
		t.Fatalf("%d %s", code, body)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not reach its source")
	}
	path := "/api/v1/runtime/provider-sources/" + string(source.ID)
	data := []byte("new.example.test\n")
	code, body := s.request(t, "POST", path+":write", webRuntimeProviderWriteBody(t, source, "2", "newer-local-provider-edit", data), s.cookie, "")
	releaseOnce.Do(func() { close(release) })
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if state := s.phase(t, "refresh-before-local-edit"); state.Phase != WebAPIRuntimeFailed || state.Error == nil || state.Error.Code != "operation_superseded" {
		t.Fatalf("earlier refresh published over newer edit: %+v", state)
	}
	if state := s.phase(t, "newer-local-provider-edit"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	code, body = s.request(t, "GET", path+"/content", "", s.cookie, "")
	var content WebAPIEnvelope[WebAPIRuntimeProviderContent]
	if code != 200 || json.Unmarshal(body, &content) != nil || !bytes.Equal(content.Data.Bytes, data) || content.Data.Source.Phase != "ready" {
		t.Fatalf("late refresh/failure discarded newer edit %d %s", code, body)
	}
}
