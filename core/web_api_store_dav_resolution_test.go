//go:build webpanel && !android

package core

import (
	"context"
	"strings"
	"testing"
)

func webAPITestDAVRecordAt(t *testing.T, r *webAPITestRig, ctx context.Context, revision WebAPIRevision, record WebAPIDAVRecord) WebAPIDAVJob {
	t.Helper()
	var job WebAPIDAVJob
	_, err := r.store.Transaction(ctx, WebAPIMutation{revision, record.Job.OperationID}, "dav:test", record.Job.OperationID, func(tx *WebAPIWriteTx) (any, error) {
		var err error
		job, err = tx.RecordDAVJob(record)
		return job, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func webAPITestDAVFinish(t *testing.T, r *webAPITestRig, ctx context.Context, job WebAPIDAVJob, phase WebAPIDAVPhase, result *WebAPIDAVResult) WebAPIDAVJob {
	t.Helper()
	next := job
	next.Phase = WebAPIDAVApplying
	next.Stage = "starting"
	current, err := r.store.TransitionDAVJob(ctx, job.OperationID, job.Phase, next)
	if err != nil {
		t.Fatal(err)
	}
	next = current
	next.Phase = phase
	next.Stage = string(phase)
	next.Result = result
	authority := newWebAPIServiceAuthority()
	defer authority.close()
	current, err = r.store.TransitionDAVJob(authority.ctx, current.OperationID, current.Phase, next)
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func webAPITestDAVReconciliation(old WebAPIDAVRecord, job WebAPIDAVJob, op, session string) WebAPIDAVRecord {
	return WebAPIDAVRecord{Job: WebAPIDAVJob{OperationID: op, Action: WebAPIDAVReconcileAction, Phase: WebAPIDAVPending, Stage: "accepted"}, SessionID: session, Desired: WebAPIDAVDesired{TupleDigest: old.Desired.TupleDigest, ArchiveDigest: old.Desired.ArchiveDigest, ArchiveSize: old.Desired.ArchiveSize, TargetOperationID: job.OperationID, TargetDesiredRevision: job.DesiredRevision}}
}

func TestWebAPIDAVExplicitPositiveResolutionIsConsumedWithoutChangingUnknown(t *testing.T) {
	r := newWebAPITestRig(t)
	oldCtx := webAPITestSourceContext("old-dav-session")
	newCtx := webAPITestSourceContext("new-dav-session")
	original := webAPITestDAVRecord("resolution-original-upload", WebAPIRequestSessionID(oldCtx))
	oldJob := webAPITestDAVRecordAt(t, r, oldCtx, "0", original)
	oldJob = webAPITestDAVFinish(t, r, oldCtx, oldJob, WebAPIDAVUnknown, nil)
	proof := webAPITestDAVReconciliation(original, oldJob, "resolution-new-session-proof", WebAPIRequestSessionID(newCtx))
	proofJob := webAPITestDAVRecordAt(t, r, newCtx, "1", proof)
	result := &WebAPIDAVResult{ReconciliationOf: oldJob.OperationID, RemoteMatches: true, ArchiveDigest: original.Desired.ArchiveDigest, ArchiveSize: original.Desired.ArchiveSize}
	proofJob = webAPITestDAVFinish(t, r, newCtx, proofJob, WebAPIDAVSucceeded, result)
	without := webAPITestDAVRecord("resolution-without-reference", WebAPIRequestSessionID(newCtx))
	_, err := r.store.Transaction(newCtx, WebAPIMutation{"2", without.Job.OperationID}, "dav:backup", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return tx.RecordDAVJob(without) })
	webAPITestErrorCode(t, err, "dav_unresolved_upload")
	wrongSession := webAPITestDAVRecord("resolution-wrong-session", WebAPIRequestSessionID(oldCtx))
	wrongSession.Desired.ResolutionOperationID = proofJob.OperationID
	_, err = r.store.Transaction(oldCtx, WebAPIMutation{"2", wrongSession.Job.OperationID}, "dav:backup", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return tx.RecordDAVJob(wrongSession) })
	webAPITestErrorCode(t, err, "invalid_dav_resolution")
	next := webAPITestDAVRecord("resolution-explicit-next-upload", WebAPIRequestSessionID(newCtx))
	next.Desired.ResolutionOperationID = proofJob.OperationID
	next.Desired.ArchiveDigest = strings.Repeat("c", 64)
	nextJob := webAPITestDAVRecordAt(t, r, newCtx, "2", next)
	reused := webAPITestDAVRecord("resolution-cannot-reuse-proof", WebAPIRequestSessionID(newCtx))
	reused.Desired.ResolutionOperationID = proofJob.OperationID
	_, err = r.store.Transaction(newCtx, WebAPIMutation{"3", reused.Job.OperationID}, "dav:backup", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return tx.RecordDAVJob(reused) })
	webAPITestErrorCode(t, err, "dav_resolution_consumed")
	nextJob = webAPITestDAVFinish(t, r, newCtx, nextJob, WebAPIDAVSucceeded, &WebAPIDAVResult{ArchiveDigest: next.Desired.ArchiveDigest, ArchiveSize: next.Desired.ArchiveSize})
	third := webAPITestDAVRecord("resolution-later-upload", WebAPIRequestSessionID(newCtx))
	_ = webAPITestDAVRecordAt(t, r, newCtx, "3", third)
	_, err = r.store.View(newCtx, func(tx *WebAPIReadTx) error {
		record, err := tx.DAVJob(oldJob.OperationID)
		if err != nil {
			return err
		}
		if record.Job.Phase != WebAPIDAVUnknown || record.Desired != original.Desired {
			t.Fatal("positive proof rewrote immutable old intent", record)
		}
		consumer, err := tx.DAVJob(nextJob.OperationID)
		if consumer.Desired.ResolutionOperationID != proofJob.OperationID {
			t.Fatal("consumed relation was not durable")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebAPIDAVReconcileRequiresExactUnknownGenerationAndEvidence(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("resolution-owner")
	original := webAPITestDAVRecord("evidence-original-upload", WebAPIRequestSessionID(ctx))
	job := webAPITestDAVRecordAt(t, r, ctx, "0", original)
	pendingProof := webAPITestDAVReconciliation(original, job, "evidence-pending-not-unknown", WebAPIRequestSessionID(ctx))
	_, err := r.store.Transaction(ctx, WebAPIMutation{"1", pendingProof.Job.OperationID}, "dav:reconcile", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return tx.RecordDAVJob(pendingProof) })
	webAPITestErrorCode(t, err, "invalid_dav_resolution")
	job = webAPITestDAVFinish(t, r, ctx, job, WebAPIDAVUnknown, nil)
	proof := webAPITestDAVReconciliation(original, job, "evidence-proof", WebAPIRequestSessionID(ctx))
	for i, change := range []func(*WebAPIDAVRecord){func(r *WebAPIDAVRecord) { r.Desired.TargetDesiredRevision = "2" }, func(r *WebAPIDAVRecord) { r.Desired.TupleDigest = strings.Repeat("c", 64) }, func(r *WebAPIDAVRecord) { r.Desired.ArchiveDigest = strings.Repeat("c", 64) }, func(r *WebAPIDAVRecord) { r.Desired.ArchiveSize++ }} {
		candidate := proof
		candidate.Job.OperationID = proof.Job.OperationID + string(rune('a'+i))
		change(&candidate)
		_, err = r.store.Transaction(ctx, WebAPIMutation{"1", candidate.Job.OperationID}, "dav:reconcile", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return tx.RecordDAVJob(candidate) })
		webAPITestErrorCode(t, err, "invalid_dav_resolution")
	}
	proofJob := webAPITestDAVRecordAt(t, r, ctx, "1", proof)
	proofJob = webAPITestDAVFinish(t, r, ctx, proofJob, WebAPIDAVSucceeded, &WebAPIDAVResult{ReconciliationOf: job.OperationID, RemoteMatches: false, ArchiveDigest: original.Desired.ArchiveDigest, ArchiveSize: original.Desired.ArchiveSize})
	next := webAPITestDAVRecord("evidence-false-is-not-proof", WebAPIRequestSessionID(ctx))
	next.Desired.ResolutionOperationID = proofJob.OperationID
	_, err = r.store.Transaction(ctx, WebAPIMutation{"2", next.Job.OperationID}, "dav:backup", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return tx.RecordDAVJob(next) })
	webAPITestErrorCode(t, err, "invalid_dav_resolution")
}

func TestWebAPIDAVNewUnknownRequiresItsOwnProofAfterRestart(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("resolution-session")
	original := webAPITestDAVRecord("generation-one-upload", WebAPIRequestSessionID(ctx))
	oldJob := webAPITestDAVRecordAt(t, r, ctx, "0", original)
	oldJob = webAPITestDAVFinish(t, r, ctx, oldJob, WebAPIDAVUnknown, nil)
	proof := webAPITestDAVReconciliation(original, oldJob, "generation-one-proof", WebAPIRequestSessionID(ctx))
	proofJob := webAPITestDAVRecordAt(t, r, ctx, "1", proof)
	proofJob = webAPITestDAVFinish(t, r, ctx, proofJob, WebAPIDAVSucceeded, &WebAPIDAVResult{RemoteMatches: true, ReconciliationOf: oldJob.OperationID, ArchiveDigest: original.Desired.ArchiveDigest, ArchiveSize: original.Desired.ArchiveSize})
	next := webAPITestDAVRecord("generation-two-upload", WebAPIRequestSessionID(ctx))
	next.Desired.ResolutionOperationID = proofJob.OperationID
	job := webAPITestDAVRecordAt(t, r, ctx, "2", next)
	_ = webAPITestDAVFinish(t, r, ctx, job, WebAPIDAVUnknown, nil)
	r.server.Close()
	r.store.Close()
	var err error
	r.store, err = OpenWebAPIStore(r.opts)
	if err != nil {
		t.Fatal(err)
	}
	third := webAPITestDAVRecord("generation-three-blocked", WebAPIRequestSessionID(ctx))
	_, err = r.store.Transaction(ctx, WebAPIMutation{"3", third.Job.OperationID}, "dav:backup", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return tx.RecordDAVJob(third) })
	webAPITestErrorCode(t, err, "dav_unresolved_upload")
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		record, err := tx.DAVJob(oldJob.OperationID)
		if record.Job.Phase != WebAPIDAVUnknown {
			t.Fatal(record)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
