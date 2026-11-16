//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"testing"
)

const selectionTestSource = "proxies:\n  - {name: A, type: socks5, server: 192.0.2.1, port: 1080}\n  - {name: B, type: socks5, server: 192.0.2.2, port: 1080}\nproxy-groups:\n  - {name: Pick, type: select, proxies: [A, B]}\n  - {name: Auto, type: url-test, proxies: [A, B], url: 'http://probe.invalid/generate_204', interval: 300}\nrules:\n  - MATCH,Pick\n"

type selectionTestRig struct {
	t *testing.T
	f *webContentHTTPFixture
}

func (r selectionTestRig) revision() WebAPIRevision {
	r.t.Helper()
	status, body := r.f.request(r.t, "GET", "/api/v1/meta", nil, r.f.cookie)
	var meta WebAPIEnvelope[json.RawMessage]
	if status != 200 || json.Unmarshal(body, &meta) != nil {
		r.t.Fatalf("meta %d: %s", status, body)
	}
	return meta.Revision
}

func (r selectionTestRig) profile(label string) WebAPIProfile {
	r.t.Helper()
	status, body := r.f.request(r.t, "GET", "/api/v1/profiles", nil, r.f.cookie)
	var listed WebAPIEnvelope[[]WebAPIProfile]
	if status != 200 || json.Unmarshal(body, &listed) != nil {
		r.t.Fatalf("profiles %d: %s", status, body)
	}
	for _, profile := range listed.Data {
		if profile.Label == label {
			return profile
		}
	}
	r.t.Fatalf("profile %q is absent: %s", label, body)
	return WebAPIProfile{}
}

// update saves the whole profile with one change, as the Web client does.
func (r selectionTestRig) update(operation string, want int, code string, change func(*WebAPIProfileInput)) {
	r.t.Helper()
	profile := r.profile("Selection")
	input := profile.WebAPIProfileInput
	input.SelectedMap = maps.Clone(input.SelectedMap)
	change(&input)
	before := r.revision()
	status, body := r.f.request(r.t, "POST", "/api/v1/profiles:update", map[string]any{"expectedRevision": before, "operationId": operation, "id": profile.ID, "profile": input}, r.f.cookie)
	if status != want || code != "" && !strings.Contains(string(body), `"code":"`+code+`"`) {
		r.t.Fatalf("%s: status %d want %d %s: %s", operation, status, want, code, body)
	}
	if want != 200 && r.revision() != before {
		r.t.Fatalf("%s: rejected update committed a revision", operation)
	}
}

func (r selectionTestRig) refresh(operation, source string) {
	r.t.Helper()
	write := WebAPIContentWrite{WebAPIMutation: WebAPIMutation{ExpectedRevision: r.revision(), OperationID: operation}, Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: r.profile("Selection").ID}, Bytes: []byte(source)}
	if status, body := r.f.request(r.t, "POST", "/api/v1/content:write", write, r.f.cookie); status != 200 {
		r.t.Fatalf("%s %d: %s", operation, status, body)
	}
}

func newSelectionTestRig(t *testing.T) selectionTestRig {
	t.Helper()
	r := selectionTestRig{t: t, f: newWebContentHTTPFixture(t, nil, nil)}
	request := webContentImportRequest("selection-import", "0")
	request.Label = "Selection"
	request.Source.Bytes = []byte(selectionTestSource)
	if status, body := r.f.request(t, "POST", "/api/v1/profiles:import", request, r.f.cookie); status != 200 {
		t.Fatalf("import %d: %s", status, body)
	}
	return r
}

func selectionName(name string) *string { return &name }

// An imported subscription stores no group members in its catalog: the open group and the picked
// members resolve against its source, where GLOBAL exists without being declared.
func TestWebAPIProfileSelectionFollowsSourceGroupsAndImplicitGlobal(t *testing.T) {
	r := newSelectionTestRig(t)
	r.update("selection-open-group", 200, "", func(p *WebAPIProfileInput) { p.CurrentGroupName = selectionName("Pick") })
	r.update("selection-open-global", 200, "", func(p *WebAPIProfileInput) { p.CurrentGroupName = selectionName("GLOBAL") })
	r.update("selection-pick-members", 200, "", func(p *WebAPIProfileInput) { p.SelectedMap = map[string]string{"Auto": "B", "GLOBAL": "Pick"} })
	r.update("selection-absent-member", 409, "invalid_reference", func(p *WebAPIProfileInput) { p.SelectedMap["Pick"] = "Missing" })
	r.update("selection-absent-group", 404, "not_found", func(p *WebAPIProfileInput) { p.SelectedMap["Missing group"] = "A" })
	r.update("selection-open-proxy", 409, "invalid_reference", func(p *WebAPIProfileInput) { p.CurrentGroupName = selectionName("A") })
	r.update("selection-bad-name", 400, "invalid_selection", func(p *WebAPIProfileInput) { p.SelectedMap["Pick"] = "bad\nname" })
	profile := r.profile("Selection")
	if profile.CurrentGroupName == nil || *profile.CurrentGroupName != "GLOBAL" || !maps.Equal(profile.SelectedMap, map[string]string{"Auto": "B", "GLOBAL": "Pick"}) {
		t.Fatalf("stored selection changed: %+v %v", profile.CurrentGroupName, profile.SelectedMap)
	}
}

