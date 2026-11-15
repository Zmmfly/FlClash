//go:build webpanel && !android

package core

import (
	"fmt"
	"strings"
	"testing"
)

func TestWebAPICustomProfileImportGrantsBindCustomOwnerJobAndDerivedReference(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("custom-import-session")
	_, err := r.store.Transaction(ctx, WebAPIMutation{"0", "custom-import-owner-seed"}, "test:owners", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		if _, err := tx.CreateProfile(WebAPIProfileInput{Type: "file", Label: "File"}); err != nil {
			return nil, err
		}
		return tx.CreateProfile(WebAPIProfileInput{Type: "custom", Label: "Custom"})
	})
	if err != nil {
		t.Fatal(err)
	}
	root := webAPITestSourceGrant("custom-import-root-grant", WebAPIRequestSessionID(ctx))
	root.Key = &WebAPISourceResource{Kind: "profile", ID: "2"}
	root.Purpose = "custom-profile-import"
	root.ImportJobID = "custom-profile-job"
	root.ExpectedRevision = "1"
	if _, err = webAPITestAuthorize(r.store, ctx, webAPITestSourceRequest("custom-import-root-authorization", "1"), root); err != nil {
		t.Fatal(err)
	}
	dependency := root
	dependency.ID = "custom-import-dependency-grant"
	dependency.Key = &WebAPISourceResource{Kind: "custom-import-dependency", ID: "2", Name: "server-reference"}
	dependency.Purpose = "custom-profile-dependency"
	dependency.SourceReference = "server-reference"
	dependency.RequestDigest = strings.Repeat("b", 64)
	if _, err = webAPITestAuthorize(r.store, ctx, webAPITestSourceRequest("custom-import-dependency-authorization", "1"), dependency); err != nil {
		t.Fatal(err)
	}
	for i, change := range []func(*WebAPISourceGrant){
		func(g *WebAPISourceGrant) {
			g.Key = &WebAPISourceResource{Kind: "profile", ID: "1"}
			g.Purpose = "custom-profile-import"
			g.SourceReference = ""
			g.RequestDigest = ""
		},
		func(g *WebAPISourceGrant) {
			g.Key = &WebAPISourceResource{Kind: "custom-import-dependency", ID: "1", Name: "server-reference"}
		},
		func(g *WebAPISourceGrant) {
			g.Key = &WebAPISourceResource{Kind: "custom-import-dependency", ID: "999", Name: "server-reference"}
		},
	} {
		candidate := dependency
		candidate.ID = fmt.Sprintf("custom-owner-invalid-%d", i)
		change(&candidate)
		_, err = webAPITestAuthorize(r.store, ctx, webAPITestSourceRequest(fmt.Sprintf("custom-owner-invalid-op-%d", i), "1"), candidate)
		webAPITestErrorCode(t, err, "invalid_reference")
	}
	for i, change := range []func(*WebAPISourceGrant){
		func(g *WebAPISourceGrant) { g.ImportJobID = "" },
		func(g *WebAPISourceGrant) { g.SourceReference = "other-reference" },
		func(g *WebAPISourceGrant) { g.RequestDigest = "" },
		func(g *WebAPISourceGrant) { g.RequestDigest = strings.Repeat("B", 64) },
		func(g *WebAPISourceGrant) { g.Purpose = WebAPISourceProfileRefresh },
		func(g *WebAPISourceGrant) {
			g.Key = &WebAPISourceResource{Kind: "http-relay", ID: "2", Name: "server-reference"}
		},
		func(g *WebAPISourceGrant) { g.Key = nil },
	} {
		candidate := dependency
		candidate.ID = fmt.Sprintf("custom-binding-invalid-%d", i)
		change(&candidate)
		_, err = webAPITestAuthorize(r.store, ctx, webAPITestSourceRequest(fmt.Sprintf("custom-binding-invalid-op-%d", i), "1"), candidate)
		webAPITestErrorCode(t, err, "invalid_source_grant")
	}
	_, err = r.store.Transaction(ctx, WebAPIMutation{"1", "custom-import-owner-delete"}, "profiles:delete", WebAPIID("2"), func(tx *WebAPIWriteTx) (any, error) { return tx.DeleteProfile("2") })
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{root.ID, dependency.ID} {
		_, err = r.store.SourceGrant(ctx, id)
		webAPITestErrorCode(t, err, "not_found")
	}
	if _, err = r.store.SourceGrantReceipt(ctx, "custom-import-dependency-authorization"); err != nil {
		t.Fatal("owner deletion erased durable security receipt", err)
	}
}
