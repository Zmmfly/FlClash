//go:build webpanel && !android

package core

import (
	"context"
	"net/http"
	"strings"
)

func (s *WebAPIDAVService) Restore(ctx context.Context, request WebAPIDAVRestore) (WebAPIReceipt, error) {
	work, release, err := s.begin(ctx, request.OperationID)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	defer release()
	return s.backup.ConfirmDAV(work, request)
}

func RegisterWebAPIDAVRoutes(mux *http.ServeMux, store *WebAPIStore, backup *WebAPIBackupService, options WebAPIDAVOptions) (*WebAPIDAVService, error) {
	s, err := NewWebAPIDAVService(store, backup, options)
	if err != nil {
		return nil, err
	}
	mux.HandleFunc("/api/v1/dav:probe", webContentCommand(webAPIMaxJSONBytes, s.Probe))
	mux.HandleFunc("/api/v1/dav:backup", webContentCommand(webAPIMaxJSONBytes, s.Backup))
	mux.HandleFunc("/api/v1/dav:prepare-restore", webContentCommand(webAPIMaxJSONBytes, s.PrepareRestore))
	mux.HandleFunc("/api/v1/dav:restore", webContentCommand(webAPIMaxJSONBytes, s.Restore))
	mux.HandleFunc("/api/v1/dav/jobs/", s.jobHTTP)
	mux.HandleFunc("/api/v1/dav/jobs", func(w http.ResponseWriter, r *http.Request) {
		if !webAPIGetMethod(w, r) {
			return
		}
		if _, err := WebAPIQuery(r); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		revision, jobs, err := s.Jobs(r.Context())
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteSuccess(w, revision, jobs)
	})
	return s, nil
}

func (s *WebAPIDAVService) jobHTTP(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/dav/jobs/")
	reconcile := strings.HasSuffix(id, ":reconcile")
	if reconcile {
		id = strings.TrimSuffix(id, ":reconcile")
	}
	if !webAPIOperationID(id) {
		WebAPIWriteError(w, webAPIError(400, "invalid_operation_id", "invalid DAV job identity"))
		return
	}
	if reconcile {
		if !webContentPost(w, r) {
			return
		}
		var request WebAPIDAVReconcile
		if err := WebAPIDecodeJSON(w, r, &request); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		receipt, err := s.Reconcile(r.Context(), id, request)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteReceipt(w, receipt)
		return
	}
	if !webAPIGetMethod(w, r) {
		return
	}
	if _, err := WebAPIQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	revision, job, err := s.Job(r.Context(), id)
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	WebAPIWriteSuccess(w, revision, job)
}
