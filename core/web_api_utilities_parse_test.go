//go:build webpanel && !android

package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWebUtilityReleaseDerivesOnlyProjectLinks(t *testing.T) {
	metadata := map[string]any{
		"tag_name": "v1.2.3", "published_at": "2026-01-02T03:04:05Z",
		"name": "Release", "body": "Notes https://attacker.test/never-trust", "html_url": "http://169.254.169.254/",
		"assets": []any{map[string]any{"name": "FlClash-1.2.3-android.apk", "size": 12345, "state": "uploaded", "browser_download_url": "https://attacker.test/file.apk"}},
	}
	data, _ := json.Marshal(metadata)
	release, err := webUtilityParseRelease(data, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if release.ReleaseURL != "https://github.com/chen08209/FlClash/releases/tag/v1.2.3" || release.Assets[0].URL != "https://github.com/chen08209/FlClash/releases/download/v1.2.3/FlClash-1.2.3-android.apk" || release.UpdatePolicy != "server-image" {
		t.Fatalf("untrusted or ambiguous release navigation: %+v", release)
	}
	for _, bad := range []string{"v1.2.3/../../evil", "https://evil.test", "v01.2.3", "v1.2.3-pre.1", "v1.2", "v1.2.3?x"} {
		metadata["tag_name"] = bad
		data, _ = json.Marshal(metadata)
		if _, err := webUtilityParseRelease(data, time.Now()); err == nil {
			t.Fatalf("accepted tag %q", bad)
		}
	}
	metadata["tag_name"] = "v1.2.3"
	for _, bad := range []string{"../../x.apk", "x\\evil.exe", "file?download", "x#fragment", "x%2fescape", "double..exe"} {
		metadata["assets"] = []any{map[string]any{"name": bad, "size": 1}}
		data, _ = json.Marshal(metadata)
		if _, err := webUtilityParseRelease(data, time.Now()); err == nil {
			t.Fatalf("accepted asset %q", bad)
		}
	}
}

func TestWebUtilityReleaseRejectsMalformedBoundedMetadata(t *testing.T) {
	for _, data := range []string{
		`{"tag_name":"v1.2.3","tag_name":"v8.9.0","published_at":"2026-01-01T00:00:00Z"}`,
		`{"tag_name":"v1.2.3","published_at":"no-time"}`,
		`{"tag_name":"v1.2.3","published_at":"2026-01-01T00:00:00Z","draft":true}`,
		`{"tag_name":"v1.2.3","published_at":"2026-01-01T00:00:00Z","assets":[{"name":"x.apk","size":"1"}]}`,
		`{"tag_name":"v1.2.3","published_at":"2026-01-01T00:00:00Z"} {}`,
		`[]`,
		`{"tag_name":"v1.2.3","published_at":"2026-01-01T00:00:00Z","body":"` + strings.Repeat("x", webUtilityReleaseBodyLimit+1) + `"}`,
	} {
		if _, err := webUtilityParseRelease([]byte(data), time.Now()); err == nil {
			t.Fatalf("accepted malformed source: %.100s", data)
		}
	}
}

func TestWebUtilityIPParsersMatchNativeSixSources(t *testing.T) {
	for _, fixture := range []struct {
		source, body, kind, org           string
		inferred, proxy, vpn, tor, abuser bool
		asn                               uint32
	}{
		{"identMe", `{"ip":"8.8.8.8","type":"isp","aso":"Example","asn":"AS123"}`, "residential", "Example", false, false, false, false, false, 123},
		{"ipApiCom", `{"status":"success","org":"Example","as":"AS42 Provider","mobile":true,"proxy":true}`, "mobile", "Example", false, true, false, false, false, 42},
		{"ipQuery", `{"ip":"8.8.8.8","isp":{"org":"Example","asn":99},"risk":{"is_datacenter":true,"is_vpn":true}}`, "hosting", "Example", false, false, true, false, false, 99},
		{"ipLocate", `{"company":{"type":"education","name":"Example"},"asn":{"asn":123},"privacy":{"is_tor":true,"is_abuser":true}}`, "business", "Example", false, false, false, true, true, 123},
		{"proxyCheck", `{"status":"warning","8.8.8.8":{"network":{"type":"Wireless","organisation":"Example","asn":"AS321"},"detections":{"proxy":true,"vpn":true,"tor":true}}}`, "mobile", "Example", false, true, true, true, false, 321},
		{"ipApiIs", `{"company":{"type":"government","name":"Example"},"asn":{"asn":321},"is_datacenter":true,"is_abuser":true}`, "hosting", "Example", false, false, false, false, true, 321},
		{"ipApiCom", `{"status":"success"}`, "residential", "", true, false, false, false, false, 0},
		{"ipQuery", `{}`, "residential", "", true, false, false, false, false, 0},
	} {
		t.Run(fixture.source+fixture.kind, func(t *testing.T) {
			answer, err := webUtilityParseIP(fixture.source, "8.8.8.8", []byte(fixture.body))
			if err != nil {
				t.Fatal(err)
			}
			q := answer.quality
			org := ""
			if q.Organization != nil {
				org = *q.Organization
			}
			var asn uint32
			if q.ASN != nil {
				asn = *q.ASN
			}
			if q.Type != fixture.kind || org != fixture.org || asn != fixture.asn || answer.inferred != fixture.inferred || q.IsProxy != fixture.proxy || q.IsVPN != fixture.vpn || q.IsTor != fixture.tor || q.IsAbuser != fixture.abuser {
				t.Fatalf("native parser mismatch: %+v inferred=%v", q, answer.inferred)
			}
		})
	}
}

func TestWebUtilityIPParserRejectsSourceConfusionAndTypedBodyAttacks(t *testing.T) {
	for _, fixture := range []struct{ source, body string }{
		{"identMe", `{"ip":"1.1.1.1","type":"isp"}`},
		{"ipQuery", `{"ip":"1.1.1.1","risk":{"is_mobile":true}}`},
		{"ipLocate", `{"privacy":{"is_hosting":"true"}}`},
		{"ipApiIs", `{"company":{"type":"hosting"},"asn":{"asn":4294967296}}`},
		{"ipQuery", `{"risk":[],"isp":{}}`},
		{"ipQuery", `{"risk":{"is_mobile":true,"is_mobile":false}}`},
		{"proxyCheck", `{"status":"denied"}`},
		{"proxyCheck", `{"status":"ok","1.1.1.1":{"network":{"type":"Hosting"}}}`},
		{"ipApiCom", `{"status":"fail"}`},
	} {
		if _, err := webUtilityParseIP(fixture.source, "8.8.8.8", []byte(fixture.body)); err == nil {
			t.Fatalf("accepted invalid %s body %s", fixture.source, fixture.body)
		}
	}
	for _, ip := range []string{"unknown", "127.0.0.1/../../metadata", "8.8.8.8?host=evil", "::1%lo", "http://evil.test", "8.8.8.8\r\nX: y"} {
		if _, err := webUtilityIPURL("ipQuery", ip); err == nil {
			t.Fatalf("accepted IP injection %q", ip)
		}
	}
	for _, source := range webUtilityIPSources {
		for _, ip := range []string{"8.8.8.8", "127.0.0.1", "10.0.0.1", "2001:4860:4860::8888"} {
			if _, err := webUtilityIPURL(source, ip); err != nil {
				t.Fatalf("native fixed-input IP refused: %s/%s: %v", source, ip, err)
			}
		}
	}
}
