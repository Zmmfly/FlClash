//go:build webpanel && !android

package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	bolt "github.com/metacubex/bbolt"
)

func NewWebAPIScheduler(store *WebAPIStore, options WebAPISchedulerOptions) (*WebAPIScheduler, error) {
	if store == nil {
		return nil, errors.New("scheduler store is required")
	}
	if options.Clock == nil {
		options.Clock = webSchedulerClock{}
	}
	if options.PollInterval == 0 {
		options.PollInterval = time.Minute
	}
	if options.TaskTimeout == 0 {
		options.TaskTimeout = 90 * time.Second
	}
	if options.BackoffMin == 0 {
		options.BackoffMin = time.Minute
	}
	if options.BackoffMax == 0 {
		options.BackoffMax = time.Hour
	}
	if options.MaxTasksPerRun == 0 {
		options.MaxTasksPerRun = 64
	}
	if options.PollInterval < time.Second || options.TaskTimeout < time.Second || options.BackoffMin < time.Second || options.BackoffMax < options.BackoffMin || options.MaxTasksPerRun < 1 || options.MaxTasksPerRun > 256 {
		return nil, errors.New("invalid scheduler bounds")
	}
	base := options.InternalContext
	if base == nil {
		base = context.Background()
	}
	if WebAPIRequestSessionID(base) != "" {
		return nil, errors.New("scheduler cannot retain a browser session")
	}
	ctx, cancel := context.WithCancel(base)
	return &WebAPIScheduler{store: store, options: options, clock: options.Clock, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), loopDone: make(chan struct{})}, nil
}

func (s *WebAPIScheduler) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return webAPIError(503, "scheduler_closed", "scheduler is closed")
	}
	if s.started {
		return nil
	}
	if s.options.SafeMode && !s.options.Backend.Synthetic {
		return nil
	}
	if !WebAPIInternalAuthorized(s.ctx) {
		return webAPIError(403, "background_authority_required", "scheduler requires service-owned authority")
	}
	s.started = true
	go s.loop()
	return nil
}

func (s *WebAPIScheduler) Notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *WebAPIScheduler) loop() {
	defer func() {
		if recover() != nil {
			s.cancel()
		}
		close(s.loopDone)
	}()
	s.Notify()
	for {
		timer := s.clock.NewTimer(s.options.PollInterval)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
		case <-timer.C():
		}
		timer.Stop()
		s.triggerMu.Lock()
		flight, fresh, err := s.reserveFlight()
		if err == nil && fresh {
			s.launch(flight)
		}
		s.triggerMu.Unlock()
	}
}

func webSchedulerID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "scheduler-" + hex.EncodeToString(value[:]), nil
}

func (s *WebAPIScheduler) reserveFlight() (*webSchedulerFlight, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return nil, false, webAPIError(503, "scheduler_closed", "scheduler is closed")
	}
	if s.flight != nil {
		return s.flight, false, nil
	}
	id, err := webSchedulerID()
	if err != nil {
		return nil, false, err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	f := &webSchedulerFlight{id: id, done: make(chan struct{}), ctx: ctx, cancel: cancel}
	s.flight = f
	s.lastFlight = id
	return f, true, nil
}

func (s *WebAPIScheduler) launch(f *webSchedulerFlight) {
	go func() {
		defer func() {
			if recover() != nil {
				f.err = webAPIError(503, "effect_unknown", "scheduler was interrupted")
			}
			f.cancel()
			s.mu.Lock()
			if s.flight == f {
				s.flight = nil
				s.activeOperation = ""
			}
			close(f.done)
			s.mu.Unlock()
		}()
		f.err = s.run(f.ctx)
	}()
}

func (s *WebAPIScheduler) abandon(f *webSchedulerFlight, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f.err = err
	f.cancel()
	if s.flight == f {
		s.flight = nil
	}
	close(f.done)
}

func (s *WebAPIScheduler) RunOnce(ctx context.Context) error {
	s.triggerMu.Lock()
	f, fresh, err := s.reserveFlight()
	if err == nil && fresh {
		s.launch(f)
	}
	s.triggerMu.Unlock()
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.done:
		return f.err
	}
}

func (s *WebAPIScheduler) CancelFlight() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.flight == nil {
		return false
	}
	s.flight.cancel()
	return true
}

func (s *WebAPIScheduler) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
}

func (s *WebAPIScheduler) Wait(ctx context.Context) error {
	s.mu.Lock()
	f := s.flight
	waitLoop := s.closed && s.started
	s.mu.Unlock()
	if waitLoop {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.loopDone:
		}
	}
	if f == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.done:
		return f.err
	}
}

