//go:build webpanel && !android

package core

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type webRuntimeEffectResult struct {
	result  any
	failure *WebAPIError
}
type webRuntimeEffectLifecycle struct {
	mu             sync.Mutex
	closing        bool
	active         int
	cleanup        sync.Once
	transition     atomic.Bool
	needsReconcile atomic.Bool
}

func webRuntimeChangesGeneration(action WebAPIRuntimeAction) bool {
	return action == webRuntimeApply || action == webRuntimeReconcile || action == webRuntimeListener
}

func (rt *WebAPIRuntime) effectTimeout() time.Duration {
	timeout := rt.workspace.options.EffectTimeout
	if timeout <= 0 {
		timeout = 75 * time.Second
	}
	return timeout
}

func (rt *WebAPIRuntime) beginEffect(ctx context.Context, record webRuntimeRecord) (<-chan webRuntimeEffectResult, bool) {
	rt.effect.mu.Lock()
	if rt.effect.closing {
		rt.effect.mu.Unlock()
		return nil, false
	}
	rt.effect.active++
	if webRuntimeChangesGeneration(record.State.Action) {
		rt.effect.transition.Store(true)
	}
	rt.effect.mu.Unlock()
	done := make(chan webRuntimeEffectResult, 1)
	go func() {
		var final webRuntimeEffectResult
		func() {
			defer func() {
				rt.effect.transition.Store(false)
				rt.effect.mu.Lock()
				rt.effect.active--
				cleanup := rt.effect.closing && rt.effect.active == 0
				rt.effect.mu.Unlock()
				if cleanup {
					rt.effect.cleanup.Do(rt.cleanupRuntimeWorkspace)
				}
			}()
			if record.State.Action == webRuntimeRefreshProvider || record.State.Action == webRuntimeGeoUpdate || record.State.Action == webRuntimeWriteProvider || record.State.Action == webRuntimeSideloadProvider {
				final.result, final.failure = rt.applySafely(ctx, record)
			} else {
				rt.backendMu.Lock()
				defer rt.backendMu.Unlock()
				final.result, final.failure = rt.applySafely(ctx, record)
			}
		}()
		done <- final
	}()
	return done, true
}

func (rt *WebAPIRuntime) finishEffect(record webRuntimeRecord, result webRuntimeEffectResult) {
	phase := WebAPIRuntimeApplied
	if result.failure != nil {
		phase = WebAPIRuntimeFailed
		if result.failure.Code == "effect_unknown" {
			phase = WebAPIRuntimeUnknown
		}
	}
	if phase != WebAPIRuntimeApplied {
		rt.markProviderOperationFailure(record, phase)
	}
	if phase == WebAPIRuntimeUnknown && webRuntimeChangesGeneration(record.State.Action) {
		rt.effect.needsReconcile.Store(true)
	}
	if phase == WebAPIRuntimeApplied && (record.State.Action == webRuntimeApply || record.State.Action == webRuntimeReconcile) {
		rt.effect.needsReconcile.Store(false)
	}
	rt.recordAppliedProvenance(record, phase, result.result)
	state, err := rt.store.transitionRuntime(record.State.OperationID, WebAPIRuntimeApplying, phase, result.result, result.failure)
	if err != nil {
		state, _ = rt.store.transitionRuntime(record.State.OperationID, WebAPIRuntimeApplying, WebAPIRuntimeUnknown, nil, webAPIError(500, "effect_unknown", "runtime status could not be durably recorded"))
		if webRuntimeChangesGeneration(record.State.Action) {
			rt.effect.needsReconcile.Store(true)
			rt.applied.clear()
		}
	}
	if state.OperationID != "" {
		rt.publishRuntimeState(state)
	}
}

func (rt *WebAPIRuntime) runEffect(record webRuntimeRecord) bool {
	ctx, cancel := context.WithTimeout(rt.ctx, rt.effectTimeout())
	defer cancel()
	done, started := rt.beginEffect(ctx, record)
	if !started {
		rt.finishEffect(record, webRuntimeEffectResult{failure: webAPIError(409, "effect_unknown", "runtime closed before its accepted effect was confirmed")})
		return false
	}
	select {
	case result := <-done:
		if ctx.Err() != nil {
			rt.finishEffect(record, webRuntimeEffectResult{failure: webAPIError(504, "effect_unknown", "runtime deadline elapsed; late completion is not an application receipt")})
		} else {
			rt.finishEffect(record, result)
		}
		return rt.ctx.Err() == nil
	case <-ctx.Done():
		rt.finishEffect(record, webRuntimeEffectResult{failure: webAPIError(504, "effect_unknown", "runtime execution exceeded its deadline; reconcile its actual state before retrying")})
		select {
		case <-done:
			return rt.ctx.Err() == nil
		case <-rt.ctx.Done():
			return false
		}
	}
}

func (rt *WebAPIRuntime) cleanupRuntimeWorkspace() {
	rt.workspace.close()
	if manager := rt.workspace.options.StatefulManager; manager != nil {
		manager.Close()
	}
}

func (rt *WebAPIRuntime) closeEffects() {
	rt.effect.mu.Lock()
	rt.effect.closing = true
	rt.effect.mu.Unlock()
	rt.cancel()
	rt.Events.Close()
	<-rt.done
	rt.effect.mu.Lock()
	cleanup := rt.effect.active == 0
	rt.effect.mu.Unlock()
	if cleanup {
		rt.effect.cleanup.Do(rt.cleanupRuntimeWorkspace)
	}
}
