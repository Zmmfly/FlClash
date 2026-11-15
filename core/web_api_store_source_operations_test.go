//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"errors"
	"testing"

	bolt "github.com/metacubex/bbolt"
)

func TestWebAPISourceRevisionEnvelopeReplayBeforeCAS(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("source-revision-session")
	request := webAPITestSourceRequest("source-revision-first")
	grant := webAPITestSourceGrant("source-revision-grant", WebAPIRequestSessionID(ctx))
	first, err := webAPITestAuthorize(r.store, ctx, request, grant)
	if err != nil {
		t.Fatal(err)
	}
	body := webAPITestData[WebAPISourceAuthorizationResult](t, first.Body)
	if first.Revision != "0" || body.Revision != "0" || body.OperationID != request.OperationID {
		t.Fatal(first, body)
	}
	r.call("POST", "/api/v1/profiles:create", webAPITestMutation("0", "source-revision-config", map[string]any{"profile": webAPITestProfile("New business revision")}), 200)
	replay, err := r.store.SourceAuthorizationTransaction(ctx, request.OperationID, request, func(*WebAPISourceAuthorizationTx) (any, error) {
		t.Fatal("old successful operation was rerun")
		return nil, nil
	})
	if err != nil || replay.Revision != "0" || !bytes.Equal(replay.Body, first.Body) {
		t.Fatal(replay, err)
	}
	resolved, err := r.store.ResolveSourceOperation(ctx, request.OperationID)
	if err != nil || resolved.Revision != "0" || !bytes.Equal(resolved.Body, first.Body) {
		t.Fatal(resolved, err)
	}
	changed := request
	changed.ExpectedRevision = "1"
	_, err = r.store.SourceAuthorizationTransaction(ctx, changed.OperationID, changed, func(*WebAPISourceAuthorizationTx) (any, error) { t.Fatal("modified replay executed"); return nil, nil })
	webAPITestErrorCode(t, err, "operation_conflict")
	stale := webAPITestSourceRequest("source-revision-stale")
	_, err = r.store.SourceAuthorizationTransaction(ctx, stale.OperationID, stale, func(*WebAPISourceAuthorizationTx) (any, error) { t.Fatal("stale prepare executed"); return nil, nil })
	webAPITestErrorCode(t, err, "revision_conflict")
	fresh := webAPITestSourceRequest("source-revision-restamp", "1")
	grant.ID = "restamped-old-preparation"
	_, err = webAPITestAuthorize(r.store, ctx, fresh, grant)
	webAPITestErrorCode(t, err, "revision_conflict")
	grant.ExpectedRevision = "1"
	success, err := webAPITestAuthorize(r.store, ctx, fresh, grant)
	if err != nil || success.Revision != "1" {
		t.Fatal(success, err)
	}
	meta := webAPITestData[WebAPIStoreMeta](t, r.call("GET", "/api/v1/meta", nil, 200))
	if meta.Revision != "1" || r.commits.Load() != 1 {
		t.Fatal("source operation incremented business revision", meta)
	}
	missing := webAPITestSourceRequest("source-missing-revision")
	missing.ExpectedRevision = ""
	_, err = r.store.SourceAuthorizationTransaction(ctx, missing.OperationID, missing, func(*WebAPISourceAuthorizationTx) (any, error) { t.Fatal("missing revision executed"); return nil, nil })
	webAPITestErrorCode(t, err, "invalid_revision")
}

