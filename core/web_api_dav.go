//go:build webpanel && !android

package core

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"time"
)

type WebAPIDAVService struct {
	store   *WebAPIStore
	backup  *WebAPIBackupService
	options WebAPIDAVOptions
	now     func() time.Time
	mu      sync.Mutex
	closed  bool
	active  map[string]context.CancelFunc
	slots   chan struct{}
	stop    chan struct{}
	idle    chan struct{}
}

type webDAVSnapshot struct {
	dav     WebAPISettingsDAV
	grant   *WebAPISourceGrant
	archive []byte
}

var webDAVPreflight = errors.New("DAV receipt preflight")

func NewWebAPIDAVService(store *WebAPIStore, backup *WebAPIBackupService, options WebAPIDAVOptions) (*WebAPIDAVService, error) {
	if store == nil || backup == nil {
		return nil, webAPIError(500, "dav_service_unavailable", "DAV service dependencies are unavailable")
	}
	if options.StatusContext == nil || !WebAPIInternalAuthorized(options.StatusContext) {
		return nil, webAPIError(500, "dav_service_unavailable", "DAV status authority is unavailable")
	}
	if err := store.RecoverDAVJobs(options.StatusContext); err != nil {
		return nil, err
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	idle := make(chan struct{})
	close(idle)
	return &WebAPIDAVService{store: store, backup: backup, options: options, now: now, active: map[string]context.CancelFunc{}, slots: make(chan struct{}, WebAPIDAVMaxConcurrent), stop: make(chan struct{}), idle: idle}, nil
}

func (s *WebAPIDAVService) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.stop)
		for _, cancel := range s.active {
			cancel()
		}
	}
	idle := s.idle
	s.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-time.After(2 * time.Second):
		return webAPIError(503, "dav_close_timeout", "DAV shutdown did not finish within its deadline")
	}
}

func (s *WebAPIDAVService) begin(ctx context.Context, id string) (context.Context, func(), error) {
	select {
	case s.slots <- struct{}{}:
	case <-s.stop:
		return nil, nil, webAPIError(503, "dav_closed", "DAV service is closed")
	default:
		return nil, nil, webAPIError(429, "dav_busy", "DAV transfer capacity is full")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.slots
		return nil, nil, webAPIError(503, "dav_closed", "DAV service is closed")
	}
	if _, exists := s.active[id]; exists {
		s.mu.Unlock()
		<-s.slots
		return nil, nil, webAPIError(409, "dav_operation_active", "DAV operation is already active")
	}
	if len(s.active) == 0 {
		s.idle = make(chan struct{})
	}
	work, cancel := context.WithTimeout(ctx, 90*time.Second)
	s.active[id] = cancel
	s.mu.Unlock()
	return work, func() {
		cancel()
		s.mu.Lock()
		delete(s.active, id)
		if len(s.active) == 0 {
			close(s.idle)
		}
		s.mu.Unlock()
		<-s.slots
	}, nil
}

func webDAVRequest(ctx context.Context, request any) any {
	return struct {
		SessionID string `json:"sessionId"`
		Request   any    `json:"request"`
	}{WebAPIRequestSessionID(ctx), request}
}

func (s *WebAPIDAVService) preflight(ctx context.Context, mutation WebAPIMutation, command string, request any) (WebAPIReceipt, bool, error) {
	if WebAPIRequestSessionID(ctx) == "" {
		return WebAPIReceipt{}, false, webAPIError(401, "unauthorized", "DAV requires an authenticated session")
	}
	receipt, err := s.store.Transaction(ctx, mutation, command, webDAVRequest(ctx, request), func(*WebAPIWriteTx) (any, error) { return nil, webDAVPreflight })
	if errors.Is(err, webDAVPreflight) {
		return WebAPIReceipt{}, false, nil
	}
	return receipt, err == nil, err
}

