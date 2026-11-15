//go:build webpanel && !android

package core

import "net/http"

func RegisterWebAPISchedulerRoutes(mux *http.ServeMux, scheduler *WebAPIScheduler) {
	mux.HandleFunc("/api/v1/scheduler", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			WebAPIWriteError(w, webAPIError(405, "method_not_allowed", "GET is required"))
			return
		}
		if _, err := WebAPIQuery(r); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		revision, status, err := scheduler.Status(r.Context())
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteSuccess(w, revision, status)
	})
	mux.HandleFunc("/api/v1/scheduler:trigger", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			WebAPIWriteError(w, webAPIError(405, "method_not_allowed", "POST is required"))
			return
		}
		if _, err := WebAPIQuery(r); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		var request WebAPISchedulerTrigger
		if err := WebAPIDecodeJSON(w, r, &request); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		receipt, err := scheduler.Trigger(r.Context(), request)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteReceipt(w, receipt)
	})
}
