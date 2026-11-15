//go:build webpanel && !android

package core

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"
)

type WebAPIBackupOptions struct {
	Now   func() time.Time
	Fault func(string) error
}

type WebAPIBackupService struct {
	store   *WebAPIStore
	content *WebAPIContentService
	now     func() time.Time
	fault   func(string) error
	stages  webBackupStages
}

type WebAPIBackupRestore struct {
	WebAPIMutation
	Intent        string `json:"intent"`
	Strategy      string `json:"strategy"`
	OnlyProfiles  bool   `json:"onlyProfiles"`
	ArchiveBase64 []byte `json:"archiveBase64"`
}

type WebAPIBackupResult struct {
	Restored     bool `json:"restored"`
	ProfileCount int  `json:"profileCount"`
	ContentCount int  `json:"contentCount"`
}

type WebAPIReset struct {
	WebAPIMutation
	Intent string `json:"intent"`
}
type WebAPIResetResult struct {
	Reset bool `json:"reset"`
}

func NewWebAPIBackupService(store *WebAPIStore, options WebAPIBackupOptions) *WebAPIBackupService {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &WebAPIBackupService{store: store, content: NewWebAPIContentService(store, WebAPIContentOptions{Now: now}), now: now, fault: options.Fault}
}

func webBackupSnapshot(tx *WebAPIReadTx, includeDAV bool) (webBackupCandidate, error) {
	var c webBackupCandidate
	if err := tx.CheckBackupBudget(100000, WebAPIBackupMetadataLimit, WebAPIBackupExpandedLimit); err != nil {
		return c, err
	}
	var err error
	if c.Data.Profiles, err = tx.Profiles(); err != nil {
		return c, err
	}
	if c.Data.Scripts, err = tx.Scripts(); err != nil {
		return c, err
	}
	if c.Data.Rules, err = tx.Rules(); err != nil {
		return c, err
	}
	if c.Data.DisabledRules, err = tx.DisabledRules(); err != nil {
		return c, err
	}
	if c.Data.ProxyGroups, err = tx.ProxyGroups(); err != nil {
		return c, err
	}
	if c.Data.CustomProxies, err = tx.CustomProxies(); err != nil {
		return c, err
	}
	if c.Data.ProxyDialers, err = tx.ProxyDialers(); err != nil {
		return c, err
	}
	if c.Data.RuleProviders, err = tx.ClashProviders(); err != nil {
		return c, err
	}
	if c.Data.MitmModules, err = tx.MitmModules(); err != nil {
		return c, err
	}
	if c.Data.IconSets, err = tx.IconSets(); err != nil {
		return c, err
	}
	if c.Contents, err = tx.Contents(); err != nil {
		return c, err
	}
	settings, err := tx.Settings()
	if err != nil {
		return c, err
	}
	snapshot := SnapshotWebAPISettingsWithOptions(settings, WebAPISettingsSnapshotOptions{IncludeDAVCredentials: includeDAV})
	c.Data.Settings = &snapshot
	return c, nil
}

func (s *WebAPIBackupService) Export(ctx context.Context, format string, includeDAV bool) (WebAPIRevision, []byte, error) {
	var candidate webBackupCandidate
	revision, err := s.store.View(ctx, func(tx *WebAPIReadTx) error {
		var err error
		candidate, err = webBackupSnapshot(tx, includeDAV)
		return err
	})
	if err != nil {
		return revision, nil, err
	}
	archive, err := webBackupEncodeSnapshot(ctx, candidate, format, includeDAV)
	return revision, archive, err
}

func (s *WebAPIBackupService) decode(ctx context.Context, data []byte, includeSettings bool) (webBackupCandidate, error) {
	archive, err := webReadBackupArchive(data)
	if err != nil {
		return webBackupCandidate{}, err
	}
	if archive.Format == "flclash-v1" {
		return webBackupTypedCandidate(archive)
	}
	candidate, err := webNativeReadArchive(ctx, archive)
	if err != nil {
		return candidate, err
	}
	if !includeSettings {
		return candidate, nil
	}
	snapshot, _, err := webBackupNativeSettings(candidate, false)
	if err != nil {
		return candidate, err
	}
	candidate.Data.Settings = &snapshot
	return candidate, nil
}

func webBackupMergeRows[T any](old, imported []T, key func(T) string) []T {
	rows := make(map[string]T, len(old)+len(imported))
	keys := make([]string, 0, len(old)+len(imported))
	for _, list := range [][]T{old, imported} {
		for _, v := range list {
			k := key(v)
			if _, ok := rows[k]; !ok {
				keys = append(keys, k)
			}
			rows[k] = v
		}
	}
	sort.Strings(keys)
	result := make([]T, 0, len(keys))
	for _, k := range keys {
		result = append(result, rows[k])
	}
	return result
}

