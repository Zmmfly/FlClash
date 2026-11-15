//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestWebContentProfileAssetHTTPUploadCloneBackupRestoreAndNativeRefusal(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	status, body := f.request(t, "POST", "/api/v1/profiles:import", webContentImportRequest("asset-owner", "0"), f.cookie)
	profile := webContentHTTPDecode[WebAPIProfile](t, status, body).Data
	planet := webStatefulTestPlanet(t, profile.ID)
	request := WebAPIProfileAssetUpload{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "upload-planet"}, ID: profile.ID, Purpose: WebAPIStatefulPlanetPurpose, Bytes: planet.Bytes}
	status, body = f.request(t, "POST", "/api/v1/profiles:upload-asset", request, f.cookie)
	asset := webContentHTTPDecode[WebAPIProfileAssetResult](t, status, body)
	if asset.Revision != "2" || asset.Data.Reference != planet.Reference || asset.Data.Content.Key.Name != "zerotier-planet:"+planet.Reference.SHA256 {
		t.Fatalf("asset identity mismatch: %s", body)
	}
	path := "/api/v1/content/profile-asset/" + string(profile.ID) + "?resourceKey=" + asset.Data.Content.Key.Name
	status, body = f.request(t, "GET", path, nil, f.cookie)
	read := webContentHTTPDecode[WebAPIContentResult](t, status, body)
	if !bytes.Equal(read.Data.Bytes, planet.Bytes) || read.Revision != "2" {
		t.Fatal("typed asset read lost bytes or revision")
	}
	data := []byte(fmt.Sprintf("proxies:\n - {name: planet-node, type: zerotier, network: '0123456789abcdef', planet-asset: {purpose: zerotier-planet, sha256: '%s'}}\nrules: [MATCH,DIRECT]\n", planet.Reference.SHA256))
	write := WebAPIContentWrite{WebAPIMutation: WebAPIMutation{ExpectedRevision: "2", OperationID: "profile-planet-reference"}, Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: profile.ID}, Bytes: data}
	status, body = f.request(t, "POST", "/api/v1/content:write", write, f.cookie)
	webContentHTTPDecode[WebAPIContentMutationResult](t, status, body)
	clone := WebAPIProfileClone{WebAPIMutation: WebAPIMutation{ExpectedRevision: "3", OperationID: "clone-asset-profile"}, ID: profile.ID}
	status, body = f.request(t, "POST", "/api/v1/profiles:clone", clone, f.cookie)
	cloned := webContentHTTPDecode[WebAPIProfile](t, status, body)
	if cloned.Revision != "4" || cloned.Data.ID == profile.ID {
		t.Fatal("clone reused owner identity")
	}
	status, body = f.request(t, "GET", "/api/v1/content/profile-asset/"+string(cloned.Data.ID)+"?resourceKey="+asset.Data.Content.Key.Name, nil, f.cookie)
	copied := webContentHTTPDecode[WebAPIContentResult](t, status, body)
	if !bytes.Equal(copied.Data.Bytes, planet.Bytes) || copied.Data.Key.ID != cloned.Data.ID {
		t.Fatal("clone did not re-own planet bytes")
	}
	if status, body = f.request(t, "GET", "/api/v1/backups?format=native-v1", nil, f.cookie); status != 409 || !bytes.Contains(body, []byte("backup_native_profile_asset_unsupported")) {
		t.Fatalf("native export silently lost typed asset: %d %s", status, body)
	}
	status, archive := f.request(t, "GET", "/api/v1/backups?format=flclash-v1", nil, f.cookie)
	if status != 200 {
		t.Fatalf("typed asset archive: %d %s", status, archive)
	}
	status, body = f.request(t, "POST", "/api/v1/app:reset", WebAPIReset{WebAPIMutation: WebAPIMutation{ExpectedRevision: "4", OperationID: "reset-assets"}, Intent: "reset-application-data"}, f.cookie)
	webContentHTTPDecode[WebAPIResetResult](t, status, body)
	if status, _ = f.request(t, "GET", path, nil, f.cookie); status != 404 {
		t.Fatalf("reset retained asset: %d", status)
	}
	status, body = f.request(t, "POST", "/api/v1/backups:restore", WebAPIBackupRestore{WebAPIMutation: WebAPIMutation{ExpectedRevision: "5", OperationID: "restore-assets"}, Intent: "restore-application-data", Strategy: "override", ArchiveBase64: archive}, f.cookie)
	webContentHTTPDecode[WebAPIBackupResult](t, status, body)
	status, body = f.request(t, "GET", path, nil, f.cookie)
	restored := webContentHTTPDecode[WebAPIContentResult](t, status, body)
	if restored.Revision != "6" || !bytes.Equal(restored.Data.Bytes, planet.Bytes) {
		t.Fatal("typed restore did not preserve exact asset")
	}
	bad := WebAPIContentWrite{WebAPIMutation: WebAPIMutation{ExpectedRevision: "6", OperationID: "bypass-asset-upload"}, Key: asset.Data.Content.Key, Bytes: []byte("not a planet")}
	if status, body = f.request(t, "POST", "/api/v1/content:write", bad, f.cookie); status != 409 || !bytes.Contains(body, []byte("profile_asset_upload_required")) {
		t.Fatalf("generic content bypass = %d %s", status, body)
	}
}

