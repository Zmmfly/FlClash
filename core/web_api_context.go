//go:build webpanel && !android

package core

import (
	"context"
	"sync"
)

type webAPIRequestAuthorizationKey struct{}
type webAPIInternalAuthorizationKey struct{}

type webAPIInternalAuthorization struct {
	acquire func(context.Context) (func(), error)
}

type webAPIRequestAuthorization struct {
	sessionID string
	acquire   func(context.Context) (func(), error)
}

func WithWebAPIRequestAuthorization(ctx context.Context, sessionID string, acquire func(context.Context) (func(), error)) context.Context {
	return context.WithValue(ctx, webAPIRequestAuthorizationKey{}, webAPIRequestAuthorization{
		sessionID: sessionID,
		acquire:   acquire,
	})
}

func WebAPIRequestSessionID(ctx context.Context) string {
	authorization, _ := ctx.Value(webAPIRequestAuthorizationKey{}).(webAPIRequestAuthorization)
	return authorization.sessionID
}

func WithWebAPIInternalAuthorization(ctx context.Context, acquire func(context.Context) (func(), error)) context.Context {
	return context.WithValue(ctx, webAPIInternalAuthorizationKey{}, webAPIInternalAuthorization{acquire: acquire})
}

func WebAPIInternalAuthorized(ctx context.Context) bool {
	_, browser := ctx.Value(webAPIRequestAuthorizationKey{}).(webAPIRequestAuthorization)
	authorization, ok := ctx.Value(webAPIInternalAuthorizationKey{}).(webAPIInternalAuthorization)
	return !browser && ok && authorization.acquire != nil && ctx.Err() == nil
}

func WebAPIAcquireCommitAuthorization(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	authorization, ok := ctx.Value(webAPIRequestAuthorizationKey{}).(webAPIRequestAuthorization)
	if !ok {
		internal, supplied := ctx.Value(webAPIInternalAuthorizationKey{}).(webAPIInternalAuthorization)
		if !supplied {
			return func() {}, nil
		}
		if internal.acquire == nil {
			return nil, webAPIError(503, "service_unavailable", "The internal service is no longer authorized")
		}
		authorization.acquire = internal.acquire
	} else if authorization.sessionID == "" || authorization.acquire == nil {
		return nil, webAPIError(401, "unauthorized", "The session is no longer authorized")
	}
	release, err := authorization.acquire(ctx)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, webAPIError(401, "unauthorized", "The session is no longer authorized")
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

type webAPIServiceAuthority struct {
	mu     sync.RWMutex
	closed bool
	ctx    context.Context
	cancel context.CancelFunc
}

func newWebAPIServiceAuthority() *webAPIServiceAuthority {
	ctx, cancel := context.WithCancel(context.Background())
	authority := &webAPIServiceAuthority{cancel: cancel}
	authority.ctx = WithWebAPIInternalAuthorization(ctx, authority.acquire)
	return authority
}

func (a *webAPIServiceAuthority) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.RLock()
	if a.closed || a.ctx.Err() != nil {
		a.mu.RUnlock()
		return nil, webAPIError(503, "service_unavailable", "The internal service is no longer authorized")
	}
	if err := ctx.Err(); err != nil {
		a.mu.RUnlock()
		return nil, err
	}
	return a.mu.RUnlock, nil
}

func (a *webAPIServiceAuthority) close() {
	a.cancel()
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
}