func webBackupMerge(current, incoming webBackupCandidate, strategy string) webBackupCandidate {
	if strategy == "override" {
		return incoming
	}
	a, b := current.Data, incoming.Data
	b.Profiles = webBackupMergeRows(a.Profiles, b.Profiles, func(v WebAPIProfile) string { return string(v.ID) })
	b.Scripts = webBackupMergeRows(a.Scripts, b.Scripts, func(v WebAPIScript) string { return string(v.ID) })
	b.Rules = webBackupMergeRows(a.Rules, b.Rules, func(v WebAPIRule) string { return string(v.ID) })
	b.DisabledRules = webBackupMergeRows(a.DisabledRules, b.DisabledRules, func(v WebAPIDisabledRule) string { return string(v.ProfileID) + ":" + string(v.RuleID) })
	b.ProxyGroups = webBackupMergeRows(a.ProxyGroups, b.ProxyGroups, func(v WebAPIProxyGroup) string { return string(v.ID) })
	b.CustomProxies = webBackupMergeRows(a.CustomProxies, b.CustomProxies, func(v WebAPICustomProxy) string { return string(v.ID) })
	b.ProxyDialers = webBackupMergeRows(a.ProxyDialers, b.ProxyDialers, func(v WebAPIProxyDialer) string { return string(v.ProfileID) + ":" + string(v.ProxyID) })
	b.RuleProviders = webBackupMergeRows(a.RuleProviders, b.RuleProviders, func(v WebAPIClashProvider) string { return string(v.ID) })
	b.MitmModules = webBackupMergeRows(a.MitmModules, b.MitmModules, func(v WebAPIMitmModule) string { return string(v.ID) })
	b.IconSets = webBackupMergeRows(a.IconSets, b.IconSets, func(v WebAPIIconSet) string { return string(v.ID) })
	blobs := webBackupMergeRows(current.Contents, incoming.Contents, func(v WebAPIContent) string { return string(v.Key.Kind) + ":" + string(v.Key.ID) + ":" + v.Key.Name })
	return webBackupCandidate{Data: b, Contents: blobs}
}

func (s *WebAPIBackupService) checkFault(stage string) error {
	if s.fault != nil {
		return s.fault(stage)
	}
	return nil
}

func (s *WebAPIBackupService) Restore(ctx context.Context, request WebAPIBackupRestore) (WebAPIReceipt, error) {
	if receipt, done, err := s.content.preflight(ctx, request.WebAPIMutation, "backups:restore", request); done || err != nil {
		return receipt, err
	}
	if request.Intent != "restore-application-data" {
		return WebAPIReceipt{}, webAPIError(400, "restore_intent_required", "explicit restore intent is required")
	}
	if request.Strategy != "merge" && request.Strategy != "override" {
		return WebAPIReceipt{}, webAPIError(400, "invalid_restore_strategy", "restore strategy must be merge or override")
	}
	release, err := s.content.slot(ctx)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	defer release()
	candidate, err := s.decode(ctx, request.ArchiveBase64, !request.OnlyProfiles)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	return s.store.Transaction(ctx, request.WebAPIMutation, "backups:restore", request, func(tx *WebAPIWriteTx) (any, error) {
		return s.applyRestore(tx, candidate, request.Strategy, request.OnlyProfiles, false)
	})
}

