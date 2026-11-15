//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func webContentHTTPDecode[T any](t *testing.T, status int, body []byte) WebAPIEnvelope[T] {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("HTTP %d: %s", status, body)
	}
	var result WebAPIEnvelope[T]
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func webContentHTTPPrepareModule(t *testing.T, f *webContentHTTPFixture, revision WebAPIRevision, data []byte) WebAPIContentPreparation {
	t.Helper()
	status, body := f.request(t, "POST", "/api/v1/mitm-modules:prepare-import", WebAPIResourceImportCommand[webAPIModuleInput]{WebAPIMutation: WebAPIMutation{ExpectedRevision: revision}, Label: "Module", Source: WebAPIContentImportSource{Kind: "file", Bytes: data}}, f.cookie)
	return webContentHTTPDecode[WebAPIContentPreparation](t, status, body).Data
}

func webContentHTTPModuleCommit(job WebAPIContentPreparation, revision WebAPIRevision) WebAPIResourceImportCommand[webAPIModuleInput] {
	return WebAPIResourceImportCommand[webAPIModuleInput]{WebAPIMutation: WebAPIMutation{ExpectedRevision: revision, OperationID: job.OperationID}, WebAPIContentJobSelection: WebAPIContentJobSelection{PreparedJobID: job.PreparedJobID, RequestDigest: job.RequestDigest}}
}

func TestWebContentHTTPResourceImportMetadataAndBlobAtomic(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	create := WebAPIResourceImportCommand[webAPIScriptInput]{WebAPIMutation: WebAPIMutation{ExpectedRevision: "0", OperationID: "script-import"}, Label: "Script", Metadata: &webAPIScriptInput{Label: "Script"}, Source: WebAPIContentImportSource{Kind: "file", Bytes: []byte("export default function() { return 1; }")}}
	status, body := f.request(t, "POST", "/api/v1/scripts:import", create, f.cookie)
	first := webContentHTTPDecode[WebAPIContentMutationResult](t, status, body)
	if first.Revision != "1" || first.Data.Script == nil || first.Data.Script.ID != first.Data.Content.Key.ID {
		t.Fatalf("incomplete create: %s", body)
	}
	second := create
	second.OperationID, second.ExpectedRevision, second.Label = "script-second", "1", "Other"
	second.Metadata = &webAPIScriptInput{Label: "Other"}
	status, body = f.request(t, "POST", "/api/v1/scripts:import", second, f.cookie)
	webContentHTTPDecode[WebAPIContentMutationResult](t, status, body)
	update := create
	update.ID = &first.Data.Script.ID
	update.OperationID, update.ExpectedRevision, update.Label = "script-bad-update", "2", "Other"
	update.Metadata = &webAPIScriptInput{Label: "Other"}
	update.Source.Bytes = []byte("changed but must roll back")
	if status, body = f.request(t, "POST", "/api/v1/scripts:import", update, f.cookie); status != 409 || !bytes.Contains(body, []byte("duplicate_label")) {
		t.Fatalf("duplicate metadata was accepted: %d %s", status, body)
	}
	status, body = f.request(t, "GET", "/api/v1/content/script/"+string(*update.ID), nil, f.cookie)
	content := webContentHTTPDecode[WebAPIContentResult](t, status, body)
	if content.Revision != "2" || !bytes.Equal(content.Data.Bytes, create.Source.Bytes) {
		t.Fatal("failed metadata update changed bytes or revision")
	}
	if _, err := f.store.Receipt(context.Background(), update.OperationID); err == nil {
		t.Fatal("failed update created a receipt")
	}
	update.OperationID, update.Label, update.Metadata.Label = "script-good-update", "Updated", "Updated"
	status, body = f.request(t, "POST", "/api/v1/scripts:import", update, f.cookie)
	changed := webContentHTTPDecode[WebAPIContentMutationResult](t, status, body)
	if changed.Revision != "3" || changed.Data.Script.Label != "Updated" || changed.Data.Content.SHA256 != webContentDigest(update.Source.Bytes) {
		t.Fatalf("metadata/content not committed together: %s", body)
	}
	status, replay := f.request(t, "POST", "/api/v1/scripts:import", update, f.cookie)
	if status != 200 || !bytes.Equal(body, replay) {
		t.Fatal("resource import did not replay exact durable receipt")
	}
	provider := WebAPIResourceImportCommand[webAPIProviderInput]{WebAPIMutation: WebAPIMutation{ExpectedRevision: "3", OperationID: "provider-import"}, Label: "Domain", Source: WebAPIContentImportSource{Kind: "file", Bytes: []byte("example.com\n+.example.net\n")}, Metadata: &webAPIProviderInput{Label: "Domain"}}
	status, body = f.request(t, "POST", "/api/v1/rule-providers:import", provider, f.cookie)
	result := webContentHTTPDecode[WebAPIContentMutationResult](t, status, body)
	if result.Revision != "4" || result.Data.RuleProvider == nil || result.Data.RuleProvider.Behavior != "domain" {
		t.Fatalf("rule provider metadata not derived: %s", body)
	}
}

