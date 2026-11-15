//go:build webpanel && !android

package core

import (
	"context"
	"strings"
)

func webRuntimeRequiresAdmission(path string) bool {
	if strings.HasPrefix(path, "/api/v1/profiles/") || strings.HasPrefix(path, "/api/v1/runtime/provider-sources") {
		return true
	}
	switch path {
	case "/api/v1/profiles:build", "/api/v1/runtime:apply", "/api/v1/runtime:reconcile", "/api/v1/runtime/proxies:select", "/api/v1/runtime/external-providers":
		return true
	}
	return false
}

func (rt *WebAPIRuntime) acquirePreparation(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, webAPIError(408, "request_canceled", "runtime preparation request was canceled")
	}
	select {
	case rt.preparations <- struct{}{}:
		return func() { <-rt.preparations }, nil
	default:
		return nil, webAPIError(429, "runtime_preparation_busy", "the bounded runtime preparation slots are busy; retry after a preparation completes")
	}
}
