//go:build webpanel && !android

package core

func webBackupNativeSettings(candidate webBackupCandidate, prepared bool) (WebAPISettingsSnapshot, []WebAPIBackupChange, error) {
	snapshot, plan, err := NativeWebAPISettingsSnapshotForSchema(candidate.NativeSchema, candidate.NativeConfig)
	if err != nil {
		return snapshot, nil, err
	}
	if plan.RequiresProjection {
		return snapshot, nil, webAPIError(400, "settings_conversion_required", "legacy settings require a separately supported typed projection")
	}
	if plan.RequiresConfirmation && !prepared {
		return snapshot, nil, &WebAPIError{Status: 400, Code: "settings_conversion_required", Message: "legacy presentation changes require prepared archive confirmation", Details: map[string]any{"requiresPreparation": true}}
	}
	changes := make([]WebAPIBackupChange, 0, len(plan.Changes))
	for _, change := range plan.Changes {
		copy := change
		changes = append(changes, WebAPIBackupChange{Kind: "settings-conversion", Name: change.SourceField, Count: 1, Action: change.Action, SettingsChange: &copy})
	}
	return snapshot, changes, nil
}