func TestWebContentHTTPPreparedModuleImportAttachmentsConsentAndReplay(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	data := []byte("#!name=Module\n[Script]\ntest = type=http-request,pattern=.*,script-path=main.js\n[Map Local]\n.* data-path=body.json\n")
	job := webContentHTTPPrepareModule(t, f, "0", data)
	if len(job.Dependencies) != 0 || len(job.AttachmentReferences) != 2 {
		t.Fatalf("incorrect preparation: %+v", job)
	}
	other := webLogin(t, f.auth)
	commit := webContentHTTPModuleCommit(job, "0")
	if status, _ := f.request(t, "POST", "/api/v1/mitm-modules:import", commit, other); status != 409 {
		t.Fatalf("cross-session finalization = %d", status)
	}
	changed := commit
	changed.Source = WebAPIContentImportSource{Kind: "file", Bytes: []byte("[MITM]\nhostname=other")}
	if status, _ := f.request(t, "POST", "/api/v1/mitm-modules:import", changed, f.cookie); status != 409 {
		t.Fatalf("changed prepared body = %d", status)
	}
	for _, item := range []WebAPIContentAttachment{{"main.js", []byte("$done({});")}, {"body.json", []byte(`{"ok":true}`)}} {
		request := WebAPIContentPrepareAttachment{ExpectedRevision: "0", RequestDigest: job.RequestDigest, SourceReference: item.SourceReference, Bytes: item.Bytes}
		status, body := f.request(t, "POST", "/api/v1/content-preparations/"+job.PreparedJobID+"/attachments", request, f.cookie)
		next := webContentHTTPDecode[WebAPIContentPreparation](t, status, body)
		if next.Revision != "0" || next.Data.RequestDigest == job.RequestDigest {
			t.Fatal("attachment did not rotate preparation digest without a business revision")
		}
		job = next.Data
	}
	if status, _ := f.request(t, "POST", "/api/v1/mitm-modules:import", commit, f.cookie); status != 409 {
		t.Fatalf("old attachment digest accepted: %d", status)
	}
	commit = webContentHTTPModuleCommit(job, "0")
	status, receipt := f.request(t, "POST", "/api/v1/mitm-modules:import", commit, f.cookie)
	created := webContentHTTPDecode[WebAPIContentMutationResult](t, status, receipt)
	if created.Revision != "1" || created.Data.MitmModule == nil || created.Data.MitmModule.Enabled || created.Data.MitmModule.Scripts != 1 || len(created.Data.MitmModule.Resources) != 2 {
		t.Fatalf("metadata/consent incomplete: %s", receipt)
	}
	for reference, key := range created.Data.MitmModule.Resources {
		status, body := f.request(t, "GET", "/api/v1/content/mitm-resource/"+string(created.Data.MitmModule.ID)+"?resourceKey="+key, nil, f.cookie)
		content := webContentHTTPDecode[WebAPIContentResult](t, status, body)
		if content.Revision != "1" || len(content.Data.Bytes) == 0 || webContentModuleResourceKey(content.Data.Bytes) != key {
			t.Fatalf("attachment %s absent from same revision", reference)
		}
	}
	f.content.jobs.mu.Lock()
	pending := len(f.content.jobs.entries)
	f.content.jobs.mu.Unlock()
	if pending != 0 {
		t.Fatal("successful job retained staged content")
	}
	status, replay := f.request(t, "POST", "/api/v1/mitm-modules:import", commit, f.cookie)
	if status != 200 || !bytes.Equal(receipt, replay) {
		t.Fatal("receipt replay required deleted preparation")
	}
}

