//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type webDAVHTTPFixture struct {
	*webContentHTTPFixture
	dav    *WebAPIDAVService
	mux    *http.ServeMux
	before func(http.ResponseWriter, *http.Request)
}

func newWebDAVHTTPFixture(t *testing.T, wire *webDAVTestWire) *webDAVHTTPFixture {
	t.Helper()
	root := t.TempDir()
	auth, err := NewWebServer(WebServerOptions{DataDir: filepath.Join(root, "auth-data"), Username: "admin", Password: "correct horse", SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { auth.Close() })
	options := WebAPIStoreOptions{DataDir: filepath.Join(root, "managed")}
	store, err := OpenWebAPIStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	f := mountWebDAVHTTPFixture(t, wire, store, options, auth)
	status, body := f.request(t, "POST", "/api/v1/settings:dav", map[string]any{"expectedRevision": "0", "operationId": "dav-settings-seed", "dav": WebAPISettingsDAV{URI: "http://dav.test/base", User: "synthetic-user", Password: "synthetic-password", FileName: "backup.zip"}}, f.cookie)
	if status != 200 {
		t.Fatalf("DAV seed %d: %s", status, body)
	}
	return f
}

func mountWebDAVHTTPFixture(t *testing.T, wire *webDAVTestWire, store *WebAPIStore, options WebAPIStoreOptions, auth *WebServer) *webDAVHTTPFixture {
	t.Helper()
	mux := http.NewServeMux()
	RegisterWebAPIResourceRoutes(mux, store)
	RegisterWebAPISettingsRoutes(mux, store)
	backup := RegisterWebAPIBackupRoutes(mux, store, WebAPIBackupOptions{})
	sources := RegisterWebAPISourceRoutes(mux, store, WebAPISourceOptions{WebAPISourceFetchOptions: WebAPISourceFetchOptions{LookupNetIP: wire.options().LookupNetIP, DialContext: wire.options().DialContext}})
	authority := newWebAPIServiceAuthority()
	t.Cleanup(authority.close)
	davOptions := wire.options()
	davOptions.StatusContext = authority.ctx
	dav, err := RegisterWebAPIDAVRoutes(mux, store, backup, davOptions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dav.Close() })
	f := &webDAVHTTPFixture{webContentHTTPFixture: &webContentHTTPFixture{store: store, storeOptions: options, auth: auth, backup: backup, sources: sources, cookie: webLogin(t, auth)}, dav: dav, mux: mux}
	guarded := WebAPIRuntimeSessionHandler(auth, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.before != nil {
			f.before(w, r)
		}
		if r.Header.Get("X-Test-Drop-Response") == "1" {
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, r)
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				connection.Close()
			}
			return
		}
		mux.ServeHTTP(w, r)
	}))
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/auth/") {
			auth.ServeHTTP(w, r)
			return
		}
		guarded.ServeHTTP(w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *webDAVHTTPFixture) job(t *testing.T, id string) WebAPIEnvelope[WebAPIDAVJob] {
	t.Helper()
	status, body := f.request(t, "GET", "/api/v1/dav/jobs/"+id, nil, f.cookie)
	if status != 200 {
		t.Fatalf("job %d: %s", status, body)
	}
	var result WebAPIEnvelope[WebAPIDAVJob]
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func webDAVSuccessfulWire() *webDAVTestWire {
	return &webDAVTestWire{handle: func(r *http.Request, _ []byte) *http.Response {
		if r.Method == "HEAD" {
			return webDAVTestResponse(404, nil, nil)
		}
		return webDAVTestResponse(204, nil, nil)
	}}
}

func TestWebDAVHTTPCommittedTupleProbeCASReplayTwoSessionsAndTypedBoundary(t *testing.T) {
	wire := webDAVSuccessfulWire()
	f := newWebDAVHTTPFixture(t, wire)
	request := WebAPIDAVProbe{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-probe-first"}, Intent: "probe-dav"}
	if status, _ := f.request(t, "POST", "/api/v1/dav:probe", request, nil); status != 401 {
		t.Fatalf("unauthorized %d", status)
	}
	status, first := f.request(t, "POST", "/api/v1/dav:probe", request, f.cookie)
	if status != 200 {
		t.Fatalf("probe %d: %s", status, first)
	}
	var accepted WebAPIEnvelope[WebAPIDAVJob]
	json.Unmarshal(first, &accepted)
	if accepted.Data.Phase != WebAPIDAVPending || accepted.Revision != "2" {
		t.Fatal("receipt falsely asserted remote completion")
	}
	job := f.job(t, request.OperationID)
	if job.Data.Phase != WebAPIDAVSucceeded || !job.Data.Result.Reachable {
		t.Fatalf("probe state %+v", job)
	}
	if status, replay := f.request(t, "POST", "/api/v1/dav:probe", request, f.cookie); status != 200 || !bytes.Equal(first, replay) || len(wire.requests) != 1 {
		t.Fatal("receipt replay repeated probe")
	}
	other := webLogin(t, f.auth)
	if status, _ := f.request(t, "GET", "/api/v1/dav/jobs/"+request.OperationID, nil, other); status != 403 {
		t.Fatalf("cross-session job %d", status)
	}
	if status, body := f.request(t, "POST", "/api/v1/dav:probe", request, other); status != 403 || !bytes.Contains(body, []byte("dav_session_mismatch")) {
		t.Fatalf("cross-session replay %d: %s", status, body)
	}
	request.OperationID = "dav-probe-stale"
	if status, _ := f.request(t, "POST", "/api/v1/dav:probe", request, other); status != 409 {
		t.Fatal("stale revision admitted")
	}
	for _, field := range []string{"url", "path", "headers", "method", "archiveBase64"} {
		bad := map[string]any{"expectedRevision": "2", "operationId": "bad-field-" + field, "intent": "probe-dav", field: "arbitrary"}
		if status, _ := f.request(t, "POST", "/api/v1/dav:probe", bad, f.cookie); status != 400 {
			t.Fatalf("accepted %s: %d", field, status)
		}
	}
	if status, _ := f.request(t, "DELETE", "/api/v1/dav:backup", nil, f.cookie); status != 405 {
		t.Fatal("unsupported method")
	}
	if status, _ := f.request(t, "POST", "/api/v1/dav:probe?url=http://other.test", request, f.cookie); status != 400 {
		t.Fatal("unexpected query")
	}
	if len(wire.requests) != 1 {
		t.Fatal("rejected request used the network")
	}
}

func TestWebDAVHTTPUploadIsServerArchiveAndSensitivityIsIndependent(t *testing.T) {
	for _, include := range []bool{false, true} {
		t.Run(map[bool]string{false: "without-dav", true: "with-dav"}[include], func(t *testing.T) {
			var uploaded []byte
			wire := &webDAVTestWire{handle: func(r *http.Request, body []byte) *http.Response {
				if r.Method == "HEAD" {
					return webDAVTestResponse(200, http.Header{"Etag": {`"before"`}}, nil)
				}
				if r.Method == "PUT" {
					uploaded = append([]byte{}, body...)
					if r.Header.Get("If-Match") != `"before"` {
						t.Error("missing overwrite guard")
					}
				}
				return webDAVTestResponse(204, http.Header{"Etag": {`"after"`}}, nil)
			}}
			f := newWebDAVHTTPFixture(t, wire)
			intent := "backup-to-dav"
			if include {
				intent = "backup-to-dav-with-credentials"
			}
			request := WebAPIDAVBackup{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-upload-server"}, Intent: intent, Format: "flclash-v1", IncludeDAVCredentials: include}
			if include {
				wrong := request
				wrong.Intent = "backup-to-dav"
				if status, _ := f.request(t, "POST", "/api/v1/dav:backup", wrong, f.cookie); status != 400 {
					t.Fatal("sensitive flag had no independent intent")
				}
			}
			status, body := f.request(t, "POST", "/api/v1/dav:backup", request, f.cookie)
			if status != 200 {
				t.Fatalf("backup %d: %s", status, body)
			}
			job := f.job(t, request.OperationID)
			if job.Data.Phase != WebAPIDAVSucceeded || !job.Data.Result.Conditional {
				t.Fatalf("upload job %+v", job.Data)
			}
			archive, err := webReadBackupArchive(uploaded)
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := webBackupTypedCandidate(archive)
			if err != nil {
				t.Fatal(err)
			}
			if (candidate.Data.Settings.DAV != nil) != include {
				t.Fatal("DAV tuple inclusion was not atomic")
			}
			if !include {
				for _, secret := range []string{"dav.test", "synthetic-user", "synthetic-password", "backup.zip"} {
					if bytes.Contains(archive.Entries["data.json"], []byte(secret)) {
						t.Fatalf("excluded DAV tuple leaked %s", secret)
					}
				}
			}
			if bytes.Contains(uploaded, []byte("correct horse")) {
				t.Fatal("backup included web authentication")
			}
			if status, _ := f.request(t, "POST", "/api/v1/dav:backup", request, f.cookie); status != 200 {
				t.Fatal("upload replay")
			}
			puts := 0
			for _, r := range wire.requests {
				if r.Method == "PUT" {
					puts++
				}
			}
			if puts != 1 {
				t.Fatal("upload replay sent bytes")
			}
		})
	}
}

func TestWebDAVHTTPLostRemoteAndBrowserResponsesRemainQueryableWithoutRetry(t *testing.T) {
	var uploaded []byte
	puts := 0
	wire := &webDAVTestWire{handle: func(r *http.Request, body []byte) *http.Response {
		switch r.Method {
		case "HEAD":
			return webDAVTestResponse(404, nil, nil)
		case "PUT":
			puts++
			uploaded = append([]byte{}, body...)
			return nil
		case "GET":
			return webDAVTestResponse(200, http.Header{"Etag": {`"stored"`}}, uploaded)
		default:
			return webDAVTestResponse(204, nil, nil)
		}
	}}
	f := newWebDAVHTTPFixture(t, wire)
	request := WebAPIDAVBackup{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-unknown-upload"}, Intent: "backup-to-dav", Format: "flclash-v1"}
	encoded, _ := json.Marshal(request)
	r, _ := http.NewRequest("POST", f.server.URL+"/api/v1/dav:backup", bytes.NewReader(encoded))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Test-Drop-Response", "1")
	r.AddCookie(f.cookie)
	if response, err := f.server.Client().Do(r); err == nil {
		response.Body.Close()
		t.Fatal("browser response was not dropped")
	}
	job := f.job(t, request.OperationID)
	if job.Data.Phase != WebAPIDAVUnknown || job.Data.Error.Code != "dav_effect_unknown" {
		t.Fatalf("lost response state %+v", job.Data)
	}
	if status, _ := f.request(t, "POST", "/api/v1/dav:backup", request, f.cookie); status != 200 || puts != 1 {
		t.Fatal("unknown result retried upload")
	}
	reconcile := WebAPIDAVReconcile{WebAPIMutation: WebAPIMutation{ExpectedRevision: "2", OperationID: "dav-explicit-reconcile"}, Intent: "reconcile-dav-backup"}
	if status, body := f.request(t, "POST", "/api/v1/dav/jobs/"+request.OperationID+":reconcile", reconcile, f.cookie); status != 200 {
		t.Fatalf("reconcile %d: %s", status, body)
	}
	checked := f.job(t, reconcile.OperationID)
	if checked.Data.Phase != WebAPIDAVSucceeded || !checked.Data.Result.RemoteMatches || puts != 1 {
		t.Fatalf("reconciliation %+v", checked.Data)
	}
	if f.job(t, request.OperationID).Data.Phase != WebAPIDAVUnknown {
		t.Fatal("observation rewrote historical uncertain effect")
	}
}

func TestWebDAVHTTPInvalidArchiveChangesNoApplicationData(t *testing.T) {
	wire := &webDAVTestWire{handle: func(*http.Request, []byte) *http.Response {
		return webDAVTestResponse(200, nil, []byte("not an archive"))
	}}
	f := newWebDAVHTTPFixture(t, wire)
	var before WebAPISettings
	f.store.View(t.Context(), func(tx *WebAPIReadTx) error { var err error; before, err = tx.Settings(); return err })
	request := WebAPIDAVPrepareRestore{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-invalid-download"}, Intent: "prepare-dav-restore", Strategy: "override"}
	status, body := f.request(t, "POST", "/api/v1/dav:prepare-restore", request, f.cookie)
	if status != 200 {
		t.Fatalf("accepted download %d: %s", status, body)
	}
	job := f.job(t, request.OperationID)
	if job.Data.Phase != WebAPIDAVFailed || job.Data.Result.Preparation != nil {
		t.Fatal("invalid archive produced a confirmation")
	}
	f.store.View(t.Context(), func(tx *WebAPIReadTx) error {
		after, err := tx.Settings()
		if err != nil {
			return err
		}
		a, _ := json.Marshal(before)
		b, _ := json.Marshal(after)
		if !bytes.Equal(a, b) {
			t.Error("invalid archive changed settings")
		}
		profiles, err := tx.Profiles()
		if len(profiles) != 0 {
			t.Error("invalid archive wrote profiles")
		}
		return err
	})
	if len(wire.requests) != 1 || wire.requests[0].Method != "GET" {
		t.Fatal("invalid archive caused an unrelated remote effect")
	}
}

func TestWebDAVHTTPRestorePreviewTokenTupleSensitivityAndNoArchiveConnection(t *testing.T) {
	for _, restoreDAV := range []bool{false, true} {
		t.Run(map[bool]string{false: "preserve-whole", true: "replace-whole"}[restoreDAV], func(t *testing.T) {
			var archive []byte
			wire := &webDAVTestWire{handle: func(*http.Request, []byte) *http.Response { return webDAVTestResponse(200, nil, archive) }}
			f := newWebDAVHTTPFixture(t, wire)
			var candidate webBackupCandidate
			_, err := f.store.View(t.Context(), func(tx *WebAPIReadTx) error { var err error; candidate, err = webBackupSnapshot(tx, true); return err })
			if err != nil {
				t.Fatal(err)
			}
			incoming := WebAPISettingsDAV{URI: "http://archive-endpoint.test/never-contact", User: "incoming-user", Password: "incoming-password", FileName: "incoming.zip"}
			candidate.Data.Settings.DAV = &incoming
			archive, err = webBackupEncodeTyped(candidate)
			if err != nil {
				t.Fatal(err)
			}
			intent := "prepare-dav-restore"
			if restoreDAV {
				intent = "prepare-dav-restore-with-credentials"
			}
			prepare := WebAPIDAVPrepareRestore{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-restore-preview"}, Intent: intent, Strategy: "override", RestoreDAVCredentials: restoreDAV}
			if status, body := f.request(t, "POST", "/api/v1/dav:prepare-restore", prepare, f.cookie); status != 200 {
				t.Fatalf("prepare %d: %s", status, body)
			}
			job := f.job(t, prepare.OperationID)
			if job.Data.Phase != WebAPIDAVPrepared || job.Data.Result.Preparation == nil {
				t.Fatalf("preview job %+v", job.Data)
			}
			preview := job.Data.Result.Preparation
			intent = "restore-dav-backup"
			if restoreDAV {
				intent = "restore-dav-backup-with-credentials"
			}
			restore := WebAPIDAVRestore{WebAPIMutation: WebAPIMutation{ExpectedRevision: "2", OperationID: "dav-restore-confirm"}, Intent: intent, PreparationOperationID: prepare.OperationID, Token: preview.Token, ArchiveDigest: preview.ArchiveDigest, PlanDigest: preview.PlanDigest, RestoreDAVCredentials: restoreDAV}
			wrong := restore
			wrong.PlanDigest = strings.Repeat("0", 64)
			if status, _ := f.request(t, "POST", "/api/v1/dav:restore", wrong, f.cookie); status != 409 {
				t.Fatal("tampered plan accepted")
			}
			other := webLogin(t, f.auth)
			if status, _ := f.request(t, "POST", "/api/v1/dav:restore", restore, other); status != 403 && status != 409 {
				t.Fatalf("cross-session restore %d", status)
			}
			status, first := f.request(t, "POST", "/api/v1/dav:restore", restore, f.cookie)
			if status != 200 {
				t.Fatalf("restore %d: %s", status, first)
			}
			if status, replay := f.request(t, "POST", "/api/v1/dav:restore", restore, f.cookie); status != 200 || !bytes.Equal(first, replay) {
				t.Fatal("confirmed restore replay")
			}
			if f.job(t, restore.OperationID).Data.Phase != WebAPIDAVSucceeded {
				t.Fatal("restore has no atomic succeeded job")
			}
			_, err = f.store.View(t.Context(), func(tx *WebAPIReadTx) error {
				settings, err := tx.Settings()
				if err != nil {
					return err
				}
				if restoreDAV {
					if settings.DAV == nil || *settings.DAV != incoming {
						t.Error("whole tuple was not replaced")
					}
				} else if settings.DAV == nil || settings.DAV.URI != "http://dav.test/base" || settings.DAV.Password != "synthetic-password" || settings.DAV.FileName != "backup.zip" {
					t.Error("current tuple was mixed")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(wire.requests) != 1 || wire.requests[0].Host != "dav.test:80" {
				t.Fatal("archive supplied DAV settings were contacted")
			}
		})
	}
}

func TestWebDAVHTTPLogoutWinsQueuedOperationAndResponseWriterDoesNotHoldAuth(t *testing.T) {
	wire := webDAVSuccessfulWire()
	f := newWebDAVHTTPFixture(t, wire)
	entered, release := make(chan struct{}), make(chan struct{})
	f.before = func(_ http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/dav:probe" {
			close(entered)
			<-release
		}
	}
	request := WebAPIDAVProbe{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-logout-queued"}, Intent: "probe-dav"}
	done := make(chan int, 1)
	go func() { status, _ := f.request(t, "POST", "/api/v1/dav:probe", request, f.cookie); done <- status }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("queued probe did not enter")
	}
	if status, _ := f.request(t, "POST", "/api/auth/logout", nil, f.cookie); status != 204 {
		close(release)
		t.Fatalf("logout %d", status)
	}
	close(release)
	select {
	case status := <-done:
		if status != 401 {
			t.Fatalf("queued probe %d", status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("logout blocked on operation")
	}
	if len(wire.requests) != 0 {
		t.Fatal("revoked queued request performed network effect")
	}
}

func TestWebDAVHTTPBodyBudgetAndServiceClose(t *testing.T) {
	wire := webDAVSuccessfulWire()
	f := newWebDAVHTTPFixture(t, wire)
	r, _ := http.NewRequest("POST", f.server.URL+"/api/v1/dav:probe", strings.NewReader(`{"expectedRevision":"1","operationId":"dav-large-request","intent":"`+strings.Repeat("x", webAPIMaxJSONBytes)+`"}`))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(f.cookie)
	response, err := f.server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 413 {
		t.Fatalf("body budget %d", response.StatusCode)
	}
	if err = f.dav.Close(); err != nil {
		t.Fatal(err)
	}
	request := WebAPIDAVProbe{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-after-close"}, Intent: "probe-dav"}
	if status, _ := f.request(t, "POST", "/api/v1/dav:probe", request, f.cookie); status != 503 {
		t.Fatalf("admitted after close: %d", status)
	}
	if len(wire.requests) != 0 {
		t.Fatal("invalid body or closed service used network")
	}
}
