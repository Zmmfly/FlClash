//go:build webpanel && !android

package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"
)

type WebAPIBackupPrepare struct {
	ExpectedRevision      WebAPIRevision `json:"expectedRevision"`
	ArchiveBase64         []byte         `json:"archiveBase64"`
	Strategy              string         `json:"strategy"`
	OnlyProfiles          bool           `json:"onlyProfiles"`
	RestoreDAVCredentials bool           `json:"restoreDavCredentials"`
}

type WebAPIBackupPreparation struct {
	Token                string               `json:"token"`
	ExpiresAt            string               `json:"expiresAt"`
	ArchiveDigest        string               `json:"archiveDigest"`
	PlanDigest           string               `json:"planDigest"`
	Changes              []WebAPIBackupChange `json:"changes"`
	RequiresConfirmation bool                 `json:"requiresConfirmation"`
}

type WebAPIBackupConfirm struct {
	WebAPIMutation
	Intent                string `json:"intent"`
	Token                 string `json:"token"`
	ArchiveDigest         string `json:"archiveDigest"`
	PlanDigest            string `json:"planDigest"`
	RestoreDAVCredentials bool   `json:"restoreDavCredentials"`
}

type webBackupStage struct {
	request     WebAPIBackupPrepare
	preparation WebAPIBackupPreparation
	sessionID   string
	expires     time.Time
}

type webBackupStages struct {
	mu      sync.Mutex
	entries map[string]webBackupStage
	bytes   int
}

func webBackupToken() (string, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes[:]), nil
}

func (s *webBackupStages) purge(now time.Time) {
	for key, stage := range s.entries {
		if !now.Before(stage.expires) {
			delete(s.entries, key)
			s.bytes -= len(stage.request.ArchiveBase64)
		}
	}
}

