//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type settingsHTTPFixture struct {
	store   *WebAPIStore
	server  *httptest.Server
	commits atomic.Int64
}

func newSettingsHTTPFixture(t *testing.T) *settingsHTTPFixture {
	t.Helper()
	fixture := &settingsHTTPFixture{}
	store, err := OpenWebAPIStore(WebAPIStoreOptions{DataDir: filepath.Join(t.TempDir(), "data"), OnCommit: func(WebAPICommit) { fixture.commits.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	fixture.store = store
	mux := http.NewServeMux()
	RegisterWebAPIResourceRoutes(mux, store)
	RegisterWebAPISettingsRoutes(mux, store)
	fixture.server = httptest.NewServer(mux)
	t.Cleanup(func() {
		fixture.server.Close()
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return fixture
}

func (fixture *settingsHTTPFixture) request(t *testing.T, client *http.Client, method, path, body, session string) (int, []byte, http.Header) {
	t.Helper()
	request, err := http.NewRequest(method, fixture.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	request.AddCookie(&http.Cookie{Name: "synthetic-session", Value: session})
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data, response.Header
}

func settingsTestDecode(t *testing.T, body []byte) WebAPIEnvelope[WebAPISettings] {
	t.Helper()
	var envelope WebAPIEnvelope[WebAPISettings]
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func settingsTestError(t *testing.T, body []byte, code string) {
	t.Helper()
	var envelope struct {
		Error WebAPIError `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != code {
		t.Fatalf("code=%s want %s", envelope.Error.Code, code)
	}
}

func TestWebAPISettingsContractHTTPDefaultsAndStrictSections(t *testing.T) {
	fixture := newSettingsHTTPFixture(t)
	client := fixture.server.Client()
	status, body, headers := fixture.request(t, client, "GET", "/api/v1/settings", "", "one")
	if status != 200 || headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("GET status=%d", status)
	}
	read := settingsTestDecode(t, body)
	if read.Revision != "0" || !reflect.DeepEqual(read.Data, DefaultWebAPISettings()) {
		t.Fatal("defaults do not match the frozen settings contract")
	}
	invalid := []string{
		`{"expectedRevision":"0","operationId":"invalid01","config":{}}`,
		`{"expectedRevision":"0","operationId":"invalid02","patch":{"themeProps":{}}}`,
		`{"expectedRevision":"0","operationId":"invalid03","patch":{"patchClashConfig":{"mixed-port":1,"unknown":true}}}`,
		`{"expectedRevision":"0","operationId":"invalid04","patch":{"patchClashConfig":{"mode":"rule","mode":"global"}}}`,
		`{"expectedRevision":"0","operationId":"invalid05","patch":{"patchClashConfig":{"Mode":"global"}}}`,
		`{"expectedRevision":"0","operationId":"invalid06","patch":{"patchClashConfig":{"mixed-port":65536}}}`,
		`{"expectedRevision":"0","operationId":"invalid07","patch":{"patchClashConfig":{"mode":null}}}`,
		`{"expectedRevision":"0","operationId":"invalid08","patch":{"patchClashConfig":{"global-ua":{}}}}`,
		`{"expectedRevision":"0","operationId":"invalid09","patch":{"patchClashConfig":{"global-ua":{"value":null,"credential":"never-log-this"}}}}`,
		`{"expectedRevision":"0","operationId":"invalid10","patch":{"appSetting":{"sidebarExpanded":true}}}`,
		`{"expectedRevision":"0","operationId":"invalid11","patch":{"appliedRevision":"7"}}`,
	}
	for i, command := range invalid {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			status, body, _ := fixture.request(t, client, "POST", "/api/v1/settings:runtime", command, "one")
			if status != 400 {
				t.Fatalf("strict decoder accepted invalid command %d: %d", i, status)
			}
			settingsTestError(t, body, "invalid_json")
			if bytes.Contains(body, []byte("never-log-this")) {
				t.Fatal("error echoed a credential")
			}
		})
	}
	for _, path := range []string{"/api/v1/settings?sql=select", "/api/v1/settings?revision=0"} {
		status, _, _ := fixture.request(t, client, "GET", path, "", "one")
		if status != 400 {
			t.Fatal("untyped query was accepted")
		}
	}
	status, body, _ = fixture.request(t, client, "POST", "/api/v1/settings:runtime", `{"expectedRevision":"0","operationId":"emptypat01","patch":{"patchClashConfig":{}}}`, "one")
	if status != 400 {
		t.Fatal("empty nested patch accepted")
	}
	settingsTestError(t, body, "empty_patch")
	if fixture.commits.Load() != 0 {
		t.Fatal("invalid commands committed")
	}
}

func TestWebAPISettingsHTTPPartialCASAndTwoSessions(t *testing.T) {
	fixture := newSettingsHTTPFixture(t)
	first := &http.Client{Transport: fixture.server.Client().Transport}
	second := &http.Client{Transport: fixture.server.Client().Transport}
	for _, session := range []string{"one", "two"} {
		status, body, _ := fixture.request(t, first, "GET", "/api/v1/settings", "", session)
		if status != 200 || settingsTestDecode(t, body).Revision != "0" {
			t.Fatal("initial snapshot")
		}
	}
	command := `{"expectedRevision":"0","operationId":"runtime0001","patch":{"patchClashConfig":{"mode":"global","secret":"synthetic-private","tun":{"enable":true}},"appSetting":{"closeConnections":false}}}`
	status, original, headers := fixture.request(t, first, "POST", "/api/v1/settings:runtime", command, "one")
	if status != 200 || headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("runtime status=%d", status)
	}
	settings := settingsTestDecode(t, original)
	if settings.Revision != "1" || settings.Data.Runtime.PatchClashConfig.Mode != "global" || !settings.Data.Runtime.PatchClashConfig.Tun.Enable || settings.Data.Runtime.AppSetting.CloseConnections {
		t.Fatal("explicit runtime patch was not applied")
	}
	if settings.Data.Runtime.PatchClashConfig.MixedPort != 7890 || !settings.Data.Network.NetworkProps.SystemProxy {
		t.Fatal("unprovided fields changed")
	}
	stale := `{"expectedRevision":"0","operationId":"network0001","patch":{"networkProps":{"authentication":{"enable":true,"username":"local-user","password":"local-password"}}}}`
	status, body, _ := fixture.request(t, second, "POST", "/api/v1/settings:network", stale, "two")
	if status != 409 {
		t.Fatal("second session stale mutation succeeded")
	}
	settingsTestError(t, body, "revision_conflict")
	fresh := strings.Replace(stale, `"expectedRevision":"0"`, `"expectedRevision":"1"`, 1)
	status, body, _ = fixture.request(t, second, "POST", "/api/v1/settings:network", fresh, "two")
	if status != 200 {
		t.Fatalf("fresh network status=%d", status)
	}
	settings = settingsTestDecode(t, body)
	if settings.Revision != "2" || settings.Data.Network.NetworkProps.Authentication.Password != "local-password" || settings.Data.Runtime.PatchClashConfig.Secret != "synthetic-private" || settings.Data.Runtime.PatchClashConfig.Mode != "global" {
		t.Fatal("partial patch lost another section")
	}
	status, replay, _ := fixture.request(t, second, "POST", "/api/v1/settings:runtime", command, "two")
	if status != 200 || !bytes.Equal(replay, original) {
		t.Fatal("exact replay did not return original receipt")
	}
	status, body, _ = fixture.request(t, second, "POST", "/api/v1/settings:runtime", strings.Replace(command, "global", "direct", 1), "two")
	if status != 409 {
		t.Fatal("operation identity body change accepted")
	}
	settingsTestError(t, body, "operation_conflict")
	status, body, _ = fixture.request(t, second, "POST", "/api/v1/settings:runtime", `{"expectedRevision":"2","operationId":"controller01","patch":{"patchClashConfig":{"external-controller":"127.0.0.1:9090"}}}`, "two")
	if status != 403 {
		t.Fatal("Web controller was opened")
	}
	settingsTestError(t, body, "unsafe_controller")
	if fixture.commits.Load() != 2 {
		t.Fatal("replay/conflict applied another transaction")
	}
}

func settingsTestProfiles(t *testing.T, fixture *settingsHTTPFixture) (WebAPIID, WebAPIID, WebAPIRevision) {
	t.Helper()
	var first, second WebAPIProfile
	receipt, err := fixture.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "0", OperationID: "seedprofiles"}, "test:profiles", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		// Selector membership is read from each profile's own source, as a real import stores it.
		seed := func(label, member string) (WebAPIProfile, error) {
			profile, err := tx.CreateProfile(WebAPIProfileInput{Type: "file", Label: label})
			if err != nil {
				return profile, err
			}
			source := []byte("proxies:\n  - {name: " + member + ", type: socks5, server: 192.0.2.1, port: 1080}\nproxy-groups:\n  - {name: G, type: select, proxies: [" + member + "]}\nrules: [\"MATCH,G\"]\n")
			_, catalog, err := webContentProfile(source, "")
			if err != nil {
				return profile, err
			}
			if err = tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: profile.ID}, MediaType: "application/yaml", Bytes: source}); err != nil {
				return profile, err
			}
			return profile, tx.SetProfileCatalog(profile.ID, catalog)
		}
		var err error
		if first, err = seed("one", "A"); err != nil {
			return nil, err
		}
		if second, err = seed("two", "B"); err != nil {
			return nil, err
		}
		return []WebAPIProfile{first, second}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return first.ID, second.ID, receipt.Revision
}

func TestWebAPISettingsHTTPCurrentProfileFKAndCrossProfileSelection(t *testing.T) {
	fixture := newSettingsHTTPFixture(t)
	first, second, revision := settingsTestProfiles(t, fixture)
	client := fixture.server.Client()
	status, body, _ := fixture.request(t, client, "POST", "/api/v1/settings:current-profile", fmt.Sprintf(`{"expectedRevision":%q,"operationId":"profilebad01","currentProfileId":"999"}`, revision), "one")
	if status != 409 {
		t.Fatal("dangling profile FK accepted")
	}
	settingsTestError(t, body, "invalid_reference")
	status, _, _ = fixture.request(t, client, "POST", "/api/v1/settings:current-profile", `{"expectedRevision":"1","operationId":"profilenum01","currentProfileId":9007199254740993}`, "one")
	if status != 400 {
		t.Fatal("numeric profile ID accepted")
	}
	status, _, _ = fixture.request(t, client, "POST", "/api/v1/settings:current-profile", fmt.Sprintf(`{"expectedRevision":%q,"operationId":"current00001","currentProfileId":%q}`, revision, first), "one")
	if status != 200 {
		t.Fatal("valid current profile rejected")
	}
	status, body, _ = fixture.request(t, client, "POST", "/api/v1/settings:selected-map", fmt.Sprintf(`{"expectedRevision":"2","operationId":"crossprof001","profileId":%q,"groupName":"G","proxyName":"A"}`, second), "two")
	if status != 409 {
		t.Fatal("other profile catalog was used")
	}
	settingsTestError(t, body, "invalid_selection")
	status, _, _ = fixture.request(t, client, "POST", "/api/v1/settings:selected-map", fmt.Sprintf(`{"expectedRevision":"2","operationId":"selectone001","profileId":%q,"groupName":"G","proxyName":"A"}`, first), "one")
	if status != 200 {
		t.Fatal("valid selection rejected")
	}
	status, body, _ = fixture.request(t, client, "POST", "/api/v1/settings:selected-map", fmt.Sprintf(`{"expectedRevision":"2","operationId":"selecttwo001","profileId":%q,"groupName":"G","proxyName":"B"}`, second), "two")
	if status != 409 {
		t.Fatal("stale other-profile pick succeeded")
	}
	settingsTestError(t, body, "revision_conflict")
	status, _, _ = fixture.request(t, client, "POST", "/api/v1/settings:selected-map", fmt.Sprintf(`{"expectedRevision":"3","operationId":"selecttwo002","profileId":%q,"groupName":"G","proxyName":"B"}`, second), "two")
	if status != 200 {
		t.Fatal("inactive profile exact selection rejected")
	}
	_, err := fixture.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		a, err := tx.Profile(first)
		if err != nil {
			return err
		}
		b, err := tx.Profile(second)
		if err != nil {
			return err
		}
		if a.SelectedMap["G"] != "A" || b.SelectedMap["G"] != "B" {
			t.Fatal("selected map crossed profile owners")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	status, _, _ = fixture.request(t, client, "POST", "/api/v1/settings:selected-map", fmt.Sprintf(`{"expectedRevision":"4","operationId":"clearone0001","profileId":%q,"groupName":"G","proxyName":null}`, first), "one")
	if status != 200 {
		t.Fatal("explicit clear rejected")
	}
	status, _, _ = fixture.request(t, client, "POST", "/api/v1/settings:selected-map", fmt.Sprintf(`{"expectedRevision":"5","operationId":"globalone001","profileId":%q,"groupName":"GLOBAL","proxyName":"G"}`, first), "one")
	if status != 200 {
		t.Fatal("implicit GLOBAL selector rejected")
	}
	status, body, _ = fixture.request(t, client, "POST", "/api/v1/settings:selected-map", fmt.Sprintf(`{"expectedRevision":"6","operationId":"globalbad001","profileId":%q,"groupName":"GLOBAL","proxyName":"B"}`, first), "one")
	if status != 409 {
		t.Fatal("GLOBAL accepted another profile's member")
	}
	settingsTestError(t, body, "invalid_selection")
	status, body, _ = fixture.request(t, client, "POST", "/api/v1/settings:selected-map", fmt.Sprintf(`{"expectedRevision":"6","operationId":"missinggrp01","profileId":%q,"groupName":"Absent","proxyName":"A"}`, first), "one")
	if status != 409 {
		t.Fatal("undeclared group accepted")
	}
	settingsTestError(t, body, "invalid_group")
}

type settingsLostResponse struct {
	base http.RoundTripper
	lost bool
}

func (transport *settingsLostResponse) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if !transport.lost && request.Method == http.MethodPost {
		transport.lost = true
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		return nil, errors.New("synthetic response loss after commit")
	}
	return response, nil
}

func TestWebAPISettingsHTTPLostResponseRecoverAndDAVPartial(t *testing.T) {
	fixture := newSettingsHTTPFixture(t)
	transport := &settingsLostResponse{base: fixture.server.Client().Transport}
	client := &http.Client{Transport: transport}
	command := `{"expectedRevision":"0","operationId":"davlost00001","dav":{"uri":"https://dav.invalid/path?token=synthetic","user":"alice","password":"dav-private","fileName":"archive.zip"}}`
	request, err := http.NewRequest("POST", fixture.server.URL+"/api/v1/settings:dav", strings.NewReader(command))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if _, err = client.Do(request); err == nil {
		t.Fatal("response was not lost")
	}
	status, body, _ := fixture.request(t, fixture.server.Client(), "GET", "/api/v1/operations/davlost00001", "", "two")
	if status != 200 {
		t.Fatal("committed receipt could not be recovered")
	}
	recovered := settingsTestDecode(t, body)
	if recovered.Revision != "1" || recovered.OperationID != "davlost00001" || recovered.Data.DAV.Password != "dav-private" {
		t.Fatal("invalid recovered settings")
	}
	status, body, _ = fixture.request(t, fixture.server.Client(), "POST", "/api/v1/settings:dav", `{"expectedRevision":"1","operationId":"davuser00001","dav":{"user":"bob"}}`, "two")
	if status != 200 {
		t.Fatal("partial DAV update failed")
	}
	dav := settingsTestDecode(t, body).Data.DAV
	if dav.User != "bob" || dav.Password != "dav-private" || dav.URI != recovered.Data.DAV.URI || dav.FileName != "archive.zip" {
		t.Fatal("partial DAV update erased unprovided fields")
	}
	if fixture.commits.Load() != 2 {
		t.Fatal("unknown result duplicated a commit")
	}
}

func TestWebAPISettingsSnapshotNativeRoundTripAndCredentialIntent(t *testing.T) {
	settings := DefaultWebAPISettings()
	profile := WebAPIID("9007199254740993")
	settings.CurrentProfileID = &profile
	settings.Runtime.PatchClashConfig.Secret = "native-controller-private"
	settings.Runtime.PatchClashConfig.ExternalController = "127.0.0.1:9090"
	settings.Network.NetworkProps.Authentication = WebAPISettingsAuthentication{Enable: true, Username: "inbound", Password: "inbound-private"}
	presentation := webAPISettingsDefaultPresentation()
	locale := "zh_CN"
	presentation.AppSetting.Locale = &locale
	presentation.WindowGeometry.Width = 1320
	presentation.AppSetting.TableColumnWidths["records"] = map[string]float64{"name": 271.5}
	settings.NativePresentation = &presentation
	settings.DAV = &WebAPISettingsDAV{URI: "https://dav.invalid/backup?token=archive-secret", User: "native-user", Password: "native-dav-秘密", FileName: "native.zip"}
	snapshot := SnapshotWebAPISettingsWithOptions(settings, WebAPISettingsSnapshotOptions{IncludeDAVCredentials: true})
	native, err := NativeWebAPISettingsConfigWithOptions(snapshot, WebAPISettingsSnapshotOptions{IncludeDAVCredentials: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(native, []byte(`"currentProfileId":9007199254740993`)) {
		t.Fatal("native int64 identity was rounded or changed format")
	}
	if bytes.Contains(native, []byte("native-dav-秘密")) {
		t.Fatal("native password compatibility encoding was skipped")
	}
	decoded, err := NativeWebAPISettingsSnapshot(native)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot, decoded) {
		t.Fatal("typed native settings round trip lost application fields")
	}
	public := SnapshotWebAPISettings(settings)
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("archive-secret")) || bytes.Contains(encoded, []byte("native-user")) || bytes.Contains(encoded, []byte("native.zip")) {
		t.Fatal("default snapshot leaked part of the DAV tuple")
	}
	plain, err := NativeWebAPISettingsConfig(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(plain, []byte("davProps")) {
		t.Fatal("default native export included DAV")
	}
	current := DefaultWebAPISettings()
	current.DAV = &WebAPISettingsDAV{URI: "https://current.invalid/", User: "current", Password: "current-secret", FileName: "current.zip"}
	malicious := snapshot
	malicious.DAV = &WebAPISettingsDAV{URI: "http://attacker.invalid/?credential=never-connect", User: "attacker", Password: "changed", FileName: "stolen.zip"}
	restored, err := RestoreWebAPISettings(current, malicious)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.DAV, current.DAV) {
		t.Fatal("default restore combined an archive URI with current credentials")
	}
	restored, err = RestoreWebAPISettingsWithOptions(current, snapshot, WebAPISettingsRestoreOptions{RestoreDAVCredentials: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.DAV, settings.DAV) || restored.Runtime.PatchClashConfig.Secret != settings.Runtime.PatchClashConfig.Secret || !reflect.DeepEqual(restored.Network.NetworkProps.Authentication, settings.Network.NetworkProps.Authentication) {
		t.Fatal("explicit restore lost an application credential tuple")
	}
	root := t.TempDir()
	auth := filepath.Join(root, "credentials.json")
	before := []byte("synthetic-web-auth-state")
	if err = os.WriteFile(auth, before, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = RestoreWebAPISettingsWithOptions(current, snapshot, WebAPISettingsRestoreOptions{RestoreDAVCredentials: true})
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(auth)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("settings restore changed Web auth state")
	}
	if _, err = NativeWebAPISettingsSnapshot([]byte(`{"themeProps":{},"arbitraryAuthority":true}`)); err == nil {
		t.Fatal("unknown native settings field silently dropped")
	}
}

func TestWebAPISettingsPublicProjectionDoesNotPublishNativePresentation(t *testing.T) {
	fixture := newSettingsHTTPFixture(t)
	_, err := fixture.store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "0", OperationID: "presentation01"}, "test:presentation", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		settings := DefaultWebAPISettings()
		presentation := webAPISettingsDefaultPresentation()
		locale := "fr"
		presentation.AppSetting.Locale = &locale
		settings.NativePresentation = &presentation
		return struct{}{}, tx.SaveSettings(settings)
	})
	if err != nil {
		t.Fatal(err)
	}
	status, body, _ := fixture.request(t, fixture.server.Client(), "GET", "/api/v1/settings", "", "one")
	if status != 200 || bytes.Contains(body, []byte("nativePresentation")) || bytes.Contains(body, []byte("tableColumnWidths")) {
		t.Fatal("private native presentation entered the browser authority DTO")
	}
	status, body, _ = fixture.request(t, fixture.server.Client(), "POST", "/api/v1/settings:runtime", `{"expectedRevision":"1","operationId":"presentation02","patch":{"patchClashConfig":{"mode":"direct"}}}`, "one")
	if status != 200 || bytes.Contains(body, []byte("nativePresentation")) {
		t.Fatal("receipt published native presentation")
	}
}
