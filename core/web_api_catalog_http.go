//go:build webpanel && !android

package core

import "net/http"

type webAPIGroupCreateCommand struct {
	WebAPIMutation
	ProfileID WebAPIID         `json:"profileId"`
	Group     webAPIGroupInput `json:"group"`
}
type webAPIGroupUpdateCommand struct {
	WebAPIMutation
	ProfileID WebAPIID         `json:"profileId"`
	ID        WebAPIID         `json:"id"`
	Group     webAPIGroupInput `json:"group"`
}
type webAPIGroupIDsCommand struct {
	WebAPIMutation
	ProfileID WebAPIID   `json:"profileId"`
	IDs       []WebAPIID `json:"ids"`
}
type webAPIGroupReplacement struct {
	ID           *WebAPIID                  `json:"id"`
	Definition   WebAPIProxyGroupDefinition `json:"definition"`
	MemberLayout []string                   `json:"memberLayout"`
}
type webAPIGroupReplaceCommand struct {
	WebAPIMutation
	ProfileID WebAPIID                  `json:"profileId"`
	Groups    *[]webAPIGroupReplacement `json:"groups"`
}

type webAPIProxyCreateCommand struct {
	WebAPIMutation
	Proxy webAPIProxyInput `json:"proxy"`
}
type webAPIProxyUpdateCommand struct {
	WebAPIMutation
	ID    WebAPIID         `json:"id"`
	Proxy webAPIProxyInput `json:"proxy"`
}
type webAPIProxyIDsCommand struct {
	WebAPIMutation
	IDs []WebAPIID `json:"ids"`
}
type webAPIProxyReplacement struct {
	ID           *WebAPIID             `json:"id"`
	Definition   WebAPIProxyDefinition `json:"definition"`
	EmptyOptions []string              `json:"emptyOptions"`
}
type webAPIProxyReplaceCommand struct {
	WebAPIMutation
	Proxies *[]webAPIProxyReplacement `json:"proxies"`
}

type webAPIProviderCreateCommand struct {
	WebAPIMutation
	Provider webAPIProviderInput `json:"provider"`
}
type webAPIProviderUpdateCommand struct {
	WebAPIMutation
	ID       WebAPIID            `json:"id"`
	Provider webAPIProviderInput `json:"provider"`
}
type webAPIProviderIDsCommand struct {
	WebAPIMutation
	IDs []WebAPIID `json:"ids"`
}
type webAPIProviderReplacement struct {
	ID    *WebAPIID `json:"id"`
	Label string    `json:"label"`
	URL   string    `json:"url"`
}
type webAPIProviderReplaceCommand struct {
	WebAPIMutation
	Providers *[]webAPIProviderReplacement `json:"providers"`
}

type webAPIScriptCreateCommand struct {
	WebAPIMutation
	Script webAPIScriptInput `json:"script"`
}
type webAPIScriptUpdateCommand struct {
	WebAPIMutation
	ID     WebAPIID          `json:"id"`
	Script webAPIScriptInput `json:"script"`
}
type webAPIScriptIDsCommand struct {
	WebAPIMutation
	IDs []WebAPIID `json:"ids"`
}
type webAPIScriptReplacement struct {
	ID    *WebAPIID `json:"id"`
	Label string    `json:"label"`
	URL   *string   `json:"url"`
}
type webAPIScriptReplaceCommand struct {
	WebAPIMutation
	Scripts *[]webAPIScriptReplacement `json:"scripts"`
}

