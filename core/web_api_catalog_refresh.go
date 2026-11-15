//go:build webpanel && !android

package core

import (
	"context"
	"net/http"
	"time"
)

type WebAPICatalogOptions struct {
	SourceFetchOptions WebAPISourceFetchOptions
	Now                func() time.Time
}

type webAPIIconSetRefreshCommand struct {
	WebAPIMutation
	ID              WebAPIID `json:"id"`
	AuthorizationID string   `json:"authorizationId,omitempty"`
}

type webAPIIconSetDocument struct {
	Name  string              `json:"name"`
	Icons []WebAPIIconSetIcon `json:"icons"`
}

func webAPIRefreshIconSet(ctx context.Context, service *WebAPIContentService, command webAPIIconSetRefreshCommand, options WebAPISourceFetchOptions) (WebAPIReceipt, error) {
	if receipt, done, err := service.preflight(ctx, command.WebAPIMutation, "icon-sets:refresh", command); done || err != nil {
		return receipt, err
	}
	release, err := service.slot(ctx)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	defer release()
	var previous WebAPIIconSet
	_, err = service.store.View(ctx, func(tx *WebAPIReadTx) error { var err error; previous, err = tx.IconSet(command.ID); return err })
	if err != nil {
		return WebAPIReceipt{}, err
	}
	if previous.URL == "" {
		return WebAPIReceipt{}, webAPIError(409, "resource_not_remote", "icon set has no remote source")
	}
	fetched, err := WebAPIFetchSource(ctx, service.store, WebAPISourceRequest{Purpose: WebAPISourceIconSetRefresh, Key: &WebAPISourceResource{Kind: "icon-set", ID: string(previous.ID)}, URL: previous.URL, AuthorizationID: command.AuthorizationID}, options)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	document, parseErr := webAPIParseIconSet(fetched.Bytes)
	if parseErr != nil {
		return WebAPIReceipt{}, webAPIError(400, "invalid_icon_set_document", "remote source is not a nonempty typed icon gallery")
	}
	return service.store.Transaction(ctx, command.WebAPIMutation, "icon-sets:refresh", command, func(tx *WebAPIWriteTx) (any, error) {
		latest, err := tx.IconSet(command.ID)
		if err != nil {
			return nil, err
		}
		if latest.URL != previous.URL {
			return nil, webAPIError(409, "source_changed", "icon set source changed while it was fetched")
		}
		result, err := webAPICatalogUpdate(tx, webAPIIconSetResource(), latest.ID, webAPIIconSetInput{Name: latest.Name, URL: latest.URL, Icons: document.Icons})
		if err != nil {
			return nil, err
		}
		now := service.now().UTC().Format(time.RFC3339Nano)
		result.LastUpdateTime = &now
		return result, tx.SaveIconSet(result)
	})
}

func registerWebAPIIconSetRefresh(mux *http.ServeMux, store *WebAPIStore, options WebAPICatalogOptions) {
	service := NewWebAPIContentService(store, WebAPIContentOptions{Now: options.Now})
	registerWebAPIIconSetImport(mux, service, options.SourceFetchOptions)
	mux.HandleFunc("/api/v1/icon-sets:refresh", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			WebAPIWriteError(w, webAPIError(405, "method_not_allowed", "POST is required"))
			return
		}
		if _, err := WebAPIQuery(r); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		var command webAPIIconSetRefreshCommand
		if err := WebAPIDecodeJSON(w, r, &command); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		receipt, err := webAPIRefreshIconSet(r.Context(), service, command, options.SourceFetchOptions)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteReceipt(w, receipt)
	})
}
