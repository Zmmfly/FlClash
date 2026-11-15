//go:build webpanel && !android

package core

import (
	"encoding/json"
	"sort"
	"strconv"

	bolt "github.com/metacubex/bbolt"
)

type WebAPICatalogReadBudget struct {
	maxBytes  int64
	remaining int64
	tx        *bolt.Tx
	charged   map[string]bool
}

func NewWebAPICatalogReadBudget(maxBytes int64) *WebAPICatalogReadBudget {
	return &WebAPICatalogReadBudget{maxBytes: maxBytes, remaining: maxBytes, charged: map[string]bool{}}
}

func (budget *WebAPICatalogReadBudget) charge(tx *bolt.Tx, bucket string, key, raw []byte) error {
	if budget == nil || budget.maxBytes < 0 {
		return webAPIError(500, "invalid_catalog_budget", "catalog reads require a nonnegative scoped budget")
	}
	if budget.tx != nil && budget.tx != tx {
		return webAPIError(500, "invalid_catalog_budget", "catalog budget cannot be reused across snapshots")
	}
	budget.tx = tx
	identity := bucket + "\x00" + string(key)
	if budget.charged[identity] {
		return nil
	}
	size := int64(len(key)) + int64(len(raw))
	if size > budget.remaining {
		problem := webAPIError(413, "catalog_budget_exceeded", "profile and its referenced resources exceed the catalog read budget")
		problem.Details = map[string]any{"maxBytes": strconv.FormatInt(budget.maxBytes, 10)}
		return problem
	}
	budget.remaining -= size
	budget.charged[identity] = true
	return nil
}

func webAPICatalogGet[T any](tx *WebAPIReadTx, bucket, key string, budget *WebAPICatalogReadBudget) (T, error) {
	var value T
	raw := tx.tx.Bucket([]byte(bucket)).Get([]byte(key))
	if raw == nil {
		return value, webAPIError(404, "not_found", "catalog resource does not exist")
	}
	if err := budget.charge(tx.tx, bucket, []byte(key), raw); err != nil {
		return value, err
	}
	err := json.Unmarshal(raw, &value)
	return value, err
}

func (tx *WebAPIReadTx) CatalogProfile(id WebAPIID, budget *WebAPICatalogReadBudget) (WebAPIProfile, error) {
	return webAPICatalogGet[WebAPIProfile](tx, "profiles", string(id), budget)
}
func (tx *WebAPIReadTx) CatalogProfileCatalogState(id WebAPIID, budget *WebAPICatalogReadBudget) (WebAPIProfileCatalog, error) {
	return webAPICatalogGet[WebAPIProfileCatalog](tx, "profile-catalogs", string(id), budget)
}

func (tx *WebAPIReadTx) CatalogCustomProxy(id WebAPIID, budget *WebAPICatalogReadBudget) (WebAPICustomProxy, error) {
	return webAPICatalogGet[WebAPICustomProxy](tx, "custom-proxies", string(id), budget)
}
func (tx *WebAPIReadTx) CatalogContent(key WebAPIContentKey, budget *WebAPICatalogReadBudget) (WebAPIContent, error) {
	name, _, err := webAPIContentKey(key)
	if err != nil {
		return WebAPIContent{}, err
	}
	return webAPICatalogGet[WebAPIContent](tx, "content", name, budget)
}

func webAPICatalogOwned[T any](tx *WebAPIReadTx, bucket string, owner WebAPIID, budget *WebAPICatalogReadBudget) ([]T, error) {
	items := []T{}
	err := tx.tx.Bucket([]byte(bucket)).ForEach(func(key, raw []byte) error {
		var identity struct {
			ProfileID *string `json:"profileId"`
		}
		if err := json.Unmarshal(raw, &identity); err != nil {
			return err
		}
		if identity.ProfileID == nil || *identity.ProfileID != string(owner) {
			return nil
		}
		if err := budget.charge(tx.tx, bucket, key, raw); err != nil {
			return err
		}
		var value T
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		items = append(items, value)
		return nil
	})
	return items, err
}

