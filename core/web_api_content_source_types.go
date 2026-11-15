//go:build webpanel && !android

package core

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/netip"
	"time"
)

type WebAPISourcePurpose string

const (
	WebAPISourceProfileImport           WebAPISourcePurpose = "profile-import"
	WebAPISourceScriptImport            WebAPISourcePurpose = "script-import"
	WebAPISourceRuleProviderImport      WebAPISourcePurpose = "rule-provider-import"
	WebAPISourceMitmModuleImport        WebAPISourcePurpose = "mitm-module-import"
	WebAPISourceMitmResourceImport      WebAPISourcePurpose = "mitm-resource-import"
	WebAPISourceIconSetImport           WebAPISourcePurpose = "icon-set-import"
	WebAPISourceProfileRefresh          WebAPISourcePurpose = "profile-refresh"
	WebAPISourceScriptRefresh           WebAPISourcePurpose = "script-refresh"
	WebAPISourceRuleProviderRefresh     WebAPISourcePurpose = "rule-provider-refresh"
	WebAPISourceMitmModuleRefresh       WebAPISourcePurpose = "mitm-module-refresh"
	WebAPISourceMitmResourceRefresh     WebAPISourcePurpose = "mitm-resource-refresh"
	WebAPISourceIconSetRefresh          WebAPISourcePurpose = "icon-set-refresh"
	WebAPISourceExternalProviderRefresh WebAPISourcePurpose = "external-provider-refresh"
	WebAPISourceGeoRefresh              WebAPISourcePurpose = "geo-refresh"
	WebAPISourceAppRelease              WebAPISourcePurpose = "app-release"
	WebAPISourceIPQualityLookup         WebAPISourcePurpose = "ip-quality-lookup"
	WebAPISourceIconImage               WebAPISourcePurpose = "icon-image"
	WebAPISourceDAVProbe                WebAPISourcePurpose = "dav-probe"
	WebAPISourceDAVBackup               WebAPISourcePurpose = "dav-backup"
	WebAPISourceDAVRestore              WebAPISourcePurpose = "dav-restore"
	WebAPISourceCustomProfileImport     WebAPISourcePurpose = "custom-profile-import"
	WebAPISourceCustomProfileDependency WebAPISourcePurpose = "custom-profile-dependency"
	WebAPIMaxGeoSourceBytes                                 = 128 << 20
	WebAPISourcePolicyVersion                               = 1
)

type WebAPISourceResource struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type WebAPISourcePrepare struct {
	ExpectedRevision WebAPIRevision        `json:"expectedRevision"`
	Purpose          WebAPISourcePurpose   `json:"purpose"`
	Key              *WebAPISourceResource `json:"key,omitempty"`
	URL              string                `json:"url,omitempty"`
	ImportJobID      string                `json:"importJobId,omitempty"`
	SourceReference  string                `json:"sourceReference,omitempty"`
}

type WebAPISourceAuthorize struct {
	ExpectedRevision WebAPIRevision `json:"expectedRevision"`
	OperationID      string         `json:"operationId"`
	PreparationID    string         `json:"preparationId"`
	URLDigest        string         `json:"urlDigest"`
	Intent           string         `json:"intent"`
}

type WebAPISourcePreparation struct {
	PreparationID                      string                `json:"preparationId"`
	ExpiresAt                          string                `json:"expiresAt"`
	URLDigest                          string                `json:"urlDigest"`
	Purpose                            WebAPISourcePurpose   `json:"purpose"`
	Key                                *WebAPISourceResource `json:"key,omitempty"`
	NetworkView                        string                `json:"networkView"`
	Scheme                             string                `json:"scheme"`
	Host                               string                `json:"host"`
	Port                               uint16                `json:"port"`
	Addresses                          []string              `json:"addresses"`
	RequiresPrivateNetworkConfirmation bool                  `json:"requiresPrivateNetworkConfirmation"`
}

type WebAPISourceGrant struct {
	ID               string                `json:"id"`
	Purpose          WebAPISourcePurpose   `json:"purpose"`
	Key              *WebAPISourceResource `json:"key,omitempty"`
	ImportJobID      string                `json:"importJobId,omitempty"`
	SourceReference  string                `json:"sourceReference,omitempty"`
	URLDigest        string                `json:"urlDigest"`
	Scheme           string                `json:"scheme"`
	Host             string                `json:"host"`
	Port             uint16                `json:"port"`
	Addresses        []string              `json:"addresses"`
	PolicyVersion    int                   `json:"policyVersion"`
	SessionID        string                `json:"sessionId"`
	ExpectedRevision WebAPIRevision        `json:"expectedRevision"`
	ExpiresAt        string                `json:"expiresAt"`
	RequestDigest    string                `json:"requestDigest,omitempty"`
}

type WebAPISourceAuthorizationReceipt struct {
	Revision    WebAPIRevision
	OperationID string
	Body        json.RawMessage
	Replayed    bool
}

type WebAPISourceAuthorizationResult struct {
	AuthorizationID   string `json:"authorizationId"`
	ExpiresAt         string `json:"expiresAt"`
	ImportOperationID string `json:"importOperationId,omitempty"`
}

type WebAPICommittedSource struct {
	URL              string
	CommittedHeaders map[string][]string
}

type WebAPISourceRequest struct {
	CommittedHeaders map[string][]string `json:"-"`
	Purpose          WebAPISourcePurpose
	Key              *WebAPISourceResource
	URL              string
	AuthorizationID  string
	ImportJobID      string
	SourceReference  string
	RequestDigest    string
}

type WebAPISourceFetchResult struct {
	ContentType  string
	StatusCode   int
	Bytes        []byte
	Subscription *WebAPISubscriptionInfo
}

type WebAPISourceFetchOptions struct {
	ResolveRuntimeSourceIdentity func(context.Context, *WebAPIReadTx, WebAPISourceResource) (WebAPICommittedSource, error)
	TLSRootCAs                   *x509.CertPool
	LookupNetIP                  func(context.Context, string, string) ([]netip.Addr, error)
	DialContext                  func(context.Context, string, string) (net.Conn, error)
	Now                          func() time.Time
}

type WebAPIProspectiveSource struct {
	CommittedHeaders map[string][]string `json:"-"`
	URL              string
	Key              *WebAPISourceResource
	ExpectedRevision WebAPIRevision
	ImportJobID      string
	SourceReference  string
	RequestDigest    string
}
