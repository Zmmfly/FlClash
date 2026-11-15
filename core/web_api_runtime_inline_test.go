//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestWebRuntimeInlineProvidersRemainSelfContainedForApplyAndPreview(t *testing.T) {
	const source = "proxy-providers:\n  local:\n    type: inline\n    payload:\n      - {name: P, type: socks5, server: 192.0.2.1, port: 1080}\nrule-providers:\n  local-rules:\n    type: inline\n    behavior: classical\n    payload: ['DOMAIN,example.test']\nproxy-groups:\n  - {name: G, type: select, proxies: [DIRECT], use: [local]}\nrules: ['RULE-SET,local-rules,DIRECT', 'MATCH,G']\n"
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{Apply: func(_ context.Context, p WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
		data, err := os.ReadFile(p.ConfigPath)
		if err != nil {
			t.Error(err)
		}
		if bytes.Count(data, []byte("type: inline")) != 2 || bytes.Contains(data, []byte("path:")) {
			t.Error("self-contained provider was converted to a remote/file source")
		}
		return WebAPIRuntimeApplyResult{ActualConfig: "candidate"}, nil
	}})
	webRuntimeSeedProfile(t, s, "file", source)
	code, body := s.request(t, "POST", "/api/v1/runtime:apply", webRuntimeApplyBody(t, "inline-provider-apply", nil), s.cookie, "")
	if code != 200 {
		t.Fatalf("inline apply %d %s", code, body)
	}
	if state := s.phase(t, "inline-provider-apply"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	overlay := source
	request, _ := json.Marshal(WebAPIRuntimeBuildRequest{ID: webRuntimeTestProfileID, Overlay: &overlay})
	code, body = s.request(t, "POST", "/api/v1/profiles:build", string(request), s.cookie, "")
	if code != 200 || !bytes.Contains(body, []byte("type: inline")) {
		t.Fatalf("inline preview %d %s", code, body)
	}
}
