//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestWebAPIDAVUnresolvedUploadBlocksNewOperationAcrossSessionsAndRestart(t *testing.T) {
	r := newWebAPITestRig(t)
	firstCtx := webAPITestSourceContext("dav-first-browser")
	secondCtx := webAPITestSourceContext("dav-second-browser")
	original := webAPITestDAVRecord("dav-original-upload", WebAPIRequestSessionID(firstCtx))
	first, err := r.store.Transaction(firstCtx, WebAPIMutation{"0", original.Job.OperationID}, "dav:backup", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return tx.RecordDAVJob(original) })
	if err != nil {
		t.Fatal(err)
	}
	assertBlocked := func(revision WebAPIRevision, op string) {
		t.Helper()
		record := webAPITestDAVRecord(op, WebAPIRequestSessionID(secondCtx))
		_, err := r.store.Transaction(secondCtx, WebAPIMutation{revision, op}, "dav:backup", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return tx.RecordDAVJob(record) })
		var problem *WebAPIError
		if !errors.As(err, &problem) || problem.Code != "dav_unresolved_upload" || problem.Status != 409 || problem.Details["operationId"] != original.Job.OperationID {
			t.Fatal(err)
		}
		_, err = r.store.Receipt(secondCtx, op)
		webAPITestErrorCode(t, err, "not_found")
	}
	assertBlocked("1", "dav-browser-new-id-pending")
	job := webAPITestData[WebAPIDAVJob](t, first.Body).Data
	next := job
	next.Phase = WebAPIDAVApplying
	next.Stage = "starting"
	job, err = r.store.TransitionDAVJob(firstCtx, job.OperationID, job.Phase, next)
	if err != nil {
		t.Fatal(err)
	}
	assertBlocked("1", "dav-browser-new-id-applying")
	other := webAPITestDAVRecord("dav-other-target-upload", WebAPIRequestSessionID(secondCtx))
	other.Desired.TupleDigest = strings.Repeat("c", 64)
	_, err = r.store.Transaction(secondCtx, WebAPIMutation{"1", other.Job.OperationID}, "dav:backup", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return tx.RecordDAVJob(other) })
	if err != nil {
		t.Fatal("unrelated DAV target was blocked", err)
	}
	r.server.Close()
	r.store.Close()
	r.store, err = OpenWebAPIStore(r.opts)
	if err != nil {
		t.Fatal(err)
	}
	authority := newWebAPIServiceAuthority()
	defer authority.close()
	if err = r.store.RecoverDAVJobs(authority.ctx); err != nil {
		t.Fatal(err)
	}
	assertBlocked("2", "dav-browser-new-id-after-restart")
	replay, err := r.store.Transaction(firstCtx, WebAPIMutation{"0", original.Job.OperationID}, "dav:backup", struct{}{}, func(*WebAPIWriteTx) (any, error) {
		t.Fatal("same operation replay repeated an upload")
		return nil, nil
	})
	if err != nil || !bytes.Equal(first.Body, replay.Body) {
		t.Fatal(replay, err)
	}
	_, err = r.store.Transaction(secondCtx, WebAPIMutation{"2", "dav-unresolved-config-reset"}, "app:reset", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return struct{}{}, tx.ResetConfiguration() })
	if err != nil {
		t.Fatal(err)
	}
	assertBlocked("3", "dav-browser-new-id-after-reset")
}

func TestWebAPIDAVTupleGuardSerializesAdmission(t *testing.T) {
	r := newWebAPITestRig(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, name := range []string{"dav-session-one", "dav-session-two"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			<-start
			ctx := webAPITestSourceContext(name)
			_, err := r.store.Transaction(ctx, WebAPIMutation{"0", name + "-upload"}, "dav:backup", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
				return tx.RecordDAVJob(webAPITestDAVRecord(name+"-upload", name))
			})
			results <- err
		}(name)
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
			continue
		}
		var problem *WebAPIError
		if !errors.As(err, &problem) || problem.Code != "revision_conflict" {
			t.Fatal(err)
		}
		conflicts++
	}
	if success != 1 || conflicts != 1 {
		t.Fatal(success, conflicts)
	}
	revision, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		records, err := tx.DAVJobs()
		if len(records) != 1 {
			t.Fatal("two same-target jobs admitted", records)
		}
		return err
	})
	if err != nil || revision != "1" {
		t.Fatal(revision, err)
	}
}

