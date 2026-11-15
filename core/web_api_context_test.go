//go:build webpanel && !android

package core

import (
	"context"
	"errors"
	"testing"
)

func TestWebAPIInternalAuthorityDoesNotCreateBrowserSession(t *testing.T) {
	authority := newWebAPIServiceAuthority()
	defer authority.close()
	if !WebAPIInternalAuthorized(authority.ctx) || WebAPIRequestSessionID(authority.ctx) != "" {
		t.Fatal("service authority must be separate from a browser session")
	}
	release, err := WebAPIAcquireCommitAuthorization(authority.ctx)
	if err != nil || release == nil {
		t.Fatalf("internal commit lease: %v", err)
	}
	release()
	calls := 0
	browser := WithWebAPIRequestAuthorization(authority.ctx, "", func(context.Context) (func(), error) {
		calls++
		return func() {}, nil
	})
	if WebAPIInternalAuthorized(browser) {
		t.Fatal("browser context inherited internal authority")
	}
	if _, err := WebAPIAcquireCommitAuthorization(browser); err == nil || calls != 0 {
		t.Fatal("internal marker bypassed an invalid browser authorization")
	}
}

func TestWebAPIInternalAuthorityRejectsMissingCancelledAndClosedLease(t *testing.T) {
	invalid := WithWebAPIInternalAuthorization(context.Background(), nil)
	if WebAPIInternalAuthorized(invalid) {
		t.Fatal("missing callback admitted")
	}
	if _, err := WebAPIAcquireCommitAuthorization(invalid); err == nil {
		t.Fatal("missing callback accepted a commit")
	}
	authority := newWebAPIServiceAuthority()
	ctx, cancel := context.WithCancel(authority.ctx)
	cancel()
	if _, err := WebAPIAcquireCommitAuthorization(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context: %v", err)
	}
	authority.close()
	if WebAPIInternalAuthorized(authority.ctx) {
		t.Fatal("closed authority remains active")
	}
	if _, err := WebAPIAcquireCommitAuthorization(authority.ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed context: %v", err)
	}
}

func TestWebAPIInternalAuthorityCancellationPrecedesLeaseDrain(t *testing.T) {
	authority := newWebAPIServiceAuthority()
	release, err := WebAPIAcquireCommitAuthorization(authority.ctx)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() {
		authority.close()
		close(closed)
	}()
	<-authority.ctx.Done()
	select {
	case <-closed:
		t.Fatal("close did not drain the in-flight commit lease")
	default:
	}
	release()
	<-closed
	if _, err := authority.acquire(context.Background()); err == nil {
		t.Fatal("closed authority admitted a new commit")
	}
}