func (s *WebAPIScheduler) callback(kind WebAPISchedulerKind) WebAPISchedulerCallback {
	b := s.options.Backend
	switch kind {
	case WebAPISchedulerProfile, WebAPISchedulerScript, WebAPISchedulerRuleProvider:
		return b.RefreshContent
	case WebAPISchedulerIconSet:
		return b.RefreshIconSet
	case WebAPISchedulerModule:
		return b.RefreshModule
	case WebAPISchedulerGeo:
		return b.RefreshGeo
	case WebAPISchedulerExternalProvider:
		return b.RefreshExternalProvider
	}
	return nil
}

func (s *WebAPIScheduler) run(ctx context.Context) error {
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return err
	}
	if s.options.SafeMode && !s.options.Backend.Synthetic {
		return nil
	}
	if !WebAPIInternalAuthorized(ctx) {
		return webAPIError(403, "background_authority_required", "scheduler requires service-owned authority")
	}
	if err = s.recoverAttempts(ctx, snapshot); err != nil {
		return err
	}
	expected := snapshot.revision
	attempted := 0
	for _, target := range snapshot.targets {
		if target.status.Phase != "due" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if attempted >= s.options.MaxTasksPerRun {
			break
		}
		attempted++
		id, err := webSchedulerID()
		if err != nil {
			return err
		}
		task := WebAPISchedulerTask{Mutation: WebAPIMutation{ExpectedRevision: expected, OperationID: id}, Kind: target.status.Kind, ID: target.status.ID, Name: target.status.Name, SourceFingerprint: target.fingerprint, MissingContent: target.status.MissingContent}
		previous := snapshot.attempts[target.key()]
		if previous.Fingerprint != target.fingerprint {
			previous = webSchedulerAttempt{}
		}
		record := previous
		record.Epoch = snapshot.epoch
		record.Fingerprint, record.OperationID, record.ExpectedRevision = target.fingerprint, id, expected
		record.Phase, record.ErrorCode, record.LastAttempt, record.NextAttempt = "inflight", "", s.clock.Now().UTC(), time.Time{}
		if err = s.beginAttempt(ctx, target.key(), record); err != nil {
			return err
		}
		s.mu.Lock()
		s.activeOperation = id
		s.mu.Unlock()
		effect, callbackErr := s.execute(ctx, task)
		s.mu.Lock()
		s.activeOperation = ""
		s.mu.Unlock()
		if callbackErr == nil && !webSchedulerEffectValid(task, effect) {
			callbackErr = webAPIError(503, "effect_unknown", "background callback did not return a confirmed typed effect")
		}
		record = s.finishRecord(record, effect, callbackErr)
		if callbackErr == nil {
			after, readErr := s.snapshot(ctx)
			if readErr != nil {
				return readErr
			}
			if after.revision == effect.Revision {
				for _, current := range after.targets {
					if current.key() == target.key() {
						record.Fingerprint = current.fingerprint
						break
					}
				}
			}
		}
		if err = s.saveAttempt(ctx, target.key(), record); err != nil {
			return err
		}
		if record.Phase == "unknown" {
			return webAPIError(409, "effect_unknown", "background effect needs explicit reconciliation")
		}
		if callbackErr != nil {
			var api *WebAPIError
			if errors.As(callbackErr, &api) && (api.Code == "revision_conflict" || api.Code == "source_target_changed") {
				return callbackErr
			}
			if effect.Revision != "" && effect.Revision != expected {
				return callbackErr
			}
			continue
		}
		expected = effect.Revision
	}
	return nil
}

func (s *WebAPIScheduler) execute(ctx context.Context, task WebAPISchedulerTask) (effect WebAPISchedulerEffect, err error) {
	callback := s.callback(task.Kind)
	if callback == nil {
		return effect, webAPIError(501, "background_unavailable", "typed background service is not attached")
	}
	defer func() {
		if recover() != nil {
			err = webAPIError(503, "effect_unknown", "background callback was interrupted")
		}
	}()
	taskContext, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := s.clock.NewTimer(s.options.TaskTimeout)
	defer timer.Stop()
	done := make(chan struct{})
	defer close(done)
	go func() {
		defer func() {
			if recover() != nil {
				cancel()
			}
		}()
		select {
		case <-done:
		case <-taskContext.Done():
		case <-timer.C():
			cancel()
		}
	}()
	effect, err = callback(taskContext, task)
	if taskContext.Err() != nil {
		return effect, webAPIError(409, "effect_unknown", "background cancellation needs explicit reconciliation")
	}
	return effect, err
}

func webSchedulerEffectValid(task WebAPISchedulerTask, effect WebAPISchedulerEffect) bool {
	before, e1 := webAPIInteger(string(task.Mutation.ExpectedRevision), false)
	after, e2 := webAPIInteger(string(effect.Revision), false)
	return e1 == nil && e2 == nil && after >= before && after-before <= 1 && effect.OperationID == task.Mutation.OperationID && effect.Phase == "completed"
}

