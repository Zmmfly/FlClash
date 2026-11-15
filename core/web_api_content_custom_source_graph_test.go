//go:build webpanel && !android

package core

import (
	"strings"
	"testing"
)

func TestWebCustomSourcePureManagedProviderGraph(t *testing.T) {
	snapshot := webCustomSourceSnapshot{Target: WebAPIProfile{ID: "1", WebAPIProfileInput: WebAPIProfileInput{Type: "custom", SelectedMap: map[string]string{}}}, Profiles: []WebAPIProfile{{ID: "2", WebAPIProfileInput: WebAPIProfileInput{Type: "url", Label: " held,provider ", URL: "https://provider.test/sub"}}}, Contents: map[WebAPIID]WebAPIContent{"2": {Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: "2"}, Bytes: []byte("proxies:\n - {name: Node, type: trojan, server: synthetic.test, port: 443, password: synthetic}\n")}}}
	parsed, err := webCustomSourceParse([]byte("proxy-providers: {original: {type: http, url: https://provider.test/sub}}\nproxy-groups: [{name: G, type: select, use: [original], default-selected: Node}]\nrules: ['MATCH,G']\n"), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Custom.ProxyProviders) != 0 || len(parsed.Dependencies) != 0 {
		t.Fatal("managed provider was changed into remote fetch")
	}
	if err := webCustomSourceGraph(snapshot, parsed.Custom, nil, nil); err != nil {
		t.Fatal(err)
	}
	delete(snapshot.Contents, "2")
	if err := webCustomSourceGraph(snapshot, parsed.Custom, nil, nil); err == nil {
		t.Fatal("unknown managed provider membership accepted")
	}
}

func TestWebCustomSourcePureGraphRejectsCycleAndMissingDialer(t *testing.T) {
	for _, target := range []string{"G", "Missing"} {
		snapshot := webCustomSourceSnapshot{Target: WebAPIProfile{ID: "1", WebAPIProfileInput: WebAPIProfileInput{Type: "custom", SelectedMap: map[string]string{}}}}
		parsed, err := webCustomSourceParse([]byte("proxies:\n - {name: A, type: trojan, server: synthetic.test, port: 443, password: synthetic, dialer-proxy: "+target+"}\nproxy-groups: [{name: G, type: select, proxies: [A]}]\n"), snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if err := webCustomSourceGraph(snapshot, parsed.Custom, nil, nil); err == nil {
			t.Fatalf("unsafe dialer %q was changed into direct routing", target)
		}
	}
}

func TestWebCustomSourceLocalProviderNeverReadsItsPath(t *testing.T) {
	snapshot := webCustomSourceSnapshot{Target: WebAPIProfile{ID: "1", WebAPIProfileInput: WebAPIProfileInput{Type: "custom", SelectedMap: map[string]string{}}}}
	parsed, err := webCustomSourceParse([]byte("proxy-providers: {Local: {type: file, path: /synthetic/nonexistent/no-read.yaml}}\nproxy-groups: [{name: G, type: select, use: [Local]}]\n"), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	source := parsed.Custom.ProxyProviders[0]
	if strings.Contains(source.Content, "path") || len(parsed.Dependencies) != 0 {
		t.Fatal("filesystem authority escaped into provider")
	}
	prepared, err := webCustomSourcePrepareProvider(source, []byte(source.Content), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := webCustomSourceGraph(snapshot, parsed.Custom, []webContentCustomProvider{prepared}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWebCustomSourceExpandedScalarBudgetBeforeSerialization(t *testing.T) {
	// Alias node limits alone do not bound repeated long scalar serialization.
	remaining := 64
	value := map[string]any{"x": []any{strings.Repeat("x", 40), strings.Repeat("x", 40)}}
	if err := webCustomSourceValueBudget(value, &remaining); err == nil {
		t.Fatal("expanded scalar byte budget was bypassed")
	}
	remaining = 512
	if err := webCustomSourceValueBudget(value, &remaining); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"proxy-providers: {p: {type: http, url: https://source.test, proxy: G}}\n", "proxy-providers: {p: {type: inline, payload: [], override: {dialer-proxy: G}}}\n"} {
		if _, err := webCustomSourceParse([]byte(source), webCustomSourceSnapshot{}); err == nil {
			t.Fatal("unrepresentable provider dependency silently discarded")
		}
	}
}
