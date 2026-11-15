//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	bolt "github.com/metacubex/bbolt"
)

func webAPITestSourceContext(session string) context.Context {
	return WithWebAPIRequestAuthorization(context.Background(), session, func(context.Context) (func(), error) { return func() {}, nil })
}

func webAPITestSourceGrant(id, session string) WebAPISourceGrant {
	return WebAPISourceGrant{ID: id, Purpose: WebAPISourceExternalProviderRefresh, Key: &WebAPISourceResource{Kind: "external-provider", ID: "123456789012345678901234567890123456789012345678901234567890"}, URLDigest: strings.Repeat("a", 64), Scheme: "https", Host: "example.invalid", Port: 443, Addresses: []string{"192.0.2.1"}, PolicyVersion: WebAPISourcePolicyVersion, SessionID: session, ExpectedRevision: "0", ExpiresAt: "2099-01-01T00:00:00Z"}
}

func webAPITestSourceRequest(operationID string, revisions ...WebAPIRevision) WebAPISourceAuthorize {
	request := WebAPISourceAuthorize{PreparationID: "prepared-source-token", URLDigest: strings.Repeat("a", 64), Intent: "authorize-source"}
	request.OperationID = operationID
	request.ExpectedRevision = "0"
	if len(revisions) > 0 {
		request.ExpectedRevision = revisions[0]
	}
	return request
}

func webAPITestAuthorize(store *WebAPIStore, ctx context.Context, request WebAPISourceAuthorize, grant WebAPISourceGrant) (WebAPISourceAuthorizationReceipt, error) {
	return store.SourceAuthorizationTransaction(ctx, request.OperationID, request, func(tx *WebAPISourceAuthorizationTx) (any, error) {
		return WebAPISourceAuthorizationResult{AuthorizationID: grant.ID, ExpiresAt: grant.ExpiresAt}, tx.SaveSourceGrant(grant)
	})
}

func TestWebAPISourceAuthorizationAtomicReceiptRestartAndIsolation(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("synthetic-source-session")
	grant := webAPITestSourceGrant("opaque-grant-id", WebAPIRequestSessionID(ctx))
	request := webAPITestSourceRequest("source-authorization-op")
	first, err := webAPITestAuthorize(r.store, ctx, request, grant)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Data        WebAPISourceAuthorizationResult `json:"data"`
		OperationID string                          `json:"operationId"`
		Revision    *string                         `json:"revision"`
	}
	if err = json.Unmarshal(first.Body, &body); err != nil || body.Data.AuthorizationID != grant.ID || body.Revision == nil || *body.Revision != "0" || first.Revision != "0" || body.OperationID != request.OperationID {
		t.Fatal(body, err)
	}
	got, err := r.store.SourceGrant(ctx, grant.ID)
	if err != nil || !reflect.DeepEqual(got, grant) {
		t.Fatal(got, err)
	}
	second, err := r.store.SourceAuthorizationTransaction(ctx, request.OperationID, request, func(*WebAPISourceAuthorizationTx) (any, error) { t.Fatal("replay reauthorized"); return nil, nil })
	if err != nil || !second.Replayed || !bytes.Equal(first.Body, second.Body) {
		t.Fatal(second, err)
	}
	changed := request
	changed.URLDigest = strings.Repeat("b", 64)
	_, err = r.store.SourceAuthorizationTransaction(ctx, changed.OperationID, changed, func(*WebAPISourceAuthorizationTx) (any, error) { t.Fatal("changed request executed"); return nil, nil })
	webAPITestErrorCode(t, err, "operation_conflict")
	other := webAPITestSourceContext("other-source-session")
	_, err = r.store.SourceGrant(other, grant.ID)
	webAPITestErrorCode(t, err, "source_authorization_mismatch")
	_, err = r.store.SourceGrantReceipt(other, request.OperationID)
	webAPITestErrorCode(t, err, "source_authorization_mismatch")
	_, err = r.store.SourceAuthorizationTransaction(other, request.OperationID, request, func(*WebAPISourceAuthorizationTx) (any, error) {
		t.Fatal("cross-session replay executed")
		return nil, nil
	})
	webAPITestErrorCode(t, err, "source_authorization_mismatch")
	_, err = r.store.SourceGrant(context.Background(), grant.ID)
	webAPITestErrorCode(t, err, "unauthorized")
	rev, err := r.store.View(ctx, func(tx *WebAPIReadTx) error {
		profiles, err := tx.Profiles()
		if len(profiles) != 0 {
			t.Fatal("security authorization wrote config")
		}
		contents, e := tx.Contents()
		if len(contents) != 0 {
			t.Fatal("security authorization became content")
		}
		return errors.Join(err, e)
	})
	if err != nil || rev != "0" || r.commits.Load() != 0 {
		t.Fatal(rev, err)
	}
	cacheRev, err := r.store.CacheView(ctx, func(tx *WebAPICacheTx) error {
		icons, err := tx.IconRecords()
		if len(icons) != 0 {
			t.Fatal("source authorization entered icon cache")
		}
		return err
	})
	if err != nil || cacheRev != "0" {
		t.Fatal(cacheRev, err)
	}
	_, err = r.store.Receipt(ctx, request.OperationID)
	webAPITestErrorCode(t, err, "not_found")
	_, err = r.store.CacheReceipt(ctx, request.OperationID)
	webAPITestErrorCode(t, err, "not_found")
	r.server.Close()
	r.store.Close()
	r.store, err = OpenWebAPIStore(r.opts)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := r.store.SourceGrantReceipt(ctx, request.OperationID)
	if err != nil || !bytes.Equal(first.Body, receipt.Body) {
		t.Fatal(receipt, err)
	}
	got, err = r.store.SourceGrant(ctx, grant.ID)
	if err != nil || !reflect.DeepEqual(got, grant) {
		t.Fatal(got, err)
	}
}

