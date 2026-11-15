//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebAPISchedulerRuleProviderManualHTTPWriteResetsRealDueBaseline(t *testing.T) {
	clock := newWebSchedulerFakeClock()
	store := newWebSchedulerStore(t, clock)
	var id WebAPIID
	webSchedulerMutate(t, store, "scheduler-mtime-seed", func(tx *WebAPIWriteTx) (any, error) {
		settings := DefaultWebAPISettings()
		settings.Runtime.PatchClashConfig.GeoAutoUpdate = true
		settings.Runtime.PatchClashConfig.GeoUpdateInterval = 6
		if err := tx.SaveSettings(settings); err != nil {
			return nil, err
		}
		var err error
		id, err = tx.AllocateID()
		if err != nil {
			return nil, err
		}
		if err = tx.InsertClashProvider(WebAPIClashProvider{ID: id, Label: "Rules", URL: "http://public.test/rules", Behavior: "domain", Format: "yaml"}); err != nil {
			return nil, err
		}
		return true, tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentRuleProvider, ID: id}, MediaType: "application/yaml", Bytes: []byte("payload:\n - initial.test\n"), ModifiedAt: "2001-01-01T00:00:00Z"})
	})
	var dials atomic.Int32
	options := webSchedulerSourceOptions(clock, false, func(context.Context, *http.Request) string { return "payload:\n - refreshed.test\n" }, &dials)
	content := NewWebAPIContentService(store, WebAPIContentOptions{SourceOptions: options, Now: clock.Now})
	s := webSchedulerNew(t, store, clock, WebAPISchedulerContentBackend(content, options))
	fixture := newWebSchedulerHTTPFixture(t, store, s, WebAPIContentOptions{SourceOptions: options, Now: clock.Now})
	key := WebAPIContentKey{Kind: WebAPIContentRuleProvider, ID: id}
	_, err := store.View(t.Context(), func(tx *WebAPIReadTx) error {
		value, err := tx.Content(key)
		if err == nil && value.ModifiedAt != clock.Now().UTC().Format(time.RFC3339Nano) {
			t.Error("PutContent trusted a caller modifiedAt")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if state := webSchedulerTargetStatus(t, s, WebAPISchedulerRuleProvider, string(id)); state.Phase != "idle" || state.LastSuccess != nil {
		t.Fatal("initial real modifiedAt was not used", state)
	}
	clock.Advance(6 * time.Hour)
	if err = s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if dials.Load() != 1 {
		t.Fatal("initial due provider did not use typed refresh")
	}
	clock.Advance(time.Hour)
	manual := WebAPIContentWrite{WebAPIMutation: WebAPIMutation{ExpectedRevision: webSchedulerRevision(t, store), OperationID: "scheduler-manual-provider-write"}, Key: key, Bytes: []byte("payload:\n - manually-edited.test\n")}
	status, body := fixture.request(t, "POST", "/api/v1/content:write", manual, fixture.cookie)
	if status != 200 {
		t.Fatalf("manual typed write %d %s", status, body)
	}
	clock.Advance(5 * time.Hour)
	if replayStatus, replay := fixture.request(t, "POST", "/api/v1/content:write", manual, fixture.cookie); replayStatus != 200 || !bytes.Equal(body, replay) {
		t.Fatal("manual replay changed immutable receipt")
	}
	if state := webSchedulerTargetStatus(t, s, WebAPISchedulerRuleProvider, string(id)); state.Phase != "idle" || state.DueAt == nil || !state.DueAt.Equal(clock.Now().Add(time.Hour)) {
		t.Fatal("manual write lost its true timestamp baseline", state)
	}
	if err = s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if dials.Load() != 1 {
		t.Fatal("scheduler used stale LastSuccess instead of the newer manual write")
	}
	clock.Advance(time.Hour)
	if err = s.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if dials.Load() != 2 {
		t.Fatal("manual-write based period did not become due")
	}
}
