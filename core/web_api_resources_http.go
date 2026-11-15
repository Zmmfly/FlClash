//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"unicode/utf8"
)

func WebAPIDecodeJSON(w http.ResponseWriter, r *http.Request, out any) error {
	return WebAPIDecodeJSONLimit(w, r, out, webAPIMaxJSONBytes)
}

func WebAPIDecodeJSONLimit(w http.ResponseWriter, r *http.Request, out any, limit int64) error {
	if limit <= 0 || (limit > WebAPIMaxContentJSONBytes && (limit != WebAPIMaxPreviewJSONBytes || r.URL.Path != "/api/v1/scripts:preview")) {
		return webAPIError(500, "invalid_body_limit", "invalid server request limit")
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return webAPIError(415, "unsupported_media_type", "expected application/json")
	}
	if r.Header.Get("Content-Encoding") != "" {
		return webAPIError(415, "unsupported_encoding", "compressed request bodies are not accepted")
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			return webAPIError(413, "body_too_large", "request exceeds the JSON body limit")
		}
		return webAPIError(400, "invalid_json", "cannot read request body")
	}
	if err = webAPIStrictJSON(body, out); err != nil {
		return webAPIError(400, "invalid_json", err.Error())
	}
	return nil
}

func webAPIStrictJSON(body []byte, out any) error {
	if !utf8.Valid(body) {
		return errors.New("JSON must contain valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := webAPIJSONTokens(dec, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("request must contain exactly one JSON value")
	}
	t := reflect.TypeOf(out)
	if t == nil || t.Kind() != reflect.Pointer {
		return errors.New("invalid decode target")
	}
	if err := webAPIJSONFields(body, t.Elem(), 0); err != nil {
		return err
	}
	dec = json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return errors.New("JSON field has an invalid value or numeric range")
	}
	return nil
}

func webAPIJSONTokens(dec *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON nesting exceeds the limit")
	}
	token, err := dec.Token()
	if err != nil {
		return errors.New("malformed JSON")
	}
	if delim, ok := token.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return errors.New("malformed JSON member")
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate JSON member")
				}
				seen[name] = true
				if err = webAPIJSONTokens(dec, depth+1); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim('}') {
				return errors.New("malformed JSON object")
			}
		case '[':
			for dec.More() {
				if err := webAPIJSONTokens(dec, depth+1); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim(']') {
				return errors.New("malformed JSON array")
			}
		default:
			return errors.New("malformed JSON delimiter")
		}
	}
	return nil
}

func webAPIJSONStructFields(t reflect.Type, fields map[string]reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "-" {
			continue
		}
		if f.Anonymous && tag == "" {
			webAPIJSONStructFields(f.Type, fields)
			continue
		}
		if tag == "" {
			tag = f.Name
		}
		fields[tag] = f.Type
	}
}

