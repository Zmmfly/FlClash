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

	bolt "github.com/metacubex/bbolt"
)

func TestWebAPIDAVSummaryCannotExposeOtherSessionGenericReceipt(t *testing.T) {
	r := newWebAPITestRig(t)
	r.server.Close()
	authority := newWebAPIServiceAuthority()
	defer authority.close()
	mux := http.NewServeMux()
	RegisterWebAPIResourceRoutes(mux, r.store)
	dav, err := RegisterWebAPIDAVRoutes(mux, r.store, NewWebAPIBackupService(r.store, WebAPIBackupOptions{}), WebAPIDAVOptions{StatusContext: authority.ctx})
	if err != nil {
		t.Fatal(err)
	}
	defer dav.Close()
	var session atomic.Value
	session.Store("old-receipt-session")
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		ctx := WithWebAPIRequestAuthorization(request.Context(), session.Load().(string), func(context.Context) (func(), error) { return func() {}, nil })
		mux.ServeHTTP(w, request.WithContext(ctx))
	}))
	ctx := webAPITestSourceContext("old-receipt-session")
	record := webAPITestDAVRecord("dav-private-restore-receipt", WebAPIRequestSessionID(ctx))
	record.Job.Action = WebAPIDAVRestoreAction
	record.Job.Phase = WebAPIDAVSucceeded
	record.Job.Stage = "restored"
	record.Job.Result = &WebAPIDAVResult{ArchiveDigest: strings.Repeat("e", 64), ArchiveSize: 123, Restored: &WebAPIBackupResult{Restored: true, ProfileCount: 1}}
	receipt, err := r.store.Transaction(ctx, WebAPIMutation{"0", record.Job.OperationID}, "dav:restore", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return tx.RecordDAVJob(record) })
	if err != nil {
		t.Fatal(err)
	}
	if body := r.call("GET", "/api/v1/operations/"+record.Job.OperationID, nil, 200); !bytes.Equal(body, receipt.Body) {
		t.Fatal("owner receipt changed")
	}
	session.Store("new-receipt-session")
	summary := webAPITestData[[]WebAPIDAVJobSummary](t, r.call("GET", "/api/v1/dav/jobs", nil, 200))
	if len(summary.Data) != 1 || summary.Data[0].OperationID != record.Job.OperationID {
		t.Fatal(summary)
	}
	for _, request := range []struct {
		method, path string
		body         any
	}{{"GET", "/api/v1/dav/jobs/" + record.Job.OperationID, nil}, {"GET", "/api/v1/operations/" + summary.Data[0].OperationID, nil}, {"POST", "/api/v1/operations/" + summary.Data[0].OperationID + ":resolve", map[string]any{"intent": "resolve-unknown-result"}}} {
		body := r.call(request.method, request.path, request.body, 403)
		if bytes.Contains(body, []byte(record.Job.Result.ArchiveDigest)) || bytes.Contains(body, []byte("archiveSize")) {
			t.Fatal("generic receipt recovery leaked old DAV result", string(body))
		}
	}
	profile := webAPITestProfile("Business recovery remains portable")
	plain, err := r.store.Transaction(ctx, WebAPIMutation{"1", "plain-reauth-recovery"}, "profiles:create", profile, func(tx *WebAPIWriteTx) (any, error) {
		return tx.CreateProfile(WebAPIProfileInput{Type: "file", Label: "Portable"})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct {
		method, path string
		body         any
	}{{"GET", "/api/v1/operations/plain-reauth-recovery", nil}, {"POST", "/api/v1/operations/plain-reauth-recovery:resolve", map[string]any{"intent": "resolve-unknown-result"}}} {
		if body := r.call(request.method, request.path, request.body, 200); !bytes.Equal(body, plain.Body) {
			t.Fatal("normal business receipt was session-bound")
		}
	}
	_, err = r.store.Transaction(webAPITestSourceContext("new-receipt-session"), WebAPIMutation{"0", record.Job.OperationID}, "dav:restore", struct{}{}, func(*WebAPIWriteTx) (any, error) { t.Fatal("cross-session replay reran DAV"); return nil, nil })
	webAPITestErrorCode(t, err, "dav_session_mismatch")
}

func TestWebAPIDAVReceiptClassFailsClosedWithoutPrivateRecord(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("dav-receipt-owner")
	record := webAPITestDAVRecord("dav-missing-record-receipt", WebAPIRequestSessionID(ctx))
	_, err := r.store.Transaction(ctx, WebAPIMutation{"0", record.Job.OperationID}, "dav:backup", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return tx.RecordDAVJob(record) })
	if err != nil {
		t.Fatal(err)
	}
	if err = r.store.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("dav-jobs")).Delete([]byte(record.Job.OperationID)) }); err != nil {
		t.Fatal(err)
	}
	_, err = r.store.Receipt(ctx, record.Job.OperationID)
	webAPITestErrorCode(t, err, "dav_receipt_state_invalid")
	_, err = r.store.ResolveOperation(ctx, record.Job.OperationID)
	webAPITestErrorCode(t, err, "dav_receipt_state_invalid")
	_, err = r.store.Transaction(ctx, WebAPIMutation{"1", "dav-command-missing-record"}, "dav:probe", struct{}{}, func(*WebAPIWriteTx) (any, error) { return WebAPIDAVJob{OperationID: "dav-command-missing-record"}, nil })
	webAPITestErrorCode(t, err, "dav_receipt_state_invalid")
	_, err = r.store.Receipt(ctx, "dav-command-missing-record")
	webAPITestErrorCode(t, err, "not_found")
}

type webAPIDAVAuthCheckingWriter struct {
	http.ResponseWriter
	t    *testing.T
	held *atomic.Bool
}

func (w webAPIDAVAuthCheckingWriter) Write(body []byte) (int, error) {
	if w.held.Load() {
		w.t.Error("DAV authorization held while writing response")
	}
	return w.ResponseWriter.Write(body)
}

func TestWebAPIDAVGenericReceiptReadReleasesGuardBeforeResponse(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := webAPITestSourceContext("dav-receipt-owner")
	record := webAPITestDAVRecord("dav-release-before-write", WebAPIRequestSessionID(ctx))
	_, err := r.store.Transaction(ctx, WebAPIMutation{"0", record.Job.OperationID}, "dav:backup", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return tx.RecordDAVJob(record) })
	if err != nil {
		t.Fatal(err)
	}
	var held atomic.Bool
	guarded := WithWebAPIRequestAuthorization(context.Background(), record.SessionID, func(context.Context) (func(), error) { held.Store(true); return func() { held.Store(false) }, nil })
	for _, method := range []string{"GET", "POST"} {
		path := "/api/v1/operations/" + record.Job.OperationID
		var body []byte
		if method == "POST" {
			path += ":resolve"
			body, _ = json.Marshal(WebAPIOperationResolve{Intent: "resolve-unknown-result"})
		}
		request := httptest.NewRequest(method, path, bytes.NewReader(body)).WithContext(guarded)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		NewWebAPIHandler(r.store).ServeHTTP(webAPIDAVAuthCheckingWriter{response, t, &held}, request)
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
}
