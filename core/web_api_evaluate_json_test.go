//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestWebEvaluateLinearJSONDecoderBudgets(t *testing.T) {
	for _, invalid := range []string{
		`{"x":1,"x":2}`, `{"x":1,"\u0078":2}`, `{"a":{"x":null,"x":1}}`,
		`[]`, `null`, `true`, `{} {}`, `{"x":1e999}`, `{"x":}`, `{"x":0,}`,
		`{"x":` + strings.Repeat(`[`, webEvaluateMaxJSONDepth+1) + `0` + strings.Repeat(`]`, webEvaluateMaxJSONDepth+1) + `}`,
	} {
		if _, err := webEvaluateDecodeJSON(context.Background(), []byte(invalid)); err == nil {
			t.Fatalf("invalid JSON admitted: %.80s", invalid)
		}
	}
	large := []byte(`{"x":[` + strings.Repeat("0,", webEvaluateMaxJSONNodes) + `0]}`)
	_, err := webEvaluateDecodeJSON(context.Background(), large)
	var fault *webEvaluateFault
	if !errors.As(err, &fault) || fault.Code != "evaluation_structure_limit" {
		t.Fatalf("node budget not enforced: %v", err)
	}
	metadata := map[string]int{}
	for i := 0; i < 40000; i++ {
		metadata[fmt.Sprintf("key-%05d", i)] = i
	}
	encoded, _ := json.Marshal(map[string]any{"metadata": metadata, "__proto__": map[string]any{"constructor": "plain data"}})
	start := time.Now()
	root, err := webEvaluateDecodeJSON(context.Background(), encoded)
	if err != nil || len(root["metadata"].(map[string]any)) != len(metadata) || root["__proto__"] == nil {
		t.Fatalf("bounded flat object lost data: %v", err)
	}
	t.Logf("40000-member strict JSON token decode: %s", time.Since(start))
}

type webEvaluateJSONCancelReader struct {
	reader *bytes.Reader
	cancel context.CancelFunc
}

func (r *webEvaluateJSONCancelReader) Read(data []byte) (int, error) {
	n, err := r.reader.Read(data)
	r.cancel()
	return n, err
}

func TestWebEvaluateLinearJSONDecoderCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &webEvaluateJSONCancelReader{reader: bytes.NewReader([]byte(`{"values":[` + strings.Repeat("0,", 10000) + `0]}`)), cancel: cancel}
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	bounded := webEvaluationJSONDecoder{ctx: ctx, decoder: decoder}
	if _, err := bounded.value(0); !errors.Is(err, context.Canceled) || bounded.nodes > 2 || reader.reader.Len() == 0 {
		t.Fatalf("cancelled decoder kept consuming tokens: %v", err)
	}
}
