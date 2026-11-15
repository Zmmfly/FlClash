//go:build webpanel && !android

package core

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

const (
	webAPIHeavyCapacity   = 4
	webAPIControlCapacity = 8
)

func webAPIControlRequest(r *http.Request) bool {
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	if read && (r.URL.Path == "/api/v1/meta" || r.URL.Path == "/api/v1/runtime/status") {
		return true
	}
	for _, prefix := range []string{"/api/v1/operations/", "/api/v1/cache-operations/", "/api/v1/source-operations/", "/api/v1/runtime/operations/"} {
		if !strings.HasPrefix(r.URL.Path, prefix) {
			continue
		}
		id := strings.TrimPrefix(r.URL.Path, prefix)
		if read {
			return webAPIOperationID(id)
		}
		return r.Method == http.MethodPost && strings.HasSuffix(id, ":resolve") && webAPIOperationID(strings.TrimSuffix(id, ":resolve")) && prefix != "/api/v1/runtime/operations/"
	}
	return false
}

func (s *WebServer) acquireAPIAdmission(ctx context.Context, control bool) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, webAPIError(408, "request_cancelled", "The request was cancelled before admission")
	}
	s.apiAdmissionOnce.Do(func() {
		s.apiHeavySlots = make(chan struct{}, webAPIHeavyCapacity)
		s.apiControlSlots = make(chan struct{}, webAPIControlCapacity)
	})
	slots, lane := s.apiHeavySlots, "heavy"
	if control {
		slots, lane = s.apiControlSlots, "control"
	}
	select {
	case slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-slots
			return nil, webAPIError(408, "request_cancelled", "The request was cancelled before admission")
		}
		var once sync.Once
		return func() { once.Do(func() { <-slots }) }, nil
	default:
		err := webAPIError(429, "api_busy", "The API is busy; this request was not accepted")
		err.Details = map[string]any{"lane": lane, "state": "not_accepted"}
		return nil, err
	}
}