func TestWebAPIProfileKeepsSelectionsRenamedByRefresh(t *testing.T) {
	r := newSelectionTestRig(t)
	remembered := map[string]string{"Auto": "B", "Pick": "A"}
	r.update("stale-remember", 200, "", func(p *WebAPIProfileInput) {
		p.CurrentGroupName, p.SelectedMap = selectionName("Auto"), maps.Clone(remembered)
	})
	r.refresh("stale-refresh", strings.NewReplacer("name: B", "name: C", "[A, B]", "[A, C]", "Auto", "Fast").Replace(selectionTestSource))
	r.update("stale-unrelated-edit", 200, "", func(p *WebAPIProfileInput) { p.UnfoldSet = []string{"Pick"} })
	profile := r.profile("Selection")
	if profile.CurrentGroupName == nil || *profile.CurrentGroupName != "Auto" || !maps.Equal(profile.SelectedMap, remembered) {
		t.Fatalf("remembered selection was rewritten: %+v %v", profile.CurrentGroupName, profile.SelectedMap)
	}
	r.update("stale-new-absent", 409, "invalid_reference", func(p *WebAPIProfileInput) { p.SelectedMap["Pick"] = "B" })
	r.update("stale-reopen-absent", 409, "invalid_reference", func(p *WebAPIProfileInput) { p.CurrentGroupName = selectionName("Missing") })
	r.update("stale-repick", 200, "", func(p *WebAPIProfileInput) {
		p.CurrentGroupName = selectionName("Fast")
		p.SelectedMap["Fast"] = "C"
	})

	merged := webBackupFixture()
	archive, err := webBackupEncodeTyped(merged)
	if err != nil {
		t.Fatal(err)
	}
	restore := WebAPIBackupRestore{WebAPIMutation: WebAPIMutation{ExpectedRevision: r.revision(), OperationID: "stale-merge"}, Intent: "restore-application-data", Strategy: "merge", OnlyProfiles: true, ArchiveBase64: archive}
	if status, body := r.f.request(t, "POST", "/api/v1/backups:restore", restore, r.f.cookie); status != 200 {
		t.Fatalf("merge beside a stale selection %d: %s", status, body)
	}
	if got := r.profile("Selection").SelectedMap; got["Auto"] != "B" || r.profile(merged.Data.Profiles[0].Label).ID != merged.Data.Profiles[0].ID {
		t.Fatalf("merge changed the retained profile: %v", got)
	}
}

