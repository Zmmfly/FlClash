//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestWebContentHTTPProfileCatalogKeepsUniqueValidatedIconURLs(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	request := webContentImportRequest("profile-icons", "0")
	request.Source.Bytes = []byte("proxies: []\nproxy-groups:\n - {name: one, type: select, proxies: [DIRECT], icon: 'https://icons.test/icon.png?token=synthetic'}\n - {name: two, type: select, proxies: [DIRECT], icon: 'https://icons.test/icon.png?token=synthetic'}\n - {name: local, type: select, proxies: [DIRECT], icon: 'file:///private/icon.png'}\n")
	status, body := f.request(t, "POST", "/api/v1/profiles:import", request, f.cookie)
	created := webContentHTTPDecode[WebAPIProfile](t, status, body)
	_, catalog, err := webContentProfile(request.Source.Bytes, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.IconURLs) != 1 || catalog.IconURLs[0] != "https://icons.test/icon.png?token=synthetic" {
		t.Fatalf("icon projection changed raw URLs or included paths: %q", catalog.IconURLs)
	}
	status, body = f.request(t, "GET", "/api/v1/content/profile/"+string(created.Data.ID), nil, f.cookie)
	content := webContentHTTPDecode[WebAPIContentResult](t, status, body)
	if _, err = time.Parse(time.RFC3339Nano, content.Data.ModifiedAt); err != nil {
		t.Fatalf("stored modification time is missing: %q", content.Data.ModifiedAt)
	}
}

func TestWebContentHTTPDetachReturnsOwnerAndExactDetachedKeyWithoutFakeContent(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	input := WebAPIResourceImportCommand[webAPIModuleInput]{WebAPIMutation: WebAPIMutation{ExpectedRevision: "0", OperationID: "detach-module"}, Label: "Module", Source: WebAPIContentImportSource{Kind: "file", Bytes: []byte("#!name=Module\n[Map Local]\n.* data-type=file data=body.json\n")}, Attachments: []WebAPIContentAttachment{{SourceReference: "body.json", Bytes: []byte(`{"ok":true}`)}}}
	status, body := f.request(t, "POST", "/api/v1/mitm-modules:import", input, f.cookie)
	created := webContentHTTPDecode[WebAPIContentMutationResult](t, status, body)
	id := created.Data.MitmModule.ID
	name := created.Data.MitmModule.Resources["body.json"]
	request := WebAPIMitmDetach{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "detach-body"}, ID: id, SourceReference: "body.json"}
	status, receipt := f.request(t, "POST", "/api/v1/mitm-modules:detach", request, f.cookie)
	result := webContentHTTPDecode[WebAPIMitmDetachResult](t, status, receipt)
	if result.Data.MitmModule.ID != id || result.Data.DetachedKey != (WebAPIContentKey{Kind: WebAPIContentMitmResource, ID: id, Name: name}) || len(result.Data.MitmModule.Resources) != 0 {
		t.Fatalf("wrong detach result: %s", receipt)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(receipt, &envelope); err != nil {
		t.Fatal(err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(envelope["data"], &data); err != nil {
		t.Fatal(err)
	}
	if _, exists := data["content"]; exists {
		t.Fatal("detach invented a zero-byte content summary with an empty SHA")
	}
	status, replay := f.request(t, "POST", "/api/v1/mitm-modules:detach", request, f.cookie)
	if status != 200 || !bytes.Equal(receipt, replay) {
		t.Fatal("detach did not replay exact shape")
	}
	if status, _ = f.request(t, "GET", "/api/v1/content/mitm-resource/"+string(id)+"?resourceKey="+name, nil, f.cookie); status != 404 {
		t.Fatalf("detached resource remained addressable: %d", status)
	}
}
