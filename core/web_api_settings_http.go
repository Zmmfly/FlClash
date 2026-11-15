//go:build webpanel && !android

package core

import "net/http"

func PublicWebAPISettings(settings WebAPISettings) WebAPISettings {
	settings.NativePresentation = nil
	return settings
}

func RegisterWebAPISettingsRoutes(mux *http.ServeMux, store *WebAPIStore) {
	mux.HandleFunc("/api/v1/settings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			WebAPIWriteError(w, webAPIError(405, "method_not_allowed", "GET is required"))
			return
		}
		if _, err := WebAPIQuery(r); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		var settings WebAPISettings
		revision, err := store.View(r.Context(), func(tx *WebAPIReadTx) error {
			var err error
			settings, err = tx.Settings()
			return err
		})
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteSuccess(w, revision, PublicWebAPISettings(settings))
	})
	mux.HandleFunc("/api/v1/settings:runtime", webAPIHTTPCommand(store, "settings:runtime", func(c *WebAPISettingsRuntimeCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *WebAPISettingsRuntimeCommand) (any, error) {
		settings, err := ApplyWebAPISettingsRuntime(tx, *c)
		return PublicWebAPISettings(settings), err
	}))
	mux.HandleFunc("/api/v1/settings:network", webAPIHTTPCommand(store, "settings:network", func(c *WebAPISettingsNetworkCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *WebAPISettingsNetworkCommand) (any, error) {
		settings, err := ApplyWebAPISettingsNetwork(tx, *c)
		return PublicWebAPISettings(settings), err
	}))
	mux.HandleFunc("/api/v1/settings:current-profile", webAPIHTTPCommand(store, "settings:current-profile", func(c *WebAPISettingsCurrentProfileCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *WebAPISettingsCurrentProfileCommand) (any, error) {
		settings, err := ApplyWebAPISettingsCurrentProfile(tx, *c)
		return PublicWebAPISettings(settings), err
	}))
	mux.HandleFunc("/api/v1/settings:dav", webAPIHTTPCommand(store, "settings:dav", func(c *WebAPISettingsDAVCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *WebAPISettingsDAVCommand) (any, error) {
		settings, err := ApplyWebAPISettingsDAV(tx, *c)
		return PublicWebAPISettings(settings), err
	}))
	mux.HandleFunc("/api/v1/settings:selected-map", webAPIHTTPCommand(store, "settings:selected-map", func(c *WebAPISettingsSelectedMapCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *WebAPISettingsSelectedMapCommand) (any, error) {
		return ApplyWebAPISettingsSelectedMap(tx, *c)
	}))
}