func selectionTestArchive(t *testing.T, group *string, picks map[string]string) []byte {
	t.Helper()
	candidate := webBackupFixture()
	candidate.Data.Profiles[0].Label = "Selection"
	candidate.Data.Profiles[0].CurrentGroupName = group
	candidate.Data.Profiles[0].SelectedMap = picks
	candidate.Contents[0].Bytes = []byte(selectionTestSource)
	candidate.Contents[0].SHA256 = webContentDigest(candidate.Contents[0].Bytes)
	archive, err := webNativeEncodeArchive(context.Background(), candidate, []byte(`{"version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

// The native client stores GLOBAL as the open group whenever the outbound mode becomes global.
func TestWebBackupRestoreKeepsNativeRememberedSelections(t *testing.T) {
	r := selectionTestRig{t: t, f: newWebContentHTTPFixture(t, nil, nil)}
	picks := map[string]string{"Auto": "B", "Renamed group": "Renamed member"}
	restore := WebAPIBackupRestore{WebAPIMutation: WebAPIMutation{ExpectedRevision: "0", OperationID: "selection-restore"}, Intent: "restore-application-data", Strategy: "override", OnlyProfiles: true, ArchiveBase64: selectionTestArchive(t, selectionName("GLOBAL"), picks)}
	if status, body := r.f.request(t, "POST", "/api/v1/backups:restore", restore, r.f.cookie); status != 200 {
		t.Fatalf("restore %d: %s", status, body)
	}
	profile := r.profile("Selection")
	if profile.CurrentGroupName == nil || *profile.CurrentGroupName != "GLOBAL" || !maps.Equal(profile.SelectedMap, picks) {
		t.Fatalf("restored selection changed: %+v %v", profile.CurrentGroupName, profile.SelectedMap)
	}
	r.update("selection-after-restore", 200, "", func(p *WebAPIProfileInput) { p.UnfoldSet = []string{"Pick"} })
	restore.WebAPIMutation = WebAPIMutation{ExpectedRevision: r.revision(), OperationID: "selection-restore-bad"}
	restore.ArchiveBase64 = selectionTestArchive(t, nil, map[string]string{"Auto": "bad\nname"})
	if status, body := r.f.request(t, "POST", "/api/v1/backups:restore", restore, r.f.cookie); status != 400 || !strings.Contains(string(body), `"code":"invalid_selection"`) {
		t.Fatalf("malformed remembered selection %d: %s", status, body)
	}
}

func TestWebAPICatalogRememberedSelectionsFollowRenameAndOutliveRemoval(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	profile := r.customProfile("Remembering")
	for _, name := range []string{"Node", "Other"} {
		r.call("POST", "/api/v1/custom-proxies:create", catalogMutation(r.revision(), "remember-proxy-"+name, map[string]any{"proxy": catalogProxyInput(name)}), 200)
	}
	group := catalogData[WebAPIProxyGroup](t, r.call("POST", "/api/v1/proxy-groups:create", catalogMutation(r.revision(), "remember-group", map[string]any{"profileId": profile.ID, "group": catalogGroupInput("G", "Node", "Other")}), 200)).Data
	remember := func(operation string, picks map[string]string) {
		t.Helper()
		input := profile.WebAPIProfileInput
		input.CurrentGroupName, input.SelectedMap = selectionName("GLOBAL"), picks
		r.call("POST", "/api/v1/profiles:update", catalogMutation(r.revision(), operation, map[string]any{"id": profile.ID, "profile": input}), 200)
	}
	stored := func() map[string]string {
		t.Helper()
		var picks map[string]string
		if _, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
			p, err := tx.Profile(profile.ID)
			picks = p.SelectedMap
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return picks
	}
	remember("remember-global-group", map[string]string{"GLOBAL": "G", "G": "Node"})
	r.call("POST", "/api/v1/proxy-groups:update", catalogMutation(r.revision(), "remember-rename-group", map[string]any{"profileId": profile.ID, "id": group.ID, "group": catalogGroupInput("G2", "Node", "Other")}), 200)
	if got := stored(); !maps.Equal(got, map[string]string{"GLOBAL": "G2", "G2": "Node"}) {
		t.Fatalf("GLOBAL did not follow the group rename: %v", got)
	}
	remember("remember-global-proxy", map[string]string{"GLOBAL": "Other", "G2": "Node"})
	var other WebAPIID
	for _, proxy := range catalogData[[]WebAPICustomProxy](t, r.call("GET", "/api/v1/custom-proxies", nil, 200)).Data {
		if webAPIProxyString(proxy.Definition, "name") == "Other" {
			other = proxy.ID
		}
	}
	r.call("POST", "/api/v1/custom-proxies:update", catalogMutation(r.revision(), "remember-rename-proxy", map[string]any{"id": other, "proxy": catalogProxyInput("Other2")}), 200)
	if got := stored(); !maps.Equal(got, map[string]string{"GLOBAL": "Other2", "G2": "Node"}) {
		t.Fatalf("GLOBAL did not follow the proxy rename: %v", got)
	}
	r.call("POST", "/api/v1/proxy-groups:update", catalogMutation(r.revision(), "remember-drop-member", map[string]any{"profileId": profile.ID, "id": group.ID, "group": catalogGroupInput("G2", "Other2")}), 200)
	r.call("POST", "/api/v1/proxy-groups:create", catalogMutation(r.revision(), "remember-later-edit", map[string]any{"profileId": profile.ID, "group": catalogGroupInput("Later", "Other2")}), 200)
	if got := stored(); got["G2"] != "Node" {
		t.Fatalf("a remembered member was dropped with its group entry: %v", got)
	}
}

func TestWebAPIResetDialersIgnoresRememberedSelections(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	profile := catalogData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", catalogMutation(r.revision(), "reset-remembered-create", map[string]any{"profile": map[string]any{"type": "file", "label": "Refreshed", "autoUpdateDurationMillis": 86400000}}), 200)).Data
	r.seed("reset-remembered-seed", func(tx *WebAPIWriteTx) error {
		source := []byte(selectionTestSource)
		_, catalog, err := webContentProfile(source, "")
		if err != nil {
			return err
		}
		if err = tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: profile.ID}, MediaType: "application/yaml", Bytes: source}); err != nil {
			return err
		}
		if err = tx.SetProfileCatalog(profile.ID, catalog); err != nil {
			return err
		}
		stored, err := tx.Profile(profile.ID)
		if err != nil {
			return err
		}
		stored.CurrentGroupName, stored.SelectedMap = selectionName("Renamed group"), map[string]string{"Auto": "Renamed member"}
		return tx.SaveProfile(stored)
	})
	r.call("POST", "/api/v1/profiles:reset-dialers", catalogMutation(r.revision(), "reset-remembered", map[string]any{"id": profile.ID}), 200)
}
