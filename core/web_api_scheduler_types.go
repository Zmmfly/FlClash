//go:build webpanel && !android

package core

import (
	"context"
	"sync"
	"time"
)

type WebAPISchedulerKind string

const (
	WebAPISchedulerProfile          WebAPISchedulerKind = "profile"
	WebAPISchedulerScript           WebAPISchedulerKind = "script"
	WebAPISchedulerIconSet          WebAPISchedulerKind = "icon-set"
	WebAPISchedulerRuleProvider     WebAPISchedulerKind = "rule-provider"
	WebAPISchedulerModule           WebAPISchedulerKind = "mitm-module"
	WebAPISchedulerGeo              WebAPISchedulerKind = "geo"
	WebAPISchedulerExternalProvider WebAPISchedulerKind = "external-provider"
)

const webSchedulerMaxTargets = 4096
const webSchedulerBucket = "scheduler-attempts"

type WebAPISchedulerTask struct {
	Mutation          WebAPIMutation
	Kind              WebAPISchedulerKind
	ID                string
	Name              string
	SourceFingerprint string
	MissingContent    bool
}

type WebAPISchedulerEffect struct {
	Revision             WebAPIRevision
	OperationID          string
	Phase                string
	RequiresApply        bool
	ConfirmationRequired bool
}

type WebAPISchedulerCallback func(context.Context, WebAPISchedulerTask) (WebAPISchedulerEffect, error)

type WebAPISchedulerBackend struct {
	RefreshContent          WebAPISchedulerCallback
	RefreshIconSet          WebAPISchedulerCallback
	RefreshModule           WebAPISchedulerCallback
	RefreshGeo              WebAPISchedulerCallback
	RefreshExternalProvider WebAPISchedulerCallback
	Synthetic               bool
}

type WebAPISchedulerClock interface {
	Now() time.Time
	NewTimer(time.Duration) WebAPISchedulerTimer
}

type WebAPISchedulerTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type webSchedulerClock struct{}
type webSchedulerTimer struct{ *time.Timer }

func (webSchedulerClock) Now() time.Time { return time.Now() }
func (webSchedulerClock) NewTimer(d time.Duration) WebAPISchedulerTimer {
	return webSchedulerTimer{time.NewTimer(d)}
}
func (t webSchedulerTimer) C() <-chan time.Time { return t.Timer.C }

type WebAPISchedulerOptions struct {
	InternalContext context.Context
	Clock           WebAPISchedulerClock
	Backend         WebAPISchedulerBackend
	SafeMode        bool
	PollInterval    time.Duration
	TaskTimeout     time.Duration
	BackoffMin      time.Duration
	BackoffMax      time.Duration
	MaxTasksPerRun  int
}

type WebAPISchedulerTarget struct {
	Kind                 WebAPISchedulerKind `json:"kind"`
	ID                   string              `json:"id"`
	Name                 string              `json:"name,omitempty"`
	Enabled              bool                `json:"enabled"`
	MissingContent       bool                `json:"missingContent"`
	DueAt                *time.Time          `json:"dueAt,omitempty"`
	Phase                string              `json:"phase"`
	OperationID          string              `json:"operationId,omitempty"`
	LastSuccess          *time.Time          `json:"lastSuccess,omitempty"`
	NextAttempt          *time.Time          `json:"nextAttempt,omitempty"`
	Failures             int                 `json:"failures"`
	ErrorCode            string              `json:"errorCode,omitempty"`
	RequiresApply        bool                `json:"requiresApply"`
	ConfirmationRequired bool                `json:"confirmationRequired"`
}

type WebAPISchedulerStatus struct {
	FlightID string                  `json:"flightId,omitempty"`
	Running  bool                    `json:"running"`
	Started  bool                    `json:"started"`
	Closed   bool                    `json:"closed"`
	SafeMode bool                    `json:"safeMode"`
	Targets  []WebAPISchedulerTarget `json:"targets"`
}

type WebAPISchedulerTrigger struct {
	WebAPIMutation
	Intent string `json:"intent"`
}

type WebAPISchedulerAccepted struct {
	FlightID  string `json:"flightId"`
	Coalesced bool   `json:"coalesced"`
	Phase     string `json:"phase"`
}

type webSchedulerTarget struct {
	status       WebAPISchedulerTarget
	fingerprint  string
	interval     time.Duration
	periodMillis int64
	lastUpdate   time.Time
	unavailable  string
}

func (t webSchedulerTarget) key() string {
	return string(t.status.Kind) + ":" + t.status.ID + ":" + t.status.Name
}

type webSchedulerSnapshot struct {
	revision WebAPIRevision
	epoch    string
	targets  []webSchedulerTarget
	attempts map[string]webSchedulerAttempt
}

type webSchedulerFlight struct {
	id     string
	done   chan struct{}
	err    error
	ctx    context.Context
	cancel context.CancelFunc
}

type WebAPIScheduler struct {
	store           *WebAPIStore
	options         WebAPISchedulerOptions
	clock           WebAPISchedulerClock
	ctx             context.Context
	cancel          context.CancelFunc
	mu              sync.Mutex
	triggerMu       sync.Mutex
	flight          *webSchedulerFlight
	lastFlight      string
	activeOperation string
	started         bool
	closed          bool
	wake            chan struct{}
	loopDone        chan struct{}
}
