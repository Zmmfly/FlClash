//go:build webpanel && !android

package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"testing"
	"time"
)

func (f *webDAVHTTPFixture) boundContext(t *testing.T) context.Context {
	t.Helper()
	r := httptest.NewRequest("GET", f.server.URL+"/api/v1/meta", nil)
	r.AddCookie(f.cookie)
	var ctx context.Context
	WebAPIRuntimeSessionHandler(f.auth, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() })).ServeHTTP(httptest.NewRecorder(), r)
	if ctx == nil {
		t.Fatal("fixture session did not bind")
	}
	return ctx
}

type webDAVBlockingWriter struct {
	http.ResponseWriter
	entered chan struct{}
	release chan struct{}
}

func (w *webDAVBlockingWriter) Write(b []byte) (int, error) {
	close(w.entered)
	<-w.release
	return w.ResponseWriter.Write(b)
}

func TestWebDAVHTTPLogoutDoesNotWaitForJobResponseWrite(t *testing.T) {
	f := newWebDAVHTTPFixture(t, webDAVSuccessfulWire())
	request := WebAPIDAVProbe{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-writer-probe"}, Intent: "probe-dav"}
	if status, body := f.request(t, "POST", "/api/v1/dav:probe", request, f.cookie); status != 200 {
		t.Fatalf("probe %d: %s", status, body)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(WebAPIRuntimeSessionHandler(f.auth, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mux.ServeHTTP(&webDAVBlockingWriter{w, entered, release}, r)
	})))
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		r, _ := http.NewRequest("GET", server.URL+"/api/v1/dav/jobs/"+request.OperationID, nil)
		r.AddCookie(f.cookie)
		response, err := server.Client().Do(r)
		if err == nil {
			response.Body.Close()
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("job response did not start")
	}
	loggedOut := make(chan int, 1)
	go func() { status, _ := f.request(t, "POST", "/api/auth/logout", nil, f.cookie); loggedOut <- status }()
	select {
	case status := <-loggedOut:
		if status != 204 {
			close(release)
			t.Fatalf("logout %d", status)
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("response Write retained auth lease")
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked response did not finish")
	}
}

func TestWebDAVHTTPCommittedTupleChangesDuringDNSBeforeDial(t *testing.T) {
	wire := webDAVSuccessfulWire()
	f := newWebDAVHTTPFixture(t, wire)
	entered, release := make(chan struct{}), make(chan struct{})
	f.dav.options.LookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		close(entered)
		<-release
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	request := WebAPIDAVProbe{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-changing-tuple"}, Intent: "probe-dav"}
	done := make(chan int, 1)
	go func() { status, _ := f.request(t, "POST", "/api/v1/dav:probe", request, f.cookie); done <- status }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("DNS did not enter")
	}
	status, body := f.request(t, "POST", "/api/v1/settings:dav", map[string]any{"expectedRevision": "2", "operationId": "change-dav-credentials", "dav": map[string]any{"user": "different-committed-user"}}, f.cookie)
	if status != 200 {
		close(release)
		t.Fatalf("settings change %d: %s", status, body)
	}
	close(release)
	select {
	case status := <-done:
		if status != 200 {
			t.Fatalf("accepted transfer response %d", status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stale DNS transfer did not finish")
	}
	job := f.job(t, request.OperationID)
	if job.Data.Phase != WebAPIDAVFailed || job.Data.Error.Code != "dav_target_changed" || len(wire.dials) != 0 {
		t.Fatalf("stale tuple used: %+v", job.Data)
	}
}

func TestWebDAVRestartRecoversWithoutBlindUploadOrReplay(t *testing.T) {
	for _, applying := range []bool{false, true} {
		t.Run(strconv.FormatBool(applying), func(t *testing.T) {
			wire := webDAVSuccessfulWire()
			f := newWebDAVHTTPFixture(t, wire)
			ctx := f.boundContext(t)
			request := WebAPIDAVBackup{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-interrupted-upload"}, Intent: "backup-to-dav", Format: "flclash-v1"}
			var state WebAPIDAVJob
			_, err := f.store.Transaction(ctx, request.WebAPIMutation, "dav:backup", webDAVRequest(ctx, request), func(tx *WebAPIWriteTx) (any, error) {
				settings, err := tx.Settings()
				if err != nil {
					return nil, err
				}
				state, err = tx.RecordDAVJob(WebAPIDAVRecord{Job: WebAPIDAVJob{OperationID: request.OperationID, Action: WebAPIDAVBackupAction, Phase: WebAPIDAVPending, Stage: "accepted"}, SessionID: WebAPIRequestSessionID(ctx), Desired: WebAPIDAVDesired{TupleDigest: webDAVTupleDigest(*settings.DAV), Format: request.Format, ArchiveDigest: webContentDigest([]byte("synthetic archive")), ArchiveSize: len("synthetic archive")}})
				return state, err
			})
			if err != nil {
				t.Fatal(err)
			}
			if applying {
				next := state
				next.Phase = WebAPIDAVApplying
				next.Stage = "put-sending"
				next.UpdatedAt = time.Now().UTC()
				if _, err = f.store.TransitionDAVJob(ctx, state.OperationID, state.Phase, next); err != nil {
					t.Fatal(err)
				}
			}
			if err = f.dav.Close(); err != nil {
				t.Fatal(err)
			}
			if err = f.store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := OpenWebAPIStore(f.storeOptions)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			authority := newWebAPIServiceAuthority()
			defer authority.close()
			options := wire.options()
			options.StatusContext = authority.ctx
			service, err := NewWebAPIDAVService(store, NewWebAPIBackupService(store, WebAPIBackupOptions{}), options)
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()
			_, job, err := service.Job(ctx, request.OperationID)
			if err != nil || job.Phase != WebAPIDAVUnknown {
				t.Fatalf("restart job %+v: %v", job, err)
			}
			receipt, err := service.Backup(ctx, request)
			if err != nil || !receipt.Replayed {
				t.Fatalf("replay %v", err)
			}
			if len(wire.requests) != 0 || len(wire.dials) != 0 {
				t.Fatal("startup or replay blindly repeated network effect")
			}
		})
	}
}

func TestWebDAVCloseCancelsApplyingPUTAndPersistsUnknown(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	wire := &webDAVTestWire{handle: func(r *http.Request, _ []byte) *http.Response {
		if r.Method == "HEAD" {
			return webDAVTestResponse(404, nil, nil)
		}
		if r.Method == "PUT" {
			close(entered)
			<-release
			return nil
		}
		return webDAVTestResponse(204, nil, nil)
	}}
	f := newWebDAVHTTPFixture(t, wire)
	request := WebAPIDAVBackup{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-close-applying"}, Intent: "backup-to-dav", Format: "flclash-v1"}
	done := make(chan int, 1)
	go func() { status, _ := f.request(t, "POST", "/api/v1/dav:backup", request, f.cookie); done <- status }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("PUT did not enter")
	}
	start := time.Now()
	err := f.dav.Close()
	close(release)
	if err != nil || time.Since(start) > 3*time.Second {
		t.Fatalf("close not bounded: %v", err)
	}
	select {
	case status := <-done:
		if status != 200 {
			t.Fatalf("accepted response %d", status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("close stranded request")
	}
	job := f.job(t, request.OperationID)
	if job.Data.Phase != WebAPIDAVUnknown {
		t.Fatalf("cancelled PUT %+v", job.Data)
	}
}

func TestWebDAVConcurrentBudgetDoesNotAdmitFifthEffect(t *testing.T) {
	entered := make(chan struct{}, WebAPIDAVMaxConcurrent)
	release := make(chan struct{})
	wire := &webDAVTestWire{handle: func(*http.Request, []byte) *http.Response {
		entered <- struct{}{}
		<-release
		return webDAVTestResponse(204, nil, nil)
	}}
	f := newWebDAVHTTPFixture(t, wire)
	done := make(chan int, WebAPIDAVMaxConcurrent)
	for i := 0; i < WebAPIDAVMaxConcurrent; i++ {
		request := WebAPIDAVProbe{WebAPIMutation: WebAPIMutation{ExpectedRevision: WebAPIRevision(strconv.Itoa(i + 1)), OperationID: "dav-concurrent-" + strconv.Itoa(i)}, Intent: "probe-dav"}
		go func() { status, _ := f.request(t, "POST", "/api/v1/dav:probe", request, f.cookie); done <- status }()
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatal("concurrent probe did not enter")
		}
	}
	request := WebAPIDAVProbe{WebAPIMutation: WebAPIMutation{ExpectedRevision: "5", OperationID: "dav-concurrent-overflow"}, Intent: "probe-dav"}
	if status, _ := f.request(t, "POST", "/api/v1/dav:probe", request, f.cookie); status != 429 {
		close(release)
		t.Fatalf("overflow status %d", status)
	}
	close(release)
	for range WebAPIDAVMaxConcurrent {
		select {
		case status := <-done:
			if status != 200 {
				t.Fatalf("accepted concurrent request %d", status)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent request did not finish")
		}
	}
	if len(wire.requests) != WebAPIDAVMaxConcurrent {
		t.Fatal("overflow performed effect")
	}
}
