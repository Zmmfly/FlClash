//go:build webpanel && !android

package core

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"
)

type webAPICatalogResource[T, I any] struct {
	allowDuplicateNames bool
	list                func(*WebAPIReadTx) ([]T, error)
	load                func(*WebAPIReadTx, WebAPIID) (T, error)
	insert              func(*WebAPIWriteTx, T) error
	save                func(*WebAPIWriteTx, T) error
	remove              func(*WebAPIWriteTx, WebAPIID) error
	id                  func(T) WebAPIID
	name                func(T) string
	order               func(T, int) T
	build               func(WebAPIID, I, *T) (T, error)
	changed             func(*WebAPIWriteTx, []T, []T) error
}

type webAPICatalogReplacement[I any] struct {
	ID    *WebAPIID `json:"id,omitempty"`
	Input I         `json:"-"`
}

func webAPICatalogDuplicate[T, I any](resource webAPICatalogResource[T, I], items []T) error {
	if resource.allowDuplicateNames {
		return nil
	}
	seen := map[string]bool{}
	for _, item := range items {
		name := resource.name(item)
		if seen[name] {
			return webAPIError(409, "duplicate_name", "resource name is already used in this namespace")
		}
		seen[name] = true
	}
	return nil
}

func webAPICatalogCreate[T, I any](tx *WebAPIWriteTx, resource webAPICatalogResource[T, I], input I) (T, error) {
	var result T
	before, err := resource.list(tx.WebAPIReadTx)
	if err != nil {
		return result, err
	}
	id, err := tx.AllocateID()
	if err != nil {
		return result, err
	}
	result, err = resource.build(id, input, nil)
	if err != nil {
		return result, err
	}
	result = resource.order(result, len(before))
	after := append(append([]T{}, before...), result)
	if err = webAPICatalogDuplicate(resource, after); err != nil {
		return result, err
	}
	if err = resource.insert(tx, result); err != nil {
		return result, err
	}
	if resource.changed != nil {
		if err = resource.changed(tx, before, after); err != nil {
			return result, err
		}
	}
	return resource.load(tx.WebAPIReadTx, id)
}

func webAPICatalogUpdate[T, I any](tx *WebAPIWriteTx, resource webAPICatalogResource[T, I], id WebAPIID, input I) (T, error) {
	if _, err := webAPIInteger(string(id), true); err != nil {
		var zero T
		return zero, webAPIError(400, "invalid_id", "id is required")
	}
	previous, err := resource.load(tx.WebAPIReadTx, id)
	if err != nil {
		return previous, err
	}
	before, err := resource.list(tx.WebAPIReadTx)
	if err != nil {
		return previous, err
	}
	next, err := resource.build(id, input, &previous)
	if err != nil {
		return previous, err
	}
	after := append([]T{}, before...)
	for i, item := range after {
		if resource.id(item) == id {
			after[i] = next
		}
	}
	if err = webAPICatalogDuplicate(resource, after); err != nil {
		return previous, err
	}
	if err = resource.save(tx, next); err != nil {
		return previous, err
	}
	if resource.changed != nil {
		if err = resource.changed(tx, before, after); err != nil {
			return previous, err
		}
	}
	return resource.load(tx.WebAPIReadTx, id)
}

func webAPICatalogDelete[T, I any](tx *WebAPIWriteTx, resource webAPICatalogResource[T, I], ids []WebAPIID) (WebAPIDeleted, error) {
	result := WebAPIDeleted{DeletedIDs: append([]WebAPIID{}, ids...)}
	if len(ids) == 0 {
		return result, webAPIError(400, "empty_delete", "at least one resource is required")
	}
	before, err := resource.list(tx.WebAPIReadTx)
	if err != nil {
		return result, err
	}
	seen := map[WebAPIID]bool{}
	for _, id := range ids {
		if _, err := webAPIInteger(string(id), true); err != nil {
			return result, webAPIError(400, "invalid_id", "invalid resource identity")
		}
		if seen[id] {
			return result, webAPIError(409, "duplicate_id", "duplicate identity in command")
		}
		seen[id] = true
		if _, err = resource.load(tx.WebAPIReadTx, id); err != nil {
			return result, err
		}
	}
	for _, id := range ids {
		if err = resource.remove(tx, id); err != nil {
			return result, err
		}
	}
	after, err := resource.list(tx.WebAPIReadTx)
	if err != nil {
		return result, err
	}
	if resource.changed != nil {
		if err = resource.changed(tx, before, after); err != nil {
			return result, err
		}
	}
	return result, nil
}

