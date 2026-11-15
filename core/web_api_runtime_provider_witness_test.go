//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"testing"

	bolt "github.com/metacubex/bbolt"
)

func TestWebRuntimeApplyCASRechecksPrivateProviderOperationAndByteDigestWithoutFreshRevision(t *testing.T) {
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	webRuntimeSeedProfile(t, s, "file", webRuntimeRuleSourceYAML)
	source := webRuntimeTestSource(t, s)
	if code, body := s.request(t, "POST", "/api/v1/runtime/provider-sources/"+string(source.ID)+":write", webRuntimeProviderWriteBody(t, source, "1", "witness-original-provider", []byte("original.example.test\n")), s.cookie, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if state := s.phase(t, "witness-original-provider"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	var request WebAPIRuntimeApplyRequest
	if err := json.Unmarshal([]byte(webRuntimeApplyBody(t, "witness-unaccepted-apply", map[string]any{"expectedRevision": "2"})), &request); err != nil {
		t.Fatal(err)
	}
	prepared, _, err := s.runtime.prepareApply(context.Background(), request, true)
	if err != nil || len(prepared.providers) != 1 {
		t.Fatalf("provider witness missing %+v %v", prepared.providers, err)
	}
	var original webRuntimePreparedProvider
	if _, err = s.store.View(context.Background(), func(tx *WebAPIReadTx) error { var err error; original, err = tx.runtimeProvider(source.ID); return err }); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"bytes", "operation", "phase", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			if err := s.store.updateRuntime(context.Background(), func(tx *bolt.Tx) error {
				if mode == "deleted" {
					return tx.Bucket([]byte(webRuntimeProviderBucket)).Delete([]byte(source.ID))
				}
				changed := original
				switch mode {
				case "bytes":
					changed.Bytes = []byte("changed.example.test\n")
				case "operation":
					changed.OperationID = "newer-cache-operation"
				case "phase":
					changed.Phase = "unknown"
				}
				return (&WebAPIWriteTx{WebAPIReadTx: &WebAPIReadTx{tx}}).saveRuntimeProvider(changed)
			}); err != nil {
				t.Fatal(err)
			}
			mutation := WebAPIMutation{ExpectedRevision: "2", OperationID: "provider-witness-reject-" + mode}
			_, err := s.store.Transaction(context.Background(), mutation, "test:apply-witness", mutation, func(tx *WebAPIWriteTx) (any, error) {
				if err := webRuntimeValidateProviderWitnesses(tx.WebAPIReadTx, prepared.providers); err != nil {
					return nil, err
				}
				return tx.RecordRuntimeIntent(mutation.OperationID, webRuntimeApply, webRuntimeDesiredApply{Request: request, Prepared: prepared})
			})
			if err == nil || webRuntimeFailure(err).Code != "provider_source_changed" {
				t.Fatalf("CAS accepted stale private %s with unchanged config revision: %v", mode, err)
			}
			if revision, err := s.store.View(context.Background(), func(*WebAPIReadTx) error { return nil }); err != nil || revision != "2" {
				t.Fatalf("witness check freshened the original token %s %v", revision, err)
			}
		})
	}
}
