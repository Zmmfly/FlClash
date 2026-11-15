//go:build webpanel && !android

package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestWebAPISourceProspectiveImportWhitelistAndExistingOwners(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("prospective-import-session")
	oldURL := "https://example.invalid/old"
	_, err := r.store.Transaction(ctx, WebAPIMutation{"0", "prospective-owner-seed"}, "test:seed", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		if err := tx.InsertIconSet(WebAPIIconSet{ID: "1", Name: "Icons", URL: oldURL, Icons: []WebAPIIconSetIcon{}}); err != nil {
			return nil, err
		}
		if err := tx.InsertScript(WebAPIScript{ID: "2", Label: "Script", URL: &oldURL, LastUpdateTime: "2026-11-15T00:00:00Z"}); err != nil {
			return nil, err
		}
		if err := tx.InsertClashProvider(WebAPIClashProvider{ID: "3", Label: "Rules", URL: oldURL, Behavior: "classical", Format: "yaml"}); err != nil {
			return nil, err
		}
		return struct{}{}, tx.InsertMitmModule(WebAPIMitmModule{ID: "4", Name: "Module", URL: &oldURL, LastUpdateTime: "2026-11-15T00:00:00Z"})
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, entry := range []struct {
		kind, id string
		purpose  WebAPISourcePurpose
	}{{"icon-set", "1", WebAPISourceIconSetImport}, {"script", "2", WebAPISourceScriptImport}, {"rule-provider", "3", WebAPISourceRuleProviderImport}, {"mitm-module", "4", WebAPISourceMitmModuleImport}} {
		grant := webAPITestSourceGrant(fmt.Sprintf("prospective-grant-%d", i), WebAPIRequestSessionID(ctx))
		grant.Key = &WebAPISourceResource{Kind: entry.kind, ID: entry.id}
		grant.Purpose = entry.purpose
		grant.ImportJobID = fmt.Sprintf("prospective-job-%d", i)
		grant.ExpectedRevision = "1"
		if _, err := webAPITestAuthorize(r.store, ctx, webAPITestSourceRequest(fmt.Sprintf("prospective-authorize-%d", i), "1"), grant); err != nil {
			t.Fatal(entry, err)
		}
		stored, err := r.store.SourceGrant(ctx, grant.ID)
		if err != nil || stored.ImportJobID != grant.ImportJobID || stored.Key.ID != entry.id {
			t.Fatal(stored, err)
		}
	}
	base := webAPITestSourceGrant("prospective-negative", WebAPIRequestSessionID(ctx))
	base.Key = &WebAPISourceResource{Kind: "icon-set", ID: "1"}
	base.Purpose = WebAPISourceIconSetImport
	base.ImportJobID = "fixed-import-job"
	base.ExpectedRevision = "1"
	for i, change := range []func(*WebAPISourceGrant){
		func(g *WebAPISourceGrant) { g.Purpose = WebAPISourceIconSetRefresh },
		func(g *WebAPISourceGrant) { g.Purpose = WebAPISourceScriptImport },
		func(g *WebAPISourceGrant) { g.ImportJobID = "" },
		func(g *WebAPISourceGrant) { g.SourceReference = "unrequested-dependency" },
		func(g *WebAPISourceGrant) { g.URLDigest = strings.Repeat("b", 64) },
	} {
		candidate := base
		candidate.ID = fmt.Sprintf("prospective-invalid-%d", i)
		change(&candidate)
		_, err := webAPITestAuthorize(r.store, ctx, webAPITestSourceRequest(fmt.Sprintf("prospective-invalid-op-%d", i), "1"), candidate)
		webAPITestErrorCode(t, err, "invalid_source_grant")
	}
	missing := base
	missing.ID = "prospective-missing-owner"
	missing.Key = &WebAPISourceResource{Kind: "icon-set", ID: "999"}
	_, err = webAPITestAuthorize(r.store, ctx, webAPITestSourceRequest("prospective-missing-owner-op", "1"), missing)
	webAPITestErrorCode(t, err, "invalid_reference")
	dependency := base
	dependency.ID = "pending-module-dependency"
	dependency.Key = &WebAPISourceResource{Kind: "mitm-resource", ID: "4", Name: "new-resource"}
	dependency.Purpose = WebAPISourceMitmResourceImport
	dependency.SourceReference = "server-resolved-resource"
	dependency.RequestDigest = strings.Repeat("c", 64)
	if _, err = webAPITestAuthorize(r.store, ctx, webAPITestSourceRequest("pending-module-dependency-op", "1"), dependency); err != nil {
		t.Fatal(err)
	}
	dependency.ID = "pending-module-unbound"
	dependency.RequestDigest = ""
	_, err = webAPITestAuthorize(r.store, ctx, webAPITestSourceRequest("pending-module-unbound-op", "1"), dependency)
	webAPITestErrorCode(t, err, "invalid_source_grant")
	revision, err := r.store.View(context.Background(), func(*WebAPIReadTx) error { return nil })
	if err != nil || revision != "1" || r.commits.Load() != 1 {
		t.Fatal(revision, err)
	}
}