func (s *WebAPIBackupService) preparedCandidate(ctx context.Context, request WebAPIBackupPrepare) (webBackupCandidate, []WebAPIBackupChange, error) {
	archive, err := webReadBackupArchive(request.ArchiveBase64)
	if err != nil {
		return webBackupCandidate{}, nil, err
	}
	changes := []WebAPIBackupChange{}
	var candidate webBackupCandidate
	if archive.Format == "native-v1" {
		candidate, err = webNativeReadArchivePlan(ctx, archive, &changes)
		if err == nil && !request.OnlyProfiles {
			var snapshot WebAPISettingsSnapshot
			var settingsChanges []WebAPIBackupChange
			snapshot, settingsChanges, err = webBackupNativeSettings(candidate, true)
			candidate.Data.Settings = &snapshot
			changes = append(changes, settingsChanges...)
		}
	} else {
		candidate, err = webBackupTypedCandidate(archive)
	}
	if err != nil {
		return candidate, nil, err
	}
	for _, p := range candidate.Data.Profiles {
		if webAPIUnreadable(p.WebAPIProfileInput) {
			changes = append(changes, WebAPIBackupChange{Kind: "profile", ID: string(p.ID), Name: p.Label, Count: 1, Action: "preserve-unreadable-cannot-apply-until-repaired"})
		}
	}
	if !request.OnlyProfiles && candidate.Data.Settings != nil {
		changes = append(changes, WebAPIBackupChange{Kind: "settings", Count: 1, Action: "replace-application-settings"})
		if request.RestoreDAVCredentials && candidate.Data.Settings.DAV != nil {
			changes = append(changes, WebAPIBackupChange{Kind: "dav", Count: 1, Action: "replace-whole-dav-credentials"})
		}
	}
	changes = append(changes, WebAPIBackupChange{Kind: "profiles", Count: len(candidate.Data.Profiles), Action: request.Strategy}, WebAPIBackupChange{Kind: "content", Count: len(candidate.Contents), Action: request.Strategy})
	sort.Slice(changes, func(i, j int) bool {
		a, b := changes[i], changes[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		if a.Action != b.Action {
			return a.Action < b.Action
		}
		return a.Name < b.Name
	})
	return candidate, changes, nil
}

func webBackupPlanDigest(request WebAPIBackupPrepare, changes []WebAPIBackupChange) string {
	body, _ := json.Marshal(struct {
		Version               int                  `json:"version"`
		ArchiveDigest         string               `json:"archiveDigest"`
		ExpectedRevision      WebAPIRevision       `json:"expectedRevision"`
		Strategy              string               `json:"strategy"`
		OnlyProfiles          bool                 `json:"onlyProfiles"`
		RestoreDAVCredentials bool                 `json:"restoreDavCredentials"`
		Changes               []WebAPIBackupChange `json:"changes"`
	}{1, webContentDigest(request.ArchiveBase64), request.ExpectedRevision, request.Strategy, request.OnlyProfiles, request.RestoreDAVCredentials, changes})
	return webContentDigest(body)
}

func (s *WebAPIBackupService) Prepare(ctx context.Context, request WebAPIBackupPrepare) (WebAPIRevision, WebAPIBackupPreparation, error) {
	var result WebAPIBackupPreparation
	if request.Strategy != "merge" && request.Strategy != "override" || request.OnlyProfiles && request.RestoreDAVCredentials {
		return "", result, webAPIError(400, "invalid_restore_options", "inconsistent backup preparation options")
	}
	if _, err := webAPIInteger(string(request.ExpectedRevision), false); err != nil {
		return "", result, webAPIError(400, "invalid_revision", "invalid target revision")
	}
	sessionID := WebAPIRequestSessionID(ctx)
	if sessionID == "" {
		return "", result, webAPIError(401, "unauthorized", "backup preparation requires an authenticated session")
	}
	release, err := s.content.slot(ctx)
	if err != nil {
		return "", result, err
	}
	defer release()
	revision, err := s.store.View(ctx, func(*WebAPIReadTx) error { return nil })
	if err != nil {
		return revision, result, err
	}
	if revision != request.ExpectedRevision {
		return revision, result, webAPIError(409, "revision_conflict", "target changed; prepare again")
	}
	candidate, changes, err := s.preparedCandidate(ctx, request)
	if err != nil {
		return revision, result, err
	}
	token, err := webBackupToken()
	if err != nil {
		return revision, result, err
	}
	_, err = s.store.Transaction(ctx, WebAPIMutation{ExpectedRevision: request.ExpectedRevision, OperationID: "preview-" + token}, "backups:prepare-validation", request, func(tx *WebAPIWriteTx) (any, error) {
		if _, err := s.applyRestore(tx, candidate, request.Strategy, request.OnlyProfiles, request.RestoreDAVCredentials); err != nil {
			return nil, err
		}
		return nil, webContentNeedsPreparation
	})
	if !errors.Is(err, webContentNeedsPreparation) {
		return revision, result, err
	}
	now := s.now()
	expires := now.Add(5 * time.Minute)
	result = WebAPIBackupPreparation{Token: token, ExpiresAt: expires.UTC().Format(time.RFC3339Nano), ArchiveDigest: webContentDigest(request.ArchiveBase64), PlanDigest: webBackupPlanDigest(request, changes), Changes: changes, RequiresConfirmation: true}
	request.ArchiveBase64 = append([]byte{}, request.ArchiveBase64...)
	s.stages.mu.Lock()
	defer s.stages.mu.Unlock()
	s.stages.purge(now)
	if len(s.stages.entries) >= 16 || len(request.ArchiveBase64) > WebAPIBackupExpandedLimit-s.stages.bytes {
		return revision, WebAPIBackupPreparation{}, webAPIError(429, "backup_staging_full", "too many staged archives")
	}
	if s.stages.entries == nil {
		s.stages.entries = map[string]webBackupStage{}
	}
	s.stages.entries[token] = webBackupStage{request: request, preparation: result, sessionID: sessionID, expires: expires}
	s.stages.bytes += len(request.ArchiveBase64)
	return revision, result, nil
}

func (s *WebAPIBackupService) Confirm(ctx context.Context, request WebAPIBackupConfirm) (WebAPIReceipt, error) {
	payload := struct {
		SessionID string              `json:"sessionId"`
		Request   WebAPIBackupConfirm `json:"request"`
	}{WebAPIRequestSessionID(ctx), request}
	return s.confirmPrepared(ctx, request, "backups:confirm", payload, nil, nil)
}

func (s *WebAPIBackupService) confirmPrepared(ctx context.Context, request WebAPIBackupConfirm, command string, payload any, before func(*WebAPIWriteTx, webBackupStage) error, complete func(*WebAPIWriteTx, webBackupStage, WebAPIBackupResult) (any, error)) (WebAPIReceipt, error) {
	if receipt, done, err := s.content.preflight(ctx, request.WebAPIMutation, command, payload); done || err != nil {
		return receipt, err
	}
	if request.Intent != "confirm-backup-conversion" {
		return WebAPIReceipt{}, webAPIError(400, "restore_intent_required", "explicit prepared restore confirmation is required")
	}
	s.stages.mu.Lock()
	s.stages.purge(s.now())
	stage, exists := s.stages.entries[request.Token]
	s.stages.mu.Unlock()
	if !exists || stage.sessionID != WebAPIRequestSessionID(ctx) || stage.request.ExpectedRevision != request.ExpectedRevision || stage.preparation.ArchiveDigest != request.ArchiveDigest || stage.preparation.PlanDigest != request.PlanDigest || stage.request.RestoreDAVCredentials != request.RestoreDAVCredentials {
		return WebAPIReceipt{}, webAPIError(409, "backup_preparation_invalid", "backup preparation expired or no longer matches this confirmation")
	}
	release, err := s.content.slot(ctx)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	defer release()
	candidate, changes, err := s.preparedCandidate(ctx, stage.request)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	if webBackupPlanDigest(stage.request, changes) != request.PlanDigest {
		return WebAPIReceipt{}, webAPIError(409, "backup_plan_changed", "backup conversion changed; prepare again")
	}
	receipt, err := s.store.Transaction(ctx, request.WebAPIMutation, command, payload, func(tx *WebAPIWriteTx) (any, error) {
		if !s.now().Before(stage.expires) {
			return nil, webAPIError(409, "backup_preparation_invalid", "backup preparation expired")
		}
		if before != nil {
			if err := before(tx, stage); err != nil {
				return nil, err
			}
		}
		result, err := s.applyRestore(tx, candidate, stage.request.Strategy, stage.request.OnlyProfiles, stage.request.RestoreDAVCredentials)
		if err != nil {
			return nil, err
		}
		if complete != nil {
			return complete(tx, stage, result.(WebAPIBackupResult))
		}
		return result, nil
	})
	if err == nil {
		s.stages.mu.Lock()
		if saved, ok := s.stages.entries[request.Token]; ok {
			delete(s.stages.entries, request.Token)
			s.stages.bytes -= len(saved.request.ArchiveBase64)
		}
		s.stages.mu.Unlock()
	}
	return receipt, err
}

func (s *WebAPIBackupService) registerPreparation(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/backups:prepare", func(w http.ResponseWriter, r *http.Request) {
		if !webContentPost(w, r) {
			return
		}
		var request WebAPIBackupPrepare
		if err := WebAPIDecodeJSONLimit(w, r, &request, WebAPIMaxContentJSONBytes); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		revision, result, err := s.Prepare(r.Context(), request)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteSuccess(w, revision, result)
	})
	mux.HandleFunc("/api/v1/backups:confirm", webContentCommand(webAPIMaxJSONBytes, s.Confirm))
}