func (s *WebAPIDAVService) snapshot(ctx context.Context, revision WebAPIRevision, desired WebAPIDAVDesired, action WebAPIDAVAction) (webDAVSnapshot, error) {
	var snapshot webDAVSnapshot
	observed, err := s.store.View(ctx, func(tx *WebAPIReadTx) error {
		settings, err := tx.Settings()
		if err != nil {
			return err
		}
		if settings.DAV == nil {
			return webAPIError(409, "dav_not_configured", "DAV is not configured")
		}
		snapshot.dav = *settings.DAV
		if _, _, err = webDAVBase(snapshot.dav); err != nil {
			return err
		}
		if desired.AuthorizationID != "" {
			grant, err := tx.SourceGrant(desired.AuthorizationID)
			if err != nil {
				return err
			}
			if grant.SessionID != WebAPIRequestSessionID(ctx) {
				return webAPIError(403, "source_authorization_mismatch", "DAV authorization belongs to another session")
			}
			snapshot.grant = &grant
		}
		if action == WebAPIDAVBackupAction {
			snapshot.archive, err = s.backup.ExportFromSnapshot(ctx, tx, desired.Format, desired.IncludeDAVCredentials)
			return err
		}
		return nil
	})
	if err != nil {
		return webDAVSnapshot{}, err
	}
	if observed != revision {
		return webDAVSnapshot{}, webAPIError(409, "revision_conflict", "DAV snapshot changed; submit a new operation")
	}
	return snapshot, nil
}

func (s *WebAPIDAVService) recheck(ctx context.Context, digest string, grant *WebAPISourceGrant) error {
	_, err := s.store.View(ctx, func(tx *WebAPIReadTx) error {
		release, err := WebAPIAcquireCommitAuthorization(ctx)
		if err != nil {
			return err
		}
		defer release()
		settings, err := tx.Settings()
		if err != nil {
			return err
		}
		if settings.DAV == nil || webDAVTupleDigest(*settings.DAV) != digest {
			return webAPIError(409, "dav_target_changed", "committed DAV configuration changed; the queued transfer was not continued")
		}
		if grant != nil {
			current, err := tx.SourceGrant(grant.ID)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(current, *grant) {
				return webAPIError(409, "source_authorization_invalid", "DAV authorization changed before the request")
			}
		}
		return nil
	})
	return err
}

func (s *WebAPIDAVService) execute(ctx context.Context, mutation WebAPIMutation, action WebAPIDAVAction, request any, desired WebAPIDAVDesired) (WebAPIReceipt, error) {
	command := "dav:" + string(action)
	if receipt, done, err := s.preflight(ctx, mutation, command, request); done || err != nil {
		return receipt, err
	}
	work, release, err := s.begin(ctx, mutation.OperationID)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	defer release()
	snapshot, err := s.snapshot(work, mutation.ExpectedRevision, desired, action)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	digest := webDAVTupleDigest(snapshot.dav)
	if desired.TupleDigest != "" && desired.TupleDigest != digest {
		return WebAPIReceipt{}, webAPIError(409, "dav_target_changed", "DAV reconciliation no longer matches the committed target")
	}
	desired.TupleDigest = digest
	if action == WebAPIDAVBackupAction {
		desired.ArchiveDigest = webContentDigest(snapshot.archive)
		desired.ArchiveSize = len(snapshot.archive)
	}
	var job WebAPIDAVJob
	admit := func() (WebAPIReceipt, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed {
			return WebAPIReceipt{}, webAPIError(503, "dav_closed", "DAV service is closed")
		}
		if action == WebAPIDAVReconcileAction {
			if _, active := s.active[desired.TargetOperationID]; active {
				return WebAPIReceipt{}, webAPIError(409, "dav_operation_active", "the original DAV effect has not locally converged")
			}
		}
		return s.store.Transaction(work, mutation, command, webDAVRequest(ctx, request), func(tx *WebAPIWriteTx) (any, error) {
			settings, err := tx.Settings()
			if err != nil {
				return nil, err
			}
			if settings.DAV == nil || webDAVTupleDigest(*settings.DAV) != digest {
				return nil, webAPIError(409, "dav_target_changed", "DAV target changed before admission")
			}
			job, err = tx.RecordDAVJob(WebAPIDAVRecord{Job: WebAPIDAVJob{OperationID: mutation.OperationID, Action: action, Phase: WebAPIDAVPending, Stage: "accepted", UpdatedAt: s.now().UTC()}, SessionID: WebAPIRequestSessionID(ctx), Desired: desired})
			return job, err
		})
	}
	receipt, err := admit()
	if err != nil || receipt.Replayed {
		return receipt, err
	}
	s.run(work, job, desired, snapshot)
	return receipt, nil
}

