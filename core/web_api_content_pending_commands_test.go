//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebContentHTTPPreparedWriteAndArgumentsUseImmutablePayloadAndReplay(t *testing.T) {
	var gets atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gets.Add(1)
		fmt.Fprintf(w, "$done({path:%q});", r.URL.Path)
	}))
	defer source.Close()
	f := newWebContentHTTPFixture(t, source.Client(), nil)
	url := strings.Replace(source.URL, "127.0.0.1", "public.fixture.test", 1)
	data := []byte("#!name=Module\n[Argument]\npath = input,first\n[Script]\njob = type=http-request,script-path=" + url + "/{path}.js\n")
	input := WebAPIResourceImportCommand[webAPIModuleInput]{WebAPIMutation: WebAPIMutation{ExpectedRevision: "0", OperationID: "seed-prepared-commands"}, Label: "Module", Source: WebAPIContentImportSource{Kind: "file", Bytes: data}}
	status, body := f.request(t, "POST", "/api/v1/mitm-modules:import", input, f.cookie)
	created := webContentHTTPDecode[WebAPIContentMutationResult](t, status, body)
	key := created.Data.Content.Key
	metadata := &WebAPIContentMetadata{MitmModule: &webAPIModuleInput{Name: "Module", Enabled: true, Arguments: map[string]string{"path": "first"}}}
	preparedRequest := WebAPIContentPrepareWrite{ExpectedRevision: "1", Key: key, Bytes: []byte(strings.Replace(string(data), "type=http-request", "type=http-response", 1) + "# edited\n"), Metadata: metadata}
	status, body = f.request(t, "POST", "/api/v1/content:prepare-write", preparedRequest, f.cookie)
	job := webContentHTTPDecode[WebAPIContentPreparation](t, status, body).Data
	if gets.Load() != 1 || len(job.Dependencies) != 1 {
		t.Fatal("prepare-write fetched dependencies instead of staging a manifest")
	}
	write := WebAPIContentWrite{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: job.OperationID}, WebAPIContentJobSelection: WebAPIContentJobSelection{PreparedJobID: job.PreparedJobID, RequestDigest: job.RequestDigest}, Key: key}
	changed := write
	changed.Bytes = []byte("[MITM]\nhostname=changed\n")
	if status, _ = f.request(t, "POST", "/api/v1/content:write", changed, f.cookie); status != 409 {
		t.Fatalf("changed prepared write=%d", status)
	}
	status, receipt := f.request(t, "POST", "/api/v1/content:write", write, f.cookie)
	written := webContentHTTPDecode[WebAPIContentMutationResult](t, status, receipt)
	if written.Revision != "2" || written.Data.MitmModule.Arguments["path"] != "first" || written.Data.MitmModule.Enabled || gets.Load() != 2 {
		t.Fatalf("prepared write lost metadata/consent: %s", receipt)
	}
	status, replay := f.request(t, "POST", "/api/v1/content:write", write, f.cookie)
	if status != 200 || !bytes.Equal(receipt, replay) || gets.Load() != 2 {
		t.Fatal("prepared write replay needed staged payload or refetched")
	}
	status, body = f.request(t, "POST", "/api/v1/mitm-modules:prepare-arguments", WebAPIContentPrepareArguments{ExpectedRevision: "2", ID: key.ID, Arguments: map[string]string{"path": "second"}}, f.cookie)
	job = webContentHTTPDecode[WebAPIContentPreparation](t, status, body).Data
	if gets.Load() != 2 || len(job.Dependencies) != 1 {
		t.Fatal("prepare-arguments fetched dependencies")
	}
	arguments := WebAPIMitmArguments{WebAPIMutation: WebAPIMutation{ExpectedRevision: "2", OperationID: job.OperationID}, WebAPIContentJobSelection: WebAPIContentJobSelection{PreparedJobID: job.PreparedJobID, RequestDigest: job.RequestDigest}, ID: key.ID}
	badArguments := arguments
	badArguments.Arguments = map[string]string{"path": "tampered"}
	if status, _ = f.request(t, "POST", "/api/v1/mitm-modules:arguments", badArguments, f.cookie); status != 409 {
		t.Fatalf("changed prepared arguments=%d", status)
	}
	status, receipt = f.request(t, "POST", "/api/v1/mitm-modules:arguments", arguments, f.cookie)
	updated := webContentHTTPDecode[WebAPIContentMutationResult](t, status, receipt)
	if updated.Revision != "3" || updated.Data.MitmModule.Arguments["path"] != "second" || updated.Data.MitmModule.Enabled || gets.Load() != 3 {
		t.Fatalf("prepared arguments not committed: %s", receipt)
	}
	status, replay = f.request(t, "POST", "/api/v1/mitm-modules:arguments", arguments, f.cookie)
	if status != 200 || !bytes.Equal(receipt, replay) || gets.Load() != 3 {
		t.Fatal("prepared argument replay refetched dependencies")
	}
}

