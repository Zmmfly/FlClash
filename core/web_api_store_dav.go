//go:build webpanel && !android

package core

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	bolt "github.com/metacubex/bbolt"
)

func (tx *WebAPIReadTx) DAVJob(id string) (WebAPIDAVRecord, error) {
	var record WebAPIDAVRecord
	if !webAPIOperationID(id) {
		return record, webAPIError(400, "invalid_operation_id", "invalid DAV job identity")
	}
	err := webAPIGet(tx.tx, "dav-jobs", id, &record)
	return record, err
}

func (tx *WebAPIReadTx) DAVJobs() ([]WebAPIDAVRecord, error) {
	return webAPIList[WebAPIDAVRecord](tx.tx, "dav-jobs")
}

func webAPIDAVActive(phase WebAPIDAVPhase) bool {
	return phase == WebAPIDAVPending || phase == WebAPIDAVApplying
}
func webAPIDAVTerminal(phase WebAPIDAVPhase) bool {
	return phase == WebAPIDAVPrepared || phase == WebAPIDAVSucceeded || phase == WebAPIDAVFailed || phase == WebAPIDAVUnknown
}

func (tx *WebAPIWriteTx) RecordDAVJob(record WebAPIDAVRecord) (WebAPIDAVJob, error) {
	var empty WebAPIDAVJob
	if tx.authority == nil || tx.authority.sessionID == "" || record.SessionID != tx.authority.sessionID || record.Job.OperationID != tx.authority.operationID {
		return empty, webAPIError(401, "unauthorized", "DAV intent must belong to the current authenticated configuration transaction")
	}
	if tx.authority.davRecorded {
		return empty, webAPIError(409, "duplicate_dav_job", "a configuration operation can record only one DAV intent")
	}
	restored := record.Job.Action == WebAPIDAVRestoreAction && record.Job.Phase == WebAPIDAVSucceeded && record.Job.Result != nil && record.Job.Result.Restored != nil && record.Job.Result.Restored.Restored && record.Job.Error == nil
	if !webAPIOperationID(record.Job.OperationID) || (!restored && (record.Job.Phase != WebAPIDAVPending || record.Job.Result != nil || record.Job.Error != nil)) {
		return empty, webAPIError(400, "invalid_dav_job", "new DAV intent must be pending or an atomically completed restore")
	}
	if !webAPIContains([]string{string(WebAPIDAVProbeAction), string(WebAPIDAVBackupAction), string(WebAPIDAVPrepareRestoreAction), string(WebAPIDAVRestoreAction), string(WebAPIDAVReconcileAction)}, string(record.Job.Action)) {
		return empty, webAPIError(400, "invalid_dav_job", "invalid DAV action")
	}
	if !webAPISourceDigest(record.Desired.TupleDigest) || record.Desired.ArchiveSize < 0 || record.Desired.ArchiveSize > WebAPIDAVMaxArchiveBytes || record.Desired.ArchiveDigest != "" && !webAPISourceDigest(record.Desired.ArchiveDigest) {
		return empty, webAPIError(400, "invalid_dav_job", "invalid DAV content identity")
	}
	if record.Desired.AuthorizationID != "" && !webAPISourceOpaqueKey(record.Desired.AuthorizationID) || record.Desired.TargetOperationID != "" && !webAPIOperationID(record.Desired.TargetOperationID) {
		return empty, webAPIError(400, "invalid_dav_job", "invalid DAV authorization or target identity")
	}
	if record.Desired.Format != "" && !webAPIContains([]string{"flclash-v1", "native-v1"}, record.Desired.Format) || record.Desired.Strategy != "" && !webAPIContains([]string{"override", "merge"}, record.Desired.Strategy) {
		return empty, webAPIError(400, "invalid_dav_job", "invalid DAV format or restore strategy")
	}
	if len(record.Job.Stage) > 128 || strings.ContainsAny(record.Job.Stage, "\x00\r\n") {
		return empty, webAPIError(400, "invalid_dav_job", "invalid DAV stage")
	}
	current, err := webAPIInteger(string(tx.authority.revision), false)
	if err != nil || current < 0 || current == math.MaxInt64 {
		return empty, webAPIError(409, "revision_exhausted", "configuration revision space is exhausted")
	}
	desired := WebAPIRevision(strconv.FormatInt(current+1, 10))
	if record.Job.DesiredRevision != "" && record.Job.DesiredRevision != desired {
		return empty, webAPIError(409, "revision_conflict", "DAV desired revision does not match its configuration commit")
	}
	record.Job.DesiredRevision = desired
	jobs, err := tx.DAVJobs()
	if err != nil {
		return empty, err
	}
	covered, err := webAPIDAVAdmissionResolution(record, jobs)
	if err != nil {
		return empty, err
	}
	active := 0
	for _, saved := range jobs {
		if saved.Job.OperationID == record.Job.OperationID {
			return empty, webAPIError(409, "duplicate_dav_job", "DAV intent identity is already recorded")
		}
		if record.Job.Action == WebAPIDAVBackupAction && saved.Job.Action == WebAPIDAVBackupAction && saved.Desired.TupleDigest == record.Desired.TupleDigest && (webAPIDAVActive(saved.Job.Phase) || saved.Job.Phase == WebAPIDAVUnknown && !covered[saved.Job.OperationID]) {
			problem := webAPIError(409, "dav_unresolved_upload", "an earlier upload to this DAV target has not been resolved")
			problem.Details = map[string]any{"operationId": saved.Job.OperationID}
			return empty, problem
		}
		if webAPIDAVActive(saved.Job.Phase) {
			active++
		}
	}
	if len(jobs) >= WebAPIDAVMaxJobs {
		return empty, webAPIError(429, "dav_job_capacity", "DAV intent ledger is full")
	}
	if !restored && active >= WebAPIDAVMaxConcurrent {
		return empty, webAPIError(429, "dav_busy", "too many DAV jobs are in flight")
	}

	if record.Job.UpdatedAt.IsZero() {
		record.Job.UpdatedAt = time.Now().UTC()
	}
	if err := webAPIPut(tx.tx, "dav-jobs", record.Job.OperationID, record); err != nil {
		return empty, err
	}
	tx.authority.davRecorded = true
	return record.Job, nil
}