func webAPICatalogReorder[T, I any](tx *WebAPIWriteTx, resource webAPICatalogResource[T, I], ids []WebAPIID) ([]T, error) {
	before, err := resource.list(tx.WebAPIReadTx)
	if err != nil {
		return nil, err
	}
	existing := make([]WebAPIID, len(before))
	byID := make(map[WebAPIID]T, len(before))
	for i, item := range before {
		existing[i] = resource.id(item)
		byID[existing[i]] = item
	}
	if err = webAPIExactIDs(ids, existing); err != nil {
		return nil, err
	}
	for i, id := range ids {
		if err = resource.save(tx, resource.order(byID[id], i)); err != nil {
			return nil, err
		}
	}
	return resource.list(tx.WebAPIReadTx)
}

func webAPICatalogReplace[T, I any](tx *WebAPIWriteTx, resource webAPICatalogResource[T, I], items []webAPICatalogReplacement[I]) ([]T, error) {
	before, err := resource.list(tx.WebAPIReadTx)
	if err != nil {
		return nil, err
	}
	seen := map[WebAPIID]bool{}
	after := make([]T, 0, len(items))
	for i, replacement := range items {
		var id WebAPIID
		var previous *T
		if replacement.ID != nil {
			id = *replacement.ID
			if seen[id] {
				return nil, webAPIError(409, "duplicate_id", "duplicate replacement identity")
			}
			old, e := resource.load(tx.WebAPIReadTx, id)
			if e != nil {
				return nil, e
			}
			previous = &old
		} else {
			id, err = tx.AllocateID()
			if err != nil {
				return nil, err
			}
		}
		seen[id] = true
		item, err := resource.build(id, replacement.Input, previous)
		if err != nil {
			return nil, err
		}
		after = append(after, resource.order(item, i))
	}
	if err = webAPICatalogDuplicate(resource, after); err != nil {
		return nil, err
	}
	for i, item := range after {
		if items[i].ID == nil {
			err = resource.insert(tx, item)
		} else {
			err = resource.save(tx, item)
		}
		if err != nil {
			return nil, err
		}
	}
	for _, item := range before {
		if !seen[resource.id(item)] {
			if err = resource.remove(tx, resource.id(item)); err != nil {
				return nil, err
			}
		}
	}
	if resource.changed != nil {
		if err = resource.changed(tx, before, after); err != nil {
			return nil, err
		}
	}
	return resource.list(tx.WebAPIReadTx)
}

func webAPICatalogChanges[T, I any](resource webAPICatalogResource[T, I], before, after []T) (map[string]string, []string) {
	kept := make(map[WebAPIID]T, len(after))
	for _, item := range after {
		kept[resource.id(item)] = item
	}
	renames := map[string]string{}
	var removed []string
	for _, item := range before {
		if next, exists := kept[resource.id(item)]; exists {
			if resource.name(item) != resource.name(next) {
				renames[resource.name(item)] = resource.name(next)
			}
		} else {
			removed = append(removed, resource.name(item))
		}
	}
	return renames, removed
}

func webAPISortIntResources[T any](items []T, id func(T) WebAPIID, order func(T) *int32) []T {
	sort.Slice(items, func(i, j int) bool {
		a, b := order(items[i]), order(items[j])
		if a != nil && b != nil && *a != *b {
			return *a < *b
		}
		if (a == nil) != (b == nil) {
			return a != nil
		}
		return webAPIIDLess(id(items[i]), id(items[j]))
	})
	return items
}