type webAPIModuleCreateCommand struct {
	WebAPIMutation
	Module webAPIModuleInput `json:"module"`
}
type webAPIModuleUpdateCommand struct {
	WebAPIMutation
	ID     WebAPIID          `json:"id"`
	Module webAPIModuleInput `json:"module"`
}
type webAPIModuleIDsCommand struct {
	WebAPIMutation
	IDs []WebAPIID `json:"ids"`
}
type webAPIModuleReplacement struct {
	ID          *WebAPIID         `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	URL         *string           `json:"url"`
	Enabled     bool              `json:"enabled"`
	Arguments   map[string]string `json:"arguments"`
	System      *string           `json:"system"`
}
type webAPIModuleReplaceCommand struct {
	WebAPIMutation
	Modules *[]webAPIModuleReplacement `json:"modules"`
}

type webAPIIconSetCreateCommand struct {
	WebAPIMutation
	IconSet webAPIIconSetInput `json:"iconSet"`
}
type webAPIIconSetUpdateCommand struct {
	WebAPIMutation
	ID      WebAPIID           `json:"id"`
	IconSet webAPIIconSetInput `json:"iconSet"`
}
type webAPIIconSetIDsCommand struct {
	WebAPIMutation
	IDs []WebAPIID `json:"ids"`
}
type webAPIIconSetReplacement struct {
	ID    *WebAPIID           `json:"id"`
	Name  string              `json:"name"`
	URL   string              `json:"url"`
	Icons []WebAPIIconSetIcon `json:"icons"`
}
type webAPIIconSetReplaceCommand struct {
	WebAPIMutation
	IconSets *[]webAPIIconSetReplacement `json:"iconSets"`
}

type webAPIDialersReplaceCommand struct {
	WebAPIMutation
	ProfileID WebAPIID             `json:"profileId"`
	Dialers   *[]webAPIDialerInput `json:"dialers"`
}

func webAPICatalogListHandler[T, I any](store *WebAPIStore, factory func(map[string]string) (webAPICatalogResource[T, I], error), allowed ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !webAPIGetMethod(w, r) {
			return
		}
		query, err := WebAPIQuery(r, allowed...)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		resource, err := factory(query)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		var items []T
		revision, err := store.View(r.Context(), func(tx *WebAPIReadTx) error { var err error; items, err = resource.list(tx); return err })
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteSuccess(w, revision, items)
	}
}

func RegisterWebAPICatalogRoutes(mux *http.ServeMux, store *WebAPIStore) {
	RegisterWebAPICatalogRoutesWithOptions(mux, store, WebAPICatalogOptions{})
}

func RegisterWebAPICatalogRoutesWithOptions(mux *http.ServeMux, store *WebAPIStore, options WebAPICatalogOptions) {
	registerWebAPIIconSetRefresh(mux, store, options)
	mux.HandleFunc("/api/v1/proxy-groups", webAPICatalogListHandler(store, func(q map[string]string) (webAPICatalogResource[WebAPIProxyGroup, webAPIGroupInput], error) {
		id := WebAPIID(q["profileId"])
		if _, err := webAPIInteger(string(id), true); err != nil {
			return webAPICatalogResource[WebAPIProxyGroup, webAPIGroupInput]{}, webAPIError(400, "invalid_id", "profileId is required")
		}
		return webAPIGroupResource(id), nil
	}, "profileId"))
	mux.HandleFunc("/api/v1/proxy-groups:create", webAPIHTTPCommand(store, "proxy-groups:create", func(c *webAPIGroupCreateCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIGroupCreateCommand) (any, error) {
		return webAPICatalogCreate(tx, webAPIGroupResource(c.ProfileID), c.Group)
	}))
	mux.HandleFunc("/api/v1/proxy-groups:update", webAPICatalogBulkHTTPCommand(store, "proxy-groups:update", func(c *webAPIGroupUpdateCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIGroupUpdateCommand) (any, error) {
		return webAPICatalogUpdate(tx, webAPIGroupResource(c.ProfileID), c.ID, c.Group)
	}))
	mux.HandleFunc("/api/v1/proxy-groups:delete", webAPIHTTPCommand(store, "proxy-groups:delete", func(c *webAPIGroupIDsCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIGroupIDsCommand) (any, error) {
		return webAPICatalogDelete(tx, webAPIGroupResource(c.ProfileID), c.IDs)
	}))
	mux.HandleFunc("/api/v1/proxy-groups:reorder", webAPIHTTPCommand(store, "proxy-groups:reorder", func(c *webAPIGroupIDsCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIGroupIDsCommand) (any, error) {
		return webAPICatalogReorder(tx, webAPIGroupResource(c.ProfileID), c.IDs)
	}))
	mux.HandleFunc("/api/v1/proxy-groups:replace", webAPICatalogBulkHTTPCommand(store, "proxy-groups:replace", func(c *webAPIGroupReplaceCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIGroupReplaceCommand) (any, error) {
		if c.Groups == nil {
			return nil, webAPIError(400, "missing_replacement", "groups array is required")
		}
		items := make([]webAPICatalogReplacement[webAPIGroupInput], 0, len(*c.Groups))
		for _, v := range *c.Groups {
			items = append(items, webAPICatalogReplacement[webAPIGroupInput]{ID: v.ID, Input: webAPIGroupInput{Definition: v.Definition, MemberLayout: v.MemberLayout}})
		}
		return webAPICatalogReplace(tx, webAPIGroupResource(c.ProfileID), items)
	}))
	mux.HandleFunc("/api/v1/custom-proxies", webAPICatalogListHandler(store, func(map[string]string) (webAPICatalogResource[WebAPICustomProxy, webAPIProxyInput], error) {
		return webAPIProxyResource(), nil
	}))
	mux.HandleFunc("/api/v1/custom-proxies:create", webAPIHTTPCommand(store, "custom-proxies:create", func(c *webAPIProxyCreateCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIProxyCreateCommand) (any, error) {
		return webAPICatalogCreate(tx, webAPIProxyResource(), c.Proxy)
	}))
	mux.HandleFunc("/api/v1/custom-proxies:update", webAPICatalogBulkHTTPCommand(store, "custom-proxies:update", func(c *webAPIProxyUpdateCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIProxyUpdateCommand) (any, error) {
		return webAPICatalogUpdate(tx, webAPIProxyResource(), c.ID, c.Proxy)
	}))
	mux.HandleFunc("/api/v1/custom-proxies:delete", webAPIHTTPCommand(store, "custom-proxies:delete", func(c *webAPIProxyIDsCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIProxyIDsCommand) (any, error) {
		return webAPICatalogDelete(tx, webAPIProxyResource(), c.IDs)
	}))
	mux.HandleFunc("/api/v1/custom-proxies:reorder", webAPIHTTPCommand(store, "custom-proxies:reorder", func(c *webAPIProxyIDsCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIProxyIDsCommand) (any, error) {
		return webAPICatalogReorder(tx, webAPIProxyResource(), c.IDs)
	}))
	mux.HandleFunc("/api/v1/custom-proxies:replace", webAPICatalogBulkHTTPCommand(store, "custom-proxies:replace", func(c *webAPIProxyReplaceCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIProxyReplaceCommand) (any, error) {
		if c.Proxies == nil {
			return nil, webAPIError(400, "missing_replacement", "proxies array is required")
		}
		items := make([]webAPICatalogReplacement[webAPIProxyInput], 0, len(*c.Proxies))
		for _, v := range *c.Proxies {
			items = append(items, webAPICatalogReplacement[webAPIProxyInput]{ID: v.ID, Input: webAPIProxyInput{Definition: v.Definition, EmptyOptions: v.EmptyOptions}})
		}
		return webAPICatalogReplace(tx, webAPIProxyResource(), items)
	}))
	mux.HandleFunc("/api/v1/clash-providers", webAPICatalogListHandler(store, func(map[string]string) (webAPICatalogResource[WebAPIClashProvider, webAPIProviderInput], error) {
		return webAPIProviderResource(), nil
	}))
	mux.HandleFunc("/api/v1/clash-providers:create", webAPIHTTPCommand(store, "clash-providers:create", func(c *webAPIProviderCreateCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIProviderCreateCommand) (any, error) {
		return webAPICatalogCreate(tx, webAPIProviderResource(), c.Provider)
	}))
	mux.HandleFunc("/api/v1/clash-providers:update", webAPICatalogBulkHTTPCommand(store, "clash-providers:update", func(c *webAPIProviderUpdateCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIProviderUpdateCommand) (any, error) {
		return webAPICatalogUpdate(tx, webAPIProviderResource(), c.ID, c.Provider)
	}))
	mux.HandleFunc("/api/v1/clash-providers:delete", webAPIHTTPCommand(store, "clash-providers:delete", func(c *webAPIProviderIDsCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIProviderIDsCommand) (any, error) {
		return webAPICatalogDelete(tx, webAPIProviderResource(), c.IDs)
	}))
	mux.HandleFunc("/api/v1/clash-providers:reorder", webAPIHTTPCommand(store, "clash-providers:reorder", func(c *webAPIProviderIDsCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIProviderIDsCommand) (any, error) {
		return webAPICatalogReorder(tx, webAPIProviderResource(), c.IDs)
	}))
	mux.HandleFunc("/api/v1/clash-providers:replace", webAPICatalogBulkHTTPCommand(store, "clash-providers:replace", func(c *webAPIProviderReplaceCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIProviderReplaceCommand) (any, error) {
		if c.Providers == nil {
			return nil, webAPIError(400, "missing_replacement", "providers array is required")
		}
		items := make([]webAPICatalogReplacement[webAPIProviderInput], 0, len(*c.Providers))
		for _, v := range *c.Providers {
			items = append(items, webAPICatalogReplacement[webAPIProviderInput]{ID: v.ID, Input: webAPIProviderInput{Label: v.Label, URL: v.URL}})
		}
		return webAPICatalogReplace(tx, webAPIProviderResource(), items)
	}))
	mux.HandleFunc("/api/v1/scripts", webAPICatalogListHandler(store, func(map[string]string) (webAPICatalogResource[WebAPIScript, webAPIScriptInput], error) {
		return webAPIScriptResource(), nil
	}))
	mux.HandleFunc("/api/v1/scripts:create", webAPIHTTPCommand(store, "scripts:create", func(c *webAPIScriptCreateCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIScriptCreateCommand) (any, error) {
		return webAPICatalogCreate(tx, webAPIScriptResource(), c.Script)
	}))
	mux.HandleFunc("/api/v1/scripts:update", webAPICatalogBulkHTTPCommand(store, "scripts:update", func(c *webAPIScriptUpdateCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIScriptUpdateCommand) (any, error) {
		return webAPICatalogUpdate(tx, webAPIScriptResource(), c.ID, c.Script)
	}))
	mux.HandleFunc("/api/v1/scripts:delete", webAPIHTTPCommand(store, "scripts:delete", func(c *webAPIScriptIDsCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIScriptIDsCommand) (any, error) {
		return webAPICatalogDelete(tx, webAPIScriptResource(), c.IDs)
	}))
	mux.HandleFunc("/api/v1/scripts:reorder", webAPIHTTPCommand(store, "scripts:reorder", func(c *webAPIScriptIDsCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIScriptIDsCommand) (any, error) {
		return webAPICatalogReorder(tx, webAPIScriptResource(), c.IDs)
	}))
	mux.HandleFunc("/api/v1/scripts:replace", webAPICatalogBulkHTTPCommand(store, "scripts:replace", func(c *webAPIScriptReplaceCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIScriptReplaceCommand) (any, error) {
		if c.Scripts == nil {
			return nil, webAPIError(400, "missing_replacement", "scripts array is required")
		}
		items := make([]webAPICatalogReplacement[webAPIScriptInput], 0, len(*c.Scripts))
		for _, v := range *c.Scripts {
			items = append(items, webAPICatalogReplacement[webAPIScriptInput]{ID: v.ID, Input: webAPIScriptInput{Label: v.Label, URL: v.URL}})
		}
		return webAPICatalogReplace(tx, webAPIScriptResource(), items)
	}))
	mux.HandleFunc("/api/v1/mitm-modules", webAPICatalogListHandler(store, func(map[string]string) (webAPICatalogResource[WebAPIMitmModule, webAPIModuleInput], error) {
		return webAPIModuleResource(), nil
	}))
	mux.HandleFunc("/api/v1/mitm-modules:create", webAPIHTTPCommand(store, "mitm-modules:create", func(c *webAPIModuleCreateCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIModuleCreateCommand) (any, error) {
		return webAPICatalogCreate(tx, webAPIModuleResource(), c.Module)
	}))
	mux.HandleFunc("/api/v1/mitm-modules:update", webAPICatalogBulkHTTPCommand(store, "mitm-modules:update", func(c *webAPIModuleUpdateCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIModuleUpdateCommand) (any, error) {
		return webAPICatalogUpdate(tx, webAPIModuleResource(), c.ID, c.Module)
	}))
	mux.HandleFunc("/api/v1/mitm-modules:delete", webAPIHTTPCommand(store, "mitm-modules:delete", func(c *webAPIModuleIDsCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIModuleIDsCommand) (any, error) {
		return webAPICatalogDelete(tx, webAPIModuleResource(), c.IDs)
	}))
	mux.HandleFunc("/api/v1/mitm-modules:reorder", webAPIHTTPCommand(store, "mitm-modules:reorder", func(c *webAPIModuleIDsCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIModuleIDsCommand) (any, error) {
		return webAPICatalogReorder(tx, webAPIModuleResource(), c.IDs)
	}))
	mux.HandleFunc("/api/v1/mitm-modules:replace", webAPICatalogBulkHTTPCommand(store, "mitm-modules:replace", func(c *webAPIModuleReplaceCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIModuleReplaceCommand) (any, error) {
		if c.Modules == nil {
			return nil, webAPIError(400, "missing_replacement", "modules array is required")
		}
		items := make([]webAPICatalogReplacement[webAPIModuleInput], 0, len(*c.Modules))
		for _, v := range *c.Modules {
			items = append(items, webAPICatalogReplacement[webAPIModuleInput]{ID: v.ID, Input: webAPIModuleInput{Name: v.Name, Description: v.Description, URL: v.URL, Enabled: v.Enabled, Arguments: v.Arguments, System: v.System}})
		}
		return webAPICatalogReplace(tx, webAPIModuleResource(), items)
	}))
	mux.HandleFunc("/api/v1/icon-sets", webAPICatalogListHandler(store, func(map[string]string) (webAPICatalogResource[WebAPIIconSet, webAPIIconSetInput], error) {
		return webAPIIconSetResource(), nil
	}))
	mux.HandleFunc("/api/v1/icon-sets:create", webAPIHTTPCommand(store, "icon-sets:create", func(c *webAPIIconSetCreateCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIIconSetCreateCommand) (any, error) {
		return webAPICatalogCreate(tx, webAPIIconSetResource(), c.IconSet)
	}))
	mux.HandleFunc("/api/v1/icon-sets:update", webAPICatalogBulkHTTPCommand(store, "icon-sets:update", func(c *webAPIIconSetUpdateCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIIconSetUpdateCommand) (any, error) {
		return webAPICatalogUpdate(tx, webAPIIconSetResource(), c.ID, c.IconSet)
	}))
	mux.HandleFunc("/api/v1/icon-sets:delete", webAPIHTTPCommand(store, "icon-sets:delete", func(c *webAPIIconSetIDsCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIIconSetIDsCommand) (any, error) {
		return webAPICatalogDelete(tx, webAPIIconSetResource(), c.IDs)
	}))
	mux.HandleFunc("/api/v1/icon-sets:reorder", webAPIHTTPCommand(store, "icon-sets:reorder", func(c *webAPIIconSetIDsCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIIconSetIDsCommand) (any, error) {
		return webAPICatalogReorder(tx, webAPIIconSetResource(), c.IDs)
	}))
	mux.HandleFunc("/api/v1/icon-sets:replace", webAPICatalogBulkHTTPCommand(store, "icon-sets:replace", func(c *webAPIIconSetReplaceCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIIconSetReplaceCommand) (any, error) {
		if c.IconSets == nil {
			return nil, webAPIError(400, "missing_replacement", "iconSets array is required")
		}
		items := make([]webAPICatalogReplacement[webAPIIconSetInput], 0, len(*c.IconSets))
		for _, v := range *c.IconSets {
			items = append(items, webAPICatalogReplacement[webAPIIconSetInput]{ID: v.ID, Input: webAPIIconSetInput{Name: v.Name, URL: v.URL, Icons: v.Icons}})
		}
		return webAPICatalogReplace(tx, webAPIIconSetResource(), items)
	}))
	mux.HandleFunc("/api/v1/proxy-dialers", func(w http.ResponseWriter, r *http.Request) {
		if !webAPIGetMethod(w, r) {
			return
		}
		q, err := WebAPIQuery(r, "profileId")
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		owner := WebAPIID(q["profileId"])
		if _, err := webAPIInteger(string(owner), true); err != nil {
			WebAPIWriteError(w, webAPIError(400, "invalid_id", "profileId is required"))
			return
		}
		var data webAPIDialerSelection
		revision, err := store.View(r.Context(), func(tx *WebAPIReadTx) error { var e error; data, e = webAPIReadDialers(tx, owner); return e })
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteSuccess(w, revision, data)
	})
	mux.HandleFunc("/api/v1/proxy-dialers:replace", webAPICatalogBulkHTTPCommand(store, "proxy-dialers:replace", func(c *webAPIDialersReplaceCommand) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *webAPIDialersReplaceCommand) (any, error) {
		if c.Dialers == nil {
			return nil, webAPIError(400, "missing_replacement", "dialers array is required")
		}
		return webAPIReplaceDialers(tx, c.ProfileID, *c.Dialers)
	}))
}

func webAPICatalogBulkHTTPCommand[T any](store *WebAPIStore, name string, mutation func(*T) WebAPIMutation, apply func(*WebAPIWriteTx, *T) (any, error)) http.HandlerFunc {
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
		if err := WebAPIDecodeJSONLimit(w, r, &command, WebAPIMaxBulkJSONBytes); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		receipt, err := store.Transaction(r.Context(), mutation(&command), name, command, func(tx *WebAPIWriteTx) (any, error) { return apply(tx, &command) })
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteReceipt(w, receipt)
	}
}
