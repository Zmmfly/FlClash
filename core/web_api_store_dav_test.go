//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	bolt "github.com/metacubex/bbolt"
)

func webAPITestDAVRecord(op, session string) WebAPIDAVRecord {
	return WebAPIDAVRecord{Job: WebAPIDAVJob{OperationID: op, Action: WebAPIDAVBackupAction, Phase: WebAPIDAVPending, Stage: "accepted", UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, SessionID: session, Desired: WebAPIDAVDesired{TupleDigest: strings.Repeat("a", 64), AuthorizationID: "private-dav-source-authorization", Format: "flclash-v1", ArchiveDigest: strings.Repeat("b", 64), ArchiveSize: 16}}
}

func TestWebAPIDAVIntentAtomicReceiptAndStatusAuthority(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("dav-session")
	op := "dav-intent-operation"
	record := webAPITestDAVRecord(op, WebAPIRequestSessionID(ctx))
	var job WebAPIDAVJob
	receipt, err := r.store.Transaction(ctx, WebAPIMutation{"0", op}, "dav:backup", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		var err error
		job, err = tx.RecordDAVJob(record)
		return job, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.DesiredRevision != "1" || receipt.Revision != "1" {
		t.Fatal(job, receipt)
	}
	for _, secret := range []string{record.SessionID, record.Desired.TupleDigest, record.Desired.AuthorizationID, record.Desired.ArchiveDigest, `"desired":`} {
		if bytes.Contains(receipt.Body, []byte(secret)) {
			t.Fatal("private DAV intent leaked into receipt", secret)
		}
	}
	authority := newWebAPIServiceAuthority()
	defer authority.close()
	next := job
	next.Phase = WebAPIDAVApplying
	next.Stage = "starting"
	_, err = r.store.TransitionDAVJob(authority.ctx, op, job.Phase, next)
	webAPITestErrorCode(t, err, "dav_status_only")
	_, err = r.store.TransitionDAVJob(webAPITestSourceContext("wrong-session"), op, job.Phase, next)
	webAPITestErrorCode(t, err, "dav_session_mismatch")
	job, err = r.store.TransitionDAVJob(ctx, op, job.Phase, next)
	if err != nil {
		t.Fatal(err)
	}
	next = job
	next.Stage = "checking-remote"
	next.Result = &WebAPIDAVResult{ETag: `"one"`, Conditional: true}
	job, err = r.store.TransitionDAVJob(authority.ctx, op, job.Phase, next)
	if err != nil {
		t.Fatal(err)
	}
	backward := job
	backward.Stage = "creating-collection"
	_, err = r.store.TransitionDAVJob(authority.ctx, op, job.Phase, backward)
	webAPITestErrorCode(t, err, "invalid_dav_transition")
	mutated := job
	mutated.DesiredRevision = "2"
	_, err = r.store.TransitionDAVJob(authority.ctx, op, job.Phase, mutated)
	webAPITestErrorCode(t, err, "immutable_dav_intent")
	next = job
	next.Phase = WebAPIDAVSucceeded
	next.Stage = "succeeded"
	job, err = r.store.TransitionDAVJob(authority.ctx, op, job.Phase, next)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.TransitionDAVJob(ctx, op, WebAPIDAVApplying, next)
	webAPITestErrorCode(t, err, "dav_phase_conflict")
	next = job
	next.Phase = WebAPIDAVApplying
	next.Stage = "starting"
	_, err = r.store.TransitionDAVJob(ctx, op, job.Phase, next)
	webAPITestErrorCode(t, err, "invalid_dav_transition")
	replay, err := r.store.Transaction(ctx, WebAPIMutation{"0", op}, "dav:backup", struct{}{}, func(*WebAPIWriteTx) (any, error) { t.Fatal("receipt replay recreated DAV effect"); return nil, nil })
	if err != nil || !bytes.Equal(replay.Body, receipt.Body) {
		t.Fatal(replay, err)
	}
	_, err = r.store.View(ctx, func(tx *WebAPIReadTx) error {
		saved, err := tx.DAVJob(op)
		if err != nil {
			return err
		}
		if saved.Desired != record.Desired || saved.SessionID != record.SessionID || saved.Job.Phase != WebAPIDAVSucceeded {
			t.Fatal(saved)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.commits.Load() != 1 {
		t.Fatal("status transitions incremented configuration or notified runtime")
	}
}

func TestWebAPIDAVRecordAuthorityRollbackAndRecovery(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("dav-owner")
	for i, session := range []string{"", "other"} {
		op := fmt.Sprintf("dav-wrong-authority-%d", i)
		_, err := r.store.Transaction(ctx, WebAPIMutation{"0", op}, "dav:backup", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return tx.RecordDAVJob(webAPITestDAVRecord(op, session)) })
		webAPITestErrorCode(t, err, "unauthorized")
	}
	_, err := r.store.Transaction(ctx, WebAPIMutation{"0", "dav-rollback-intent"}, "dav:backup", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		if _, err := tx.RecordDAVJob(webAPITestDAVRecord("dav-rollback-intent", WebAPIRequestSessionID(ctx))); err != nil {
			return nil, err
		}
		return nil, errors.New("fail before config commit")
	})
	if err == nil {
		t.Fatal("failed DAV admission committed")
	}
	_, err = r.store.View(ctx, func(tx *WebAPIReadTx) error {
		_, err := tx.DAVJob("dav-rollback-intent")
		webAPITestErrorCode(t, err, "not_found")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.Transaction(ctx, WebAPIMutation{"0", "dav-restart-pending"}, "dav:backup", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		return tx.RecordDAVJob(webAPITestDAVRecord("dav-restart-pending", WebAPIRequestSessionID(ctx)))
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.Transaction(ctx, WebAPIMutation{"1", "dav-restart-applying"}, "dav:backup", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		record := webAPITestDAVRecord("dav-restart-applying", WebAPIRequestSessionID(ctx))
		record.Desired.TupleDigest = strings.Repeat("c", 64)
		return tx.RecordDAVJob(record)
	})
	if err != nil {
		t.Fatal(err)
	}
	var applying WebAPIDAVJob
	_, err = r.store.View(ctx, func(tx *WebAPIReadTx) error {
		record, err := tx.DAVJob("dav-restart-applying")
		applying = record.Job
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	applying.Phase = WebAPIDAVApplying
	applying.Stage = "starting"
	if _, err = r.store.TransitionDAVJob(ctx, applying.OperationID, WebAPIDAVPending, applying); err != nil {
		t.Fatal(err)
	}
	r.server.Close()
	r.store.Close()
	r.store, err = OpenWebAPIStore(r.opts)
	if err != nil {
		t.Fatal(err)
	}
	webAPITestErrorCode(t, r.store.RecoverDAVJobs(context.Background()), "unauthorized")
	authority := newWebAPIServiceAuthority()
	defer authority.close()
	if err = r.store.RecoverDAVJobs(authority.ctx); err != nil {
		t.Fatal(err)
	}
	revision, err := r.store.View(ctx, func(tx *WebAPIReadTx) error {
		records, err := tx.DAVJobs()
		if len(records) != 2 {
			t.Fatal(records)
		}
		for _, record := range records {
			if record.Job.Phase != WebAPIDAVUnknown || record.Job.Error == nil {
				t.Fatal(record)
			}
		}
		return err
	})
	if err != nil || revision != "2" {
		t.Fatal(revision, err)
	}
	if err = r.store.RecoverDAVJobs(authority.ctx); err != nil {
		t.Fatal(err)
	}
	receipt, err := r.store.Receipt(ctx, "dav-restart-pending")
	if err != nil {
		t.Fatal(err)
	}
	var envelope WebAPIEnvelope[WebAPIDAVJob]
	if err = json.Unmarshal(receipt.Body, &envelope); err != nil || envelope.Data.Phase != WebAPIDAVPending {
		t.Fatal("recovery rewrote original admission receipt", err)
	}
}

func TestWebAPIDAVLedgerCapacityAndDurabilityUnknown(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("dav-owner")
	for i := 0; i < WebAPIDAVMaxConcurrent; i++ {
		op := fmt.Sprintf("dav-concurrent-job-%d", i)
		_, err := r.store.Transaction(ctx, WebAPIMutation{WebAPIRevision(fmt.Sprint(i)), op}, "dav:backup", i, func(tx *WebAPIWriteTx) (any, error) {
			record := webAPITestDAVRecord(op, WebAPIRequestSessionID(ctx))
			record.Desired.TupleDigest = fmt.Sprintf("%064x", i+1)
			return tx.RecordDAVJob(record)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := r.store.Transaction(ctx, WebAPIMutation{"4", "dav-concurrent-over-cap"}, "dav:backup", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		return tx.RecordDAVJob(webAPITestDAVRecord("dav-concurrent-over-cap", WebAPIRequestSessionID(ctx)))
	})
	webAPITestErrorCode(t, err, "dav_busy")
	if err = r.store.db.Update(func(tx *bolt.Tx) error {
		for i := 0; i < WebAPIDAVMaxJobs; i++ {
			record := webAPITestDAVRecord(fmt.Sprintf("dav-capacity-job-%03d", i), "dav-owner")
			record.Job.Phase = WebAPIDAVUnknown
			record.Job.DesiredRevision = "1"
			if err := webAPIPut(tx, "dav-jobs", record.Job.OperationID, record); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err = r.store.Transaction(ctx, WebAPIMutation{"4", "dav-total-over-cap"}, "dav:backup", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		record := webAPITestDAVRecord("dav-total-over-cap", WebAPIRequestSessionID(ctx))
		record.Desired.TupleDigest = strings.Repeat("d", 64)
		return tx.RecordDAVJob(record)
	})
	webAPITestErrorCode(t, err, "dav_job_capacity")

	r.store.commit = func(tx *bolt.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("injected DAV status ack failure")
	}
	var pending WebAPIDAVJob
	_, err = r.store.View(ctx, func(tx *WebAPIReadTx) error {
		record, err := tx.DAVJob("dav-concurrent-job-0")
		pending = record.Job
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	next := pending
	next.Phase = WebAPIDAVApplying
	next.Stage = "starting"
	_, err = r.store.TransitionDAVJob(ctx, pending.OperationID, pending.Phase, next)
	webAPITestErrorCode(t, err, "durability_unknown")
	_, err = r.store.View(ctx, func(tx *WebAPIReadTx) error { _, err := tx.DAVJob(pending.OperationID); return err })
	webAPITestErrorCode(t, err, "durability_unknown")
}

func TestWebAPIDAVRestoreRecordCommitsWithRestoredConfiguration(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("dav-restore-session")
	record := webAPITestDAVRecord("dav-confirm-restore", WebAPIRequestSessionID(ctx))
	record.Job.Action = WebAPIDAVRestoreAction
	record.Job.Phase = WebAPIDAVSucceeded
	record.Job.Stage = "succeeded"
	record.Job.Result = &WebAPIDAVResult{Restored: &WebAPIBackupResult{Restored: true, ProfileCount: 1}}
	record.Desired.Strategy = "override"
	receipt, err := r.store.Transaction(ctx, WebAPIMutation{"0", record.Job.OperationID}, "dav:restore", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		if _, err := tx.CreateProfile(WebAPIProfileInput{Type: "file", Label: "Restored"}); err != nil {
			return nil, err
		}
		return tx.RecordDAVJob(record)
	})
	if err != nil {
		t.Fatal(err)
	}
	result := webAPITestData[WebAPIDAVJob](t, receipt.Body)
	if result.Revision != "1" || result.Data.Phase != WebAPIDAVSucceeded || result.Data.DesiredRevision != "1" {
		t.Fatal(result)
	}
	revision, err := r.store.View(ctx, func(tx *WebAPIReadTx) error {
		profiles, err := tx.Profiles()
		if err != nil {
			return err
		}
		job, err := tx.DAVJob(record.Job.OperationID)
		if len(profiles) != 1 || job.Job.Phase != WebAPIDAVSucceeded {
			t.Fatal(profiles, job)
		}
		return err
	})
	if err != nil || revision != "1" {
		t.Fatal(revision, err)
	}
	record.Job.OperationID = "dav-reject-terminal-transfer"
	record.Job.Action = WebAPIDAVBackupAction
	_, err = r.store.Transaction(ctx, WebAPIMutation{"1", record.Job.OperationID}, "dav:backup", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return tx.RecordDAVJob(record) })
	webAPITestErrorCode(t, err, "invalid_dav_job")
	if r.commits.Load() != 1 {
		t.Fatal("restore took an extra configuration revision")
	}
}
