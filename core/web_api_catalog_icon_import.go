//go:build webpanel && !android

package core

import (
	"context"
	"net/http"
	"strings"
	"time"
)

type webAPIIconSetImportSource struct {
	URL             string `json:"url"`
	AuthorizationID string `json:"authorizationId,omitempty"`
}

type webAPIIconSetImportCommand struct {
	WebAPIMutation
	ID     *WebAPIID                 `json:"id,omitempty"`
	Name   *string                   `json:"name,omitempty"`
	Source webAPIIconSetImportSource `json:"source"`
}

func webAPIIconSetImportLabel(command webAPIIconSetImportCommand, document webAPIIconSetDocument, previous *WebAPIIconSet) string {
	if previous != nil {
		if command.Name != nil && *command.Name != "" {
			return *command.Name
		}
		return previous.Name
	}
	var proposed string
	if command.Name != nil {
		proposed = *command.Name
	}
	fallback := webAPIIconFileName(command.Source.URL)
	if dot := strings.LastIndexByte(fallback, '.'); dot > 0 {
		fallback = fallback[:dot]
	}
	for _, value := range []string{proposed, document.Name, fallback, "Icon set"} {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return "Icon set"
}

func webAPICommitIconSetImport(tx *WebAPIWriteTx, command webAPIIconSetImportCommand, document webAPIIconSetDocument, now time.Time) (WebAPIIconSet, error) {
	if !webAPICatalogURL(command.Source.URL, false) || len(document.Icons) == 0 {
		return WebAPIIconSet{}, webAPIError(400, "invalid_icon_set_document", "import requires a source URL and usable parsed icons")
	}
	var previous *WebAPIIconSet
	if command.ID != nil {
		stored, err := tx.IconSet(*command.ID)
		if err != nil {
			return WebAPIIconSet{}, err
		}
		previous = &stored
	} else {
		stored, err := webAPIIconSetResource().list(tx.WebAPIReadTx)
		if err != nil {
			return WebAPIIconSet{}, err
		}
		for _, item := range stored {
			if item.URL == command.Source.URL {
				previous = &item
				break
			}
		}
	}
	input := webAPIIconSetInput{Name: webAPIIconSetImportLabel(command, document, previous), URL: command.Source.URL, Icons: document.Icons}
	var result WebAPIIconSet
	var err error
	if previous == nil {
		result, err = webAPICatalogCreate(tx, webAPIIconSetResource(), input)
	} else {
		result, err = webAPICatalogUpdate(tx, webAPIIconSetResource(), previous.ID, input)
	}
	if err != nil {
		return result, err
	}
	updated := now.UTC().Format(time.RFC3339Nano)
	result.LastUpdateTime = &updated
	return result, tx.SaveIconSet(result)
}

func webAPIImportIconSet(ctx context.Context, service *WebAPIContentService, command webAPIIconSetImportCommand, options WebAPISourceFetchOptions) (WebAPIReceipt, error) {
	if receipt, done, err := service.preflight(ctx, command.WebAPIMutation, "icon-sets:import", command); done || err != nil {
		return receipt, err
	}
	if !webAPICatalogURL(command.Source.URL, false) {
		return WebAPIReceipt{}, webAPIError(400, "invalid_source_url", "icon import requires an HTTP(S) source URL")
	}
	var key *WebAPISourceResource
	if command.ID != nil {
		_, err := service.store.View(ctx, func(tx *WebAPIReadTx) error { _, err := tx.IconSet(*command.ID); return err })
		if err != nil {
			return WebAPIReceipt{}, err
		}
		key = &WebAPISourceResource{Kind: "icon-set", ID: string(*command.ID)}
	}
	if command.Source.AuthorizationID != "" {
		grant, err := service.store.SourceGrant(ctx, command.Source.AuthorizationID)
		if err != nil {
			return WebAPIReceipt{}, err
		}
		if grant.ImportJobID != command.OperationID || grant.ExpectedRevision != command.ExpectedRevision {
			return WebAPIReceipt{}, webAPIError(403, "source_import_operation_mismatch", "source import grant is bound to another operation or revision")
		}
	}
	release, err := service.slot(ctx)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	defer release()
	fetched, err := WebAPIFetchSource(ctx, service.store, WebAPISourceRequest{Purpose: WebAPISourceIconSetImport, Key: key, URL: command.Source.URL, AuthorizationID: command.Source.AuthorizationID, ImportJobID: command.OperationID}, options)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	document, err := webAPIParseIconSet(fetched.Bytes)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	return service.store.Transaction(ctx, command.WebAPIMutation, "icon-sets:import", command, func(tx *WebAPIWriteTx) (any, error) {
		return webAPICommitIconSetImport(tx, command, document, service.now())
	})
}

func registerWebAPIIconSetImport(mux *http.ServeMux, service *WebAPIContentService, options WebAPISourceFetchOptions) {
	mux.HandleFunc("/api/v1/icon-sets:import", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			WebAPIWriteError(w, webAPIError(405, "method_not_allowed", "POST is required"))
			return
		}
		if _, err := WebAPIQuery(r); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		var command webAPIIconSetImportCommand
		if err := WebAPIDecodeJSON(w, r, &command); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		receipt, err := webAPIImportIconSet(r.Context(), service, command, options)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteReceipt(w, receipt)
	})
}
