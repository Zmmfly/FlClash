//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	bolt "github.com/metacubex/bbolt"
)

func TestWebAPIBackupBudgetCountsExactExpandedBytesAndFixedBuckets(t *testing.T) {
	r := newWebAPITestRig(t)
	_, err := r.store.Transaction(context.Background(), WebAPIMutation{"0", "budget-seed-resources"}, "test:budget", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		p, err := tx.CreateProfile(WebAPIProfileInput{Type: "file", Label: "Budget"})
		if err != nil {
			return nil, err
		}
		return struct{}{}, tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: p.ID}, MediaType: "text/plain", Bytes: []byte("ab")})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.store.db.Update(func(tx *bolt.Tx) error {
		for _, bucket := range []string{"profile-catalogs", "icon-records", "ip-qualities", "receipts", "source-grants", "source-receipts", "source-operation-tombstones"} {
			if err := tx.Bucket([]byte(bucket)).Put([]byte("not-exported"), bytes.Repeat([]byte{'x'}, 1<<20)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		profile := tx.tx.Bucket([]byte("profiles")).Get([]byte("1"))
		key := []byte("profile:1:")
		content := tx.tx.Bucket([]byte("content")).Get(key)
		start, end, err := webAPIBackupContentRange(content)
		if err != nil {
			return err
		}
		metadata := int64(len(profile) + 1 + len(key) + len(content) - (end - start))
		expanded := metadata + 2
		if err := tx.CheckBackupBudget(2, metadata, expanded); err != nil {
			t.Fatal(err)
		}
		for _, limits := range []struct {
			rows               int
			metadata, expanded int64
			dimension          string
		}{{1, metadata, expanded, "rows"}, {2, metadata - 1, expanded, "metadata_bytes"}, {2, metadata, expanded - 1, "expanded_bytes"}} {
			err := tx.CheckBackupBudget(limits.rows, limits.metadata, limits.expanded)
			var problem *WebAPIError
			if !asWebAPIBudgetError(err, &problem) || problem.Details["dimension"] != limits.dimension {
				t.Fatal(limits, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func asWebAPIBudgetError(err error, out **WebAPIError) bool {
	problem, ok := err.(*WebAPIError)
	if ok {
		*out = problem
	}
	return ok && problem.Code == "backup_budget_exceeded" && problem.Status == 413
}

func TestWebAPIBackupBudgetRejectsLargeStoreWithoutDecodingBlob(t *testing.T) {
	r := newWebAPITestRig(t)
	_, err := r.store.Transaction(context.Background(), WebAPIMutation{"0", "large-backup-preflight"}, "test:budget", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		p, err := tx.CreateProfile(WebAPIProfileInput{Type: "file", Label: "Large blob"})
		if err != nil {
			return nil, err
		}
		return struct{}{}, tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: p.ID}, MediaType: "application/octet-stream", Bytes: bytes.Repeat([]byte{'x'}, WebAPIMaxContentBytes)})
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		if err := tx.CheckBackupBudget(10, 1<<20, 1<<20); err == nil {
			t.Fatal("large store passed a low budget")
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for i := 0; i < 3; i++ {
			var problem *WebAPIError
			if !asWebAPIBudgetError(tx.CheckBackupBudget(10, 1<<20, 1<<20), &problem) {
				t.Fatal("preflight did not reject content budget")
			}
		}
		runtime.ReadMemStats(&after)
		if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 2<<20 {
			t.Fatalf("low-budget preflight allocated %d bytes for a 32 MiB source", allocated)
		}
		if err := tx.CheckBackupBudget(10, 1<<20, 64<<20); err != nil {
			t.Fatal(err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebAPIBackupBudgetAllowsFullRawOverrideMetadata(t *testing.T) {
	r := newWebAPITestRig(t)
	raw := strings.Repeat("x", WebAPIMaxContentBytes)
	_, err := r.store.Transaction(context.Background(), WebAPIMutation{"0", "large-backup-metadata"}, "test:budget", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		id, err := tx.AllocateID()
		if err != nil {
			return nil, err
		}
		return struct{}{}, tx.InsertProfile(WebAPIProfile{ID: id, WebAPIProfileInput: webAPINormalizeProfile(WebAPIProfileInput{Type: "file", Label: "Unreadable", OverridesRawDocument: &raw})})
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.View(context.Background(), func(tx *WebAPIReadTx) error {
		if err := tx.CheckBackupBudget(10, 64<<20, 64<<20); err != nil {
			t.Fatal("valid 32 MiB raw metadata was cut by a per-entry limit", err)
		}
		var problem *WebAPIError
		if !asWebAPIBudgetError(tx.CheckBackupBudget(10, 8<<20, 64<<20), &problem) {
			t.Fatal("explicit aggregate metadata budget was ignored")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebAPIBackupContentBase64PreflightAndDecoys(t *testing.T) {
	for size := 0; size < 20; size++ {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			input := bytes.Repeat([]byte{byte(size)}, size)
			raw, err := json.Marshal(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: "1"}, MediaType: `text/"bytes":"YWJj"`, SHA256: "digest", Bytes: input})
			if err != nil {
				t.Fatal(err)
			}
			start, end, err := webAPIBackupContentRange(raw)
			if err != nil {
				t.Fatal(err)
			}
			payload := raw[start:end]
			if !bytes.Equal(payload, []byte(base64.StdEncoding.EncodeToString(input))) {
				t.Fatal("payload scanner selected a metadata decoy")
			}
			padding := 0
			if len(payload) > 0 && payload[len(payload)-1] == '=' {
				padding++
				if payload[len(payload)-2] == '=' {
					padding++
				}
			}
			if int64(len(payload)/4)*3-int64(padding) != int64(size) || !webAPIBackupBase64(payload, padding) {
				t.Fatal("base64 decoded length mismatch")
			}
		})
	}
	for _, value := range []string{`{"key":{},"mediaType":"x","sha256":"x","bytes":[0]}`, `{"key":{},"mediaType":"x","sha256":"x","bytes":"YQ==","bytes":""}`, `{"key":{},"mediaType":"x","sha256":"x","Bytes":"YQ=="}`, `{"key":{},"mediaType":"x","sha256":"x"}`, `[]`} {
		if _, _, err := webAPIBackupContentRange([]byte(value)); err == nil {
			t.Fatal("noncanonical content accepted", value)
		}
	}
	for _, test := range []struct {
		value   string
		padding int
	}{{"a*==", 2}, {"a===", 2}, {"YR==", 2}, {"YWJ=", 1}, {"YQ\n=", 1}} {
		if webAPIBackupBase64([]byte(test.value), test.padding) {
			t.Fatal("invalid base64 accepted", test.value)
		}
	}
}