func TestWebContentProfileAssetRejectsPurposeOwnerHashAndUncommittedReferences(t *testing.T) {
	f := newWebContentHTTPFixture(t, nil, nil)
	status, body := f.request(t, "POST", "/api/v1/profiles:import", webContentImportRequest("asset-validation-owner", "0"), f.cookie)
	profile := webContentHTTPDecode[WebAPIProfile](t, status, body).Data
	asset := webStatefulTestPlanet(t, profile.ID)
	request := WebAPIProfileAssetUpload{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "bad-asset"}, ID: profile.ID, Purpose: "arbitrary-file", Bytes: asset.Bytes}
	if status, _ = f.request(t, "POST", "/api/v1/profiles:upload-asset", request, f.cookie); status != 400 {
		t.Fatalf("untyped purpose=%d", status)
	}
	request.Purpose = WebAPIStatefulPlanetPurpose
	request.ID = "999999"
	if status, _ = f.request(t, "POST", "/api/v1/profiles:upload-asset", request, f.cookie); status != 404 {
		t.Fatalf("missing owner=%d", status)
	}
	request.ID = profile.ID
	request.Bytes = []byte("not a planet")
	if status, _ = f.request(t, "POST", "/api/v1/profiles:upload-asset", request, f.cookie); status != 400 {
		t.Fatalf("bad planet=%d", status)
	}
	write := WebAPIContentWrite{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "unknown-planet-reference"}, Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: profile.ID}, Bytes: []byte(fmt.Sprintf("proxies:\n - {name: planet-node, type: zerotier, network: '0123456789abcdef', planet-asset: {purpose: zerotier-planet, sha256: '%s'}}\n", asset.Reference.SHA256))}
	if status, body = f.request(t, "POST", "/api/v1/content:write", write, f.cookie); status != 409 || !bytes.Contains(body, []byte("planet_asset_unavailable")) {
		t.Fatalf("uncommitted planet reference=%d %s", status, body)
	}
	request.ID = profile.ID
	request.Bytes = asset.Bytes
	request.OperationID = "valid-planet"
	status, body = f.request(t, "POST", "/api/v1/profiles:upload-asset", request, f.cookie)
	webContentHTTPDecode[WebAPIProfileAssetResult](t, status, body)
	var candidate webBackupCandidate
	_, err := f.store.View(context.Background(), func(tx *WebAPIReadTx) error { var err error; candidate, err = webBackupSnapshot(tx, false); return err })
	if err != nil {
		t.Fatal(err)
	}
	for i := range candidate.Contents {
		if candidate.Contents[i].Key.Kind == WebAPIContentProfileAsset {
			candidate.Contents[i].Key.Name = "zerotier-planet:" + fmt.Sprintf("%064x", 1)
		}
	}
	if _, err = webBackupEncodeTyped(candidate); err == nil {
		t.Fatal("typed backup accepted a false purpose-bound asset digest")
	}
	metadata, err := json.Marshal(candidate.Data)
	if err != nil || bytes.Contains(metadata, []byte("state-ledger")) {
		t.Fatal("business backup copied state ledger")
	}
}
