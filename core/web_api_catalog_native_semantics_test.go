//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"reflect"
	"testing"
)

func TestWebAPICatalogContractNativeIconGalleryCompatibility(t *testing.T) {
	fixtures := []struct {
		name, body, gallery string
		icons               []WebAPIIconSetIcon
	}{
		{"QuantumultX", `{"name":"  Quantumult X set  ","description":"extra metadata is valid","icons":[{"name":"  US  ","url":"https://icons.unit.invalid/us.png","extra":true},42,{"name":"not-http","url":"file:///tmp/not-an-icon"},{"name":"duplicate","url":"https://icons.unit.invalid/us.png"},{"url":"https://icons.unit.invalid/Hong%20Kong.png?version=2"}]}`, "Quantumult X set", []WebAPIIconSetIcon{{Name: "US", URL: "https://icons.unit.invalid/us.png"}, {Name: "Hong Kong.png", URL: "https://icons.unit.invalid/Hong%20Kong.png?version=2"}}},
		{"Loon", `{"name":"Loon Gallery","icons":[null,{"name":7,"url":"https://icons.unit.invalid/jp.svg"},{"name":" ","url":"http://icons.unit.invalid/"},{"name":"invalid","url":12}]}`, "Loon Gallery", []WebAPIIconSetIcon{{Name: "jp.svg", URL: "https://icons.unit.invalid/jp.svg"}, {Name: "", URL: "http://icons.unit.invalid/"}}},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			parsed, err := webAPIParseIconSet([]byte(fixture.body))
			if err != nil || parsed.Name != fixture.gallery || !reflect.DeepEqual(parsed.Icons, fixture.icons) {
				t.Fatal(parsed, err)
			}
			if _, err := webAPIIconSetResource().build("1", webAPIIconSetInput{Name: parsed.Name, Icons: parsed.Icons}, nil); err != nil {
				t.Fatal("parsed native gallery cannot be stored", err)
			}
		})
	}
	for _, body := range []string{`{}`, `{"icons":[]}`, `{"icons":[{"url":"file:///tmp/no"}]}`, `[]`, `{"icons":{},"name":"bad"}`} {
		if _, err := webAPIParseIconSet([]byte(body)); err == nil {
			t.Fatal("invalid gallery accepted", body)
		}
	}
}

func TestWebAPICatalogContractDuplicateNativeDisplayNamesHTTP(t *testing.T) {
	for _, path := range []string{"mitm-modules", "icon-sets"} {
		t.Run(path, func(t *testing.T) {
			r := newCatalogTestRig(t, nil)
			field := "module"
			input := map[string]any{"name": "Same display name", "arguments": map[string]string{}, "enabled": true}
			if path == "icon-sets" {
				field = "iconSet"
				input = map[string]any{"name": "Same display name", "url": "", "icons": []any{}}
			}
			first := catalogData[map[string]any](t, r.call("POST", "/api/v1/"+path+":create", catalogMutation("0", "display-name-first", map[string]any{field: input}), 200)).Data
			second := catalogData[map[string]any](t, r.call("POST", "/api/v1/"+path+":create", catalogMutation("1", "display-name-second", map[string]any{field: input}), 200)).Data
			if first["id"] == second["id"] {
				t.Fatal("duplicate display name reused identity")
			}
			listed := catalogData[[]map[string]any](t, r.call("GET", "/api/v1/"+path, nil, 200)).Data
			if len(listed) != 2 {
				t.Fatal(listed)
			}
			r.call("POST", "/api/v1/"+path+":reorder", catalogMutation("2", "duplicate-display-order-id", map[string]any{"ids": []any{first["id"], first["id"]}}), 409)
		})
	}
}