func (s *WebAPIDAVService) run(ctx context.Context, job WebAPIDAVJob, desired WebAPIDAVDesired, snapshot webDAVSnapshot) {
	transition := func(phase WebAPIDAVPhase, stage string, result *WebAPIDAVResult, problem *WebAPIError) error {
		next := job
		next.Phase = phase
		next.Stage = stage
		next.UpdatedAt = s.now().UTC()
		next.Error = problem
		if result != nil {
			copy := *result
			next.Result = &copy
		}
		authority := s.options.StatusContext
		if job.Phase == WebAPIDAVPending && phase == WebAPIDAVApplying {
			authority = ctx
		}
		persist, cancel := context.WithTimeout(authority, 2*time.Second)
		defer cancel()
		value, err := s.store.TransitionDAVJob(persist, job.OperationID, job.Phase, next)
		if err == nil {
			job = value
		}
		return err
	}
	if err := transition(WebAPIDAVApplying, "starting", nil, nil); err != nil {
		var problem *WebAPIError
		refused := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &problem) && problem.Code == "unauthorized"
		if refused {
			_ = transition(WebAPIDAVFailed, "before-effect", nil, webAPIError(409, "dav_not_started", "DAV effect was not started because its admission authority ended"))
		}
		return
	}
	before := func(stage string, result *WebAPIDAVResult) error {
		if err := s.recheck(ctx, desired.TupleDigest, snapshot.grant); err != nil {
			return err
		}
		return transition(WebAPIDAVApplying, stage, result, nil)
	}
	transport, err := newWebDAVTransport(snapshot.dav, job.Action, desired.AuthorizationID, snapshot.grant, s.options, before)
	var result WebAPIDAVResult
	phase := WebAPIDAVSucceeded
	if err == nil {
		switch job.Action {
		case WebAPIDAVProbeAction:
			result, err = transport.probe(ctx)
		case WebAPIDAVBackupAction:
			result, err = transport.backup(ctx, snapshot.archive)
		case WebAPIDAVPrepareRestoreAction:
			var archive []byte
			archive, result, err = transport.download(ctx)
			if err == nil {
				err = before("validating-archive", &result)
			}
			if err == nil {
				var preparation WebAPIBackupPreparation
				_, preparation, err = s.backup.Prepare(ctx, WebAPIBackupPrepare{ExpectedRevision: job.DesiredRevision, ArchiveBase64: archive, Strategy: desired.Strategy, OnlyProfiles: desired.OnlyProfiles, RestoreDAVCredentials: desired.RestoreDAVCredentials})
				if err == nil {
					result.Preparation = &preparation
					phase = WebAPIDAVPrepared
				}
			}
		case WebAPIDAVReconcileAction:
			var archive []byte
			archive, result, err = transport.download(ctx)
			if err == nil {
				result.ReconciliationOf = desired.TargetOperationID
				result.RemoteMatches = len(archive) == desired.ArchiveSize && webContentDigest(archive) == desired.ArchiveDigest
			}
		default:
			err = webAPIError(400, "dav_action_invalid", "unsupported DAV action")
		}
	}
	var problem *WebAPIError
	if err != nil {
		phase = WebAPIDAVFailed
		if !errors.As(err, &problem) {
			problem = webAPIError(502, "dav_operation_failed", "DAV operation could not be completed")
		}
		if problem.Code == "dav_effect_unknown" {
			phase = WebAPIDAVUnknown
		}
	}
	_ = transition(phase, string(phase), &result, problem)
}

