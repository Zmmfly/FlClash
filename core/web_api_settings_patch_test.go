//go:build webpanel && !android

package core

import "testing"

func TestWebAPISettingsHTTPExplicitNullableValuesAndMapReplacement(t *testing.T) {
	fixture := newSettingsHTTPFixture(t)
	client := fixture.server.Client()
	status, body, _ := fixture.request(t, client, "POST", "/api/v1/settings:runtime", `{"expectedRevision":"0","operationId":"nullable0001","patch":{"patchClashConfig":{"global-ua":{"value":"synthetic-ua"},"hosts":{"a.invalid":"192.0.2.1","b.invalid":"192.0.2.2"},"mitm":{"trusted-fingerprint":{"value":"synthetic-fingerprint"},"capture-entries":{"value":2000}}}}}`, "one")
	if status != 200 {
		t.Fatalf("nullable value set status=%d", status)
	}
	first := settingsTestDecode(t, body).Data.Runtime.PatchClashConfig
	if first.GlobalUA == nil || *first.GlobalUA != "synthetic-ua" || first.Mitm.CaptureEntries == nil || *first.Mitm.CaptureEntries != 2000 || first.Mitm.TrustedFingerprint == nil {
		t.Fatal("provided nullable values were ignored")
	}
	status, body, _ = fixture.request(t, client, "POST", "/api/v1/settings:runtime", `{"expectedRevision":"1","operationId":"nullable0002","patch":{"patchClashConfig":{"global-ua":{"value":null},"hosts":{"c.invalid":"192.0.2.3"},"mitm":{"trusted-fingerprint":{"value":null},"capture-entries":{"value":null}}}}}`, "two")
	if status != 200 {
		t.Fatalf("nullable clear status=%d", status)
	}
	second := settingsTestDecode(t, body).Data.Runtime.PatchClashConfig
	if second.GlobalUA != nil || second.Mitm.TrustedFingerprint != nil || second.Mitm.CaptureEntries != nil {
		t.Fatal("explicit null did not clear the nullable fields")
	}
	if len(second.Hosts) != 1 || second.Hosts["c.invalid"] != "192.0.2.3" {
		t.Fatal("explicit domain map replacement became an arbitrary recursive KV patch")
	}
	if second.Mode != first.Mode || second.Tun.MTU != first.Tun.MTU || second.DNS.CacheMaxSize != first.DNS.CacheMaxSize {
		t.Fatal("unprovided typed fields were reset")
	}
}
