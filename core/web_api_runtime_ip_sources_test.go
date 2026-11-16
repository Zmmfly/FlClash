//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
)

// The dashboard probes with every source lib/common/service_probe.dart parses.
func TestWebRuntimeOutboundIPAcceptsEveryDashboardSource(t *testing.T) {
	source, err := os.ReadFile("../lib/common/service_probe.dart")
	if err != nil {
		t.Fatal(err)
	}
	_, block, found := strings.Cut(string(source), "ipInfoSources = {")
	block, _, closed := strings.Cut(block, "};")
	var dashboard []string
	for _, match := range regexp.MustCompile(`'(https://[^']+)':`).FindAllStringSubmatch(block, -1) {
		dashboard = append(dashboard, match[1])
	}
	if !found || !closed || len(dashboard) == 0 {
		t.Fatal("ipInfoSources is no longer a literal map of sources")
	}
	var mu sync.Mutex
	var asked []string
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{OutboundIP: func(_ context.Context, p WebAPIRuntimeOutboundIPRequest) (*OutboundIpResult, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = p.URLs
		return &OutboundIpResult{Url: p.URLs[0], Body: "{}"}, nil
	}})
	probe := func(urls []string) int {
		body, err := json.Marshal(map[string]any{"proxyName": "DIRECT", "urls": urls, "timeoutMillis": 1000})
		if err != nil {
			t.Fatal(err)
		}
		code, _ := s.request(t, "POST", "/api/v1/runtime/probes/outbound-ip", string(body), s.cookie, "")
		return code
	}
	code := probe(dashboard)
	mu.Lock()
	probed := slices.Clone(asked)
	mu.Unlock()
	if code != 200 || !slices.Equal(probed, dashboard) {
		t.Fatalf("dashboard sources: status %d, probed %v", code, probed)
	}
	if code := probe(append(slices.Clone(dashboard), dashboard[0])); code != 400 {
		t.Fatalf("more sources than the dashboard knows: status %d", code)
	}
	if code := probe([]string{dashboard[0] + "?relay"}); code != 400 {
		t.Fatalf("unknown source: status %d", code)
	}
}
