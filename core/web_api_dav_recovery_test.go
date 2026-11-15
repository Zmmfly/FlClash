//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestWebDAVHTTPUnknownUploadBlocksNewIdentityAcrossSessionsUntilPositiveReconcile(t *testing.T) {
	var uploaded []byte
	puts := 0
	matches := false
	wire := &webDAVTestWire{handle: func(r *http.Request, body []byte) *http.Response {
		switch r.Method {
		case "HEAD":
			return webDAVTestResponse(404, nil, nil)
		case "PUT":
			puts++
			uploaded = append([]byte{}, body...)
			return nil
		case "GET":
			if matches {
				return webDAVTestResponse(200, nil, uploaded)
			}
			return webDAVTestResponse(200, nil, []byte("different remote bytes"))
		default:
			return webDAVTestResponse(204, nil, nil)
		}
	}}
	f := newWebDAVHTTPFixture(t, wire)
	original := WebAPIDAVBackup{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-original-uncertain"}, Intent: "backup-to-dav", Format: "flclash-v1"}
	if status, body := f.request(t, "POST", "/api/v1/dav:backup", original, f.cookie); status != 200 {
		t.Fatalf("admission %d: %s", status, body)
	}
	status, body := f.request(t, "GET", "/api/v1/dav/jobs", nil, f.cookie)
	if status != 200 {
		t.Fatalf("job list %d: %s", status, body)
	}
	var listed WebAPIEnvelope[[]WebAPIDAVJobSummary]
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Data) != 1 || listed.Data[0].Phase != WebAPIDAVUnknown {
		t.Fatal("reload list lost uncertain upload")
	}
	for _, private := range []string{"tupleDigest", "sessionId", "authorizationId", "synthetic-password", "dav.test"} {
		if bytes.Contains(body, []byte(private)) {
			t.Fatalf("job list leaked %s", private)
		}
	}
	other := webLogin(t, f.auth)
	status, body = f.request(t, "GET", "/api/v1/dav/jobs", nil, other)
	if status != 200 {
		t.Fatal("second session list")
	}
	json.Unmarshal(body, &listed)
	if len(listed.Data) != 1 || !listed.Data[0].NeedsPrepare || !listed.Data[0].CurrentTupleMatches {
		t.Fatal("second session did not receive a safe recovery summary")
	}
	blocked := original
	blocked.ExpectedRevision = "2"
	blocked.OperationID = "dav-new-id-no-retry"
	for _, cookie := range []*http.Cookie{f.cookie, other} {
		status, body := f.request(t, "POST", "/api/v1/dav:backup", blocked, cookie)
		if status != 409 || !bytes.Contains(body, []byte("dav_unresolved_upload")) || !bytes.Contains(body, []byte(original.OperationID)) {
			t.Fatalf("new identity bypassed unknown %d: %s", status, body)
		}
	}
	if puts != 1 {
		t.Fatal("new identity repeated uncertain PUT")
	}
	reconcile := WebAPIDAVReconcile{WebAPIMutation: WebAPIMutation{ExpectedRevision: "2", OperationID: "dav-negative-observation"}, Intent: "reconcile-dav-backup"}
	if status, body := f.request(t, "POST", "/api/v1/dav/jobs/"+original.OperationID+":reconcile", reconcile, f.cookie); status != 200 {
		t.Fatalf("reconcile %d: %s", status, body)
	}
	if f.job(t, reconcile.OperationID).Data.Result.RemoteMatches {
		t.Fatal("different bytes claimed a committed upload")
	}
	blocked.ExpectedRevision = "3"
	if status, body := f.request(t, "POST", "/api/v1/dav:backup", blocked, f.cookie); status != 409 || !bytes.Contains(body, []byte("dav_unresolved_upload")) {
		t.Fatal("negative read observation cleared uncertain upload")
	}
	matches = true
	reconcile.ExpectedRevision = "3"
	reconcile.OperationID = "dav-positive-observation"
	if status, body := f.request(t, "POST", "/api/v1/dav/jobs/"+original.OperationID+":reconcile", reconcile, f.cookie); status != 200 {
		t.Fatalf("positive reconcile %d: %s", status, body)
	}
	if !f.job(t, reconcile.OperationID).Data.Result.RemoteMatches {
		t.Fatal("matching archive was not reported")
	}
	blocked.ExpectedRevision = "4"
	if status, _ := f.request(t, "POST", "/api/v1/dav:backup", blocked, f.cookie); status != 409 {
		t.Fatal("positive observation automatically cleared the upload gate without explicit reference")
	}
	blocked.ResolutionOperationID = reconcile.OperationID
	if status, body := f.request(t, "POST", "/api/v1/dav:backup", blocked, f.cookie); status != 200 {
		t.Fatalf("explicit upload after positive observation %d: %s", status, body)
	}
	if puts != 2 {
		t.Fatalf("unexpected upload attempts %d", puts)
	}
}

func TestWebDAVConditionalTargetBindsObservedRedirectResource(t *testing.T) {
	puts := 0
	wire := &webDAVTestWire{handle: func(r *http.Request, _ []byte) *http.Response {
		switch r.Method {
		case "MKCOL":
			return webDAVTestResponse(204, nil, nil)
		case "HEAD":
			if r.URL.Path != "/observed" {
				return webDAVTestResponse(307, http.Header{"Location": {"/observed"}}, nil)
			}
			return webDAVTestResponse(200, http.Header{"Etag": {`"version"`}}, nil)
		case "PUT":
			puts++
			if r.URL.Path != "/observed" || r.Header.Get("If-Match") != `"version"` {
				t.Error("ETag applied to a different resource")
			}
			return webDAVTestResponse(307, http.Header{"Location": {"/changed-after-head"}}, nil)
		}
		return webDAVTestResponse(500, nil, nil)
	}}
	_, err := webDAVTestTransport(t, wire, WebAPIDAVBackupAction).backup(t.Context(), []byte("archive"))
	webDAVErrorCode(t, err, "dav_remote_conflict")
	if puts != 1 {
		t.Fatal("conditional upload followed an unobserved resource")
	}
}

func TestWebDAVNativeEscapedBaseAndFileNameRemainSegments(t *testing.T) {
	wire := webDAVSuccessfulWire()
	transport := webDAVTestTransport(t, wire, WebAPIDAVProbeAction)
	transport.dav.URI = "http://dav.test/base/a%2Fb/?nativeIgnored=1"
	transport.dav.FileName = "file #%.zip"
	base, _, err := webDAVBase(transport.dav)
	if err != nil {
		t.Fatal(err)
	}
	transport.origin = base
	if got := transport.location(false, false).String(); got != "http://dav.test:80/base/a%2Fb/FlClash/file%20%23%25.zip" {
		t.Fatalf("native path segments changed: %s", got)
	}
	if !webDAVWholeRange("bytes\t0-2/3", 3) {
		t.Fatal("complete native Content-Range whitespace lost")
	}
}