func (s *WebAPIBackupService) applyRestore(tx *WebAPIWriteTx, incoming webBackupCandidate, strategy string, onlyProfiles, restoreDAV bool) (any, error) {
	current := webBackupCandidate{}
	var err error
	if strategy == "merge" && !incoming.ConfigurationOnly {
		current, err = webBackupSnapshot(tx.WebAPIReadTx, true)
		if err != nil {
			return nil, err
		}
	}
	settings, err := tx.Settings()
	if err != nil {
		return nil, err
	}
	if !onlyProfiles && incoming.Data.Settings != nil {
		settings, err = RestoreWebAPISettingsWithOptions(settings, *incoming.Data.Settings, WebAPISettingsRestoreOptions{RestoreDAVCredentials: restoreDAV})
		if err != nil {
			return nil, err
		}
	}
	if incoming.ConfigurationOnly {
		if !onlyProfiles {
			if err = ValidateWebAPISettingsReferences(tx.WebAPIReadTx, settings); err != nil {
				return nil, err
			}
			if err = tx.SaveSettings(settings); err != nil {
				return nil, err
			}
		}
		if err = tx.ResetAllSourceGrants(); err != nil {
			return nil, err
		}
		if err = tx.ResetSchedulerAttempts(); err != nil {
			return nil, err
		}
		return WebAPIBackupResult{Restored: true}, nil
	}
	candidate := webBackupMerge(current, incoming, strategy)
	if err = webBackupValidateData(candidate.Data, candidate.Contents); err != nil {
		return nil, err
	}
	if settings.CurrentProfileID != nil {
		found := false
		for _, p := range candidate.Data.Profiles {
			if p.ID == *settings.CurrentProfileID {
				found = true
				break
			}
		}
		if !found {
			return nil, webBackupInvalid("backup_invalid_current_profile")
		}
	}
	if err = s.checkFault("before-write"); err != nil {
		return nil, err
	}
	if strategy == "override" {
		err = tx.DetachAllStatefulResources()
	} else {
		ids := make([]WebAPIID, 0, len(incoming.Data.Profiles))
		for _, profile := range incoming.Data.Profiles {
			ids = append(ids, profile.ID)
		}
		err = tx.DetachStatefulProfiles(ids)
	}
	if err != nil {
		return nil, err
	}
	if err = tx.ResetConfiguration(); err != nil {
		return nil, err
	}
	if err = tx.ResetAllSourceGrants(); err != nil {
		return nil, err
	}
	if err = tx.ResetSchedulerAttempts(); err != nil {
		return nil, err
	}
	for _, v := range candidate.Data.Profiles {
		if err = tx.InsertProfile(v); err != nil {
			return nil, err
		}
	}
	for _, v := range candidate.Data.Scripts {
		if err = tx.InsertScript(v); err != nil {
			return nil, err
		}
	}
	for _, v := range candidate.Data.Rules {
		if err = tx.InsertRule(v); err != nil {
			return nil, err
		}
	}
	for _, v := range candidate.Data.DisabledRules {
		if err = tx.SaveDisabledRule(v); err != nil {
			return nil, err
		}
	}
	for _, v := range candidate.Data.ProxyGroups {
		if err = tx.InsertProxyGroup(v); err != nil {
			return nil, err
		}
	}
	for _, v := range candidate.Data.CustomProxies {
		if err = tx.InsertCustomProxy(v); err != nil {
			return nil, err
		}
	}
	for _, v := range candidate.Data.ProxyDialers {
		if err = tx.SaveProxyDialer(v); err != nil {
			return nil, err
		}
	}
	for _, v := range candidate.Data.RuleProviders {
		if err = tx.InsertClashProvider(v); err != nil {
			return nil, err
		}
	}
	for _, v := range candidate.Data.MitmModules {
		if err = tx.InsertMitmModule(v); err != nil {
			return nil, err
		}
	}
	for _, v := range candidate.Data.IconSets {
		if err = tx.InsertIconSet(v); err != nil {
			return nil, err
		}
	}
	if err = s.checkFault("after-metadata"); err != nil {
		return nil, err
	}
	for _, v := range candidate.Contents {
		if err = tx.PutContent(v); err != nil {
			return nil, err
		}
	}
	if err = s.checkFault("after-content"); err != nil {
		return nil, err
	}
	if err = webBackupValidateStoredCandidate(tx); err != nil {
		return nil, err
	}
	if err = tx.SaveSettings(settings); err != nil {
		return nil, err
	}
	if err = s.checkFault("after-settings"); err != nil {
		return nil, err
	}
	return WebAPIBackupResult{Restored: true, ProfileCount: len(candidate.Data.Profiles), ContentCount: len(candidate.Contents)}, nil
}

func webBackupValidateStoredCandidate(tx *WebAPIWriteTx) error {
	profiles, err := tx.Profiles()
	if err != nil {
		return err
	}
	for _, p := range profiles {
		if p.Type == "custom" {
			continue
		}
		content, err := tx.Content(WebAPIContentKey{Kind: WebAPIContentProfile, ID: p.ID})
		if err != nil {
			var api *WebAPIError
			if errors.As(err, &api) && api.Status == 404 && p.Type == "url" {
				continue
			}
			return webBackupInvalid("backup_missing_content")
		}
		raw, catalog, err := webContentProfile(content.Bytes, p.AgeSecretKey)
		if err != nil {
			return err
		}
		if err = webContentReconcileStateful(tx, p.ID, raw); err != nil {
			return err
		}
		if !webAPIUnreadable(p.WebAPIProfileInput) && p.ExtendType == "standard" {
			dialers, _ := webAPIDialers(p.Overrides)
			for source, target := range dialers {
				found := false
				for _, proxy := range raw.Proxy {
					if proxy["name"] == source {
						found = true
						proxy["dialer-proxy"] = target
					}
				}
				if !found {
					return webAPIError(409, "outbound_policy_invalid", "profile override source is missing")
				}
			}
			if err = ValidateProxyDependencies(raw); err != nil {
				return webAPIError(409, "outbound_policy_invalid", "profile override graph is invalid")
			}
		}
		if !webAPIUnreadable(p.WebAPIProfileInput) {
			if err = tx.SetProfileCatalog(p.ID, catalog); err != nil {
				return err
			}
		}
	}
	if err = RefreshWebAPICatalogForRestore(tx); err != nil {
		return err
	}
	for _, p := range profiles {
		if err = tx.validateProfile(p.WebAPIProfileInput, p.ID); err != nil {
			return err
		}
	}
	rules, err := tx.Rules()
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if err = tx.ValidateRuleForRestore(rule.WebAPIRuleInput); err != nil {
			return err
		}
	}
	return nil
}

