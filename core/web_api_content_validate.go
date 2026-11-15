//go:build webpanel && !android

package core

import (
	"context"
	"net/http"
)

type WebAPIProfileValidate struct {
	ExpectedRevision WebAPIRevision `json:"expectedRevision"`
	ProfileID        *WebAPIID      `json:"profileId,omitempty"`
	Bytes            []byte         `json:"bytes"`
	AgeSecretKey     *string        `json:"ageSecretKey,omitempty"`
}

type WebAPIProfileValidationCounts struct {
	Outbounds     int `json:"outbounds"`
	RuleProviders int `json:"ruleProviders"`
	SubRules      int `json:"subRules"`
}

type WebAPIProfileValidation struct {
	Valid  bool                          `json:"valid"`
	SHA256 string                        `json:"sha256"`
	Counts WebAPIProfileValidationCounts `json:"counts"`
}

func (s *WebAPIContentService) ValidateProfile(ctx context.Context, request WebAPIProfileValidate) (WebAPIRevision, WebAPIProfileValidation, error) {
	var result WebAPIProfileValidation
	if _, err := webAPIInteger(string(request.ExpectedRevision), false); err != nil {
		return "", result, webAPIError(400, "invalid_revision", "invalid validation revision")
	}
	if len(request.Bytes) > WebAPIMaxContentBytes {
		return "", result, webAPIError(413, "content_too_large", "profile exceeds 32 MiB")
	}
	release, err := s.slot(ctx)
	if err != nil {
		return "", result, err
	}
	defer release()
	revision, err := s.store.View(ctx, func(tx *WebAPIReadTx) error {
		current, err := webAPIRevision(tx.tx, "meta")
		if err != nil {
			return err
		}
		if current != request.ExpectedRevision {
			return webAPIError(409, "revision_conflict", "profile changed; validate the current snapshot")
		}
		secret := ""
		id := WebAPIID("1") // A validation-only scope, never allocated or persisted.
		var assetTx *WebAPIReadTx
		if request.ProfileID != nil {
			profile, err := tx.Profile(*request.ProfileID)
			if err != nil {
				return err
			}
			if profile.Type == "custom" {
				return webAPIError(409, "custom_profile_content", "custom profiles are validated through their typed graph")
			}
			if webAPIUnreadable(profile.WebAPIProfileInput) {
				return webAPIError(409, "unreadable_overrides", "repair unreadable overrides before editing content")
			}
			secret, id, assetTx = profile.AgeSecretKey, profile.ID, tx
		}
		if request.AgeSecretKey != nil {
			secret = *request.AgeSecretKey
		}
		raw, catalog, err := webContentProfile(request.Bytes, secret)
		if err != nil {
			return err
		}
		if _, _, err = webContentStatefulPlan(assetTx, id, raw); err != nil {
			return err
		}
		result = WebAPIProfileValidation{Valid: true, SHA256: webContentDigest(request.Bytes), Counts: WebAPIProfileValidationCounts{Outbounds: len(catalog.OutboundNames), RuleProviders: len(catalog.RuleProviderNames), SubRules: len(catalog.SubRuleNames)}}
		return nil
	})
	if err != nil {
		return revision, WebAPIProfileValidation{}, err
	}
	return revision, result, nil
}

func (s *WebAPIContentService) registerProfileValidation(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/profiles:validate", func(w http.ResponseWriter, r *http.Request) {
		if !webContentPost(w, r) {
			return
		}
		var request WebAPIProfileValidate
		if err := WebAPIDecodeJSONLimit(w, r, &request, WebAPIMaxContentJSONBytes); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		revision, result, err := s.ValidateProfile(r.Context(), request)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteSuccess(w, revision, result)
	})
}