func (s *WebAPIDAVService) Probe(ctx context.Context, request WebAPIDAVProbe) (WebAPIReceipt, error) {
	if request.Intent != "probe-dav" {
		return WebAPIReceipt{}, webAPIError(400, "dav_intent_required", "explicit DAV probe intent is required")
	}
	return s.execute(ctx, request.WebAPIMutation, WebAPIDAVProbeAction, request, WebAPIDAVDesired{AuthorizationID: request.AuthorizationID})
}

func (s *WebAPIDAVService) Backup(ctx context.Context, request WebAPIDAVBackup) (WebAPIReceipt, error) {
	intent := "backup-to-dav"
	if request.IncludeDAVCredentials {
		intent = "backup-to-dav-with-credentials"
	}
	if request.Intent != intent {
		return WebAPIReceipt{}, webAPIError(400, "dav_intent_required", "explicit DAV backup sensitivity intent is required")
	}
	if request.Format != "flclash-v1" && request.Format != "native-v1" {
		return WebAPIReceipt{}, webAPIError(400, "backup_unsupported_version", "unsupported DAV backup format")
	}
	return s.execute(ctx, request.WebAPIMutation, WebAPIDAVBackupAction, request, WebAPIDAVDesired{AuthorizationID: request.AuthorizationID, Format: request.Format, IncludeDAVCredentials: request.IncludeDAVCredentials, ResolutionOperationID: request.ResolutionOperationID})
}

func (s *WebAPIDAVService) PrepareRestore(ctx context.Context, request WebAPIDAVPrepareRestore) (WebAPIReceipt, error) {
	intent := "prepare-dav-restore"
	if request.RestoreDAVCredentials {
		intent = "prepare-dav-restore-with-credentials"
	}
	if request.Intent != intent {
		return WebAPIReceipt{}, webAPIError(400, "dav_intent_required", "explicit DAV restore preparation intent is required")
	}
	if request.Strategy != "merge" && request.Strategy != "override" || request.OnlyProfiles && request.RestoreDAVCredentials {
		return WebAPIReceipt{}, webAPIError(400, "invalid_restore_options", "inconsistent DAV restore options")
	}
	return s.execute(ctx, request.WebAPIMutation, WebAPIDAVPrepareRestoreAction, request, WebAPIDAVDesired{AuthorizationID: request.AuthorizationID, Strategy: request.Strategy, OnlyProfiles: request.OnlyProfiles, RestoreDAVCredentials: request.RestoreDAVCredentials})
}

func webDAVSummaryError(problem *WebAPIError) string {
	if problem == nil {
		return ""
	}
	switch problem.Code {
	case "dav_not_started", "dav_effect_unknown", "dav_result_unknown", "dav_remote_conflict", "dav_auth_failed", "dav_request_failed", "dav_target_changed", "dav_status_failed", "dav_partial_archive", "dav_archive_too_large", "dav_redirect_limit", "dav_redirect_invalid", "dav_redirect_forbidden", "dav_downgrade_forbidden", "dav_etag_invalid", "dav_weak_etag", "dav_operation_failed", "source_authorization_required", "source_authorization_invalid", "source_authorization_mismatch", "source_target_changed", "source_address_forbidden", "source_redirect_authorization_required", "source_dns_failed", "source_address_limit", "unauthorized", "revision_conflict":
		return problem.Code
	}
	return "dav_validation_failed"
}

func webDAVSummaryStage(job WebAPIDAVJob) string {
	if webAPIDAVStageRank(job.Action, job.Stage) >= 0 {
		return job.Stage
	}
	switch job.Stage {
	case "prepared", "succeeded", "failed", "unknown", "recovered-unknown", "before-effect", "restored":
		return job.Stage
	}
	return "unknown"
}

