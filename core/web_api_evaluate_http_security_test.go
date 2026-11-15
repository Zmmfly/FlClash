//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/age"
)

func TestWebEvaluateHTTPGuestAttacksNeverReachHost(t *testing.T) {
	f := newWebEvaluateRig(t, `function main(c){return c}`)
	for i, script := range []string{
		`function main(c){window.synthetic=true;return c}`,
		`function main(){return fetch('/api/v1/meta',{credentials:'same-origin'})}`,
		`function main(){return ({}).constructor.constructor('return process')().env}`,
		`function main(){return require('fs').readFileSync('/synthetic')}`,
		`async function main(){return await import('synthetic')}`,
		`function main(){return main()}`,
		`function main(){return {data:new Uint8Array(512*1024*1024)}}`,
		`function main(){console.error('x'.repeat(20000));return {}}`,
	} {
		revision := WebAPIRevision(fmt.Sprint(i + 1))
		receipt, err := f.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: revision, OperationID: fmt.Sprintf("attack-script-%d", i)}, "test:set-script", script, func(tx *WebAPIWriteTx) (any, error) {
			return nil, tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentScript, ID: webEvaluateScriptID}, Bytes: []byte(script), MediaType: "text/javascript"})
		})
		if err != nil {
			t.Fatal(err)
		}
		request := webEvaluateRequest()
		request.ExpectedRevision = receipt.Revision
		if code, body := f.request(t, "/api/v1/profiles:evaluate", request, f.cookie); code < 400 || code >= 500 {
			t.Fatalf("attack %d: %d %s", i, code, body)
		}
	}
	if len(f.service.cache) != 0 {
		t.Fatal("rejected execution produced a snapshot")
	}
}

func TestWebEvaluateHTTPLeaseRecheckAndCancellation(t *testing.T) {
	f := newWebEvaluateRig(t, `function main(c){return c}`)
	body, _ := json.Marshal(webEvaluateRequest())
	var calls atomic.Int32
	ctx := WithWebAPIRequestAuthorization(context.Background(), "synthetic-session", func(context.Context) (func(), error) {
		if calls.Add(1) == 2 {
			return nil, webAPIError(401, "unauthorized", "synthetic logout")
		}
		return func() {}, nil
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/profiles:evaluate", bytes.NewReader(body)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	f.service.ServeHTTP(response, request)
	if response.Code != 401 || len(f.service.cache) != 0 {
		t.Fatalf("post-evaluation logout not respected: %d", response.Code)
	}
	_, err := f.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "1", OperationID: "cancel-loop-script"}, "test:set-script", nil, func(tx *WebAPIWriteTx) (any, error) {
		return nil, tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentScript, ID: webEvaluateScriptID}, Bytes: []byte(`function main(){while(true){}}`), MediaType: "text/javascript"})
	})
	if err != nil {
		t.Fatal(err)
	}
	input := webEvaluateRequest()
	input.ExpectedRevision = "2"
	body, _ = json.Marshal(input)
	ctx, cancel := context.WithTimeout(f.sessionContext(f.cookie), 50*time.Millisecond)
	defer cancel()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/profiles:evaluate", bytes.NewReader(body)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	start := time.Now()
	f.service.ServeHTTP(response, request)
	if response.Code != 408 || time.Since(start) > 2*time.Second || len(f.service.cache) != 0 {
		t.Fatalf("HTTP cancellation failed: %d %s", response.Code, response.Body.String())
	}
	if code, _ := f.request(t, "/api/v1/profiles:evaluate", webEvaluateRequest(), f.cookie); code != 409 {
		t.Fatal("old read revision was silently refreshed")
	}
}

func TestWebEvaluateHTTPDecryptsOnlyCommittedProfileIdentity(t *testing.T) {
	f := newWebEvaluateRig(t, `function main(c){return c}`)
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	writer, err := age.Encrypt(&encrypted, identity.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write([]byte(webEvaluateSource)); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "1", OperationID: "encrypted-profile"}, "test:encrypt-profile", nil, func(tx *WebAPIWriteTx) (any, error) {
		profile, err := tx.Profile(webEvaluateProfileID)
		if err != nil {
			return nil, err
		}
		profile.AgeSecretKey = identity.String()
		if err = tx.SaveProfile(profile); err != nil {
			return nil, err
		}
		return nil, tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: webEvaluateProfileID}, Bytes: encrypted.Bytes(), MediaType: "application/octet-stream"})
	})
	if err != nil {
		t.Fatal(err)
	}
	request := webEvaluateRequest()
	request.ExpectedRevision = "2"
	code, body := f.request(t, "/api/v1/profiles:evaluate", request, f.cookie)
	if code != 200 {
		t.Fatalf("encrypted source %d %s", code, body)
	}
	var response WebAPIEnvelope[WebAPIEvaluateResult]
	if err = json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.SourceDigest != webEvaluateHash(encrypted.Bytes()) || bytes.Contains(body, []byte(identity.String())) {
		t.Fatal("source digest or secret boundary changed")
	}
}
