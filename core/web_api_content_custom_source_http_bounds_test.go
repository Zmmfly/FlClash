//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type webCustomSourceLeaseWriter struct {
	*httptest.ResponseRecorder
	leases *atomic.Int32
	t      *testing.T
}

func (w webCustomSourceLeaseWriter) WriteHeader(status int) {
	if w.leases.Load() != 0 {
		w.t.Error("authorization lease crossed response write")
	}
	w.ResponseRecorder.WriteHeader(status)
}
func (w webCustomSourceLeaseWriter) Write(data []byte) (int, error) {
	if w.leases.Load() != 0 {
		w.t.Error("authorization lease crossed response write")
	}
	return w.ResponseRecorder.Write(data)
}

func TestWebCustomSourcePreparationDoesNotHoldAuthorizationAcrossResponseWrite(t *testing.T) {
	f := newWebCustomSourceHTTPRig(t, false, "proxies: []\n")
	mux := http.NewServeMux()
	f.custom.RegisterRoutes(mux)
	var leases atomic.Int32
	ctx := WithWebAPIRequestAuthorization(context.Background(), "synthetic-response-session", func(context.Context) (func(), error) { leases.Add(1); return func() { leases.Add(-1) }, nil })
	data, _ := json.Marshal(WebAPICustomSourcePrepare{ExpectedRevision: f.revision, ProfileID: f.owner, URL: f.url + "/root"})
	request := httptest.NewRequest("POST", "http://api.test/api/v1/profiles:prepare-custom-import", bytes.NewReader(data)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	writer := webCustomSourceLeaseWriter{ResponseRecorder: httptest.NewRecorder(), leases: &leases, t: t}
	mux.ServeHTTP(writer, request)
	if writer.Code != 200 || leases.Load() != 0 {
		t.Fatalf("prepare %d: %s, leases=%d", writer.Code, writer.Body.String(), leases.Load())
	}
}

func TestWebCustomSourceHTTPNoRelayFieldsBodyBoundOrImplicitAuthorization(t *testing.T) {
	f := newWebCustomSourceHTTPRig(t, false, "proxies: []\n")
	for _, key := range []string{"savePath", "storagePath", "headers", "method", "purpose", "body", "importJobId", "sourceReference"} {
		request := map[string]any{"expectedRevision": f.revision, "profileId": f.owner, "url": f.url + "/root", key: "forbidden"}
		status, body := f.request(t, "POST", "/api/v1/profiles:prepare-custom-import", request, f.cookie)
		if status != 400 {
			t.Fatalf("generic relay field %s accepted %d: %s", key, status, body)
		}
	}
	status, _ := f.request(t, "POST", "/api/v1/profiles:prepare-custom-import", map[string]any{"expectedRevision": f.revision, "profileId": f.owner, "url": strings.Repeat("x", (1<<20)+1)}, f.cookie)
	if status != 413 {
		t.Fatalf("oversized JSON accepted %d", status)
	}
	if f.fetches.Load() != 0 {
		t.Fatal("invalid prepare performed network work")
	}
	f.requireUnchanged(t)
}

func TestWebCustomSourceHTTPRootGrantCannotAuthorizeDependency(t *testing.T) {
	f := newWebCustomSourceHTTPRig(t, true, "proxy-providers: {p: {type: http, url: SOURCE/proxy}}\nproxy-groups: [{name: G, type: select, use: [p]}]\n")
	root := f.authorize(t, WebAPISourcePrepare{ExpectedRevision: f.revision, Purpose: webCustomSourceRootPurpose, Key: &WebAPISourceResource{Kind: "profile", ID: string(f.owner)}, URL: f.url + "/root"}, "separate-root-authority")
	job := f.prepare(t, root)
	status, body := f.request(t, "POST", "/api/v1/profiles:import-custom", f.confirm(job, root), f.cookie)
	if status != 409 || f.fetches.Load() != 1 {
		t.Fatalf("root grant escaped into child source %d: %s", status, body)
	}
	f.requireUnchanged(t)
	job = f.prepare(t, root)
	status, body = f.request(t, "POST", "/api/v1/profiles:import-custom", f.confirm(job), f.cookie)
	if status != 403 || f.fetches.Load() != 2 {
		t.Fatalf("missing child grant accepted %d: %s", status, body)
	}
	f.requireUnchanged(t)
}
