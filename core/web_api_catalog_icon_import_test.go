//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWebAPICatalogContractIconImportAtomicURLReuseAndReplayHTTP(t *testing.T) {
	var hits atomic.Int64
	var malformed atomic.Bool
	options := catalogSourceHTTPOptions(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		if request.Method != "GET" {
			t.Error(request.Method)
		}
		if malformed.Load() {
			_, _ = w.Write([]byte(`{"icons":[]}`))
			return
		}
		name := strings.TrimPrefix(request.URL.Path, "/")
		_, _ = w.Write([]byte(`{"name":"Gallery","description":"native-compatible metadata","icons":[{"url":"https://icons.unit.invalid/` + name + `.png"},null,{"url":"file:///not/used"}]}`))
	}))
	r := newCatalogTestRig(t, options)
	command := catalogMutation("0", "import-canonical-gallery", map[string]any{"source": map[string]any{"url": "http://gallery.unit.invalid/one"}})
	first := r.call("POST", "/api/v1/icon-sets:import", command, 200)
	imported := catalogData[WebAPIIconSet](t, first)
	if imported.Revision != "1" || imported.Data.ID == "" || len(imported.Data.Icons) != 1 || imported.Data.Name != "Gallery" || r.commits.Load() != 1 {
		t.Fatal(imported, r.commits.Load())
	}
	if replay := r.call("POST", "/api/v1/icon-sets:import", command, 200); !bytes.Equal(first, replay) || hits.Load() != 1 {
		t.Fatal("replay fetched or changed import")
	}
	reused := catalogData[WebAPIIconSet](t, r.call("POST", "/api/v1/icon-sets:import", catalogMutation(r.revision(), "import-reuse-same-url", map[string]any{"name": "Custom display", "source": map[string]any{"url": "http://gallery.unit.invalid/one"}}), 200))
	if reused.Data.ID != imported.Data.ID || reused.Data.Name != "Custom display" {
		t.Fatal(reused)
	}
	before, revision := r.snapshot(), r.revision()
	malformed.Store(true)
	r.call("POST", "/api/v1/icon-sets:import", catalogMutation(revision, "import-invalid-source", map[string]any{"source": map[string]any{"url": "http://gallery.unit.invalid/bad"}}), 400)
	if r.revision() != revision || !bytes.Equal(before, r.snapshot()) {
		t.Fatal("failed import created an empty gallery")
	}
	malformed.Store(false)
	count := hits.Load()
	r.call("POST", "/api/v1/icon-sets:import", catalogMutation(revision, "import-unknown-id", map[string]any{"id": "999999", "source": map[string]any{"url": "http://gallery.unit.invalid/two"}}), 404)
	r.call("POST", "/api/v1/icon-sets:import", catalogMutation(revision, "import-no-relay-fields", map[string]any{"source": map[string]any{"url": "http://gallery.unit.invalid/two", "headers": map[string]string{"Authorization": "not-a-header-api"}}}), 400)
	if hits.Load() != count {
		t.Fatal("invalid command fetched a source")
	}
	updated := catalogData[WebAPIIconSet](t, r.call("POST", "/api/v1/icon-sets:import", catalogMutation(revision, "import-edit-url-atomic", map[string]any{"id": imported.Data.ID, "source": map[string]any{"url": "http://gallery.unit.invalid/two"}}), 200))
	if updated.Data.ID != imported.Data.ID || updated.Data.URL != "http://gallery.unit.invalid/two" || updated.Data.Name != "Custom display" || !strings.Contains(updated.Data.Icons[0].URL, "two.png") || updated.Revision != catalogNextRevision(revision) {
		t.Fatal(updated)
	}
	lost := catalogMutation(r.revision(), "import-lost-response", map[string]any{"source": map[string]any{"url": "http://gallery.unit.invalid/lost"}})
	r.drop.Store(true)
	if body := r.call("POST", "/api/v1/icon-sets:import", lost, 200); len(body) != 0 {
		t.Fatal("response was not dropped")
	}
	count = hits.Load()
	revision = r.revision()
	recovered := r.call("POST", "/api/v1/icon-sets:import", lost, 200)
	if r.revision() != revision || hits.Load() != count || !bytes.Equal(recovered, r.call("GET", "/api/v1/operations/import-lost-response", nil, 200)) {
		t.Fatal("unknown result import fetched or committed twice")
	}
}

func TestWebAPICatalogContractPrivateProspectiveIconImportJobHTTP(t *testing.T) {
	var dials atomic.Int64
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		name := strings.TrimPrefix(request.URL.Path, "/")
		_, _ = w.Write([]byte(`{"name":"Private Gallery","icons":[{"name":"` + name + `","url":"https://icons.unit.invalid/` + name + `"}]}`))
	}))
	defer source.Close()
	options := &WebAPISourceFetchOptions{LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.1.2.3")}, nil
	}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		if address != "10.1.2.3:80" {
			t.Error(address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, source.Listener.Addr().String())
	}}
	r := newCatalogTestRig(t, options)
	r.session.Store("synthetic-icon-import-session")
	grantFor := func(id *WebAPIID, path string, index int) WebAPISourceAuthorizationResult {
		input := map[string]any{"expectedRevision": r.revision(), "purpose": "icon-set-import", "url": "http://lan.test/" + path}
		if id != nil {
			input["key"] = map[string]any{"kind": "icon-set", "id": *id}
		}
		prepared := catalogData[WebAPISourcePreparation](t, r.call("POST", "/api/v1/sources:prepare", input, 200)).Data
		if !prepared.RequiresPrivateNetworkConfirmation {
			t.Fatal(prepared)
		}
		return catalogData[WebAPISourceAuthorizationResult](t, r.call("POST", "/api/v1/sources:authorize", map[string]any{"expectedRevision": r.revision(), "operationId": "private-import-authorize-" + strconv.Itoa(index), "preparationId": prepared.PreparationID, "urlDigest": prepared.URLDigest, "intent": "authorize-server-private-source"}, 200)).Data
	}
	grant := grantFor(nil, "old", 1)
	r.call("POST", "/api/v1/icon-sets:import", catalogMutation("0", "wrong-import-job", map[string]any{"source": map[string]any{"url": "http://lan.test/old", "authorizationId": grant.AuthorizationID}}), 403)
	if dials.Load() != 0 || r.revision() != "0" {
		t.Fatal("grant was not import-operation bound")
	}
	created := catalogData[WebAPIIconSet](t, r.call("POST", "/api/v1/icon-sets:import", catalogMutation("0", grant.ImportOperationID, map[string]any{"source": map[string]any{"url": "http://lan.test/old", "authorizationId": grant.AuthorizationID}}), 200)).Data
	if dials.Load() != 1 || created.ID == "" {
		t.Fatal(dials.Load(), created)
	}
	prospective := grantFor(&created.ID, "new", 2)
	updated := catalogData[WebAPIIconSet](t, r.call("POST", "/api/v1/icon-sets:import", catalogMutation("1", prospective.ImportOperationID, map[string]any{"id": created.ID, "source": map[string]any{"url": "http://lan.test/new", "authorizationId": prospective.AuthorizationID}}), 200)).Data
	if updated.ID != created.ID || updated.URL != "http://lan.test/new" || updated.Icons[0].Name != "new" || r.revision() != "2" || dials.Load() != 2 {
		t.Fatal(updated, r.revision(), dials.Load())
	}
	listed := catalogData[[]WebAPIIconSet](t, r.call("GET", "/api/v1/icon-sets", nil, 200)).Data
	if len(listed) != 1 {
		t.Fatal("URL edit was not one resource transition", listed)
	}
}
