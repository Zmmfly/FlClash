//go:build webpanel && !android

package core

import (
	"encoding/json"
	"errors"
	"strconv"
)

type WebAPIID string
type WebAPIRevision string
type WebAPIInt64 string

func webAPIInteger(s string, positive bool) (int64, error) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || strconv.FormatInt(v, 10) != s || (positive && v <= 0) {
		return 0, errors.New("expected a canonical decimal int64 string")
	}
	return v, nil
}

func (v *WebAPIID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if _, err := webAPIInteger(s, true); err != nil {
		return err
	}
	*v = WebAPIID(s)
	return nil
}

func (v *WebAPIRevision) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	n, err := webAPIInteger(s, false)
	if err != nil || n < 0 {
		return errors.New("expected a nonnegative decimal revision string")
	}
	*v = WebAPIRevision(s)
	return nil
}

func (v *WebAPIInt64) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if _, err := webAPIInteger(s, false); err != nil {
		return err
	}
	*v = WebAPIInt64(s)
	return nil
}

type WebAPIMutation struct {
	ExpectedRevision WebAPIRevision `json:"expectedRevision"`
	OperationID      string         `json:"operationId"`
}

type WebAPIError struct {
	Code     string         `json:"code"`
	Message  string         `json:"message"`
	Details  map[string]any `json:"details,omitempty"`
	Status   int            `json:"-"`
	Revision WebAPIRevision `json:"-"`
}

func (e *WebAPIError) Error() string { return e.Code + ": " + e.Message }

func webAPIError(status int, code, message string) *WebAPIError {
	return &WebAPIError{Status: status, Code: code, Message: message}
}

type WebAPIEnvelope[T any] struct {
	Revision    WebAPIRevision `json:"revision"`
	Data        T              `json:"data"`
	OperationID string         `json:"operationId,omitempty"`
}

type WebAPIReceipt struct {
	Revision    WebAPIRevision
	OperationID string
	Body        json.RawMessage
	Replayed    bool
}

type WebAPIStoreMeta struct {
	StoreID       string         `json:"storeId"`
	SchemaVersion int32          `json:"schemaVersion"`
	Revision      WebAPIRevision `json:"revision"`
}

type WebAPICommit struct {
	Revision    WebAPIRevision
	OperationID string
}

type WebAPISubscriptionInfo struct {
	Upload   WebAPIInt64 `json:"upload"`
	Download WebAPIInt64 `json:"download"`
	Total    WebAPIInt64 `json:"total"`
	Expire   WebAPIInt64 `json:"expire"`
}

type WebAPIProfileInput struct {
	Type                     string                  `json:"type"`
	Label                    string                  `json:"label"`
	CurrentGroupName         *string                 `json:"currentGroupName"`
	URL                      string                  `json:"url"`
	AgeSecretKey             string                  `json:"ageSecretKey"`
	ExtendType               string                  `json:"extendType"`
	ScriptID                 *WebAPIID               `json:"scriptId"`
	MatchTarget              *string                 `json:"matchTarget"`
	AutoUpdateDurationMillis int64                   `json:"autoUpdateDurationMillis"`
	AutoUpdate               bool                    `json:"autoUpdate"`
	SelectedMap              map[string]string       `json:"selectedMap"`
	UnfoldSet                []string                `json:"unfoldSet"`
	Overrides                *WebAPIProfileOverrides `json:"overrides"`
	OverridesRawDocument     *string                 `json:"overridesRawDocument,omitempty"`
}

type WebAPIProfile struct {
	ID WebAPIID `json:"id"`
	WebAPIProfileInput
	LastUpdateDate   *string                 `json:"lastUpdateDate"`
	SubscriptionInfo *WebAPISubscriptionInfo `json:"subscriptionInfo"`
	Order            *int32                  `json:"order"`
}

type WebAPIRuleInput struct {
	ProfileID    *WebAPIID `json:"profileId"`
	RuleAction   string    `json:"ruleAction"`
	Content      *string   `json:"content"`
	RuleTarget   *string   `json:"ruleTarget"`
	RuleProvider *string   `json:"ruleProvider"`
	SubRule      *string   `json:"subRule"`
	NoResolve    bool      `json:"noResolve"`
	Src          bool      `json:"src"`
}

type WebAPIRule struct {
	ID WebAPIID `json:"id"`
	WebAPIRuleInput
	Order *string `json:"order"`
}

type WebAPIDisabledRule struct {
	ProfileID WebAPIID `json:"profileId"`
	RuleID    WebAPIID `json:"ruleId"`
}

type WebAPIProfileCreate struct {
	WebAPIMutation
	Profile WebAPIProfileInput `json:"profile"`
}

type WebAPIProfileUpdate struct {
	WebAPIMutation
	ID      WebAPIID           `json:"id"`
	Profile WebAPIProfileInput `json:"profile"`
}

type WebAPIProfileDelete struct {
	WebAPIMutation
	ID WebAPIID `json:"id"`
}

type WebAPIProfileRename struct {
	WebAPIMutation
	ID    WebAPIID `json:"id"`
	Label string   `json:"label"`
}

type WebAPIProfileReorder struct {
	WebAPIMutation
	IDs []WebAPIID `json:"ids"`
}

type WebAPIRuleCreate struct {
	WebAPIMutation
	Rule WebAPIRuleInput `json:"rule"`
}

type WebAPIRuleUpdate struct {
	WebAPIMutation
	ID   WebAPIID        `json:"id"`
	Rule WebAPIRuleInput `json:"rule"`
}

type WebAPIRuleDelete struct {
	WebAPIMutation
	IDs []WebAPIID `json:"ids"`
}

type WebAPIRuleReorder struct {
	WebAPIMutation
	ProfileID *WebAPIID  `json:"profileId"`
	IDs       []WebAPIID `json:"ids"`
}

type WebAPIRuleReplacement struct {
	ID *WebAPIID `json:"id"`
	WebAPIRuleInput
}

type WebAPIRuleReplace struct {
	WebAPIMutation
	ProfileID *WebAPIID               `json:"profileId"`
	Rules     []WebAPIRuleReplacement `json:"rules"`
}

type WebAPIDisabledReplace struct {
	WebAPIMutation
	ProfileID WebAPIID   `json:"profileId"`
	RuleIDs   []WebAPIID `json:"ruleIds"`
}

type WebAPIDeleted struct {
	DeletedIDs []WebAPIID `json:"deletedIds"`
}

type WebAPIDisabledSelection struct {
	ProfileID WebAPIID   `json:"profileId"`
	RuleIDs   []WebAPIID `json:"ruleIds"`
}

type WebAPIProfileResetDialers struct {
	WebAPIMutation
	ID WebAPIID `json:"id"`
}

type WebAPIOperationResolve struct {
	Intent string `json:"intent"`
}

const (
	WebAPICatalogUnreadableOverrides   = "unreadable_overrides"
	WebAPICatalogDependencyQuarantined = "dependency_quarantined"
	WebAPICatalogContentUnavailable    = "content_unavailable"
)

type WebAPIProfileCatalog struct {
	IconURLs          []string            `json:"iconUrls,omitempty"`
	UnavailableReason string              `json:"unavailableReason,omitempty"`
	GroupMembers      map[string][]string `json:"groupMembers"`
	OutboundNames     []string            `json:"outboundNames"`
	RuleProviderNames []string            `json:"ruleProviderNames"`
	SubRuleNames      []string            `json:"subRuleNames"`
}