func webAPISortStringResources[T any](items []T, id func(T) WebAPIID, order func(T) *string) []T {
	sort.Slice(items, func(i, j int) bool {
		a, b := order(items[i]), order(items[j])
		if a != nil && b != nil && *a != *b {
			return *a < *b
		}
		if (a == nil) != (b == nil) {
			return a != nil
		}
		return webAPIIDLess(id(items[i]), id(items[j]))
	})
	return items
}

type webAPIScriptInput struct {
	Label string  `json:"label"`
	URL   *string `json:"url"`
}
type webAPIProviderInput struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}
type webAPIModuleInput struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	URL         *string           `json:"url"`
	Enabled     bool              `json:"enabled"`
	Arguments   map[string]string `json:"arguments"`
	System      *string           `json:"system"`
}
type webAPIIconSetInput struct {
	Name  string              `json:"name"`
	URL   string              `json:"url"`
	Icons []WebAPIIconSetIcon `json:"icons"`
}
type webAPIGroupInput = WebAPIProxyGroupInput
type webAPIProxyInput struct {
	Definition   WebAPIProxyDefinition `json:"definition"`
	EmptyOptions []string              `json:"emptyOptions"`
}

func webAPIScriptResource() webAPICatalogResource[WebAPIScript, webAPIScriptInput] {
	return webAPICatalogResource[WebAPIScript, webAPIScriptInput]{
		list: func(tx *WebAPIReadTx) ([]WebAPIScript, error) {
			items, err := tx.Scripts()
			return webAPISortIntResources(items, func(v WebAPIScript) WebAPIID { return v.ID }, func(v WebAPIScript) *int32 { return v.Order }), err
		},
		load: (*WebAPIReadTx).Script, insert: (*WebAPIWriteTx).InsertScript, save: (*WebAPIWriteTx).SaveScript,
		remove: func(tx *WebAPIWriteTx, id WebAPIID) error {
			if err := webAPICatalogDeleteContents(tx, WebAPIContentScript, id); err != nil {
				return err
			}
			return tx.DeleteScript(id)
		},
		id: func(v WebAPIScript) WebAPIID { return v.ID }, name: func(v WebAPIScript) string { return v.Label },
		order: func(v WebAPIScript, i int) WebAPIScript { order := int32(i); v.Order = &order; return v },
		build: func(id WebAPIID, input webAPIScriptInput, old *WebAPIScript) (WebAPIScript, error) {
			v := WebAPIScript{ID: id, LastUpdateTime: time.Now().UTC().Format(time.RFC3339Nano)}
			if old != nil {
				v = *old
			}
			if !webAPICatalogName(input.Label) || input.URL != nil && !webAPICatalogURL(*input.URL, false) {
				return v, webAPIError(400, "invalid_script", "script requires a name and an optional HTTP(S) URL")
			}
			v.Label, v.URL = input.Label, input.URL
			return v, nil
		},
		changed: func(tx *WebAPIWriteTx, _, _ []WebAPIScript) error {
			profiles, err := tx.Profiles()
			if err != nil {
				return err
			}
			var references []webAPICatalogReference
			for _, profile := range profiles {
				if profile.ScriptID != nil {
					if _, e := tx.Script(*profile.ScriptID); e != nil {
						references = append(references, webAPICatalogReference{Resource: "profiles", ID: profile.ID, Path: "scriptId", Name: string(*profile.ScriptID)})
					}
				}
			}
			return webAPICatalogReferenced(references)
		},
	}
}

