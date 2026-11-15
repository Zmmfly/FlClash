//go:build webpanel && !android

package core

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWebAPICatalogContractIconRefreshFixedSourceReplayAndRollbackHTTP(t *testing.T) {
	var hits atomic.Int64
	var source atomic.Value
	source.Store(`{"name":"Remote name must not overwrite a custom name","icons":[{"name":"new","url":"https://icons.unit.invalid/new.svg"}]}`)
	var overLimit atomic.Bool
	sourceOptions := catalogSourceHTTPOptions(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		if request.Method != "GET" || request.URL.Path != "/icons.json" || request.Header.Get("Cookie") != "" {
			t.Error("refresh became a relay", request.Method, request.URL.String())
		}
		if overLimit.Load() {
			w.Header().Set("Content-Length", strconv.Itoa(WebAPIMaxContentBytes+1))
		}
		_, _ = w.Write([]byte(source.Load().(string)))
	}))
	r := newCatalogTestRig(t, sourceOptions)
	iconSet := catalogData[WebAPIIconSet](t, r.call("POST", "/api/v1/icon-sets:create", catalogMutation("0", "create-remote-gallery", map[string]any{"iconSet": map[string]any{"name": "Custom name", "url": "http://gallery.unit.invalid/icons.json", "icons": []any{}}}), 200)).Data
	command := catalogMutation(r.revision(), "refresh-remote-gallery", map[string]any{"id": iconSet.ID})
	first := r.call("POST", "/api/v1/icon-sets:refresh", command, 200)
	updated := catalogData[WebAPIIconSet](t, first)
	if updated.Data.Name != "Custom name" || len(updated.Data.Icons) != 1 || updated.Data.Icons[0].Name != "new" || updated.Data.LastUpdateTime == nil || hits.Load() != 1 {
		t.Fatal(updated, hits.Load())
	}
	if !bytes.Equal(first, r.call("POST", "/api/v1/icon-sets:refresh", command, 200)) || hits.Load() != 1 {
		t.Fatal("refresh replay fetched or changed content")
	}
	r.call("POST", "/api/v1/icon-sets:refresh", catalogMutation("0", "stale-gallery-refresh", map[string]any{"id": iconSet.ID}), 409)
	r.call("POST", "/api/v1/icon-sets:refresh", catalogMutation(r.revision(), "relay-gallery-refresh", map[string]any{"id": iconSet.ID, "url": "https://other.unit.invalid"}), 400)
	if hits.Load() != 1 {
		t.Fatal("invalid/stale refresh fetched before CAS or field validation")
	}
	before, revision := r.snapshot(), r.revision()
	for i, body := range []string{`{"icons":[]}`, `{"icons":[{"name":"bad","url":"file:///tmp/no"}]}`, `{"icons":[],"icons":[]}`} {
		source.Store(body)
		r.call("POST", "/api/v1/icon-sets:refresh", catalogMutation(revision, "invalid-gallery-"+strconv.Itoa(i), map[string]any{"id": iconSet.ID}), 400)
		if r.revision() != revision || !bytes.Equal(before, r.snapshot()) {
			t.Fatal("invalid source partially replaced metadata")
		}
	}
	overLimit.Store(true)
	r.call("POST", "/api/v1/icon-sets:refresh", catalogMutation(revision, "oversized-gallery-source", map[string]any{"id": iconSet.ID}), 413)
	if r.revision() != revision {
		t.Fatal("oversized source committed")
	}
}

func TestWebAPICatalogContractIconRefreshConcurrentMetadataCAS(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	sourceOptions := catalogSourceHTTPOptions(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"icons":[{"name":"old-source","url":"https://unit.invalid/old"}]}`))
	}))
	r := newCatalogTestRig(t, sourceOptions)
	iconSet := catalogData[WebAPIIconSet](t, r.call("POST", "/api/v1/icon-sets:create", catalogMutation("0", "create-download-gallery", map[string]any{"iconSet": map[string]any{"name": "Gallery", "url": "http://gallery.unit.invalid/old", "icons": []any{}}}), 200)).Data
	code := make(chan int, 1)
	go func() {
		code <- r.callIfAny("POST", "/api/v1/icon-sets:refresh", catalogMutation("1", "download-with-concurrent-edit", map[string]any{"id": iconSet.ID}))
	}()
	<-started
	r.call("POST", "/api/v1/icon-sets:update", catalogMutation("1", "edit-gallery-during-download", map[string]any{"id": iconSet.ID, "iconSet": map[string]any{"name": "Renamed", "url": "http://gallery.unit.invalid/new", "icons": []any{}}}), 200)
	close(release)
	if status := <-code; status != 409 {
		t.Fatal(status)
	}
	listed := catalogData[[]WebAPIIconSet](t, r.call("GET", "/api/v1/icon-sets", nil, 200))
	if len(listed.Data) != 1 || listed.Data[0].Name != "Renamed" || listed.Data[0].URL != "http://gallery.unit.invalid/new" || len(listed.Data[0].Icons) != 0 {
		t.Fatal(listed)
	}
}

func TestWebAPICatalogContractBulkUpdateDoesNotInheritControlBodyCap(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	iconSet := catalogData[WebAPIIconSet](t, r.call("POST", "/api/v1/icon-sets:create", catalogMutation("0", "create-large-gallery", map[string]any{"iconSet": map[string]any{"name": "Large", "url": "", "icons": []any{}}}), 200)).Data
	icons := make([]map[string]any, 700)
	for i := range icons {
		icons[i] = map[string]any{"name": "icon-" + strconv.Itoa(i), "url": "https://icons.unit.invalid/" + strings.Repeat("a", 1600) + "/" + strconv.Itoa(i)}
	}
	body := r.call("POST", "/api/v1/icon-sets:update", catalogMutation("1", "update-large-gallery", map[string]any{"id": iconSet.ID, "iconSet": map[string]any{"name": "Large", "url": "", "icons": icons}}), 200)
	if len(body) <= 1<<20 || len(catalogData[WebAPIIconSet](t, body).Data.Icons) != len(icons) {
		t.Fatal("bulk update did not cross the control cap")
	}
	oversized := append([]byte(`{"expectedRevision":"2","operationId":"oversized-catalog-body","id":"1","iconSet":{"name":"`), bytes.Repeat([]byte("a"), WebAPIMaxBulkJSONBytes)...)
	status, _ := r.raw("POST", "/api/v1/icon-sets:update", oversized)
	if status != 413 || r.revision() != "2" {
		t.Fatal(status, r.revision())
	}
}
