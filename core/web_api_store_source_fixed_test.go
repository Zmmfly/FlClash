//go:build webpanel && !android

package core

import (
	"fmt"
	"strings"
	"testing"
)

func TestWebAPISourceFixedDAVAndOpaqueUint256Domains(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("fixed-source-session")
	maximum := "115792089237316195423570985008687907853269984665640564039457584007913129639935"
	cases := []struct{ kind, id, purpose, digest string }{
		{"dav", "settings", "dav-probe", strings.Repeat("b", 64)},
		{"dav", "settings", "dav-backup", strings.Repeat("b", 64)},
		{"dav", "settings", "dav-restore", strings.Repeat("b", 64)},
		{"icon-image", "0", "icon-image", ""},
		{"icon-image", maximum, "icon-image", strings.Repeat("c", 64)},
		{"geo", maximum, "geo-refresh", ""},
		{"external-provider", maximum, "external-provider-refresh", strings.Repeat("d", 64)},
	}
	for i, item := range cases {
		grant := webAPITestSourceGrant(fmt.Sprintf("fixed-domain-grant-%d", i), WebAPIRequestSessionID(ctx))
		grant.Purpose = WebAPISourcePurpose(item.purpose)
		grant.Key = &WebAPISourceResource{Kind: item.kind, ID: item.id}
		grant.RequestDigest = item.digest
		receipt, err := webAPITestAuthorize(r.store, ctx, webAPITestSourceRequest(fmt.Sprintf("fixed-domain-op-%d", i)), grant)
		if err != nil || receipt.Revision != "0" {
			t.Fatal(item, err)
		}
		stored, err := r.store.SourceGrant(ctx, grant.ID)
		if err != nil || stored.Key.ID != item.id || stored.RequestDigest != item.digest {
			t.Fatal(stored, err)
		}
	}
	if r.commits.Load() != 0 {
		t.Fatal("source grant changed configuration")
	}
}

func TestWebAPISourceFixedDomainsRejectWrongPurposeIdentityAndDigest(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("fixed-source-session")
	cases := []struct{ kind, id, name, purpose, digest string }{
		{"dav", "settings", "", "dav-probe", ""},
		{"dav", "other", "", "dav-probe", strings.Repeat("b", 64)},
		{"dav", "settings", "child", "dav-backup", strings.Repeat("b", 64)},
		{"dav", "settings", "", "profile-refresh", strings.Repeat("b", 64)},
		{"dav", "settings", "", "dav-restore", strings.Repeat("B", 64)},
		{"icon-image", "01", "", "icon-image", ""},
		{"icon-image", "-1", "", "icon-image", ""},
		{"icon-image", "1e2", "", "icon-image", ""},
		{"icon-image", "115792089237316195423570985008687907853269984665640564039457584007913129639936", "", "icon-image", ""},
		{"icon-image", "1", "path", "icon-image", ""},
		{"geo", "0", "", "geo-refresh", ""},
		{"geo", "1", "", "external-provider-refresh", ""},
		{"external-provider", "arbitrary-runtime-string", "", "external-provider-refresh", ""},
		{"external-provider", "1", "name", "external-provider-refresh", ""},
		{"external-provider", "1", "", "external-provider-refresh", "not-hex"},
		{"http-relay", "1", "", "external-provider-refresh", ""},
		{"profile", "1", "", "profile-refresh", strings.Repeat("d", 64)},
	}
	for i, item := range cases {
		grant := webAPITestSourceGrant(fmt.Sprintf("rejected-fixed-grant-%d", i), WebAPIRequestSessionID(ctx))
		grant.Purpose = WebAPISourcePurpose(item.purpose)
		grant.Key = &WebAPISourceResource{Kind: item.kind, ID: item.id, Name: item.name}
		grant.RequestDigest = item.digest
		_, err := webAPITestAuthorize(r.store, ctx, webAPITestSourceRequest(fmt.Sprintf("rejected-fixed-op-%d", i)), grant)
		webAPITestErrorCode(t, err, "invalid_source_grant")
	}
}