func webAPIProviderResource() webAPICatalogResource[WebAPIClashProvider, webAPIProviderInput] {
	var resource webAPICatalogResource[WebAPIClashProvider, webAPIProviderInput]
	resource = webAPICatalogResource[WebAPIClashProvider, webAPIProviderInput]{
		list: func(tx *WebAPIReadTx) ([]WebAPIClashProvider, error) {
			items, err := tx.ClashProviders()
			return webAPISortIntResources(items, func(v WebAPIClashProvider) WebAPIID { return v.ID }, func(v WebAPIClashProvider) *int32 { return v.Order }), err
		},
		load: (*WebAPIReadTx).ClashProvider, insert: (*WebAPIWriteTx).InsertClashProvider, save: (*WebAPIWriteTx).SaveClashProvider,
		remove: func(tx *WebAPIWriteTx, id WebAPIID) error {
			if err := webAPICatalogDeleteContents(tx, WebAPIContentRuleProvider, id); err != nil {
				return err
			}
			return tx.DeleteClashProvider(id)
		},
		id: func(v WebAPIClashProvider) WebAPIID { return v.ID }, name: func(v WebAPIClashProvider) string { return v.Label },
		order: func(v WebAPIClashProvider, i int) WebAPIClashProvider { order := int32(i); v.Order = &order; return v },
		build: func(id WebAPIID, input webAPIProviderInput, old *WebAPIClashProvider) (WebAPIClashProvider, error) {
			v := WebAPIClashProvider{ID: id, Behavior: "classical", Format: "yaml"}
			if old != nil {
				v = *old
			}
			if !webAPICatalogName(input.Label) || !webAPICatalogURL(input.URL, true) {
				return v, webAPIError(400, "invalid_provider", "rule provider requires an unambiguous name and an optional HTTP(S) URL")
			}
			v.Label, v.URL = input.Label, input.URL
			return v, nil
		},
		changed: func(tx *WebAPIWriteTx, before, after []WebAPIClashProvider) error {
			renames, removed := webAPICatalogChanges(resource, before, after)
			if err := webAPIRenameCatalogReferences(tx, "rule-provider", "", renames); err != nil {
				return err
			}
			if err := webAPIRejectCatalogReferences(tx, "rule-provider", "", removed); err != nil {
				return err
			}
			return RefreshWebAPICatalog(tx)
		},
	}
	return resource
}

func webAPIModuleResource() webAPICatalogResource[WebAPIMitmModule, webAPIModuleInput] {
	return webAPICatalogResource[WebAPIMitmModule, webAPIModuleInput]{
		allowDuplicateNames: true,
		list: func(tx *WebAPIReadTx) ([]WebAPIMitmModule, error) {
			items, err := tx.MitmModules()
			return webAPISortIntResources(items, func(v WebAPIMitmModule) WebAPIID { return v.ID }, func(v WebAPIMitmModule) *int32 { return v.Order }), err
		},
		load: (*WebAPIReadTx).MitmModule, insert: (*WebAPIWriteTx).InsertMitmModule, save: (*WebAPIWriteTx).SaveMitmModule,
		remove: func(tx *WebAPIWriteTx, id WebAPIID) error {
			if err := webAPICatalogDeleteContents(tx, WebAPIContentMitmModule, id); err != nil {
				return err
			}
			return tx.DeleteMitmModule(id)
		},
		id: func(v WebAPIMitmModule) WebAPIID { return v.ID }, name: func(v WebAPIMitmModule) string { return v.Name },
		order: func(v WebAPIMitmModule, i int) WebAPIMitmModule { order := int32(i); v.Order = &order; return v },
		build: func(id WebAPIID, input webAPIModuleInput, old *WebAPIMitmModule) (WebAPIMitmModule, error) {
			v := WebAPIMitmModule{ID: id, Resources: map[string]string{}, LastUpdateTime: time.Now().UTC().Format(time.RFC3339Nano)}
			if old != nil {
				v = *old
			}
			if !webAPICatalogName(input.Name) || len(input.Description) > 8192 || input.URL != nil && !webAPICatalogURL(*input.URL, false) {
				return v, webAPIError(400, "invalid_module", "invalid MITM module metadata")
			}
			if input.System != nil && (len(*input.System) > 1024 || strings.ContainsAny(*input.System, "\x00\r\n")) {
				return v, webAPIError(400, "invalid_module_system", "invalid MITM module system metadata")
			}
			for key, value := range input.Arguments {
				if key == "" || len(key) > 256 || len(value) > 8192 || strings.ContainsAny(key+value, "\x00\r\n") {
					return v, webAPIError(400, "invalid_module_arguments", "invalid MITM argument")
				}
			}
			if old != nil && old.Scripts > 0 && !reflect.DeepEqual(old.Arguments, input.Arguments) {
				return v, webAPIError(409, "content_command_required", "script-bearing module arguments require the content validation command")
			}
			v.Name, v.Description, v.URL, v.Enabled, v.Arguments, v.System = input.Name, input.Description, input.URL, input.Enabled, input.Arguments, input.System
			if v.Arguments == nil {
				v.Arguments = map[string]string{}
			}
			return v, nil
		},
		changed: func(tx *WebAPIWriteTx, before, after []WebAPIMitmModule) error {
			old := map[WebAPIID]WebAPIMitmModule{}
			for _, v := range before {
				old[v.ID] = v
			}
			for _, v := range after {
				previous, exists := old[v.ID]
				if !exists || reflect.DeepEqual(previous.Arguments, v.Arguments) {
					continue
				}
				if _, err := tx.Content(WebAPIContentKey{Kind: WebAPIContentMitmModule, ID: v.ID}); err == nil {
					return webAPIError(409, "content_command_required", "module arguments require the atomic content validation command")
				} else {
					var e *WebAPIError
					if !errors.As(err, &e) || e.Status != 404 {
						return err
					}
				}
			}
			return nil
		},
	}
}