func TestWebAPICatalogContractExactOutboundNamesAndCSVGuardHTTP(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	profile := r.customProfile("Exact")
	names := []string{" node, ", " target, ", `say "hi"`, `back\slash`}
	proxies := make([]WebAPICustomProxy, 0, len(names))
	for i, name := range names {
		body := r.call("POST", "/api/v1/custom-proxies:create", catalogMutation(r.revision(), "exact-proxy-"+string(rune('a'+i)), map[string]any{"proxy": catalogProxyInput(name)}), 200)
		proxies = append(proxies, catalogData[WebAPICustomProxy](t, body).Data)
	}
	groupName := " group, "
	input := catalogGroupInput(groupName, names...)
	input["definition"].(map[string]any)["include-all"] = true
	group := catalogData[WebAPIProxyGroup](t, r.call("POST", "/api/v1/proxy-groups:create", catalogMutation(r.revision(), "exact-group-create", map[string]any{"profileId": profile.ID, "group": input}), 200)).Data
	if group.Definition.Name != groupName || !webAPIBool(group.Definition.IncludeAll) {
		t.Fatal(group)
	}
	selection := catalogData[webAPIDialerSelection](t, r.call("POST", "/api/v1/proxy-dialers:replace", catalogMutation(r.revision(), "exact-chain-create", map[string]any{"profileId": profile.ID, "dialers": []any{map[string]any{"proxyId": proxies[0].ID, "target": names[1]}}}), 200)).Data
	if len(selection.Dialers) != 1 || selection.Dialers[0].Target != names[1] {
		t.Fatal(selection)
	}
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		catalog, err := tx.ProfileCatalog(profile.ID)
		if err != nil {
			return err
		}
		for _, name := range names {
			if !webAPIContains(catalog.OutboundNames, name) {
				t.Fatal("authority catalog silently filtered a valid identity", catalog)
			}
		}
		members, ready, err := WebAPICatalogGroupMemberNames(tx, profile.ID, groupName)
		if !ready || len(members) != len(names) {
			t.Fatal(members, ready)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r.call("POST", "/api/v1/rules:create", catalogMutation(r.revision(), "reject-unroundtrippable-target", map[string]any{"rule": map[string]any{"profileId": profile.ID, "ruleAction": "DOMAIN", "content": "unit.invalid", "ruleTarget": names[0]}}), 400)
}

func TestWebAPICatalogContractRenameCannotCorruptRuleText(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	profile := r.customProfile("CSV")
	proxy := catalogData[WebAPICustomProxy](t, r.call("POST", "/api/v1/custom-proxies:create", catalogMutation(r.revision(), "csv-proxy-create", map[string]any{"proxy": catalogProxyInput("Normal")}), 200)).Data
	r.call("POST", "/api/v1/proxy-groups:create", catalogMutation(r.revision(), "csv-group-create", map[string]any{"profileId": profile.ID, "group": catalogGroupInput("G", "Normal")}), 200)
	r.call("POST", "/api/v1/rules:create", catalogMutation(r.revision(), "csv-rule-create", map[string]any{"rule": map[string]any{"profileId": profile.ID, "ruleAction": "DOMAIN", "content": "unit.invalid", "ruleTarget": "Normal"}}), 200)
	before, revision := r.snapshot(), r.revision()
	r.call("POST", "/api/v1/custom-proxies:update", catalogMutation(revision, "csv-proxy-rename-reject", map[string]any{"id": proxy.ID, "proxy": catalogProxyInput(" node, ")}), 409)
	if r.revision() != revision || !bytes.Equal(before, r.snapshot()) {
		t.Fatal("rule reference was partially renamed")
	}
}

func TestWebAPICatalogContractOrdinaryGroupMembersAreOwnerScoped(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	var owners []WebAPIID
	fixtures := []string{catalogProfileFixture, "proxies:\n  - {name: Other, type: direct}\nproxy-groups:\n  - {name: G, type: select, proxies: [Other]}\nrules: [MATCH,G]\n", "proxy-providers:\n  Dynamic: {type: http, url: 'https://provider.unit.invalid/list', path: cache.yaml}\nproxy-groups:\n  - {name: G, type: select, use: [Dynamic]}\nrules: [MATCH,G]\n"}
	for i, fixture := range fixtures {
		label := "Source" + string(rune('A'+i))
		p := catalogData[WebAPIProfile](t, r.call("POST", "/api/v1/profiles:create", catalogMutation(r.revision(), "ordinary-create-"+label, map[string]any{"profile": map[string]any{"type": "file", "label": label, "autoUpdateDurationMillis": 86400000}}), 200)).Data
		owners = append(owners, p.ID)
		r.seed("ordinary-seed-"+label, func(tx *WebAPIWriteTx) error {
			return tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: p.ID}, MediaType: "application/yaml", Bytes: []byte(fixture)})
		})
	}
	_, err := r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		a, ready, err := WebAPICatalogGroupMemberNames(tx, owners[0], "G")
		if err != nil || !ready || !reflect.DeepEqual(a, []string{"A"}) {
			t.Fatal(a, ready, err)
		}
		b, ready, err := WebAPICatalogGroupMemberNames(tx, owners[1], "G")
		if err != nil || !ready || !reflect.DeepEqual(b, []string{"Other"}) {
			t.Fatal(b, ready, err)
		}
		unknown, ready, err := WebAPICatalogGroupMemberNames(tx, owners[2], "G")
		if err != nil || ready || len(unknown) != 0 {
			t.Fatal("unknown provider manufactured members", unknown, ready, err)
		}
		global, ready, err := WebAPICatalogGroupMemberNames(tx, owners[0], "GLOBAL")
		if err != nil || !ready || webAPIContains(global, "Other") || !webAPIContains(global, "G") || !webAPIContains(global, "B") {
			t.Fatal(global, ready, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