func (s *WebAPIBackupService) Reset(ctx context.Context, request WebAPIReset) (WebAPIReceipt, error) {
	return s.store.Transaction(ctx, request.WebAPIMutation, "app:reset", request, func(tx *WebAPIWriteTx) (any, error) {
		if request.Intent != "reset-application-data" {
			return nil, webAPIError(400, "reset_intent_required", "explicit reset intent is required")
		}
		if err := tx.DetachAllStatefulResources(); err != nil {
			return nil, err
		}
		if err := tx.ResetConfiguration(); err != nil {
			return nil, err
		}
		if err := tx.ResetAllSourceGrants(); err != nil {
			return nil, err
		}
		if err := tx.ResetSchedulerAttempts(); err != nil {
			return nil, err
		}
		if err := tx.SaveSettings(DefaultWebAPISettings()); err != nil {
			return nil, err
		}
		if err := s.checkFault("after-reset"); err != nil {
			return nil, err
		}
		return WebAPIResetResult{Reset: true}, nil
	})
}

func RegisterWebAPIBackupRoutes(mux *http.ServeMux, store *WebAPIStore, options WebAPIBackupOptions) *WebAPIBackupService {
	s := NewWebAPIBackupService(store, options)
	s.registerPreparation(mux)
	mux.HandleFunc("/api/v1/backups:restore", webContentCommand(WebAPIMaxContentJSONBytes, s.Restore))
	mux.HandleFunc("/api/v1/app:reset", webContentCommand(webAPIMaxJSONBytes, s.Reset))
	mux.HandleFunc("/api/v1/backups", func(w http.ResponseWriter, r *http.Request) {
		if !webAPIGetMethod(w, r) {
			return
		}
		q, err := WebAPIQuery(r, "format", "includeDavCredentials")
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		include := false
		if v, ok := q["includeDavCredentials"]; ok {
			if v != "true" && v != "false" {
				WebAPIWriteError(w, webAPIError(400, "invalid_backup_options", "includeDavCredentials must be true or false"))
				return
			}
			include = v == "true"
		}
		revision, data, err := s.Export(r.Context(), q["format"], include)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="flclash-backup.zip"`)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-FlClash-Revision", string(revision))
		_, _ = w.Write(data)
	})
	return s
}

func (s *WebAPIBackupService) ExportFromSnapshot(ctx context.Context, tx *WebAPIReadTx, format string, includeDAV bool) ([]byte, error) {
	candidate, err := webBackupSnapshot(tx, includeDAV)
	if err != nil {
		return nil, err
	}
	return webBackupEncodeSnapshot(ctx, candidate, format, includeDAV)
}

func webBackupEncodeSnapshot(ctx context.Context, candidate webBackupCandidate, format string, includeDAV bool) ([]byte, error) {
	var err error
	var archive []byte
	switch format {
	case "flclash-v1":
		archive, err = webBackupEncodeTyped(candidate)
	case "native-v1":
		for _, content := range candidate.Contents {
			if content.Key.Kind == WebAPIContentProfileAsset {
				return nil, webAPIError(409, "backup_native_profile_asset_unsupported", "native backups cannot preserve typed profile assets; use flclash-v1")
			}
		}
		var config []byte
		config, err = NativeWebAPISettingsConfigWithOptions(*candidate.Data.Settings, WebAPISettingsSnapshotOptions{IncludeDAVCredentials: includeDAV})
		if err == nil {
			archive, err = webNativeEncodeArchive(ctx, candidate, config)
		}
	default:
		err = webAPIError(400, "backup_unsupported_version", "unsupported export format")
	}
	return archive, err
}