func webAPIJSONFields(raw []byte, t reflect.Type, depth int) error {
	if depth > 64 {
		return errors.New("JSON nesting exceeds the limit")
	}
	if t == reflect.TypeFor[json.RawMessage]() {
		return nil
	}
	if t.Kind() == reflect.Pointer {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil
		}
		return webAPIJSONFields(raw, t.Elem(), depth+1)
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("null is not allowed for this field")
	}
	switch t.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || object == nil {
			return errors.New("expected JSON object")
		}
		fields := map[string]reflect.Type{}
		webAPIJSONStructFields(t, fields)
		for name, value := range object {
			ft, ok := fields[name]
			if !ok {
				return errors.New("unknown JSON field")
			}
			if err := webAPIJSONFields(value, ft, depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || object == nil {
			return errors.New("expected JSON object")
		}
		for _, value := range object {
			if err := webAPIJSONFields(value, t.Elem(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return errors.New("expected JSON array")
		}
		for _, value := range items {
			if err := webAPIJSONFields(value, t.Elem(), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func WebAPIWriteError(w http.ResponseWriter, err error) {
	var problem *WebAPIError
	if !errors.As(err, &problem) {
		problem = webAPIError(500, "internal_error", "request could not be completed")
	}
	status := problem.Status
	if status < 400 || status > 599 {
		status = 500
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error    *WebAPIError   `json:"error"`
		Revision WebAPIRevision `json:"revision,omitempty"`
	}{problem, problem.Revision})
}

func WebAPIWriteSuccess(w http.ResponseWriter, revision WebAPIRevision, data any) {
	body, err := json.Marshal(WebAPIEnvelope[any]{Revision: revision, Data: data})
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	webAPIWriteJSON(w, body)
}

func WebAPIWriteReceipt(w http.ResponseWriter, receipt WebAPIReceipt) {
	webAPIWriteJSON(w, receipt.Body)
}

func webAPIWriteJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}

func WebAPIQuery(r *http.Request, allowed ...string) (map[string]string, error) {
	values, err := urlQuery(r.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	for name := range values {
		if !webAPIContains(allowed, name) {
			return nil, webAPIError(400, "invalid_query", "query parameter is not allowed")
		}
	}
	return values, nil
}

func webAPIHTTPCommand[T any](store *WebAPIStore, name string, mutation func(*T) WebAPIMutation, apply func(*WebAPIWriteTx, *T) (any, error)) http.HandlerFunc {
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
		limit := int64(webAPIMaxJSONBytes)
		switch name {
		case "rules:replace":
			limit = WebAPIMaxBulkJSONBytes
		case "profiles:create", "profiles:update":
			limit = WebAPIMaxContentJSONBytes
		}
		if err := WebAPIDecodeJSONLimit(w, r, &command, limit); err != nil {
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

func NewWebAPIHandler(store *WebAPIStore) http.Handler {
	mux := http.NewServeMux()
	RegisterWebAPIResourceRoutes(mux, store)
	return mux
}

func RegisterWebAPIResourceRoutes(mux *http.ServeMux, store *WebAPIStore) {
	register := func(path string, h http.HandlerFunc) { mux.HandleFunc("/api/v1/"+path, h) }
	register("meta", func(w http.ResponseWriter, r *http.Request) {
		if !webAPIGetMethod(w, r) {
			return
		}
		if _, err := WebAPIQuery(r); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		var data WebAPIStoreMeta
		rev, err := store.View(r.Context(), func(tx *WebAPIReadTx) error { var err error; data, err = tx.Meta(); return err })
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteSuccess(w, rev, data)
	})
	register("profiles:create", webAPIHTTPCommand(store, "profiles:create", func(c *WebAPIProfileCreate) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *WebAPIProfileCreate) (any, error) { return tx.CreateProfile(c.Profile) }))
	register("profiles:update", webAPIHTTPCommand(store, "profiles:update", func(c *WebAPIProfileUpdate) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *WebAPIProfileUpdate) (any, error) { return tx.UpdateProfile(c.ID, c.Profile) }))
	register("profiles:delete", webAPIHTTPCommand(store, "profiles:delete", func(c *WebAPIProfileDelete) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *WebAPIProfileDelete) (any, error) { return tx.DeleteProfile(c.ID) }))
	register("profiles:rename", webAPIHTTPCommand(store, "profiles:rename", func(c *WebAPIProfileRename) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *WebAPIProfileRename) (any, error) { return tx.RenameProfile(c.ID, c.Label) }))
	register("profiles:reset-dialers", webAPIHTTPCommand(store, "profiles:reset-dialers", func(c *WebAPIProfileResetDialers) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *WebAPIProfileResetDialers) (any, error) {
		return tx.ResetProfileDialers(c.ID)
	}))
	register("profiles:reorder", webAPIHTTPCommand(store, "profiles:reorder", func(c *WebAPIProfileReorder) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *WebAPIProfileReorder) (any, error) { return tx.ReorderProfiles(c.IDs) }))
	register("rules:create", webAPIHTTPCommand(store, "rules:create", func(c *WebAPIRuleCreate) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *WebAPIRuleCreate) (any, error) { return tx.CreateRule(c.Rule) }))
	register("rules:update", webAPIHTTPCommand(store, "rules:update", func(c *WebAPIRuleUpdate) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *WebAPIRuleUpdate) (any, error) { return tx.UpdateRule(c.ID, c.Rule) }))
	register("rules:delete", webAPIHTTPCommand(store, "rules:delete", func(c *WebAPIRuleDelete) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *WebAPIRuleDelete) (any, error) { return tx.DeleteRules(c.IDs) }))
	register("rules:reorder", webAPIHTTPCommand(store, "rules:reorder", func(c *WebAPIRuleReorder) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *WebAPIRuleReorder) (any, error) { return tx.ReorderRules(c.ProfileID, c.IDs) }))
	register("rules:replace", webAPIHTTPCommand(store, "rules:replace", func(c *WebAPIRuleReplace) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *WebAPIRuleReplace) (any, error) {
		return tx.ReplaceRules(c.ProfileID, c.Rules)
	}))
	register("disabled-rules:replace", webAPIHTTPCommand(store, "disabled-rules:replace", func(c *WebAPIDisabledReplace) WebAPIMutation { return c.WebAPIMutation }, func(tx *WebAPIWriteTx, c *WebAPIDisabledReplace) (any, error) {
		return tx.ReplaceDisabledRules(c.ProfileID, c.RuleIDs)
	}))
	register("profiles", func(w http.ResponseWriter, r *http.Request) {
		if !webAPIGetMethod(w, r) {
			return
		}
		q, err := WebAPIQuery(r, "id")
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		var profiles []WebAPIProfile
		rev, err := store.View(r.Context(), func(tx *WebAPIReadTx) error {
			if id, ok := q["id"]; ok {
				if _, err := webAPIInteger(id, true); err != nil {
					return webAPIError(400, "invalid_id", "invalid profile identity")
				}
				p, err := tx.Profile(WebAPIID(id))
				profiles = []WebAPIProfile{p}
				return err
			}
			var err error
			profiles, err = tx.Profiles()
			return err
		})
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteSuccess(w, rev, profiles)
	})
	register("rules", func(w http.ResponseWriter, r *http.Request) {
		if !webAPIGetMethod(w, r) {
			return
		}
		q, err := WebAPIQuery(r, "scope", "profileId")
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		scope := q["scope"]
		if scope == "" {
			scope = "global"
		}
		var owner *WebAPIID
		if scope == "profile" || scope == "added" {
			id := q["profileId"]
			if _, err := webAPIInteger(id, true); err != nil {
				WebAPIWriteError(w, webAPIError(400, "invalid_id", "profile scope requires a valid profileId"))
				return
			}
			v := WebAPIID(id)
			owner = &v
		} else if scope != "global" || q["profileId"] != "" {
			WebAPIWriteError(w, webAPIError(400, "invalid_scope", "invalid rule query scope"))
			return
		}
		var rules []WebAPIRule
		rev, err := store.View(r.Context(), func(tx *WebAPIReadTx) error {
			if owner != nil {
				if _, err := tx.Profile(*owner); err != nil {
					return err
				}
			}
			var err error
			rules, err = tx.OwnedRules(owner)
			if err != nil || scope != "added" {
				return err
			}
			disabled, err := tx.DisabledSelection(*owner)
			if err != nil {
				return err
			}
			skip := map[WebAPIID]bool{}
			for _, id := range disabled.RuleIDs {
				skip[id] = true
			}
			globals, err := tx.OwnedRules(nil)
			if err != nil {
				return err
			}
			for _, rule := range globals {
				if !skip[rule.ID] {
					rules = append(rules, rule)
				}
			}
			return nil
		})
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteSuccess(w, rev, rules)
	})
	register("disabled-rules", func(w http.ResponseWriter, r *http.Request) {
		if !webAPIGetMethod(w, r) {
			return
		}
		q, err := WebAPIQuery(r, "profileId")
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		if _, err = webAPIInteger(q["profileId"], true); err != nil {
			WebAPIWriteError(w, webAPIError(400, "invalid_id", "profileId is required"))
			return
		}
		var result WebAPIDisabledSelection
		rev, err := store.View(r.Context(), func(tx *WebAPIReadTx) error {
			var err error
			result, err = tx.DisabledSelection(WebAPIID(q["profileId"]))
			return err
		})
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteSuccess(w, rev, result)
	})
	register("operations/", webAPIOperationsHandler(store, false))
	register("cache-operations/", webAPIOperationsHandler(store, true))
	register("", func(w http.ResponseWriter, r *http.Request) {
		WebAPIWriteError(w, webAPIError(404, "not_found", "API resource or command does not exist"))
	})
}