func TestWebAPIDAVLogoutBeforeStartCanRecordNoEffectFailure(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("dav-before-effect-session")
	record := webAPITestDAVRecord("dav-before-effect-job", WebAPIRequestSessionID(ctx))
	job := webAPITestDAVRecordAt(t, r, ctx, "0", record)
	revoked := WithWebAPIRequestAuthorization(context.Background(), record.SessionID, func(context.Context) (func(), error) { return nil, webAPIError(401, "unauthorized", "logged out") })
	next := job
	next.Phase = WebAPIDAVApplying
	next.Stage = "starting"
	effects := 0
	_, err := r.store.TransitionDAVJob(revoked, job.OperationID, job.Phase, next)
	if err == nil {
		effects++
	}
	webAPITestErrorCode(t, err, "unauthorized")
	authority := newWebAPIServiceAuthority()
	defer authority.close()
	_, err = r.store.TransitionDAVJob(authority.ctx, job.OperationID, job.Phase, next)
	webAPITestErrorCode(t, err, "dav_status_only")
	failed := job
	failed.Phase = WebAPIDAVFailed
	failed.Stage = "before-effect"
	failed.Error = webAPIError(401, "dav_not_started", "DAV operation did not start")
	if _, err = r.store.TransitionDAVJob(authority.ctx, job.OperationID, job.Phase, failed); err != nil {
		t.Fatal(err)
	}
	if effects != 0 {
		t.Fatal("logout allowed a new DAV effect")
	}
	newCtx := webAPITestSourceContext("new-dav-session-after-logout")
	again := webAPITestDAVRecord("dav-after-zero-effect-failure", WebAPIRequestSessionID(newCtx))
	_ = webAPITestDAVRecordAt(t, r, newCtx, "1", again)
}

func TestWebAPIDAVInternalPendingTerminalRequiresNarrowEvidence(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("dav-narrow-status-session")
	record := webAPITestDAVRecord("dav-narrow-pending-job", WebAPIRequestSessionID(ctx))
	job := webAPITestDAVRecordAt(t, r, ctx, "0", record)
	authority := newWebAPIServiceAuthority()
	defer authority.close()
	for _, next := range []WebAPIDAVJob{
		{OperationID: job.OperationID, DesiredRevision: job.DesiredRevision, Action: job.Action, Phase: WebAPIDAVSucceeded, Stage: "before-effect"},
		{OperationID: job.OperationID, DesiredRevision: job.DesiredRevision, Action: job.Action, Phase: WebAPIDAVFailed, Stage: "before-effect", Error: webAPIError(500, "arbitrary_error", "no proof")},
		{OperationID: job.OperationID, DesiredRevision: job.DesiredRevision, Action: job.Action, Phase: WebAPIDAVFailed, Stage: "failed", Error: webAPIError(401, "dav_not_started", "bad stage")},
		{OperationID: job.OperationID, DesiredRevision: job.DesiredRevision, Action: job.Action, Phase: WebAPIDAVFailed, Stage: "before-effect", Error: webAPIError(401, "dav_not_started", "result is forbidden"), Result: &WebAPIDAVResult{}},
	} {
		_, err := r.store.TransitionDAVJob(authority.ctx, job.OperationID, job.Phase, next)
		webAPITestErrorCode(t, err, "dav_status_only")
	}
	applying := job
	applying.Phase = WebAPIDAVApplying
	applying.Stage = "starting"
	if _, err := r.store.TransitionDAVJob(ctx, job.OperationID, job.Phase, applying); err != nil {
		t.Fatal(err)
	}
	failed := job
	failed.Phase = WebAPIDAVFailed
	failed.Stage = "before-effect"
	failed.Error = webAPIError(401, "dav_not_started", "stale assertion")
	_, err := r.store.TransitionDAVJob(authority.ctx, job.OperationID, WebAPIDAVPending, failed)
	webAPITestErrorCode(t, err, "dav_phase_conflict")
}
