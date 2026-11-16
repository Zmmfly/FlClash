//go:build webpanel && !android

package core

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestWebRuntimeProfileOutboundsListTargets(t *testing.T) {
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	webRuntimeSeedProfile(t, s, "file", webRuntimeTestYAML)
	code, body := s.request(t, "GET", "/api/v1/profiles/"+string(webRuntimeTestProfileID)+"/outbounds", "", s.cookie, "")
	var answer WebAPIEnvelope[ProfileOutbounds]
	if code != 200 || json.Unmarshal(body, &answer) != nil {
		t.Fatalf("outbounds %d: %s", code, body)
	}
	proxies, groups := answer.Data.Proxies, answer.Data.ProxyGroups
	if len(proxies) != 1 || proxies[0].Name != "P" || proxies[0].Type != "socks5" {
		t.Fatalf("proxies: %+v", proxies)
	}
	if len(groups) != 1 || groups[0].Name != "G" || !slices.Equal(groups[0].Proxies, []string{"P", "DIRECT"}) {
		t.Fatalf("groups: %+v", groups)
	}
}
