//go:build webpanel && !android

package core

import (
	"net/http"
	"time"
)

type webAPIIconCachePutCommand struct {
	WebAPIMutation
	URL string `json:"url"`
}
type webAPIIconCacheDeleteCommand struct {
	WebAPIMutation
	URLs []string `json:"urls"`
}
type webAPIIPCachePutCommand struct {
	WebAPIMutation
	Quality WebAPIIPQuality `json:"quality"`
}
type webAPIIPCacheDeleteCommand struct {
	WebAPIMutation
	IPs []string `json:"ips"`
}
type webAPIIconCacheDeleted struct {
	DeletedURLs []string `json:"deletedUrls"`
}
type webAPIIPCacheDeleted struct {
	DeletedIPs []string `json:"deletedIps"`
}

func webAPICacheHTTPCommand[T any](store *WebAPIStore, name string, mutation func(*T) WebAPIMutation, apply func(*WebAPICacheTx, *T) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			WebAPIWriteError(w, webAPIError(405, "method_not_allowed", "POST is required"))
			return
		}
		if _, err := WebAPIQuery(r); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		var command T
		if err := WebAPIDecodeJSON(w, r, &command); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		receipt, err := store.CacheTransaction(r.Context(), mutation(&command), name, command, func(tx *WebAPICacheTx) (any, error) { return apply(tx, &command) })
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteReceipt(w, receipt)
	}
}

func RegisterWebAPICacheRoutes(mux *http.ServeMux, store *WebAPIStore) {
	registerWebAPICacheRoutesWithClock(mux, store, time.Now)
}

func registerWebAPICacheRoutesWithClock(mux *http.ServeMux, store *WebAPIStore, now func() time.Time) {
	mux.HandleFunc("/api/v1/icon-cache", func(w http.ResponseWriter, r *http.Request) {
		if !webAPIGetMethod(w, r) {
			return
		}
		query, err := WebAPIQuery(r, "query", "url")
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		var exact *string
		if value, exists := query["url"]; exists {
			if _, exists := query["query"]; exists {
				WebAPIWriteError(w, webAPIError(400, "invalid_query", "query and url are mutually exclusive"))
				return
			}
			if !webAPIIconURL(value) {
				WebAPIWriteError(w, webAPIError(400, "invalid_icon_url", "invalid icon URL"))
				return
			}
			exact = &value
		}
		if len(query["query"]) > 4096 {
			WebAPIWriteError(w, webAPIError(400, "invalid_query", "icon query is too long"))
			return
		}
		var data []WebAPIIconRecord
		revision, err := store.CacheView(r.Context(), func(tx *WebAPICacheTx) error {
			var e error
			data, e = webAPIQueryIcons(tx, query["query"], exact)
			return e
		})
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteSuccess(w, revision, data)
	})
	mux.HandleFunc("/api/v1/icon-cache:put", webAPICacheHTTPCommand(store, "icon-cache:put", func(c *webAPIIconCachePutCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPICacheTx, c *webAPIIconCachePutCommand) (any, error) {
		return webAPIPutIcon(tx, c.URL, now(), false)
	}))
	mux.HandleFunc("/api/v1/icon-cache:touch", webAPICacheHTTPCommand(store, "icon-cache:touch", func(c *webAPIIconCachePutCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPICacheTx, c *webAPIIconCachePutCommand) (any, error) {
		return webAPIPutIcon(tx, c.URL, now(), true)
	}))
	mux.HandleFunc("/api/v1/icon-cache:delete", webAPICacheHTTPCommand(store, "icon-cache:delete", func(c *webAPIIconCacheDeleteCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPICacheTx, c *webAPIIconCacheDeleteCommand) (any, error) {
		values, err := webAPIDeleteIcons(tx, c.URLs)
		return webAPIIconCacheDeleted{DeletedURLs: values}, err
	}))
	mux.HandleFunc("/api/v1/ip-quality-cache", func(w http.ResponseWriter, r *http.Request) {
		if !webAPIGetMethod(w, r) {
			return
		}
		query, err := WebAPIQuery(r, "ip")
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		var data []WebAPIIPQualityRecord
		revision, err := store.CacheView(r.Context(), func(tx *WebAPICacheTx) error {
			var e error
			data, e = webAPIQueryIPQuality(tx, query["ip"], now())
			return e
		})
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteSuccess(w, revision, data)
	})
	mux.HandleFunc("/api/v1/ip-quality-cache:put", webAPICacheHTTPCommand(store, "ip-quality-cache:put", func(c *webAPIIPCachePutCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPICacheTx, c *webAPIIPCachePutCommand) (any, error) {
		return webAPIPutIPQuality(tx, c.Quality, now())
	}))
	mux.HandleFunc("/api/v1/ip-quality-cache:delete", webAPICacheHTTPCommand(store, "ip-quality-cache:delete", func(c *webAPIIPCacheDeleteCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPICacheTx, c *webAPIIPCacheDeleteCommand) (any, error) {
		values, err := webAPIDeleteIPQualities(tx, c.IPs)
		return webAPIIPCacheDeleted{DeletedIPs: values}, err
	}))
}