func TestWebAPISourceResolveTombstoneSessionRestartAndLateAuthorization(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("source-resolve-session")
	request := webAPITestSourceRequest("source-never-committed")
	_, err := r.store.SourceGrantReceipt(ctx, request.OperationID)
	webAPITestErrorCode(t, err, "not_found")
	_, err = r.store.ResolveSourceOperation(ctx, request.OperationID)
	webAPITestErrorCode(t, err, "operation_not_committed")
	_, err = r.store.SourceGrantReceipt(ctx, request.OperationID)
	webAPITestErrorCode(t, err, "operation_not_committed")
	_, err = r.store.SourceAuthorizationTransaction(ctx, request.OperationID, request, func(*WebAPISourceAuthorizationTx) (any, error) {
		t.Fatal("resolved operation authorized a late request")
		return nil, nil
	})
	webAPITestErrorCode(t, err, "operation_not_committed")
	other := webAPITestSourceContext("source-other-session")
	_, err = r.store.ResolveSourceOperation(other, request.OperationID)
	webAPITestErrorCode(t, err, "source_authorization_mismatch")
	_, err = r.store.SourceGrantReceipt(other, request.OperationID)
	webAPITestErrorCode(t, err, "source_authorization_mismatch")
	_, err = r.store.SourceAuthorizationTransaction(other, request.OperationID, request, func(*WebAPISourceAuthorizationTx) (any, error) {
		t.Fatal("another session bypassed a tombstone")
		return nil, nil
	})
	webAPITestErrorCode(t, err, "source_authorization_mismatch")
	_, err = r.store.ResolveSourceOperation(context.Background(), request.OperationID)
	webAPITestErrorCode(t, err, "unauthorized")
	_, err = r.store.Transaction(ctx, WebAPIMutation{"0", "source-resolve-reset"}, "test:reset", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return struct{}{}, tx.ResetConfiguration() })
	if err != nil {
		t.Fatal(err)
	}
	r.server.Close()
	r.store.Close()
	r.store, err = OpenWebAPIStore(r.opts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.ResolveSourceOperation(ctx, request.OperationID)
	webAPITestErrorCode(t, err, "operation_not_committed")
	_, err = r.store.SourceAuthorizationTransaction(ctx, request.OperationID, request, func(*WebAPISourceAuthorizationTx) (any, error) {
		t.Fatal("restart or reset erased the source tombstone")
		return nil, nil
	})
	webAPITestErrorCode(t, err, "operation_not_committed")
	_, err = r.store.Receipt(ctx, request.OperationID)
	webAPITestErrorCode(t, err, "not_found")
	_, err = r.store.CacheReceipt(ctx, request.OperationID)
	webAPITestErrorCode(t, err, "not_found")
}

func TestWebAPISourceResolveRacesCommittedAuthorization(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("source-race-session")
	request := webAPITestSourceRequest("source-resolve-inflight")
	grant := webAPITestSourceGrant("source-inflight-grant", WebAPIRequestSessionID(ctx))
	entered := make(chan struct{})
	release := make(chan struct{})
	type outcome struct {
		receipt WebAPISourceAuthorizationReceipt
		err     error
	}
	commit := make(chan outcome, 1)
	resolution := make(chan outcome, 1)
	go func() {
		receipt, err := r.store.SourceAuthorizationTransaction(ctx, request.OperationID, request, func(tx *WebAPISourceAuthorizationTx) (any, error) {
			close(entered)
			<-release
			return WebAPISourceAuthorizationResult{AuthorizationID: grant.ID, ExpiresAt: grant.ExpiresAt}, tx.SaveSourceGrant(grant)
		})
		commit <- outcome{receipt, err}
	}()
	<-entered
	go func() {
		receipt, err := r.store.ResolveSourceOperation(ctx, request.OperationID)
		resolution <- outcome{receipt, err}
	}()
	close(release)
	before, after := <-commit, <-resolution
	if before.err != nil || after.err != nil || !bytes.Equal(before.receipt.Body, after.receipt.Body) || after.receipt.Revision != "0" {
		t.Fatal(before, after)
	}
	if r.commits.Load() != 0 {
		t.Fatal("source resolution notified config")
	}
	_, err := r.store.ResolveSourceOperation(webAPITestSourceContext("wrong-session"), request.OperationID)
	webAPITestErrorCode(t, err, "source_authorization_mismatch")
}

func TestWebAPISourceResolveDurabilityUnknownQuarantines(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("source-resolution-uncertain")
	r.store.commit = func(tx *bolt.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("injected source tombstone sync acknowledgement failure")
	}
	_, err := r.store.ResolveSourceOperation(ctx, "uncertain-source-tombstone")
	webAPITestErrorCode(t, err, "durability_unknown")
	_, err = r.store.SourceGrantReceipt(ctx, "uncertain-source-tombstone")
	webAPITestErrorCode(t, err, "durability_unknown")
	r.server.Close()
	r.store.Close()
	r.store, err = OpenWebAPIStore(r.opts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.SourceGrantReceipt(ctx, "uncertain-source-tombstone")
	webAPITestErrorCode(t, err, "operation_not_committed")
	request := webAPITestSourceRequest("uncertain-source-tombstone")
	_, err = r.store.SourceAuthorizationTransaction(ctx, request.OperationID, request, func(*WebAPISourceAuthorizationTx) (any, error) {
		t.Fatal("uncertain resolved operation executed after reopen")
		return nil, nil
	})
	webAPITestErrorCode(t, err, "operation_not_committed")
	if r.commits.Load() != 0 {
		t.Fatal("resolution changed configuration")
	}
}
