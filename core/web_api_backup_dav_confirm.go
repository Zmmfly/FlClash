//go:build webpanel && !android

package core

import "context"

func (s *WebAPIBackupService) ConfirmDAV(ctx context.Context, request WebAPIDAVRestore) (WebAPIReceipt, error) {
	required := "restore-dav-backup"
	if request.RestoreDAVCredentials {
		required = "restore-dav-backup-with-credentials"
	}
	if request.Intent != required {
		return WebAPIReceipt{}, webAPIError(400, "restore_intent_required", "DAV restore sensitivity requires matching explicit intent")
	}
	if WebAPIRequestSessionID(ctx) == "" {
		return WebAPIReceipt{}, webAPIError(401, "unauthorized", "DAV restore requires an authenticated session")
	}
	confirm := WebAPIBackupConfirm{WebAPIMutation: request.WebAPIMutation, Intent: "confirm-backup-conversion", Token: request.Token, ArchiveDigest: request.ArchiveDigest, PlanDigest: request.PlanDigest, RestoreDAVCredentials: request.RestoreDAVCredentials}
	payload := struct {
		SessionID string           `json:"sessionId"`
		Request   WebAPIDAVRestore `json:"request"`
	}{WebAPIRequestSessionID(ctx), request}
	var source WebAPIDAVRecord
	before := func(tx *WebAPIWriteTx, stage webBackupStage) error {
		var err error
		source, err = tx.DAVJob(request.PreparationOperationID)
		if err != nil {
			return err
		}
		if source.SessionID != WebAPIRequestSessionID(ctx) {
			return webAPIError(403, "dav_session_mismatch", "DAV preparation belongs to another session")
		}
		preview := source.Job.Result
		if source.Job.Action != WebAPIDAVPrepareRestoreAction || source.Job.Phase != WebAPIDAVPrepared || preview == nil || preview.Preparation == nil || preview.Preparation.Token != request.Token || preview.Preparation.ArchiveDigest != request.ArchiveDigest || preview.Preparation.PlanDigest != request.PlanDigest || source.Desired.RestoreDAVCredentials != request.RestoreDAVCredentials || source.Desired.Strategy != stage.request.Strategy || source.Desired.OnlyProfiles != stage.request.OnlyProfiles {
			return webAPIError(409, "dav_preparation_mismatch", "DAV preparation no longer matches the confirmed restore")
		}
		settings, err := tx.Settings()
		if err != nil {
			return err
		}
		if settings.DAV == nil || webDAVTupleDigest(*settings.DAV) != source.Desired.TupleDigest {
			return webAPIError(409, "dav_target_changed", "DAV configuration changed after preparation")
		}
		return nil
	}
	complete := func(tx *WebAPIWriteTx, stage webBackupStage, result WebAPIBackupResult) (any, error) {
		record := WebAPIDAVRecord{SessionID: WebAPIRequestSessionID(ctx), Job: WebAPIDAVJob{OperationID: request.OperationID, Action: WebAPIDAVRestoreAction, Phase: WebAPIDAVSucceeded, Stage: "restored", UpdatedAt: s.now().UTC(), Result: &WebAPIDAVResult{Restored: &result, ArchiveDigest: request.ArchiveDigest, ArchiveSize: len(stage.request.ArchiveBase64)}}, Desired: WebAPIDAVDesired{TupleDigest: source.Desired.TupleDigest, ArchiveDigest: request.ArchiveDigest, ArchiveSize: len(stage.request.ArchiveBase64), Strategy: stage.request.Strategy, OnlyProfiles: stage.request.OnlyProfiles, RestoreDAVCredentials: request.RestoreDAVCredentials, TargetOperationID: request.PreparationOperationID}}
		return tx.RecordDAVJob(record)
	}
	return s.confirmPrepared(ctx, confirm, "dav:restore", payload, before, complete)
}
