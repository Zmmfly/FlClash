//go:build webpanel && !android

package core

import (
	"context"
	"errors"
	"net/http"
	"time"
)

func webEvaluateAPIError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return webAPIError(http.StatusRequestTimeout, "evaluation_cancelled", "Evaluation was cancelled")
	}
	var fault *webEvaluateFault
	if errors.As(err, &fault) {
		status := http.StatusUnprocessableEntity
		switch fault.Code {
		case "evaluation_busy":
			status = http.StatusTooManyRequests
		case "evaluation_input_limit", "evaluation_output_limit", "evaluation_memory_limit", "evaluation_structure_limit":
			status = http.StatusRequestEntityTooLarge
		case "evaluation_timeout":
			status = http.StatusRequestTimeout
		case "evaluation_cancelled":
			status = http.StatusRequestTimeout
		}
		return webAPIError(status, fault.Code, fault.Message)
	}
	return webRuntimeFailure(err)
}

func (e *WebAPIEvaluator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	preview := r.URL.Path == "/api/v1/scripts:preview"
	if !preview && r.URL.Path != "/api/v1/profiles:evaluate" {
		WebAPIWriteError(w, webAPIError(404, "not_found", "Evaluation operation does not exist"))
		return
	}
	if r.Method != http.MethodPost {
		WebAPIWriteError(w, webAPIError(405, "method_not_allowed", "Evaluation requires POST"))
		return
	}
	if _, err := WebAPIQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	if WebAPIRequestSessionID(r.Context()) == "" {
		WebAPIWriteError(w, webAPIError(401, "unauthorized", "An authenticated evaluation session is required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	release, err := webEvaluateAcquire(ctx)
	if err != nil {
		WebAPIWriteError(w, webEvaluateAPIError(err))
		return
	}
	defer release()
	lease, err := WebAPIAcquireCommitAuthorization(ctx)
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	lease()
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(10 * time.Second))
	var request WebAPIEvaluateRequest
	var draft WebAPIScriptPreviewRequest
	if preview {
		err = WebAPIDecodeJSONLimit(w, r, &draft, WebAPIEvaluatePreviewBodyLimit)
		request = draft.WebAPIEvaluateRequest
	} else {
		err = WebAPIDecodeJSON(w, r, &request)
	}
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	script, input, name, sourceDigest, revision, err := e.source(ctx, request)
	if err != nil {
		WebAPIWriteError(w, webEvaluateAPIError(err))
		return
	}
	if preview {
		if len(draft.ScriptText) > webEvaluateMaxBytes || len(draft.Config) > webEvaluateMaxBytes {
			WebAPIWriteError(w, webAPIError(413, "evaluation_input_limit", "The draft exceeds its evaluation input budget"))
			return
		}
		input, _, err = webEvaluateValidate(ctx, draft.Config)
		if err != nil {
			WebAPIWriteError(w, webEvaluateAPIError(err))
			return
		}
		script = []byte(draft.ScriptText)
	}
	output, err := e.engine.evaluateReserved(ctx, script, input, name)
	if err != nil {
		WebAPIWriteError(w, webEvaluateAPIError(err))
		return
	}
	config, counts, err := webEvaluateValidate(ctx, output.JSON)
	if err != nil {
		WebAPIWriteError(w, webEvaluateAPIError(err))
		return
	}
	result := WebAPIEvaluateResult{
		WebAPIEvaluationSnapshot: WebAPIEvaluationSnapshot{
			ProfileID: request.ProfileID, ScriptID: request.ScriptID, Revision: revision,
			Config: config, SHA256: webEvaluateHash(config), SourceDigest: sourceDigest, ScriptDigest: webEvaluateHash(script),
		},
		Counts: counts, Console: output.Console, Preview: preview,
	}
	lease, err = WebAPIAcquireCommitAuthorization(ctx)
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	func() {
		defer lease()
		if !preview {
			result.EvaluationID, err = e.remember(ctx, result.WebAPIEvaluationSnapshot)
		}
	}()
	if err != nil {
		WebAPIWriteError(w, webEvaluateAPIError(err))
		return
	}
	WebAPIWriteSuccess(w, revision, result)
}
