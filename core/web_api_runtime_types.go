//go:build webpanel && !android

package core

import (
	"encoding/json"
	"time"
)

type WebAPIRuntimePhase string

const (
	WebAPIRuntimePending  WebAPIRuntimePhase = "pending"
	WebAPIRuntimeApplying WebAPIRuntimePhase = "applying"
	WebAPIRuntimeApplied  WebAPIRuntimePhase = "applied"
	WebAPIRuntimeFailed   WebAPIRuntimePhase = "failed"
	WebAPIRuntimeUnknown  WebAPIRuntimePhase = "unknown"
)

type WebAPIRuntimeAction string

const (
	webRuntimeSelect              WebAPIRuntimeAction = "proxy-selection"
	webRuntimeCloseConnection     WebAPIRuntimeAction = "close-connection"
	webRuntimeCloseConnections    WebAPIRuntimeAction = "close-connections"
	webRuntimeResetConnections    WebAPIRuntimeAction = "reset-connections"
	webRuntimeResetTraffic        WebAPIRuntimeAction = "reset-traffic"
	webRuntimeClearDNS            WebAPIRuntimeAction = "clear-dns-cache"
	webRuntimeNetworkChanged      WebAPIRuntimeAction = "network-changed"
	webRuntimeListener            WebAPIRuntimeAction = "listener-state"
	webRuntimeGC                  WebAPIRuntimeAction = "collect-memory"
	webRuntimeGeoUpdate           WebAPIRuntimeAction = "geo-update"
	webRuntimeGenerateCertificate WebAPIRuntimeAction = "mitm-generate-certificate"
	webRuntimeRunScript           WebAPIRuntimeAction = "mitm-run-script"
	webRuntimeApply               WebAPIRuntimeAction = "apply-configuration"
	webRuntimeReconcile           WebAPIRuntimeAction = "reconcile-settings"
)

type WebAPIRuntimeOperation struct {
	OperationID     string              `json:"operationId"`
	DesiredRevision WebAPIRevision      `json:"desiredRevision"`
	Action          WebAPIRuntimeAction `json:"action"`
	Phase           WebAPIRuntimePhase  `json:"phase"`
	Result          json.RawMessage     `json:"result,omitempty"`
	Error           *WebAPIError        `json:"error,omitempty"`
	UpdatedAt       time.Time           `json:"updatedAt"`
}

type WebAPIRuntimeCoreState struct {
	Initialized       bool           `json:"initialized"`
	ListenerEnabled   bool           `json:"listenerEnabled"`
	SafeMode          bool           `json:"safeMode"`
	MITM              bool           `json:"mitm"`
	StartTimeMillis   int64          `json:"startTimeMs"`
	RuntimeGeneration WebAPIRevision `json:"runtimeGeneration"`
	ProfileID         *WebAPIID      `json:"profileId"`
}

type WebAPIRuntimeNetworkInterface struct {
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
}

type WebAPIRuntimeSelectRequest struct {
	WebAPIMutation
	GroupName         string         `json:"groupName"`
	ProxyName         *string        `json:"proxyName"`
	RuntimeGeneration WebAPIRevision `json:"runtimeGeneration"`
}

type WebAPIRuntimeDelayRequest struct {
	ProxyName     string `json:"proxyName"`
	TestURL       string `json:"testUrl"`
	TimeoutMillis int64  `json:"timeoutMillis"`
}

type WebAPIRuntimeConvertRequest struct {
	Content string `json:"content"`
}
type WebAPIRuntimeCloseRequest struct {
	WebAPIMutation
	ID string `json:"id"`
}
type WebAPIRuntimeListenerRequest struct {
	WebAPIMutation
	Enabled *bool `json:"enabled"`
}
type WebAPIRuntimeGeoRequest struct {
	WebAPIMutation
	Type            string                  `json:"type"`
	SourceID        WebAPIRuntimeProviderID `json:"sourceId,omitempty"`
	AuthorizationID string                  `json:"authorizationId,omitempty"`
}
type WebAPIRuntimeOutboundIPRequest struct {
	ProxyName     string   `json:"proxyName"`
	URLs          []string `json:"urls"`
	TimeoutMillis int64    `json:"timeoutMillis"`
}
type WebAPIRuntimeServicesRequest struct {
	ProxyName     string   `json:"proxyName"`
	Names         []string `json:"names"`
	TimeoutMillis int64    `json:"timeoutMillis"`
}
type WebAPIRuntimeScriptRequest struct {
	WebAPIMutation
	ScriptName string `json:"scriptName"`
}
type WebAPIRuntimeIssueRequest struct {
	Host string `json:"host"`
}

