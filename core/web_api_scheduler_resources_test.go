//go:build webpanel && !android

package core

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebAPISchedulerAppResourcesInvokeTypedServicesNotClientDownloads(t *testing.T) {
	clock := newWebSchedulerFakeClock()
	store := newWebSchedulerStore(t, clock)
	webSchedulerMutate(t, store, "scheduler-app-resources", func(tx *WebAPIWriteTx) (any, error) {
		settings := DefaultWebAPISettings()
		settings.Runtime.PatchClashConfig.GeoAutoUpdate = true
		if err := tx.SaveSettings(settings); err != nil {
			return nil, err
		}
		id, err := tx.AllocateID()
		if err != nil {
			return nil, err
		}
		url := "http://public.test/script"
		if err = tx.InsertScript(WebAPIScript{ID: id, Label: "Script", URL: &url, LastUpdateTime: clock.Now().Add(-25 * time.Hour).Format(time.RFC3339Nano)}); err != nil {
			return nil, err
		}
		id, err = tx.AllocateID()
		if err != nil {
			return nil, err
		}
		if err = tx.InsertIconSet(WebAPIIconSet{ID: id, Name: "Gallery", URL: "http://public.test/gallery", Icons: []WebAPIIconSetIcon{}}); err != nil {
			return nil, err
		}
		id, err = tx.AllocateID()
		if err != nil {
			return nil, err
		}
		if err = tx.InsertClashProvider(WebAPIClashProvider{ID: id, Label: "Rules", URL: "http://public.test/rules", Behavior: "domain", Format: "yaml"}); err != nil {
			return nil, err
		}
		return true, nil
	})
	var dials atomic.Int32
	options := webSchedulerSourceOptions(clock, false, func(_ context.Context, r *http.Request) string {
		switch r.URL.Path {
		case "/script":
			return "function main(config) { return config; }"
		case "/gallery":
			return "{\"name\":\"Gallery\",\"icons\":[{\"name\":\"star\",\"url\":\"http://public.test/star.svg\"}]}"
		case "/rules":
			return "payload:\n  - example.test\n"
		default:
			t.Error("unexpected resource fetch")
			return ""
		}
	}, &dials)
	content := NewWebAPIContentService(store, WebAPIContentOptions{SourceOptions: options, Now: clock.Now})
	s := webSchedulerNew(t, store, clock, WebAPISchedulerContentBackend(content, options))
	if err := s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, status, err := s.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, target := range status.Targets {
		switch target.Kind {
		case WebAPISchedulerScript, WebAPISchedulerIconSet, WebAPISchedulerRuleProvider:
			count++
			if target.LastSuccess == nil || target.Phase != "idle" {
				t.Fatal("typed resource service did not confirm update", target)
			}
			if target.Kind != WebAPISchedulerIconSet && !target.RequiresApply {
				t.Fatal("resource update pretended runtime already applied", target)
			}
		}
	}
	if count != 3 || dials.Load() != 3 {
		t.Fatal("resource update dispatch was incomplete", count, dials.Load())
	}
}

func TestWebAPISchedulerLocalMITMRestoresMissingDependenciesAndBlocksMissingManifest(t *testing.T) {
	for _, manifestPresent := range []bool{true, false} {
		t.Run(map[bool]string{true: "missing_dependency", false: "missing_local_manifest"}[manifestPresent], func(t *testing.T) {
			clock := newWebSchedulerFakeClock()
			store := newWebSchedulerStore(t, clock)
			var id WebAPIID
			webSchedulerMutate(t, store, "scheduler-local-module", func(tx *WebAPIWriteTx) (any, error) {
				settings := DefaultWebAPISettings()
				settings.Runtime.PatchClashConfig.Mitm.Enable = true
				if err := tx.SaveSettings(settings); err != nil {
					return nil, err
				}
				var err error
				id, err = tx.AllocateID()
				if err != nil {
					return nil, err
				}
				module := WebAPIMitmModule{ID: id, Name: "Local", Enabled: true, Scripts: 1, Arguments: map[string]string{}, Resources: map[string]string{"http://public.test/dependency.js": "missing-cache"}, LastUpdateTime: clock.Now().Format(time.RFC3339Nano)}
				if err = tx.InsertMitmModule(module); err != nil {
					return nil, err
				}
				if manifestPresent {
					err = tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentMitmModule, ID: id}, MediaType: "text/plain", Bytes: []byte("#!name=Local\n[Script]\nmain = type=http-request,pattern=.*,script-path=http://public.test/dependency.js\n")})
				}
				return module, err
			})
			var dials atomic.Int32
			options := webSchedulerSourceOptions(clock, false, func(context.Context, *http.Request) string { return "function main() { return {}; }" }, &dials)
			content := NewWebAPIContentService(store, WebAPIContentOptions{SourceOptions: options, Now: clock.Now})
			s := webSchedulerNew(t, store, clock, WebAPISchedulerContentBackend(content, options))
			if err := s.RunOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			state := webSchedulerTargetStatus(t, s, WebAPISchedulerModule, string(id))
			if manifestPresent {
				if dials.Load() != 1 || state.MissingContent || state.LastSuccess == nil || !state.ConfirmationRequired {
					t.Fatal("local dependency recovery lost confirmation", state)
				}
			} else {
				if dials.Load() != 0 || state.Phase != "blocked" || state.ErrorCode != "local_content_missing" || state.LastSuccess != nil {
					t.Fatal("missing local module was invented", state)
				}
			}
		})
	}
}
