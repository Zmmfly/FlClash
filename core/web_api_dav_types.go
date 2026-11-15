//go:build webpanel && !android

package core

import (
	"context"
	"crypto/x509"
	"net"
	"net/netip"
	"time"
)

type WebAPIDAVAction string
type WebAPIDAVPhase string

const (
	WebAPIDAVProbeAction          WebAPIDAVAction = "probe"
	WebAPIDAVBackupAction         WebAPIDAVAction = "backup"
	WebAPIDAVPrepareRestoreAction WebAPIDAVAction = "prepare-restore"
	WebAPIDAVRestoreAction        WebAPIDAVAction = "restore"
	WebAPIDAVReconcileAction      WebAPIDAVAction = "reconcile"
	WebAPIDAVPending              WebAPIDAVPhase  = "pending"
	WebAPIDAVApplying             WebAPIDAVPhase  = "applying"
	WebAPIDAVPrepared             WebAPIDAVPhase  = "prepared"
	WebAPIDAVSucceeded            WebAPIDAVPhase  = "succeeded"
	WebAPIDAVFailed               WebAPIDAVPhase  = "failed"
	WebAPIDAVUnknown              WebAPIDAVPhase  = "unknown"
	WebAPIDAVMaxJobs                              = 256
	WebAPIDAVMaxConcurrent                        = 4
	WebAPIDAVMaxArchiveBytes                      = 32 << 20
)

type WebAPIDAVProbe struct {
	WebAPIMutation
	Intent          string `json:"intent"`
	AuthorizationID string `json:"authorizationId,omitempty"`
}

type WebAPIDAVBackup struct {
	WebAPIMutation
	Intent                string `json:"intent"`
	AuthorizationID       string `json:"authorizationId,omitempty"`
	Format                string `json:"format"`
	IncludeDAVCredentials bool   `json:"includeDavCredentials"`
	ResolutionOperationID string `json:"resolutionOperationId,omitempty"`
}

type WebAPIDAVPrepareRestore struct {
	WebAPIMutation
	Intent                string `json:"intent"`
	AuthorizationID       string `json:"authorizationId,omitempty"`
	Strategy              string `json:"strategy"`
	OnlyProfiles          bool   `json:"onlyProfiles"`
	RestoreDAVCredentials bool   `json:"restoreDavCredentials"`
}

type WebAPIDAVRestore struct {
	WebAPIMutation
	Intent                 string `json:"intent"`
	PreparationOperationID string `json:"preparationOperationId"`
	Token                  string `json:"token"`
	ArchiveDigest          string `json:"archiveDigest"`
	PlanDigest             string `json:"planDigest"`
	RestoreDAVCredentials  bool   `json:"restoreDavCredentials"`
}

type WebAPIDAVReconcile struct {
	WebAPIMutation
	Intent          string `json:"intent"`
	AuthorizationID string `json:"authorizationId,omitempty"`
}

type WebAPIDAVResult struct {
	Reachable        bool                     `json:"reachable,omitempty"`
	ETag             string                   `json:"etag,omitempty"`
	Conditional      bool                     `json:"conditional"`
	ArchiveDigest    string                   `json:"archiveDigest,omitempty"`
	ArchiveSize      int                      `json:"archiveSize,omitempty"`
	Preparation      *WebAPIBackupPreparation `json:"preparation,omitempty"`
	Restored         *WebAPIBackupResult      `json:"restored,omitempty"`
	ReconciliationOf string                   `json:"reconciliationOf,omitempty"`
	RemoteMatches    bool                     `json:"remoteMatches,omitempty"`
}

type WebAPIDAVJob struct {
	OperationID     string           `json:"operationId"`
	DesiredRevision WebAPIRevision   `json:"desiredRevision"`
	Action          WebAPIDAVAction  `json:"action"`
	Phase           WebAPIDAVPhase   `json:"phase"`
	Stage           string           `json:"stage"`
	UpdatedAt       time.Time        `json:"updatedAt"`
	Result          *WebAPIDAVResult `json:"result,omitempty"`
	Error           *WebAPIError     `json:"error,omitempty"`
}

// Private durable intent retains identities, never the tuple or archive plaintext.
type WebAPIDAVDesired struct {
	TupleDigest           string         `json:"tupleDigest"`
	AuthorizationID       string         `json:"authorizationId,omitempty"`
	Format                string         `json:"format,omitempty"`
	IncludeDAVCredentials bool           `json:"includeDavCredentials,omitempty"`
	ArchiveDigest         string         `json:"archiveDigest,omitempty"`
	ArchiveSize           int            `json:"archiveSize,omitempty"`
	Strategy              string         `json:"strategy,omitempty"`
	OnlyProfiles          bool           `json:"onlyProfiles,omitempty"`
	RestoreDAVCredentials bool           `json:"restoreDavCredentials,omitempty"`
	TargetOperationID     string         `json:"targetOperationId,omitempty"`
	ResolutionOperationID string         `json:"resolutionOperationId,omitempty"`
	TargetDesiredRevision WebAPIRevision `json:"targetDesiredRevision,omitempty"`
}

type WebAPIDAVJobSummary struct {
	OperationID         string          `json:"operationId"`
	Action              WebAPIDAVAction `json:"action"`
	Phase               WebAPIDAVPhase  `json:"phase"`
	Stage               string          `json:"stage"`
	ErrorCode           string          `json:"errorCode,omitempty"`
	UpdatedAt           time.Time       `json:"updatedAt"`
	CurrentTupleMatches bool            `json:"currentTupleMatches"`
	NeedsPrepare        bool            `json:"needsPrepare"`
}

type WebAPIDAVRecord struct {
	Job       WebAPIDAVJob     `json:"job"`
	SessionID string           `json:"sessionId"`
	Desired   WebAPIDAVDesired `json:"desired"`
}

type WebAPIDAVOptions struct {
	StatusContext context.Context
	TLSRootCAs    *x509.CertPool
	LookupNetIP   func(context.Context, string, string) ([]netip.Addr, error)
	DialContext   func(context.Context, string, string) (net.Conn, error)
	Now           func() time.Time
}
