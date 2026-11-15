//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"testing"
)

func webRuntimeSeedSelection(t *testing.T, store *WebAPIStore) {
	t.Helper()
	mutation := WebAPIMutation{ExpectedRevision: "0", OperationID: "seed-active-profile"}
	_, err := store.Transaction(context.Background(), mutation, "test:seed-runtime", mutation, func(tx *WebAPIWriteTx) (any, error) {
		profile := WebAPIProfile{ID: "1", WebAPIProfileInput: WebAPIProfileInput{Type: "custom", Label: "active", ExtendType: "standard", SelectedMap: map[string]string{}, UnfoldSet: []string{}, Overrides: &WebAPIProfileOverrides{}}}
		if err := tx.InsertProfile(profile); err != nil {
			return nil, err
		}
		if err := tx.InsertProxyGroup(WebAPIProxyGroup{ID: "2", ProfileID: "1", Definition: WebAPIProxyGroupDefinition{Name: "G", Type: "select", Proxies: []string{"DIRECT", "REJECT", "A", "B", "P"}, Use: []string{}}}); err != nil {
			return nil, err
		}
		for i, name := range []string{"A", "B", "P"} {
			raw, _ := json.Marshal(name)
			definition := WebAPIProxyDefinition{"name": raw, "type": json.RawMessage(`"socks5"`), "server": json.RawMessage(`"192.0.2.1"`), "port": json.RawMessage(`1080`)}
			if err := tx.InsertCustomProxy(WebAPICustomProxy{ID: []WebAPIID{"3", "4", "5"}[i], Definition: definition}); err != nil {
				return nil, err
			}
		}
		settings := DefaultWebAPISettings()
		settings.CurrentProfileID = &profile.ID
		if err := tx.SaveSettings(settings); err != nil {
			return nil, err
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebRuntimeSelectionRequiresActualGenerationAndMembership(t *testing.T) {
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{SelectProxy: func(context.Context, string, string) (*ChangeProxyResult, error) {
		return &ChangeProxyResult{Changed: true}, nil
	}})
	for _, body := range []string{
		`{"expectedRevision":"1","operationId":"bad-generation","groupName":"G","proxyName":"P","runtimeGeneration":"6"}`,
		`{"expectedRevision":"1","operationId":"bad-membership","groupName":"G","proxyName":"not-a-member","runtimeGeneration":"7"}`,
		`{"expectedRevision":"1","operationId":"bad-group-name","groupName":"missing","proxyName":"P","runtimeGeneration":"7"}`,
	} {
		if code, result := s.request(t, "POST", "/api/v1/runtime/proxies:select", body, s.cookie, ""); code != 409 && code != 400 && code != 404 {
			t.Errorf("selection accepted %d %s", code, result)
		}
	}
	var profile WebAPIProfile
	if rev, err := s.store.View(context.Background(), func(tx *WebAPIReadTx) error { var err error; profile, err = tx.Profile("1"); return err }); err != nil || rev != "1" || len(profile.SelectedMap) != 0 {
		t.Fatalf("rejected selection changed canonical profile: %s %+v %v", rev, profile, err)
	}
	body := `{"expectedRevision":"1","operationId":"valid-selection","groupName":"G","proxyName":"P","runtimeGeneration":"7"}`
	if code, result := s.request(t, "POST", "/api/v1/runtime/proxies:select", body, s.cookie, ""); code != 200 {
		t.Fatalf("valid member %d %s", code, result)
	}
	if state := s.phase(t, "valid-selection"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	if rev, err := s.store.View(context.Background(), func(tx *WebAPIReadTx) error { var err error; profile, err = tx.Profile("1"); return err }); err != nil || rev != "2" || profile.SelectedMap["G"] != "P" {
		t.Fatalf("selection metadata and intent did not commit atomically: %s %+v %v", rev, profile, err)
	}
}
