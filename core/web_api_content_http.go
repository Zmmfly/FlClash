//go:build webpanel && !android

package core

import (
	"context"
	"net/http"
	"strconv"
)

func RegisterWebAPIContentRoutes(mux *http.ServeMux, store *WebAPIStore, options WebAPIContentOptions) *WebAPIContentService {
	s := NewWebAPIContentService(store, options)
	s.registerImports(mux)
	s.registerCustomImport(mux)
	s.registerPending(mux)
	s.registerPendingImport(mux)
	s.registerPendingAttachments(mux)
	s.registerProfileAssets(mux)
	s.registerProfileValidation(mux)
	mux.HandleFunc("/api/v1/content/{kind}/{id}", s.readHTTP)
	mux.HandleFunc("/api/v1/content/{kind}/{id}/download", s.readHTTP)
	mux.HandleFunc("/api/v1/rule-providers/{id}/preview", s.previewRuleProvider)
	mux.HandleFunc("/api/v1/content:write", webContentCommand(WebAPIMaxContentJSONBytes, s.Write))
	mux.HandleFunc("/api/v1/content:refresh", webContentCommand(webAPIMaxJSONBytes, s.Refresh))
	mux.HandleFunc("/api/v1/profiles:import", webContentCommand(WebAPIMaxContentJSONBytes, s.Import))
	mux.HandleFunc("/api/v1/profiles:clone", webContentCommand(webAPIMaxJSONBytes, s.Clone))
	mux.HandleFunc("/api/v1/mitm-modules:attach", webContentCommand(WebAPIMaxContentJSONBytes, s.Attach))
	mux.HandleFunc("/api/v1/mitm-modules:detach", webContentCommand(webAPIMaxJSONBytes, s.Detach))
	mux.HandleFunc("/api/v1/mitm-modules:arguments", webContentCommand(webAPIMaxJSONBytes, s.SetArguments))
	mux.HandleFunc("/api/v1/content:decrypt", func(w http.ResponseWriter, r *http.Request) {
		if !webContentPost(w, r) {
			return
		}
		var request WebAPIContentDecrypt
		if err := WebAPIDecodeJSON(w, r, &request); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		revision, result, err := s.Decrypt(r.Context(), request)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteSuccess(w, revision, result)
	})
	return s
}

func webContentPost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		WebAPIWriteError(w, webAPIError(405, "method_not_allowed", "POST is required"))
		return false
	}
	if _, err := WebAPIQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return false
	}
	return true
}

func webContentCommand[T any](limit int64, apply func(context.Context, T) (WebAPIReceipt, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !webContentPost(w, r) {
			return
		}
		var request T
		if err := WebAPIDecodeJSONLimit(w, r, &request, limit); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		receipt, err := apply(r.Context(), request)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteReceipt(w, receipt)
	}
}

func (s *WebAPIContentService) readHTTP(w http.ResponseWriter, r *http.Request) {
	if !webAPIGetMethod(w, r) {
		return
	}
	query, err := WebAPIQuery(r, "resourceKey")
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	key := WebAPIContentKey{Kind: WebAPIContentKind(r.PathValue("kind")), ID: WebAPIID(r.PathValue("id")), Name: query["resourceKey"]}
	revision, result, err := s.Read(r.Context(), key)
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	if len(r.URL.Path) >= 9 && r.URL.Path[len(r.URL.Path)-9:] == "/download" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+string(key.Kind)+"-"+string(key.ID)+`.bin"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(result.Bytes)))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-FlClash-Revision", string(revision))
		_, _ = w.Write(result.Bytes)
		return
	}
	WebAPIWriteSuccess(w, revision, result)
}
