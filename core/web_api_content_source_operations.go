//go:build webpanel && !android

package core

import (
	"net/http"
	"strings"
)

func (s *WebAPISourceService) sourceOperationHTTP(w http.ResponseWriter, r *http.Request) {
	if _, err := WebAPIQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	operation := strings.TrimPrefix(r.URL.Path, "/api/v1/source-operations/")
	resolve := strings.HasSuffix(operation, ":resolve")
	if resolve {
		operation = strings.TrimSuffix(operation, ":resolve")
	}
	if !webAPIOperationID(operation) {
		WebAPIWriteError(w, webAPIError(400, "invalid_operation_id", "invalid source operation identity"))
		return
	}
	if resolve {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			WebAPIWriteError(w, webAPIError(405, "method_not_allowed", "POST is required"))
			return
		}
		var request struct {
			Intent string `json:"intent"`
		}
		if err := WebAPIDecodeJSON(w, r, &request); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		if request.Intent != "resolve-unknown-result" {
			WebAPIWriteError(w, webAPIError(400, "resolve_intent_required", "explicit unknown-result resolution is required"))
			return
		}
		receipt, err := s.store.ResolveSourceOperation(r.Context(), operation)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		webAPIWriteJSON(w, receipt.Body)
		return
	}
	if !webAPIGetMethod(w, r) {
		return
	}
	receipt, err := s.store.SourceGrantReceipt(r.Context(), operation)
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	webAPIWriteJSON(w, receipt.Body)
}
