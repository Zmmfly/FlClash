//go:build webpanel && !android

package core

import "testing"

func TestWebUtilitySourcePolicyAcceptsOnlyFixedProviderAndExplicitIP(t *testing.T) {
	for _, source := range webUtilityIPSources {
		for _, ip := range []string{"8.8.8.8", "10.20.30.40", "::1", "2001:4860:4860::8888"} {
			target, err := webUtilityIPURL(source, ip)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateWebAPIUtilitySource(WebAPISourcePurpose("ip-quality-lookup"), target); err != nil {
				t.Fatalf("fixed provider %s/%s rejected: %v", source, ip, err)
			}
		}
	}
	for _, target := range []string{webUtilityReleaseMetadataURL, "https://api.github.com:443/repos/chen08209/FlClash/releases/latest"} {
		if err := ValidateWebAPIUtilitySource(WebAPISourcePurpose("app-release"), target); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []string{
		"https://api.github.com/repos/evil/FlClash/releases/latest",
		"https://api.github.com/repos/chen08209/FlClash/releases/latest?url=http://169.254.169.254",
		"https://api.github.com/repos/chen08209/FlClash/releases/latest#x",
		"http://api.github.com/repos/chen08209/FlClash/releases/latest",
		"https://api.github.com.evil.test/repos/chen08209/FlClash/releases/latest",
		"https://user:password@api.github.com/repos/chen08209/FlClash/releases/latest",
		"https://api.github.com:444/repos/chen08209/FlClash/releases/latest",
		"https://api.github.com/repos/chen08209%2fFlClash/releases/latest",
	} {
		if err := ValidateWebAPIUtilitySource(WebAPISourcePurpose("app-release"), target); err == nil {
			t.Fatalf("arbitrary release source %s", target)
		}
	}
	for _, target := range []string{
		"https://api.ipquery.io/8.8.8.8?format=json&url=http://169.254.169.254",
		"https://api.ipquery.io/8.8.8.8?format=json#x",
		"https://api.ipquery.io/8.8.8.8/../metadata?format=json",
		"https://api.ipquery.io/8.8.8.8%2fmetadata?format=json",
		"https://api.ipquery.io.evil.test/8.8.8.8?format=json",
		"https://api.ipapi.is/?q=8.8.8.8&q=1.1.1.1",
		"https://api.ipapi.is/?q=http%3A%2F%2Fmetadata.google.internal",
		"http://169.254.169.254/latest/meta-data",
		"https://ident.me/json?ip=8.8.8.8",
		"https://ident.me/",
	} {
		if err := ValidateWebAPIUtilitySource(WebAPISourcePurpose("ip-quality-lookup"), target); err == nil {
			t.Fatalf("arbitrary IP source %s", target)
		}
	}
}