func urlQuery(raw string) (map[string]string, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, webAPIError(400, "invalid_query", "malformed query string")
	}
	out := map[string]string{}
	for name, items := range values {
		if len(items) != 1 {
			return nil, webAPIError(400, "invalid_query", "duplicate query parameter")
		}
		out[name] = items[0]
	}
	return out, nil
}

func webAPIGetMethod(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	w.Header().Set("Allow", http.MethodGet)
	WebAPIWriteError(w, webAPIError(405, "method_not_allowed", "GET is required"))
	return false
}

func webAPIOperationsHandler(store *WebAPIStore, cache bool) http.HandlerFunc {
	prefix := "/api/v1/operations/"
	lookup := store.Receipt
	resolve := store.ResolveOperation
	if cache {
		prefix = "/api/v1/cache-operations/"
		lookup = store.CacheReceipt
		resolve = store.ResolveCacheOperation
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := WebAPIQuery(r); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, prefix)
		if strings.HasSuffix(id, ":resolve") {
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				WebAPIWriteError(w, webAPIError(405, "method_not_allowed", "POST is required"))
				return
			}
			var command WebAPIOperationResolve
			if err := WebAPIDecodeJSON(w, r, &command); err != nil {
				WebAPIWriteError(w, err)
				return
			}
			if command.Intent != "resolve-unknown-result" {
				WebAPIWriteError(w, webAPIError(400, "invalid_intent", "explicit unknown-result resolution intent is required"))
				return
			}
			receipt, err := resolve(r.Context(), strings.TrimSuffix(id, ":resolve"))
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
		receipt, err := lookup(r.Context(), id)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteReceipt(w, receipt)
	}
}