func (s *WebAPIScheduler) finishRecord(record webSchedulerAttempt, effect WebAPISchedulerEffect, err error) webSchedulerAttempt {
	if err == nil {
		record.Phase, record.LastSuccess, record.NextAttempt, record.Failures = "completed", s.clock.Now().UTC(), time.Time{}, 0
		record.RequiresApply, record.ConfirmationRequired = effect.RequiresApply, effect.ConfirmationRequired
		return record
	}
	record.Failures++
	if record.Failures > 20 {
		record.Failures = 20
	}
	record.ErrorCode, record.Phase = "effect_unknown", "unknown"
	var api *WebAPIError
	if errors.As(err, &api) {
		record.ErrorCode = api.Code
		if api.Code != "effect_unknown" && api.Code != "durability_unknown" {
			record.Phase = "failed"
			delay := s.options.BackoffMin
			for n := 1; n < record.Failures && delay < s.options.BackoffMax; n++ {
				if delay > s.options.BackoffMax/2 {
					delay = s.options.BackoffMax
				} else {
					delay *= 2
				}
			}
			record.NextAttempt = s.clock.Now().UTC().Add(delay)
			switch api.Code {
			case "source_authorization_required", "source_authorization_invalid", "source_redirect_authorization_required", "source_address_forbidden", "source_target_changed":
				record.Phase, record.NextAttempt = "blocked", time.Time{}
			}
		}
	}
	return record
}

func (s *WebAPIScheduler) Status(ctx context.Context) (WebAPIRevision, WebAPISchedulerStatus, error) {
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return snapshot.revision, WebAPISchedulerStatus{}, err
	}
	s.mu.Lock()
	status := WebAPISchedulerStatus{FlightID: s.lastFlight, Running: s.flight != nil, Started: s.started, Closed: s.closed, SafeMode: s.options.SafeMode, Targets: []WebAPISchedulerTarget{}}
	active := s.activeOperation
	s.mu.Unlock()
	for _, target := range snapshot.targets {
		v := target.status
		if active != "" && v.OperationID == active {
			v.Phase = "inflight"
			v.ErrorCode = ""
		}
		status.Targets = append(status.Targets, v)
	}
	return snapshot.revision, status, nil
}

func (s *WebAPIScheduler) Trigger(ctx context.Context, request WebAPISchedulerTrigger) (WebAPIReceipt, error) {
	if request.Intent != "run-due-updates" {
		return WebAPIReceipt{}, webAPIError(400, "scheduler_intent_required", "explicit due-update trigger intent is required")
	}
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	flight, fresh, err := s.reserveFlight()
	if err != nil {
		return WebAPIReceipt{}, err
	}
	receipt, err := s.store.Transaction(ctx, request.WebAPIMutation, "scheduler:trigger", request, func(*WebAPIWriteTx) (any, error) {
		return WebAPISchedulerAccepted{FlightID: flight.id, Coalesced: !fresh, Phase: "accepted"}, nil
	})
	if err != nil || receipt.Replayed {
		if fresh {
			s.abandon(flight, err)
		}
		return receipt, err
	}
	if fresh {
		s.launch(flight)
	}
	return receipt, nil
}

func (s *WebAPIScheduler) beginAttempt(ctx context.Context, key string, record webSchedulerAttempt) error {
	return s.store.updateRuntime(ctx, func(tx *bolt.Tx) error {
		if (&WebAPIReadTx{tx}).SchedulerEpoch() != record.Epoch {
			return webAPIError(409, "scheduler_epoch_changed", "application data changed before background fetch")
		}
		revision, err := webAPIRevision(tx, "meta")
		if err != nil {
			return err
		}
		if revision != record.ExpectedRevision {
			e := webAPIError(409, "revision_conflict", "committed inputs changed before background fetch")
			e.Revision = revision
			return e
		}
		bucket, err := tx.CreateBucketIfNotExists([]byte(webSchedulerBucket))
		if err != nil {
			return err
		}
		if raw := bucket.Get([]byte(key)); raw != nil {
			var prior webSchedulerAttempt
			if err = json.Unmarshal(raw, &prior); err != nil {
				return err
			}
			if prior.Fingerprint == record.Fingerprint && (prior.Phase == "inflight" || prior.Phase == "unknown") {
				return webAPIError(409, "scheduler_target_busy", "background target is already claimed or unresolved")
			}
		} else if bucket.Stats().KeyN >= webSchedulerMaxTargets {
			return webAPIError(413, "scheduler_target_limit", "scheduler attempt budget exceeded")
		}
		return webAPIPut(tx, webSchedulerBucket, key, record)
	})
}
