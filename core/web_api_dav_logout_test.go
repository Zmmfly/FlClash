//go:build webpanel && !android

package core

import (
	"encoding/json"
	"testing"
)

func TestWebDAVHTTPLogoutAfterAcceptedBeforeStartHasZeroDialAndDoesNotWedgeUploads(t *testing.T) {
	wire := webDAVSuccessfulWire()
	f := newWebDAVHTTPFixture(t, wire)
	request := WebAPIDAVBackup{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "dav-revoked-after-admission"}, Intent: "backup-to-dav", Format: "flclash-v1"}
	logoutStatus := make(chan int, 1)
	f.store.onCommit = func(commit WebAPICommit) {
		if commit.OperationID == request.OperationID {
			status, _ := f.request(t, "POST", "/api/auth/logout", nil, f.cookie)
			logoutStatus <- status
		}
	}
	if status, body := f.request(t, "POST", "/api/v1/dav:backup", request, f.cookie); status != 200 {
		t.Fatalf("accepted receipt %d: %s", status, body)
	}
	if status := <-logoutStatus; status != 204 {
		t.Fatalf("postcommit logout %d", status)
	}
	if len(wire.requests) != 0 || len(wire.dials) != 0 {
		t.Fatal("revoked accepted request started a remote effect")
	}
	f.cookie = webLogin(t, f.auth)
	status, body := f.request(t, "GET", "/api/v1/dav/jobs", nil, f.cookie)
	if status != 200 {
		t.Fatalf("safe status summary %d: %s", status, body)
	}
	var summary WebAPIEnvelope[[]WebAPIDAVJobSummary]
	if err := json.Unmarshal(body, &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.Data) != 1 || summary.Data[0].Phase != WebAPIDAVFailed || summary.Data[0].ErrorCode != "dav_not_started" {
		t.Fatalf("pre-effect refusal stranded pending state: %s", body)
	}
	request.ExpectedRevision = "2"
	request.OperationID = "dav-after-new-login"
	f.store.onCommit = nil
	if status, body := f.request(t, "POST", "/api/v1/dav:backup", request, f.cookie); status != 200 {
		t.Fatalf("subsequent authorized upload %d: %s", status, body)
	}
	if job := f.job(t, request.OperationID); job.Data.Phase != WebAPIDAVSucceeded {
		t.Fatalf("new upload %+v", job.Data)
	}
}
