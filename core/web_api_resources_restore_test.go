//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebAPIRestoreRuleValidationPreservesUnknownAndRejectsKnownInvalid(t *testing.T) {
	r := newWebAPITestRig(t)
	_, err := r.store.Transaction(context.Background(), WebAPIMutation{"0", "restore-rule-context"}, "test:restore", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		for _, input := range []WebAPIProfileInput{{Type: "file", Label: "Quarantined"}, {Type: "file", Label: "Known"}, {Type: "custom", Label: "Custom"}} {
			if _, err := tx.CreateProfile(input); err != nil {
				return nil, err
			}
		}
		if err := tx.SetProfileCatalog("1", WebAPIProfileCatalog{UnavailableReason: WebAPICatalogUnreadableOverrides}); err != nil {
			return nil, err
		}
		return struct{}{}, tx.SetProfileCatalog("2", WebAPIProfileCatalog{OutboundNames: []string{"Allowed"}, RuleProviderNames: []string{"ExistingRules"}, SubRuleNames: []string{"Nested"}})
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		owner := WebAPIID("1")
		content, target := "example.test", "Saved exact target"
		rule := WebAPIRuleInput{ProfileID: &owner, RuleAction: "DOMAIN", Content: &content, RuleTarget: &target}
		if err := tx.ValidateRuleForRestore(rule); err != nil {
			t.Fatal("quarantined routing text was not preserved", err)
		}
		webAPITestErrorCode(t, tx.validateRule(rule), "catalog_unavailable")
		bad := "65536"
		rule.RuleAction = "DST_PORT"
		rule.Content = &bad
		if err := tx.ValidateRuleForRestore(rule); err == nil {
			t.Fatal("quarantine skipped syntax validation")
		}
		owner = "2"
		rule.RuleAction = "DOMAIN"
		rule.Content = &content
		webAPITestErrorCode(t, tx.ValidateRuleForRestore(rule), "invalid_reference")
		target = "Allowed"
		if err := tx.ValidateRuleForRestore(rule); err != nil {
			t.Fatal(err)
		}
		provider := "Absent"
		rule.RuleAction = "RULE_SET"
		rule.Content = nil
		rule.RuleProvider = &provider
		webAPITestErrorCode(t, tx.ValidateRuleForRestore(rule), "invalid_reference")
		provider = "ExistingRules"
		if err := tx.ValidateRuleForRestore(rule); err != nil {
			t.Fatal(err)
		}
		owner = "3"
		provider = "Absent"
		webAPITestErrorCode(t, tx.ValidateRuleForRestore(rule), "invalid_reference")
		owner = "999"
		webAPITestErrorCode(t, tx.ValidateRuleForRestore(rule), "invalid_reference")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebAPIPreview72MiBLimitIsExplicitAndPathRestricted(t *testing.T) {
	type preview struct {
		Draft string `json:"draft"`
	}
	body, err := json.Marshal(preview{Draft: string(bytes.Repeat([]byte{'x'}, 49<<20))})
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string) *http.Request {
		r := httptest.NewRequest("POST", path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	var decoded preview
	if err = WebAPIDecodeJSONLimit(httptest.NewRecorder(), request("/api/v1/scripts:preview"), &decoded, WebAPIMaxPreviewJSONBytes); err != nil || len(decoded.Draft) != 49<<20 {
		t.Fatal("explicit preview budget was rejected", err)
	}
	decoded = preview{}
	err = WebAPIDecodeJSONLimit(httptest.NewRecorder(), request("/api/v1/scripts:preview"), &decoded, WebAPIMaxContentJSONBytes)
	webAPITestErrorCode(t, err, "body_too_large")
	err = WebAPIDecodeJSONLimit(httptest.NewRecorder(), request("/api/v1/profiles:update"), &decoded, WebAPIMaxPreviewJSONBytes)
	webAPITestErrorCode(t, err, "invalid_body_limit")
	err = WebAPIDecodeJSONLimit(httptest.NewRecorder(), request("/api/v1/scripts:preview"), &decoded, WebAPIMaxPreviewJSONBytes+1)
	webAPITestErrorCode(t, err, "invalid_body_limit")
	err = WebAPIDecodeJSON(httptest.NewRecorder(), request("/api/v1/scripts:preview"), &decoded)
	webAPITestErrorCode(t, err, "body_too_large")
}
