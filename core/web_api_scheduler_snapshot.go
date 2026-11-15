//go:build webpanel && !android

package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

func webSchedulerFingerprint(v any) string {
	b, _ := json.Marshal(v)
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}

func webSchedulerTime(raw string) time.Time {
	value, _ := time.Parse(time.RFC3339Nano, raw)
	return value
}
func webSchedulerOptionalTime(raw *string) time.Time {
	if raw == nil {
		return time.Time{}
	}
	return webSchedulerTime(*raw)
}
func webSchedulerTimePointer(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (s *WebAPIScheduler) snapshot(ctx context.Context) (webSchedulerSnapshot, error) {
	out := webSchedulerSnapshot{attempts: map[string]webSchedulerAttempt{}}
	now := s.clock.Now().UTC()
	revision, err := s.store.View(ctx, func(tx *WebAPIReadTx) error {
		out.epoch = tx.SchedulerEpoch()
		meta, err := tx.Meta()
		if err != nil {
			return err
		}
		settings, err := tx.Settings()
		if err != nil {
			return err
		}
		config := settings.Runtime.PatchClashConfig
		interval := int64(config.GeoUpdateInterval) * 3600000
		if interval <= 0 {
			interval = 86400000
		}
		add := func(kind WebAPISchedulerKind, id, name string, enabled bool, period int64, last time.Time, missing bool, source any, unavailable string) error {
			if len(out.targets) >= webSchedulerMaxTargets {
				return webAPIError(413, "scheduler_target_limit", "scheduler resource budget exceeded")
			}
			fingerprint := webSchedulerFingerprint(struct {
				Store    string
				Epoch    string
				Kind     WebAPISchedulerKind
				ID, Name string
				Source   any
			}{meta.StoreID, out.epoch, kind, id, name, source})
			out.targets = append(out.targets, webSchedulerTarget{status: WebAPISchedulerTarget{Kind: kind, ID: id, Name: name, Enabled: enabled, MissingContent: missing}, fingerprint: fingerprint, interval: webSchedulerDuration(period), periodMillis: period, lastUpdate: last, unavailable: unavailable})
			return nil
		}
		profiles, err := tx.Profiles()
		if err != nil {
			return err
		}
		for _, p := range profiles {
			if p.Type == "url" {
				source := p.WebAPIProfileInput
				source.AutoUpdate, source.AutoUpdateDurationMillis = false, 0
				if err = add(WebAPISchedulerProfile, string(p.ID), "", p.AutoUpdate, p.AutoUpdateDurationMillis, webSchedulerOptionalTime(p.LastUpdateDate), false, source, ""); err != nil {
					return err
				}
			}
		}
		scripts, err := tx.Scripts()
		if err != nil {
			return err
		}
		for _, v := range scripts {
			if v.URL == nil || *v.URL == "" {
				continue
			}
			source := struct {
				Label string
				URL   *string
			}{v.Label, v.URL}
			if err = add(WebAPISchedulerScript, string(v.ID), "", config.GeoAutoUpdate, interval, webSchedulerTime(v.LastUpdateTime), false, source, ""); err != nil {
				return err
			}
		}
		icons, err := tx.IconSets()
		if err != nil {
			return err
		}
		for _, v := range icons {
			if v.URL == "" {
				continue
			}
			source := struct{ Name, URL string }{v.Name, v.URL}
			if err = add(WebAPISchedulerIconSet, string(v.ID), "", config.GeoAutoUpdate, interval, webSchedulerOptionalTime(v.LastUpdateTime), false, source, ""); err != nil {
				return err
			}
		}
		providers, err := tx.ClashProviders()
		if err != nil {
			return err
		}
		for _, v := range providers {
			if v.URL == "" {
				continue
			}
			var modified time.Time
			content, contentErr := tx.Content(WebAPIContentKey{Kind: WebAPIContentRuleProvider, ID: v.ID})
			if contentErr == nil {
				modified = webSchedulerTime(content.ModifiedAt)
			} else if !webSchedulerNotFound(contentErr) {
				return contentErr
			}
			if err = add(WebAPISchedulerRuleProvider, string(v.ID), "", config.GeoAutoUpdate, interval, modified, false, v, ""); err != nil {
				return err
			}
		}
		modules, err := tx.MitmModules()
		if err != nil {
			return err
		}
		for _, v := range modules {
			content, contentErr := tx.Content(WebAPIContentKey{Kind: WebAPIContentMitmModule, ID: v.ID})
			missing := webSchedulerNotFound(contentErr) || contentErr == nil && len(content.Bytes) == 0
			if contentErr != nil && !webSchedulerNotFound(contentErr) {
				return contentErr
			}
			dependencies := map[string]string{}
			for source, name := range v.Resources {
				dependencies[source] = ""
				_, resourceErr := tx.Content(WebAPIContentKey{Kind: WebAPIContentMitmResource, ID: v.ID, Name: name})
				if webSchedulerNotFound(resourceErr) {
					missing = true
				} else if resourceErr != nil {
					return resourceErr
				}
			}
			if len(content.Bytes) > 0 {
				parsed, parseErr := webContentParseModule(content.Bytes, v.Arguments)
				if parseErr == nil {
					for _, source := range parsed.Resources {
						dependencies[source] = ""
						if v.Resources[source] == "" {
							missing = true
						}
					}
				}
			}
			remote := v.URL != nil && *v.URL != ""
			localDigest := ""
			unavailable := ""
			if !remote {
				localDigest = content.SHA256
				if missing && len(content.Bytes) == 0 {
					unavailable = "local_content_missing"
				}
			}
			source := struct {
				Name, Description string
				URL               *string
				Enabled           bool
				Arguments         map[string]string
				System            *string
				Dependencies      map[string]string
				LocalDigest       string
			}{v.Name, v.Description, v.URL, v.Enabled, v.Arguments, v.System, dependencies, localDigest}
			enabled := config.Mitm.Enable && (remote && config.GeoAutoUpdate || missing)
			if err = add(WebAPISchedulerModule, string(v.ID), "", enabled, interval, webSchedulerTime(v.LastUpdateTime), missing, source, unavailable); err != nil {
				return err
			}
		}
		geo, err := webRuntimeGeoDescriptors(tx, meta.StoreID)
		if err != nil {
			return err
		}
		for _, v := range geo {
			if err = add(WebAPISchedulerGeo, string(v.ID), v.Type, config.GeoAutoUpdate, interval, time.Time{}, false, v, ""); err != nil {
				return err
			}
		}
		if settings.CurrentProfileID != nil {
			descriptors, descriptorErr := webRuntimeProviderDescriptors(tx, *settings.CurrentProfileID, meta.StoreID)
			if descriptorErr == nil {
				periods, periodErr := webSchedulerProviderIntervals(tx, *settings.CurrentProfileID)
				if periodErr != nil {
					return periodErr
				}
				for _, v := range descriptors {
					period := periods[v.Source.Purpose+":"+v.Source.Name]
					if err = add(WebAPISchedulerExternalProvider, string(v.Source.ID), v.Source.Purpose, period > 0, period.Milliseconds(), time.Time{}, false, v, ""); err != nil {
						return err
					}
				}
			} else if !webSchedulerNotFound(descriptorErr) {
				if err = add(WebAPISchedulerExternalProvider, string(*settings.CurrentProfileID), "unavailable", true, interval, time.Time{}, false, *settings.CurrentProfileID, "provider_source_unavailable"); err != nil {
					return err
				}
			}
		}
		out.attempts, err = webSchedulerReadAttempts(tx)
		return err
	})
	out.revision = revision
	if err != nil {
		return out, err
	}
	sort.Slice(out.targets, func(i, j int) bool { return out.targets[i].key() < out.targets[j].key() })
	for i := range out.targets {
		t := &out.targets[i]
		record, exists := out.attempts[t.key()]
		if exists && record.Fingerprint != t.fingerprint {
			exists = false
		}
		last := t.lastUpdate
		if exists {
			t.status.OperationID, t.status.Failures = record.OperationID, record.Failures
			t.status.LastSuccess, t.status.NextAttempt = webSchedulerTimePointer(record.LastSuccess), webSchedulerTimePointer(record.NextAttempt)
			t.status.RequiresApply, t.status.ConfirmationRequired = record.RequiresApply, record.ConfirmationRequired
			t.status.ErrorCode = record.ErrorCode
			if record.LastSuccess.After(last) {
				last = record.LastSuccess
			}
		}
		due := last.Add(t.interval)
		if t.periodMillis > 9223372036854 && !last.IsZero() {
			due = time.UnixMilli(last.UnixMilli() + t.periodMillis).UTC()
		}
		if last.IsZero() || t.status.MissingContent {
			due = now
		}
		if due.Year() <= 9999 {
			t.status.DueAt = webSchedulerTimePointer(due)
		}
		switch {
		case exists && (record.Phase == "inflight" || record.Phase == "unknown"):
			t.status.Phase = "unknown"
			if record.Phase == "inflight" {
				t.status.ErrorCode = "effect_unknown"
			}
		case !t.status.Enabled:
			t.status.Phase = "disabled"
		case s.options.SafeMode && !s.options.Backend.Synthetic:
			t.status.Phase, t.status.ErrorCode = "blocked", "safe_mode"
		case !WebAPIInternalAuthorized(s.ctx):
			t.status.Phase, t.status.ErrorCode = "blocked", "background_authority_required"
		case t.unavailable != "":
			t.status.Phase, t.status.ErrorCode = "blocked", t.unavailable
		case exists && record.Phase == "blocked" && record.NextAttempt.IsZero():
			t.status.Phase = "blocked"
		case exists && now.Before(record.NextAttempt):
			t.status.Phase = "backoff"
		case due.After(now):
			t.status.Phase = "idle"
		default:
			t.status.Phase = "due"
		}
	}
	return out, nil
}

func webSchedulerNotFound(err error) bool {
	var e *WebAPIError
	return errors.As(err, &e) && e.Status == 404
}

func webSchedulerProviderIntervals(tx *WebAPIReadTx, id WebAPIID) (map[string]time.Duration, error) {
	out := map[string]time.Duration{}
	source, err := webRuntimeProfileSource(tx, id)
	if err != nil {
		return out, err
	}
	document, err := webRuntimeDocument(source.Plain)
	if err != nil {
		return out, err
	}
	var root map[string]any
	if err = document.Decode(&root); err != nil {
		return out, err
	}
	for _, kind := range []string{"proxy-providers", "rule-providers"} {
		mappings, _ := root[kind].(map[string]any)
		for name, value := range mappings {
			mapping, _ := value.(map[string]any)
			if mapping["type"] != "http" {
				continue
			}
			var seconds int64
			switch n := mapping["interval"].(type) {
			case int:
				seconds = int64(n)
			case int64:
				seconds = n
			case uint64:
				if n <= 31536000 {
					seconds = int64(n)
				}
			}
			if seconds > 0 && seconds <= 31536000 {
				out[kind+":"+name] = time.Duration(seconds) * time.Second
			}
		}
	}
	return out, nil
}