func (tx *WebAPIReadTx) CatalogProxyGroups(owner WebAPIID, budget *WebAPICatalogReadBudget) ([]WebAPIProxyGroup, error) {
	items, err := webAPICatalogOwned[WebAPIProxyGroup](tx, "proxy-groups", owner, budget)
	sort.Slice(items, func(i, j int) bool {
		return webAPICatalogOrderLess(items[i].Order, items[j].Order, items[i].ID, items[j].ID)
	})
	return items, err
}
func (tx *WebAPIReadTx) CatalogRules(owner WebAPIID, budget *WebAPICatalogReadBudget) ([]WebAPIRule, error) {
	items, err := webAPICatalogOwned[WebAPIRule](tx, "rules", owner, budget)
	sort.Slice(items, func(i, j int) bool {
		return webAPICatalogOrderLess(items[i].Order, items[j].Order, items[i].ID, items[j].ID)
	})
	return items, err
}
func (tx *WebAPIReadTx) CatalogProxyDialers(owner WebAPIID, budget *WebAPICatalogReadBudget) ([]WebAPIProxyDialer, error) {
	items, err := webAPICatalogOwned[WebAPIProxyDialer](tx, "proxy-dialers", owner, budget)
	sort.Slice(items, func(i, j int) bool { return webAPIIDLess(items[i].ProxyID, items[j].ProxyID) })
	return items, err
}

func webAPICatalogOrderLess(a, b *string, aID, bID WebAPIID) bool {
	if a != nil && b != nil && *a != *b {
		return *a < *b
	}
	if (a == nil) != (b == nil) {
		return a != nil
	}
	return webAPIIDLess(aID, bID)
}

func webAPICatalogNamed[T any, P any](tx *WebAPIReadTx, bucket string, names []string, budget *WebAPICatalogReadBudget, nameOf func(P) (string, bool)) ([]T, error) {
	items := []T{}
	wanted := map[string]bool{}
	seen := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	if len(wanted) == 0 {
		return items, nil
	}
	err := tx.tx.Bucket([]byte(bucket)).ForEach(func(key, raw []byte) error {
		var projection P
		if err := json.Unmarshal(raw, &projection); err != nil {
			return err
		}
		name, eligible := nameOf(projection)
		if !eligible || !wanted[name] {
			return nil
		}
		if seen[name] {
			return webAPIError(409, "ambiguous_catalog_identity", "multiple resources have the requested catalog identity")
		}
		seen[name] = true
		if err := budget.charge(tx.tx, bucket, key, raw); err != nil {
			return err
		}
		var item T
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	})
	return items, err
}

func (tx *WebAPIReadTx) CatalogCustomProxiesByNames(names []string, budget *WebAPICatalogReadBudget) ([]WebAPICustomProxy, error) {
	type projection struct {
		Definition struct {
			Name string `json:"name"`
		} `json:"definition"`
	}
	return webAPICatalogNamed[WebAPICustomProxy, projection](tx, "custom-proxies", names, budget, func(value projection) (string, bool) { return value.Definition.Name, true })
}

func (tx *WebAPIReadTx) CatalogProviderProfilesByNames(names []string, budget *WebAPICatalogReadBudget) ([]WebAPIProfile, error) {
	type projection struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Label string `json:"label"`
		URL   string `json:"url"`
	}
	return webAPICatalogNamed[WebAPIProfile, projection](tx, "profiles", names, budget, func(value projection) (string, bool) {
		if value.Type != "file" && value.Type != "url" {
			return "", false
		}
		return webAPIEffectiveProfileLabel(WebAPIProfile{ID: WebAPIID(value.ID), WebAPIProfileInput: WebAPIProfileInput{Type: value.Type, Label: value.Label, URL: value.URL}}), true
	})
}

func (tx *WebAPIReadTx) CatalogClashProvidersByNames(names []string, budget *WebAPICatalogReadBudget) ([]WebAPIClashProvider, error) {
	type projection struct {
		Label string `json:"label"`
	}
	return webAPICatalogNamed[WebAPIClashProvider, projection](tx, "rule-providers", names, budget, func(value projection) (string, bool) { return value.Label, true })
}