func (s *WebAPIStore) TransitionDAVJob(ctx context.Context, id string, expectedPhase WebAPIDAVPhase, next WebAPIDAVJob) (WebAPIDAVJob, error) {
	var result WebAPIDAVJob
	if !webAPIOperationID(id) {
		return result, webAPIError(400, "invalid_operation_id", "invalid DAV job identity")
	}
	session := WebAPIRequestSessionID(ctx)
	internal := WebAPIInternalAuthorized(ctx)
	if session == "" && !internal {
		return result, webAPIError(401, "unauthorized", "DAV job transition requires an authenticated authority")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	err := s.write(ctx, func(tx *bolt.Tx) error {
		var record WebAPIDAVRecord
		if err := webAPIGet(tx, "dav-jobs", id, &record); err != nil {
			return err
		}
		if !internal && record.SessionID != session {
			return webAPIError(403, "dav_session_mismatch", "DAV job belongs to another session")
		}
		if internal && record.Job.Phase != WebAPIDAVApplying {
			beforeEffect := record.Job.Phase == WebAPIDAVPending && next.Result == nil && next.Error != nil && next.Stage == "before-effect"
			abandoned := beforeEffect && next.Phase == WebAPIDAVFailed && next.Error.Code == "dav_not_started"
			uncertain := beforeEffect && next.Phase == WebAPIDAVUnknown && webAPIContains([]string{"dav_result_unknown", "durability_unknown"}, next.Error.Code)
			if !abandoned && !uncertain {
				return webAPIError(403, "dav_status_only", "internal DAV authority cannot start or restart effects")
			}
		}
		if record.Job.Phase != expectedPhase {
			return webAPIError(409, "dav_phase_conflict", "DAV job phase changed")
		}
		if next.OperationID != id || next.DesiredRevision != record.Job.DesiredRevision || next.Action != record.Job.Action {
			return webAPIError(409, "immutable_dav_intent", "DAV transition cannot change its intent identity")
		}
		allowed := expectedPhase == WebAPIDAVPending && (next.Phase == WebAPIDAVApplying || next.Phase == WebAPIDAVFailed || next.Phase == WebAPIDAVUnknown) || expectedPhase == WebAPIDAVApplying && (next.Phase == WebAPIDAVApplying || webAPIDAVTerminal(next.Phase))
		if !allowed {
			return webAPIError(409, "invalid_dav_transition", "DAV job cannot move to the requested phase")
		}
		if next.Phase == WebAPIDAVPrepared && next.Action != WebAPIDAVPrepareRestoreAction {
			return webAPIError(409, "invalid_dav_transition", "only a restore preparation may finish prepared")
		}
		if next.UpdatedAt.IsZero() {
			next.UpdatedAt = time.Now().UTC()
		}
		if next.UpdatedAt.Before(record.Job.UpdatedAt) || len(next.Stage) > 128 || strings.ContainsAny(next.Stage, "\x00\r\n") {
			return webAPIError(400, "invalid_dav_job", "invalid DAV status timestamp or stage")
		}
		if next.Phase == WebAPIDAVApplying {
			if next.Error != nil {
				return webAPIError(400, "invalid_dav_job", "an applying DAV job cannot carry a terminal error")
			}
			before, after := webAPIDAVStageRank(record.Job.Action, record.Job.Stage), webAPIDAVStageRank(next.Action, next.Stage)
			if after < 0 || before < 0 || after < before {
				return webAPIError(409, "invalid_dav_transition", "DAV stage cannot move backward")
			}
		}
		if next.Result != nil && (next.Result.ArchiveSize < 0 || next.Result.ArchiveSize > WebAPIDAVMaxArchiveBytes || len(next.Result.ETag) > 4096) {
			return webAPIError(400, "invalid_dav_job", "invalid DAV result")
		}
		if next.Action == WebAPIDAVReconcileAction && next.Phase == WebAPIDAVSucceeded && next.Result != nil && next.Result.RemoteMatches {
			records, err := webAPIList[WebAPIDAVRecord](tx, "dav-jobs")
			if err != nil {
				return err
			}
			jobs := map[string]WebAPIDAVRecord{}
			for _, saved := range records {
				jobs[saved.Job.OperationID] = saved
			}
			proof := record
			proof.Job = next
			if _, err = webAPIDAVPositiveResolution(proof, jobs); err != nil {
				return err
			}
		}
		record.Job = next
		if err := webAPIPut(tx, "dav-jobs", id, record); err != nil {
			return err
		}
		result = next
		return nil
	})
	if err != nil {
		return WebAPIDAVJob{}, err
	}
	return result, nil
}

func (s *WebAPIStore) RecoverDAVJobs(ctx context.Context) error {
	if !WebAPIInternalAuthorized(ctx) {
		return webAPIError(401, "unauthorized", "DAV recovery requires private startup authority")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.write(ctx, func(tx *bolt.Tx) error {
		records, err := webAPIList[WebAPIDAVRecord](tx, "dav-jobs")
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		for _, record := range records {
			if !webAPIDAVActive(record.Job.Phase) {
				continue
			}
			record.Job.Phase = WebAPIDAVUnknown
			record.Job.Stage = "recovered-unknown"
			record.Job.Result = nil
			record.Job.Error = webAPIError(409, "dav_result_unknown", "DAV completion was not durably observed; reconcile explicitly")
			if !now.Before(record.Job.UpdatedAt) {
				record.Job.UpdatedAt = now
			}
			if err := webAPIPut(tx, "dav-jobs", record.Job.OperationID, record); err != nil {
				return err
			}
		}
		return nil
	})
}

func webAPIDAVStageRank(action WebAPIDAVAction, stage string) int {
	if stage == "accepted" {
		return 0
	}
	if stage == "starting" {
		return 1
	}
	stages := map[WebAPIDAVAction][]string{
		WebAPIDAVProbeAction:          {"probing"},
		WebAPIDAVBackupAction:         {"creating-collection", "checking-remote", "put-ready", "put-sending"},
		WebAPIDAVPrepareRestoreAction: {"downloading", "validating-archive"},
		WebAPIDAVReconcileAction:      {"downloading"},
		WebAPIDAVRestoreAction:        {"validating-archive", "restoring"},
	}
	for i, name := range stages[action] {
		if stage == name {
			return i + 2
		}
	}
	return -1
}
