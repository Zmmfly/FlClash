//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"testing"

	bolt "github.com/metacubex/bbolt"
)

func TestWebRuntimeSnapshotAggregateBudgetRejectsBeforeDecodingStoredBytes(t *testing.T) {
	budget := &webRuntimeSnapshotBudget{}
	for i := 0; i < 8; i++ {
		if err := budget.reserve(string(rune('a'+i)), 32<<20); err != nil {
			t.Fatal(err)
		}
	}
	if err := budget.reserve("ninth", 1); err == nil || webRuntimeFailure(err).Code != "runtime_snapshot_too_large" {
		t.Fatal("snapshot exceeded the approved 256 MiB aggregate budget")
	}
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	id := WebAPIRuntimeProviderID("1")
	if err := s.store.updateRuntime(context.Background(), func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte(webRuntimeProviderBucket))
		if err != nil {
			return err
		}
		return bucket.Put([]byte(id), []byte(`{"phase":"ready","bytes":"!!!!"}`))
	}); err != nil {
		t.Fatal(err)
	}
	budget = &webRuntimeSnapshotBudget{used: webRuntimeSnapshotBytes - 2}
	_, err := s.store.View(context.Background(), func(tx *WebAPIReadTx) error { _, err := budget.provider(tx, id); return err })
	if err == nil || webRuntimeFailure(err).Code != "runtime_snapshot_too_large" {
		t.Fatalf("private provider was decoded before aggregate preflight (invalid base64 would fail differently): %v", err)
	}
}

func TestWebRuntimeGeoBudgetAllows32MiBPlusAndRejectsAbove128MiBBeforeInstaller(t *testing.T) {
	data := bytes.Repeat([]byte{'x'}, (32<<20)+1)
	plain, err := webRuntimeGeoDecompress(data)
	if err != nil || len(plain) != len(data) || &plain[0] != &data[0] {
		t.Fatal("raw geo data lost its approved >32 MiB allowance or copied before preflight")
	}
	if err := webRuntimeValidateGeo("MMDB", data); err == nil || webRuntimeFailure(err).Status == 413 {
		t.Fatal("geo byte budget still rejects >32 MiB before semantic validation")
	}
	tooLarge := make([]byte, WebAPIMaxGeoBytes+1)
	if _, err = webRuntimeGeoDecompress(tooLarge); err == nil || webRuntimeFailure(err).Status != 413 {
		t.Fatal("geo raw budget exceeded 128 MiB")
	}
	if err = webRuntimeInstallGeo(context.Background(), "MMDB", tooLarge); err == nil || webRuntimeFailure(err).Status != 413 {
		t.Fatal("installer did not reject the geo byte budget before filesystem work")
	}
}