func (s *WebAPIDAVService) Jobs(ctx context.Context) (WebAPIRevision, []WebAPIDAVJobSummary, error) {
	jobs := []WebAPIDAVJobSummary{}
	if WebAPIRequestSessionID(ctx) == "" {
		return "", nil, webAPIError(401, "unauthorized", "DAV summaries require an authenticated session")
	}
	revision, err := s.store.View(ctx, func(tx *WebAPIReadTx) error {
		release, err := WebAPIAcquireCommitAuthorization(ctx)
		if err != nil {
			return err
		}
		defer release()
		records, err := tx.DAVJobs()
		if err != nil {
			return err
		}
		if len(records) > WebAPIDAVMaxJobs {
			return webAPIError(503, "dav_job_capacity", "DAV ledger exceeds its bounded capacity")
		}
		settings, err := tx.Settings()
		if err != nil {
			return err
		}
		current := ""
		if settings.DAV != nil {
			current = webDAVTupleDigest(*settings.DAV)
		}
		for _, record := range records {
			needs := record.SessionID != WebAPIRequestSessionID(ctx)
			if result := record.Job.Result; result != nil && result.Preparation != nil {
				expiry, err := time.Parse(time.RFC3339Nano, result.Preparation.ExpiresAt)
				needs = needs || err != nil || !s.now().Before(expiry)
			}
			jobs = append(jobs, WebAPIDAVJobSummary{OperationID: record.Job.OperationID, Action: record.Job.Action, Phase: record.Job.Phase, Stage: webDAVSummaryStage(record.Job), ErrorCode: webDAVSummaryError(record.Job.Error), UpdatedAt: record.Job.UpdatedAt, CurrentTupleMatches: current != "" && current == record.Desired.TupleDigest, NeedsPrepare: needs})
		}
		return nil
	})
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].UpdatedAt.Equal(jobs[j].UpdatedAt) {
			return jobs[i].OperationID < jobs[j].OperationID
		}
		return jobs[i].UpdatedAt.Before(jobs[j].UpdatedAt)
	})
	return revision, jobs, err
}

func (s *WebAPIDAVService) Job(ctx context.Context, id string) (WebAPIRevision, WebAPIDAVJob, error) {
	var job WebAPIDAVJob
	if WebAPIRequestSessionID(ctx) == "" {
		return "", job, webAPIError(401, "unauthorized", "DAV job requires an authenticated session")
	}
	revision, err := s.store.View(ctx, func(tx *WebAPIReadTx) error {
		release, err := WebAPIAcquireCommitAuthorization(ctx)
		if err != nil {
			return err
		}
		defer release()
		record, err := tx.DAVJob(id)
		if err != nil {
			return err
		}
		if record.SessionID != WebAPIRequestSessionID(ctx) {
			return webAPIError(403, "dav_session_mismatch", "DAV job belongs to another session")
		}
		job = record.Job
		return nil
	})
	return revision, job, err
}

func (s *WebAPIDAVService) Reconcile(ctx context.Context, id string, request WebAPIDAVReconcile) (WebAPIReceipt, error) {
	if request.Intent != "reconcile-dav-backup" {
		return WebAPIReceipt{}, webAPIError(400, "dav_intent_required", "explicit DAV reconciliation intent is required")
	}
	typed := struct {
		TargetOperationID string `json:"targetOperationId"`
		WebAPIDAVReconcile
	}{id, request}
	if receipt, done, err := s.preflight(ctx, request.WebAPIMutation, "dav:reconcile", typed); done || err != nil {
		return receipt, err
	}
	var record WebAPIDAVRecord
	_, err := s.store.View(ctx, func(tx *WebAPIReadTx) error { var err error; record, err = tx.DAVJob(id); return err })
	if err != nil {
		return WebAPIReceipt{}, err
	}
	if record.Job.Action != WebAPIDAVBackupAction || record.Job.Phase != WebAPIDAVUnknown {
		return WebAPIReceipt{}, webAPIError(409, "dav_reconcile_invalid", "only an uncertain backup can be reconciled")
	}
	return s.execute(ctx, request.WebAPIMutation, WebAPIDAVReconcileAction, typed, WebAPIDAVDesired{TupleDigest: record.Desired.TupleDigest, AuthorizationID: request.AuthorizationID, ArchiveDigest: record.Desired.ArchiveDigest, ArchiveSize: record.Desired.ArchiveSize, TargetOperationID: id, TargetDesiredRevision: record.Job.DesiredRevision})
}