func webAPIIconSetResource() webAPICatalogResource[WebAPIIconSet, webAPIIconSetInput] {
	return webAPICatalogResource[WebAPIIconSet, webAPIIconSetInput]{
		allowDuplicateNames: true,
		list: func(tx *WebAPIReadTx) ([]WebAPIIconSet, error) {
			items, err := tx.IconSets()
			return webAPISortIntResources(items, func(v WebAPIIconSet) WebAPIID { return v.ID }, func(v WebAPIIconSet) *int32 { return v.Order }), err
		},
		load: (*WebAPIReadTx).IconSet, insert: (*WebAPIWriteTx).InsertIconSet, save: (*WebAPIWriteTx).SaveIconSet, remove: (*WebAPIWriteTx).DeleteIconSet,
		id: func(v WebAPIIconSet) WebAPIID { return v.ID }, name: func(v WebAPIIconSet) string { return v.Name },
		order: func(v WebAPIIconSet, i int) WebAPIIconSet { order := int32(i); v.Order = &order; return v },
		build: func(id WebAPIID, input webAPIIconSetInput, old *WebAPIIconSet) (WebAPIIconSet, error) {
			v := WebAPIIconSet{ID: id}
			if old != nil {
				v = *old
			}
			if !webAPICatalogName(input.Name) || !webAPICatalogURL(input.URL, true) {
				return v, webAPIError(400, "invalid_icon_set", "icon set requires a name and an optional HTTP(S) URL")
			}
			seen := map[string]bool{}
			for _, icon := range input.Icons {
				if len(icon.Name) > 256 || strings.ContainsAny(icon.Name, "\x00\r\n") || !webAPICatalogURL(icon.URL, false) || seen[icon.URL] {
					return v, webAPIError(400, "invalid_icon", "icon set has an invalid or duplicate icon")
				}
				seen[icon.URL] = true
			}
			if old == nil || !reflect.DeepEqual(old.Icons, input.Icons) {
				now := time.Now().UTC().Format(time.RFC3339Nano)
				v.LastUpdateTime = &now
			}
			v.Name, v.URL, v.Icons = input.Name, input.URL, input.Icons
			if v.Icons == nil {
				v.Icons = []WebAPIIconSetIcon{}
			}
			return v, nil
		},
	}
}

func webAPICatalogDeleteContents(tx *WebAPIWriteTx, kind WebAPIContentKind, id WebAPIID) error {
	contents, err := tx.Contents()
	if err != nil {
		return err
	}
	for _, content := range contents {
		if content.Key.ID == id && (content.Key.Kind == kind || kind == WebAPIContentMitmModule && content.Key.Kind == WebAPIContentMitmResource) {
			if err := tx.DeleteContent(content.Key); err != nil {
				return err
			}
		}
	}
	return nil
}

func webAPIClone[T any](value T) T {
	data, _ := json.Marshal(value)
	var result T
	_ = json.Unmarshal(data, &result)
	return result
}