func TestWebContentHTTPPreparedRemoteModuleNeverRefetchesRootAtCommit(t *testing.T) {
	var rootGets, childGets atomic.Int32
	var module string
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/module" {
			rootGets.Add(1)
			io.WriteString(w, module)
			return
		}
		childGets.Add(1)
		io.WriteString(w, "$done({});")
	}))
	defer source.Close()
	url := strings.Replace(source.URL, "127.0.0.1", "public.fixture.test", 1)
	module = "#!name=Module\n[Script]\njob = type=http-request,script-path=" + url + "/script.js\n"
	f := newWebContentHTTPFixture(t, source.Client(), nil)
	input := WebAPIResourceImportCommand[webAPIModuleInput]{WebAPIMutation: WebAPIMutation{ExpectedRevision: "0"}, Label: "Module", Source: WebAPIContentImportSource{Kind: "url", URL: url + "/module"}}
	status, body := f.request(t, "POST", "/api/v1/mitm-modules:prepare-import", input, f.cookie)
	job := webContentHTTPDecode[WebAPIContentPreparation](t, status, body).Data
	if rootGets.Load() != 1 || childGets.Load() != 0 {
		t.Fatal("prepare imported children or did not fetch root exactly once")
	}
	commit := webContentHTTPModuleCommit(job, "0")
	status, body = f.request(t, "POST", "/api/v1/mitm-modules:import", commit, f.cookie)
	created := webContentHTTPDecode[WebAPIContentMutationResult](t, status, body)
	if rootGets.Load() != 1 || childGets.Load() != 1 || created.Data.Content.SHA256 != job.ContentSHA256 {
		t.Fatal("finalization did not use the fetched immutable root")
	}
}

func TestWebContentHTTPCancelDuringPreparedDependencyFetchPreventsCommit(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		io.WriteString(w, "$done({});")
	}))
	defer source.Close()
	f := newWebContentHTTPFixture(t, source.Client(), nil)
	url := strings.Replace(source.URL, "127.0.0.1", "public.fixture.test", 1)
	job := webContentHTTPPrepareModule(t, f, "0", []byte("#!name=Module\n[Script]\njob = type=http-request,script-path="+url+"/script.js\n"))
	done := make(chan int, 1)
	go func() {
		status, _ := f.request(t, "POST", "/api/v1/mitm-modules:import", webContentHTTPModuleCommit(job, "0"), f.cookie)
		done <- status
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("dependency did not begin")
	}
	status, body := f.request(t, "POST", "/api/v1/content:discard-preparation", map[string]string{"preparedJobId": job.PreparedJobID}, f.cookie)
	if status != 204 {
		close(release)
		t.Fatalf("discard during network=%d %s", status, body)
	}
	close(release)
	select {
	case status := <-done:
		if status != 409 {
			t.Fatalf("cancelled fetch committed: %d", status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled finalization did not return")
	}
	revision, err := f.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		modules, err := tx.MitmModules()
		if len(modules) != 0 {
			t.Error("cancelled job wrote module metadata")
		}
		return err
	})
	if err != nil || revision != "0" {
		t.Fatalf("cancelled job changed store: %s %v", revision, err)
	}
	if _, err = f.store.Receipt(context.Background(), job.OperationID); err == nil {
		t.Fatal("cancelled job created business receipt")
	}
}