func TestWebAPISourceAuthorizationRollbackCollisionAndInvalidation(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("source-session")
	grant := webAPITestSourceGrant("grant-one", WebAPIRequestSessionID(ctx))
	request := webAPITestSourceRequest("source-rollback-op")
	_, err := r.store.SourceAuthorizationTransaction(ctx, request.OperationID, request, func(tx *WebAPISourceAuthorizationTx) (any, error) {
		if err := tx.SaveSourceGrant(grant); err != nil {
			return nil, err
		}
		return nil, errors.New("reject candidate")
	})
	if err == nil {
		t.Fatal("failed grant committed")
	}
	_, err = r.store.SourceGrant(ctx, grant.ID)
	webAPITestErrorCode(t, err, "not_found")
	_, err = r.store.SourceGrantReceipt(ctx, request.OperationID)
	webAPITestErrorCode(t, err, "not_found")
	request = webAPITestSourceRequest("source-new-grant-op")
	first, err := webAPITestAuthorize(r.store, ctx, request, grant)
	if err != nil {
		t.Fatal(err)
	}
	_, err = webAPITestAuthorize(r.store, ctx, webAPITestSourceRequest("source-duplicate-id-op"), grant)
	webAPITestErrorCode(t, err, "duplicate_source_grant")
	second := grant
	second.ID = "grant-two"
	second.Key = &WebAPISourceResource{Kind: grant.Key.Kind, ID: grant.Key.ID}
	if _, err = webAPITestAuthorize(r.store, ctx, webAPITestSourceRequest("source-second-grant"), second); err != nil {
		t.Fatal(err)
	}
	third := grant
	third.ID = "grant-three"
	third.Key = &WebAPISourceResource{Kind: grant.Key.Kind, ID: "987654321098765432109876543210987654321098765432109876543210"}
	if _, err = webAPITestAuthorize(r.store, ctx, webAPITestSourceRequest("source-third-grant"), third); err != nil {
		t.Fatal(err)
	}
	_, err = r.store.Transaction(ctx, WebAPIMutation{"0", "invalidate-one-source"}, "test:invalidate", *grant.Key, func(tx *WebAPIWriteTx) (any, error) {
		return struct{}{}, tx.InvalidateSourceGrantsForResource(*grant.Key)
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.SourceGrant(ctx, grant.ID)
	webAPITestErrorCode(t, err, "not_found")
	_, err = r.store.SourceGrant(ctx, second.ID)
	webAPITestErrorCode(t, err, "not_found")
	_, err = r.store.Transaction(ctx, WebAPIMutation{"1", "invalidate-resource-source"}, "test:invalidate", grant.Key.ID, func(tx *WebAPIWriteTx) (any, error) {
		return struct{}{}, tx.InvalidateSourceGrantsForResource(WebAPISourceResource{Kind: grant.Key.Kind, ID: grant.Key.ID})
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.SourceGrant(ctx, second.ID)
	webAPITestErrorCode(t, err, "not_found")
	if _, err = r.store.SourceGrant(ctx, third.ID); err != nil {
		t.Fatal(err)
	}
	_, err = r.store.Transaction(ctx, WebAPIMutation{"2", "reset-all-source-grants"}, "test:reset", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return struct{}{}, tx.ResetConfiguration() })
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.SourceGrant(ctx, third.ID)
	webAPITestErrorCode(t, err, "not_found")
	replay, err := webAPITestAuthorize(r.store, ctx, request, grant)
	if err != nil || !replay.Replayed || !bytes.Equal(first.Body, replay.Body) {
		t.Fatal(replay, err)
	}
	_, err = r.store.SourceGrant(ctx, grant.ID)
	webAPITestErrorCode(t, err, "not_found")
}

func TestWebAPISourceAuthorizationGuardConcurrencyAndQuarantine(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("source-session")
	request := webAPITestSourceRequest("source-concurrent-op")
	grant := webAPITestSourceGrant("concurrent-grant", WebAPIRequestSessionID(ctx))
	var applied atomic.Int64
	var wg sync.WaitGroup
	out := make(chan string, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipt, err := r.store.SourceAuthorizationTransaction(ctx, request.OperationID, request, func(tx *WebAPISourceAuthorizationTx) (any, error) {
				applied.Add(1)
				return WebAPISourceAuthorizationResult{AuthorizationID: grant.ID, ExpiresAt: grant.ExpiresAt}, tx.SaveSourceGrant(grant)
			})
			if err != nil {
				out <- err.Error()
			} else {
				out <- string(receipt.Body)
			}
		}()
	}
	wg.Wait()
	close(out)
	unique := map[string]bool{}
	for body := range out {
		unique[body] = true
	}
	if len(unique) != 1 || applied.Load() != 1 {
		t.Fatal(unique, applied.Load())
	}
	revoked := WithWebAPIRequestAuthorization(context.Background(), "source-session", func(context.Context) (func(), error) { return nil, webAPIError(401, "unauthorized", "revoked") })
	_, err := r.store.SourceAuthorizationTransaction(revoked, request.OperationID, request, func(*WebAPISourceAuthorizationTx) (any, error) {
		t.Fatal("revoked authorization executed")
		return nil, nil
	})
	webAPITestErrorCode(t, err, "unauthorized")
	_, err = r.store.SourceGrant(revoked, grant.ID)
	webAPITestErrorCode(t, err, "unauthorized")
	_, err = r.store.SourceGrantReceipt(revoked, request.OperationID)
	webAPITestErrorCode(t, err, "unauthorized")
	var held atomic.Bool
	guarded := WithWebAPIRequestAuthorization(context.Background(), "source-session", func(context.Context) (func(), error) { held.Store(true); return func() { held.Store(false) }, nil })
	r.store.commit = func(tx *bolt.Tx) error {
		if !held.Load() {
			t.Error("source authorization guard released before commit")
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("injected sync acknowledgement failure")
	}
	grant.ID = "uncertain-source-grant"
	request = webAPITestSourceRequest("source-uncertain-op")
	_, err = webAPITestAuthorize(r.store, guarded, request, grant)
	webAPITestErrorCode(t, err, "durability_unknown")
	if held.Load() {
		t.Fatal("source guard leaked")
	}
	_, err = r.store.SourceGrantReceipt(ctx, request.OperationID)
	webAPITestErrorCode(t, err, "durability_unknown")
	_, err = r.store.SourceGrant(ctx, grant.ID)
	webAPITestErrorCode(t, err, "durability_unknown")
	_, err = webAPITestAuthorize(r.store, ctx, request, grant)
	webAPITestErrorCode(t, err, "durability_unknown")
	r.server.Close()
	r.store.Close()
	r.store, err = OpenWebAPIStore(r.opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.store.SourceGrantReceipt(ctx, request.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = r.store.SourceGrant(ctx, grant.ID); err != nil {
		t.Fatal(err)
	}
	if r.commits.Load() != 0 {
		t.Fatal("source authorization notified config")
	}
}

func TestWebAPISourceGrantPreparationRevisionAndSessionBindings(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("source-session")
	grant := webAPITestSourceGrant("stale-grant", WebAPIRequestSessionID(ctx))
	grant.ExpectedRevision = "1"
	_, err := webAPITestAuthorize(r.store, ctx, webAPITestSourceRequest("source-stale-preparation"), grant)
	webAPITestErrorCode(t, err, "revision_conflict")
	grant.ExpectedRevision = "0"
	grant.SessionID = "other-session"
	_, err = webAPITestAuthorize(r.store, ctx, webAPITestSourceRequest("source-mismatched-session"), grant)
	webAPITestErrorCode(t, err, "invalid_source_grant")
	grant.SessionID = WebAPIRequestSessionID(ctx)
	grant.Key = nil
	grant.Purpose = WebAPISourceProfileImport
	grant.ImportJobID = "opaque-import-job-id"
	if _, err = webAPITestAuthorize(r.store, ctx, webAPITestSourceRequest("source-bound-import-job"), grant); err != nil {
		t.Fatal(err)
	}
	for i, change := range []func(*WebAPISourceGrant){func(g *WebAPISourceGrant) { g.URLDigest = "bad" }, func(g *WebAPISourceGrant) { g.Addresses = []string{"not-an-ip"} }, func(g *WebAPISourceGrant) { g.Addresses = []string{"192.0.2.1", "192.0.2.1"} }, func(g *WebAPISourceGrant) { g.ExpiresAt = "yesterday" }, func(g *WebAPISourceGrant) { g.ImportJobID = "" }} {
		candidate := grant
		candidate.ID = fmt.Sprintf("invalid-grant-%d", i)
		change(&candidate)
		_, err = webAPITestAuthorize(r.store, ctx, webAPITestSourceRequest(fmt.Sprintf("source-invalid-grant-%d", i)), candidate)
		webAPITestErrorCode(t, err, "invalid_source_grant")
	}
}

func TestWebAPISourceGrantURLChangeDeleteAndRollbackAreAtomic(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("bound-resource-session")
	moduleURL := "https://example.invalid/module"
	_, err := r.store.Transaction(ctx, WebAPIMutation{"0", "source-owner-seed"}, "test:seed", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		if _, err := tx.CreateProfile(WebAPIProfileInput{Type: "url", Label: "Source", URL: "https://example.invalid/profile"}); err != nil {
			return nil, err
		}
		id, err := tx.AllocateID()
		if err != nil {
			return nil, err
		}
		return struct{}{}, tx.InsertMitmModule(WebAPIMitmModule{ID: id, Name: "Module", URL: &moduleURL})
	})
	if err != nil {
		t.Fatal(err)
	}
	grants := []WebAPISourceGrant{}
	for i, kind := range []string{"profile", "mitm-module", "mitm-resource"} {
		grant := webAPITestSourceGrant(fmt.Sprintf("bound-grant-%d", i), WebAPIRequestSessionID(ctx))
		grant.ExpectedRevision = "1"
		id := "2"
		if kind == "profile" {
			id = "1"
		}
		grant.Purpose = map[string]WebAPISourcePurpose{"profile": WebAPISourceProfileRefresh, "mitm-module": WebAPISourceMitmModuleRefresh, "mitm-resource": WebAPISourceMitmResourceRefresh}[kind]
		grant.Key = &WebAPISourceResource{Kind: kind, ID: id}
		if kind == "mitm-resource" {
			grant.Key.Name = "asset"
		}
		if _, err := webAPITestAuthorize(r.store, ctx, webAPITestSourceRequest(fmt.Sprintf("bound-grant-op-%d", i), "1"), grant); err != nil {
			t.Fatal(err)
		}
		grants = append(grants, grant)
	}
	_, err = r.store.Transaction(ctx, WebAPIMutation{"1", "source-change-rollback"}, "test:update", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		p, err := tx.Profile("1")
		if err != nil {
			return nil, err
		}
		p.URL = "https://example.invalid/changed"
		if err = tx.SaveProfile(p); err != nil {
			return nil, err
		}
		return nil, errors.New("rollback source edit")
	})
	if err == nil {
		t.Fatal("failed source edit committed")
	}
	if _, err = r.store.SourceGrant(ctx, grants[0].ID); err != nil {
		t.Fatal("rollback invalidated source grant", err)
	}
	_, err = r.store.Transaction(ctx, WebAPIMutation{"1", "source-change-commit"}, "test:update", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		p, err := tx.Profile("1")
		if err != nil {
			return nil, err
		}
		p.URL = "https://example.invalid/changed"
		return p, tx.SaveProfile(p)
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.SourceGrant(ctx, grants[0].ID)
	webAPITestErrorCode(t, err, "not_found")
	if _, err = r.store.SourceGrant(ctx, grants[1].ID); err != nil {
		t.Fatal(err)
	}
	_, err = r.store.Transaction(ctx, WebAPIMutation{"2", "source-delete-module"}, "test:delete", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return struct{}{}, tx.DeleteMitmModule("2") })
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range grants[1:] {
		_, err = r.store.SourceGrant(ctx, grant.ID)
		webAPITestErrorCode(t, err, "not_found")
	}
	for i := range grants {
		if _, err = r.store.SourceGrantReceipt(ctx, fmt.Sprintf("bound-grant-op-%d", i)); err != nil {
			t.Fatal("resource mutation deleted recovery receipt", err)
		}
	}
}