func TestWebContentHTTPPreparationExpiryCancelRevisionAndAttachmentScope(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	now := time.Now()
	f.content.now = func() time.Time { return now }
	data := []byte("#!name=Module\n[Script]\ntest = type=http-request,script-path=main.js\n")
	job := webContentHTTPPrepareModule(t, f, "0", data)
	path := "/api/v1/content-preparations/" + job.PreparedJobID + "/attachments"
	request := WebAPIContentPrepareAttachment{ExpectedRevision: "0", RequestDigest: job.RequestDigest, SourceReference: "undeclared", Bytes: []byte("x")}
	if status, _ := f.request(t, "POST", path, request, f.cookie); status != 400 {
		t.Fatalf("undeclared reference = %d", status)
	}
	request.SourceReference = "main.js"
	if status, _ := f.request(t, "POST", path, request, webLogin(t, f.auth)); status != 409 {
		t.Fatalf("cross-session upload = %d", status)
	}
	if status, _ := f.request(t, "GET", path, request, f.cookie); status != 405 {
		t.Fatalf("unexpected upload method = %d", status)
	}
	if status, _ := f.request(t, "POST", path+"?path=/tmp/no", request, f.cookie); status != 400 {
		t.Fatalf("unexpected upload query = %d", status)
	}
	if status, _ := f.request(t, "POST", "/api/v1/content:discard-preparation", map[string]string{"preparedJobId": job.PreparedJobID}, f.cookie); status != 204 {
		t.Fatalf("discard = %d", status)
	}
	if status, _ := f.request(t, "POST", "/api/v1/mitm-modules:import", webContentHTTPModuleCommit(job, "0"), f.cookie); status != 409 {
		t.Fatalf("cancelled finalization = %d", status)
	}
	job = webContentHTTPPrepareModule(t, f, "0", data)
	now = now.Add(6 * time.Minute)
	if status, _ := f.request(t, "POST", "/api/v1/mitm-modules:import", webContentHTTPModuleCommit(job, "0"), f.cookie); status != 409 {
		t.Fatalf("expired finalization = %d", status)
	}
	job = webContentHTTPPrepareModule(t, f, "0", data)
	status, body := f.request(t, "POST", "/api/v1/profiles:import", webContentImportRequest("advance-revision", "0"), f.cookie)
	webContentHTTPDecode[WebAPIProfile](t, status, body)
	request.RequestDigest = job.RequestDigest
	if status, _ := f.request(t, "POST", "/api/v1/content-preparations/"+job.PreparedJobID+"/attachments", request, f.cookie); status != 409 {
		t.Fatalf("stale upload = %d", status)
	}
	if status, _ := f.request(t, "POST", "/api/v1/mitm-modules:import", webContentHTTPModuleCommit(job, "0"), f.cookie); status != 409 {
		t.Fatalf("stale finalization = %d", status)
	}
}

func TestWebContentHTTPPreparedDependencyGrantDigestAndNoRefetchOnReplay(t *testing.T) {
	var fetched atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched.Add(1)
		if r.Method != "GET" || r.Header.Get("Cookie") != "" {
			t.Error("dependency used ambient method or credentials")
		}
		io.WriteString(w, "$done({});")
	}))
	defer remote.Close()
	f := newWebContentHTTPFixture(t, nil, nil)
	f.sources.options.ResolveImportJobSource = f.content.PendingSource
	f.sources.options.LookupNetIP = webSourceTestDNS("10.1.2.3")
	f.content.sourceOptions.LookupNetIP = webSourceTestDNS("10.1.2.3")
	f.content.sourceOptions.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(remote.URL, "http://"))
	}
	data := []byte("#!name=Module\n[Script]\nremote = type=http-request,script-path=http://private.test/task.js\nlocal = type=http-response,script-path=local.js\n")
	job := webContentHTTPPrepareModule(t, f, "0", data)
	if len(job.Dependencies) != 1 || strings.Contains(job.Dependencies[0].SourceReference, "private.test") || fetched.Load() != 0 {
		t.Fatal("preparation fetched dependency or exposed an unscoped URL reference")
	}
	authorize := func(operation string) string {
		status, body := f.request(t, "POST", "/api/v1/sources:prepare", WebAPISourcePrepare{ExpectedRevision: "0", Purpose: WebAPISourceMitmResourceImport, ImportJobID: job.PreparedJobID, SourceReference: job.Dependencies[0].SourceReference}, f.cookie)
		prepared := webContentHTTPDecode[WebAPISourcePreparation](t, status, body).Data
		status, body = f.request(t, "POST", "/api/v1/sources:authorize", WebAPISourceAuthorize{ExpectedRevision: "0", OperationID: operation, PreparationID: prepared.PreparationID, URLDigest: prepared.URLDigest, Intent: "authorize-server-private-source"}, f.cookie)
		return webContentHTTPDecode[WebAPISourceAuthorizationResult](t, status, body).Data.AuthorizationID
	}
	oldGrant := authorize("dependency-old-grant")
	status, body := f.request(t, "POST", "/api/v1/content-preparations/"+job.PreparedJobID+"/attachments", WebAPIContentPrepareAttachment{ExpectedRevision: "0", RequestDigest: job.RequestDigest, SourceReference: "local.js", Bytes: []byte("$done({local: true});")}, f.cookie)
	job = webContentHTTPDecode[WebAPIContentPreparation](t, status, body).Data
	commit := webContentHTTPModuleCommit(job, "0")
	commit.AuthorizationIDs = []string{oldGrant}
	if status, body = f.request(t, "POST", "/api/v1/mitm-modules:import", commit, f.cookie); status != 409 || fetched.Load() != 0 {
		t.Fatalf("stale requestDigest grant reached dependency: %d %s", status, body)
	}
	commit.AuthorizationIDs = []string{authorize("dependency-new-grant")}
	status, receipt := f.request(t, "POST", "/api/v1/mitm-modules:import", commit, f.cookie)
	result := webContentHTTPDecode[WebAPIContentMutationResult](t, status, receipt)
	if fetched.Load() != 1 || result.Revision != "1" || result.Data.MitmModule.Enabled || len(result.Data.MitmModule.Resources) != 2 {
		t.Fatalf("authorized dependency not committed atomically: %s", receipt)
	}
	status, replay := f.request(t, "POST", "/api/v1/mitm-modules:import", commit, f.cookie)
	if status != 200 || !bytes.Equal(receipt, replay) || fetched.Load() != 1 {
		t.Fatal("receipt replay fetched the dependency again")
	}
}

