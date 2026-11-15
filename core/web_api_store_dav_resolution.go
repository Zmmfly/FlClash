//go:build webpanel && !android

package core

func webAPIDAVResolutionError() *WebAPIError {
	return webAPIError(409, "invalid_dav_resolution", "DAV resolution does not prove the exact unresolved upload")
}

func webAPIDAVReconciliationTarget(record WebAPIDAVRecord, jobs map[string]WebAPIDAVRecord) (WebAPIDAVRecord, error) {
	target, exists := jobs[record.Desired.TargetOperationID]
	if !exists || record.Job.Action != WebAPIDAVReconcileAction || target.Job.Action != WebAPIDAVBackupAction || target.Job.Phase != WebAPIDAVUnknown || record.Desired.TargetDesiredRevision != target.Job.DesiredRevision || record.Desired.TupleDigest != target.Desired.TupleDigest || record.Desired.ArchiveDigest == "" || record.Desired.ArchiveDigest != target.Desired.ArchiveDigest || record.Desired.ArchiveSize != target.Desired.ArchiveSize {
		return WebAPIDAVRecord{}, webAPIDAVResolutionError()
	}
	targetRevision, err := webAPIInteger(string(target.Job.DesiredRevision), true)
	if err != nil {
		return WebAPIDAVRecord{}, webAPIDAVResolutionError()
	}
	proofRevision, err := webAPIInteger(string(record.Job.DesiredRevision), true)
	if err != nil || proofRevision <= targetRevision {
		return WebAPIDAVRecord{}, webAPIDAVResolutionError()
	}
	return target, nil
}

func webAPIDAVPositiveResolution(record WebAPIDAVRecord, jobs map[string]WebAPIDAVRecord) (WebAPIDAVRecord, error) {
	target, err := webAPIDAVReconciliationTarget(record, jobs)
	if err != nil {
		return target, err
	}
	result := record.Job.Result
	if record.Job.Phase != WebAPIDAVSucceeded || record.Job.Error != nil || result == nil || !result.RemoteMatches || result.ReconciliationOf != target.Job.OperationID || result.ArchiveDigest != target.Desired.ArchiveDigest || result.ArchiveSize != target.Desired.ArchiveSize {
		return WebAPIDAVRecord{}, webAPIDAVResolutionError()
	}
	return target, nil
}

func webAPIDAVBackupResolution(record WebAPIDAVRecord, jobs map[string]WebAPIDAVRecord) (string, error) {
	proof, exists := jobs[record.Desired.ResolutionOperationID]
	if !exists || proof.SessionID != record.SessionID || record.Job.Action != WebAPIDAVBackupAction {
		return "", webAPIDAVResolutionError()
	}
	target, err := webAPIDAVPositiveResolution(proof, jobs)
	if err != nil {
		return "", err
	}
	if target.Desired.TupleDigest != record.Desired.TupleDigest {
		return "", webAPIDAVResolutionError()
	}
	targetRevision, err := webAPIInteger(string(target.Job.DesiredRevision), true)
	if err != nil {
		return "", webAPIDAVResolutionError()
	}
	proofRevision, err := webAPIInteger(string(proof.Job.DesiredRevision), true)
	if err != nil || proofRevision <= targetRevision {
		return "", webAPIDAVResolutionError()
	}
	nextRevision, err := webAPIInteger(string(record.Job.DesiredRevision), true)
	if err != nil || nextRevision <= proofRevision {
		return "", webAPIDAVResolutionError()
	}
	return target.Job.OperationID, nil
}

func webAPIDAVAdmissionResolution(record WebAPIDAVRecord, records []WebAPIDAVRecord) (map[string]bool, error) {
	jobs := map[string]WebAPIDAVRecord{}
	for _, saved := range records {
		jobs[saved.Job.OperationID] = saved
	}
	covered := map[string]bool{}
	for _, saved := range records {
		if saved.Job.Action != WebAPIDAVBackupAction || saved.Desired.ResolutionOperationID == "" {
			continue
		}
		if target, err := webAPIDAVBackupResolution(saved, jobs); err == nil {
			covered[target] = true
		}
	}
	if record.Job.Action == WebAPIDAVReconcileAction {
		if record.Desired.ResolutionOperationID != "" {
			return nil, webAPIDAVResolutionError()
		}
		if _, err := webAPIDAVReconciliationTarget(record, jobs); err != nil {
			return nil, err
		}
	} else if record.Desired.TargetDesiredRevision != "" {
		return nil, webAPIDAVResolutionError()
	}
	if record.Desired.ResolutionOperationID != "" {
		if !webAPIOperationID(record.Desired.ResolutionOperationID) {
			return nil, webAPIDAVResolutionError()
		}
		for _, saved := range records {
			if saved.Job.Action == WebAPIDAVBackupAction && saved.Desired.ResolutionOperationID == record.Desired.ResolutionOperationID {
				return nil, webAPIError(409, "dav_resolution_consumed", "DAV resolution was already consumed by a different upload")
			}
		}
		target, err := webAPIDAVBackupResolution(record, jobs)
		if err != nil {
			return nil, err
		}
		covered[target] = true
	}
	return covered, nil
}
