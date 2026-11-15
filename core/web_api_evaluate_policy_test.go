//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

type webEvaluateBlockedWriter struct {
	status  int
	header  http.Header
	entered chan struct{}
	release chan struct{}
}

func (w *webEvaluateBlockedWriter) Header() http.Header    { return w.header }
func (w *webEvaluateBlockedWriter) WriteHeader(status int) { w.status = status }
func (w *webEvaluateBlockedWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	close(w.entered)
	<-w.release
	return len(data), nil
}

func TestWebEvaluateBlockedResponseDoesNotBlockLogout(t *testing.T) {
	f := newWebEvaluateRig(t, `function main(c){return c}`)
	data, _ := json.Marshal(webEvaluateRequest())
	ctx := WithWebAPIRequestAuthorization(context.Background(), "synthetic-response-session", func(context.Context) (func(), error) {
		f.auth.mu.Lock()
		return f.auth.mu.Unlock, nil
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/profiles:evaluate", bytes.NewReader(data)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	writer := &webEvaluateBlockedWriter{header: http.Header{}, entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); f.service.ServeHTTP(writer, request) }()
	defer func() { close(writer.release); <-done }()
	select {
	case <-writer.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("evaluation did not enter its response writer")
	}
	if writer.status != 200 || len(f.service.cache) != 1 {
		t.Fatalf("successful evaluation did not reach publication: %d", writer.status)
	}
	if !f.auth.mu.TryLock() {
		t.Fatal("slow evaluation response holds the logout/session mutex")
	}
	clear(f.auth.sessions)
	f.auth.mu.Unlock()
}

func TestWebEvaluateJSONUniqueKeysStructureAndCaseSensitiveProjection(t *testing.T) {
	for _, source := range []string{
		`{"rules":[],"rules":["MATCH,DIRECT"]}`,
		`{"metadata":{"x":null,"x":1}}`,
		`{} {}`, `[]`, `null`, `{"x":1e999}`,
		`{"x":` + strings.Repeat(`[`, webEvaluateMaxJSONDepth+1) + `0` + strings.Repeat(`]`, webEvaluateMaxJSONDepth+1) + `}`,
	} {
		if _, err := webEvaluateDecodeJSON(context.Background(), []byte(source)); err == nil {
			t.Fatalf("invalid bounded JSON accepted: %.100s", source)
		}
	}
	source := []byte(`{"rules":["MATCH,DIRECT"],"rule":["MATCH,MISSING"],"RULES":{},"metadata":{"__proto__":{"constructor":"plain data"}}}`)
	canonical, counts, err := webEvaluateValidate(context.Background(), source)
	if err != nil || counts.Rules != 1 || !bytes.Contains(canonical, []byte(`"__proto__"`)) {
		t.Fatalf("JSON projection mismatch: %s %+v %v", canonical, counts, err)
	}
	cyclic := []byte(`{"proxies":[{"name":"P","type":"socks5","server":"192.0.2.1","port":1080,"dialer-proxy":"P"}],"Proxies":[]}`)
	if _, _, err = webEvaluateValidate(context.Background(), cyclic); err == nil {
		t.Fatal("JSON case-insensitive struct decoding hid an outbound cycle")
	}
}

type webEvaluateCancellingReader struct {
	reader *bytes.Reader
	cancel context.CancelFunc
}

func (r *webEvaluateCancellingReader) Read(out []byte) (int, error) {
	n, err := r.reader.Read(out)
	r.cancel()
	return n, err
}

func TestWebEvaluateJSONLargeFlatObjectUsesLinearDecoderAndObservesCancellation(t *testing.T) {
	metadata := map[string]any{}
	for i := 0; i < 40000; i++ {
		metadata[fmt.Sprintf("key-%05d", i)] = i
	}
	encoded, _ := json.Marshal(map[string]any{"rules": []string{"MATCH,DIRECT"}, "metadata": metadata})
	start := time.Now()
	canonical, counts, err := webEvaluateValidate(context.Background(), encoded)
	if err != nil || counts.Rules != 1 {
		t.Fatalf("large valid JSON rejected: %v", err)
	}
	decoded, err := webEvaluateDecodeJSON(context.Background(), canonical)
	if err != nil || len(decoded["metadata"].(map[string]any)) != 40000 {
		t.Fatal("JSON members were truncated")
	}
	t.Logf("40000-member JSON decode/policy/canonicalize: %s", time.Since(start))
	ctx, cancel := context.WithCancel(context.Background())
	reader := &webEvaluateCancellingReader{reader: bytes.NewReader(encoded), cancel: cancel}
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	bounded := webEvaluationJSONDecoder{ctx: ctx, decoder: decoder}
	if _, err = bounded.value(0); !errors.Is(err, context.Canceled) || bounded.nodes > 2 || reader.reader.Len() == 0 {
		t.Fatalf("JSON cancellation was not observed between tokens: %v, nodes=%d", err, bounded.nodes)
	}
}

func TestWebEvaluateSourceBudgetPrecedesYAMLDecodeAndPreservesMergedUnsafeKeys(t *testing.T) {
	var source strings.Builder
	source.WriteString("rules: ['MATCH,DIRECT']\nmetadata:\n")
	for i := 0; i < 10000; i++ {
		fmt.Fprintf(&source, "  key-%d: %d\n", i, i)
	}
	_, err := webEvaluateParseSource([]byte(source.String()))
	var problem *WebAPIError
	if !errors.As(err, &problem) || problem.Code != "yaml_too_complex" {
		t.Fatalf("unbounded YAML mapping reached decode: %v", err)
	}
	for _, source := range []string{
		"external-ui-url: https://synthetic.invalid\n",
		"defaults: &source {external-ui-url: https://synthetic.invalid}\n<<: *source\n",
		"defaults: &source {external-ui-url: https://synthetic.invalid}\n<<: [*source]\n",
	} {
		var document yaml.Node
		if err = yaml.Unmarshal([]byte(source), &document); err != nil {
			t.Fatal(err)
		}
		if !webEvaluateSourceDeclares(document.Content[0], "external-ui-url") {
			t.Fatal("explicit merged key was mistaken for a synthetic default")
		}
	}
}