func TestWebContentHTTPPreparedImportResponseLossUsesDurableReceipt(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	job := webContentHTTPPrepareModule(t, f, "0", []byte("#!name=Module\n[MITM]\nhostname=example.test\n"))
	commit := webContentHTTPModuleCommit(job, "0")
	encoded, err := json.Marshal(commit)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest("POST", f.server.URL+"/api/v1/mitm-modules:import", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Test-Drop-Response", "1")
	request.AddCookie(f.cookie)
	if response, err := f.server.Client().Do(request); err == nil {
		response.Body.Close()
		t.Fatal("response was not dropped")
	}
	receipt, err := f.store.Receipt(context.Background(), job.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	status, body := f.request(t, "POST", "/api/v1/mitm-modules:import", commit, f.cookie)
	if status != 200 || !bytes.Equal(receipt.Body, body) {
		t.Fatal("lost response could not replay after RAM preparation removal")
	}
	f.server.Close()
	if err = f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWebAPIStore(f.storeOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	stored, err := reopened.Receipt(context.Background(), job.OperationID)
	if err != nil || !bytes.Equal(receipt.Body, stored.Body) {
		t.Fatalf("prepared receipt did not survive reopen: %v", err)
	}
}

func TestWebContentHTTPChunkedModuleExceedsSingleEnvelopeWithoutFilesystem(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	var text strings.Builder
	text.WriteString("#!name=Module\n[Map Local]\n")
	for i := 0; i < 4; i++ {
		fmt.Fprintf(&text, "^https://example.test/%d data-path=local-%d\n", i, i)
	}
	job := webContentHTTPPrepareModule(t, f, "0", []byte(text.String()))
	payload := bytes.Repeat([]byte("x"), 10<<20)
	for i := 0; i < 4; i++ {
		request := WebAPIContentPrepareAttachment{ExpectedRevision: "0", RequestDigest: job.RequestDigest, SourceReference: fmt.Sprintf("local-%d", i), Bytes: payload}
		status, body := f.request(t, "POST", "/api/v1/content-preparations/"+job.PreparedJobID+"/attachments", request, f.cookie)
		job = webContentHTTPDecode[WebAPIContentPreparation](t, status, body).Data
	}
	status, body := f.request(t, "POST", "/api/v1/mitm-modules:import", webContentHTTPModuleCommit(job, "0"), f.cookie)
	created := webContentHTTPDecode[WebAPIContentMutationResult](t, status, body)
	if created.Revision != "1" || len(created.Data.MitmModule.Resources) != 4 {
		t.Fatalf("multi-envelope staged module lost attachments: %s", body)
	}
}