type WebAPIRuntimeApplyRequest struct {
	WebAPIMutation
	ProfileID        *WebAPIID                     `json:"profileId"`
	Overlay          *string                       `json:"overlay,omitempty"`
	CandidateYAML    *string                       `json:"candidateYaml,omitempty"`
	EvaluationID     string                        `json:"evaluationId,omitempty"`
	SelectedMap      map[string]string             `json:"selectedMap"`
	Options          CoreOptions                   `json:"options"`
	DialerProxies    map[string]string             `json:"dialerProxies,omitempty"`
	ResourceBindings WebAPIRuntimeResourceBindings `json:"resourceBindings,omitempty"`
}

type WebAPIRuntimeGeoResource struct {
	Type       string                  `json:"type"`
	Updating   bool                    `json:"updating"`
	SourceID   WebAPIRuntimeProviderID `json:"sourceId"`
	Available  bool                    `json:"available"`
	Size       *int64                  `json:"size"`
	ModifiedAt *time.Time              `json:"modifiedAt"`
}

type WebAPIRuntimeGeoMetadata struct {
	Available  bool
	Size       *int64
	ModifiedAt *time.Time
}

type WebAPIRuntimeLogState struct {
	Subscribed bool `json:"subscribed"`
}

type WebAPIRuntimeMITMRules struct {
	Hostname      []string `json:"hostname"`
	URLRewrite    []string `json:"url-rewrite"`
	HeaderRewrite []string `json:"header-rewrite"`
	BodyRewrite   []string `json:"body-rewrite"`
	MapLocal      []string `json:"map-local"`
	Script        []string `json:"script"`
}
type WebAPIRuntimeMITMCertificate struct {
	PEM         string    `json:"pem"`
	Fingerprint string    `json:"fingerprint"`
	Subject     string    `json:"subject"`
	NotBefore   time.Time `json:"not-before"`
	NotAfter    time.Time `json:"not-after"`
}
type WebAPIRuntimeMITMScriptOutput struct {
	Logs   []string `json:"logs"`
	Result string   `json:"result"`
	Error  string   `json:"error"`
}
type WebAPIRuntimeMITMExchangeSummary struct {
	ID           WebAPIID  `json:"id"`
	Start        time.Time `json:"start"`
	Pending      bool      `json:"pending"`
	Duration     int64     `json:"duration"`
	Method       string    `json:"method"`
	URL          string    `json:"url"`
	Status       int       `json:"status"`
	RequestSize  int64     `json:"request-size"`
	ResponseSize int64     `json:"response-size"`
	Error        string    `json:"error"`
}
type WebAPIRuntimeMITMExchangeList struct {
	Capturing bool                               `json:"capturing"`
	Since     *time.Time                         `json:"since"`
	Exchanges []WebAPIRuntimeMITMExchangeSummary `json:"exchanges"`
	Closed    bool                               `json:"closed"`
}
type WebAPIRuntimeMITMAction struct {
	Kind   string `json:"kind"`
	Phase  string `json:"phase"`
	Detail string `json:"detail"`
}
type WebAPIRuntimeMITMExchange struct {
	WebAPIRuntimeMITMExchangeSummary
	TTFB           int64                     `json:"ttfb"`
	OriginalURL    string                    `json:"original-url"`
	Proto          string                    `json:"proto"`
	Connection     string                    `json:"connection"`
	RequestHeader  map[string][]string       `json:"request-header"`
	ResponseHeader map[string][]string       `json:"response-header"`
	Actions        []WebAPIRuntimeMITMAction `json:"actions"`
}
type WebAPIRuntimeMITMBody struct {
	Data      string `json:"data"`
	Truncated bool   `json:"truncated"`
	Encoding  string `json:"encoding"`
}
