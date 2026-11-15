//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"strings"
	"testing"

	bolt "github.com/metacubex/bbolt"
)

func webAPITestPlanetAsset(id WebAPIID, data []byte) WebAPIContent {
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	return WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfileAsset, ID: id, Name: "zerotier-planet:" + digest}, MediaType: "application/octet-stream", SHA256: digest, Bytes: data}
}

func TestWebAPIProfileAssetsAreTypedOwnerScopedAndCascadeAtomically(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := context.Background()
	first := webAPITestPlanetAsset("1", []byte("synthetic-planet-one"))
	other := webAPITestPlanetAsset("10", []byte("synthetic-planet-other"))
	_, err := r.store.Transaction(ctx, WebAPIMutation{"0", "profile-assets-seed"}, "test:assets", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		for _, id := range []WebAPIID{"1", "10"} {
			if err := tx.InsertProfile(WebAPIProfile{ID: id, WebAPIProfileInput: webAPINormalizeProfile(WebAPIProfileInput{Type: "file", Label: string(id)})}); err != nil {
				return nil, err
			}
		}
		if err := tx.PutContent(first); err != nil {
			return nil, err
		}
		return struct{}{}, tx.PutContent(other)
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.View(ctx, func(tx *WebAPIReadTx) error {
		assets, err := tx.ProfileAssets("1")
		if err != nil || len(assets) != 1 || assets[0].Key != first.Key || !bytes.Equal(assets[0].Bytes, first.Bytes) {
			t.Fatal(assets, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, key := range []WebAPIContentKey{{Kind: WebAPIContentProfileAsset, ID: "1", Name: "../planet"}, {Kind: WebAPIContentProfileAsset, ID: "1", Name: "zerotier-planet:" + strings.Repeat("A", 64)}, {Kind: WebAPIContentProfileAsset, ID: "1", Name: "other:" + strings.Repeat("a", 64)}} {
		_, err = r.store.Transaction(ctx, WebAPIMutation{"1", fmt.Sprintf("invalid-profile-asset-%d", i)}, "test:asset", key, func(tx *WebAPIWriteTx) (any, error) {
			return nil, tx.PutContent(WebAPIContent{Key: key, MediaType: "application/octet-stream", Bytes: first.Bytes})
		})
		webAPITestErrorCode(t, err, "invalid_content_name")
	}
	wrong := first
	wrong.Bytes = []byte("changed-under-same-name")
	_, err = r.store.Transaction(ctx, WebAPIMutation{"1", "profile-asset-digest-mismatch"}, "test:asset", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return nil, tx.PutContent(wrong) })
	webAPITestErrorCode(t, err, "profile_asset_digest_mismatch")
	missing := webAPITestPlanetAsset("999", []byte("source"))
	_, err = r.store.Transaction(ctx, WebAPIMutation{"1", "profile-asset-owner-missing"}, "test:asset", struct{}{}, func(tx *WebAPIWriteTx) (any, error) { return nil, tx.PutContent(missing) })
	webAPITestErrorCode(t, err, "invalid_reference")
	r.call("POST", "/api/v1/profiles:delete", webAPITestMutation("1", "profile-assets-cascade", map[string]any{"id": "1"}), 200)
	_, err = r.store.View(ctx, func(tx *WebAPIReadTx) error {
		_, err := tx.Content(first.Key)
		webAPITestErrorCode(t, err, "not_found")
		assets, err := tx.ProfileAssets("10")
		if err != nil || len(assets) != 1 || assets[0].Key != other.Key {
			t.Fatal(assets, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebAPIProfileAssetsSlotsAndReplacementQuota(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := context.Background()
	_, err := r.store.Transaction(ctx, WebAPIMutation{"0", "profile-asset-slots"}, "test:assets", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		p, err := tx.CreateProfile(WebAPIProfileInput{Type: "file"})
		if err != nil {
			return nil, err
		}
		for i := 0; i < 32; i++ {
			if err := tx.PutContent(webAPITestPlanetAsset(p.ID, []byte(fmt.Sprint(i)))); err != nil {
				return nil, err
			}
		}
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.Transaction(ctx, WebAPIMutation{"1", "profile-asset-slot-overflow"}, "test:assets", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		return nil, tx.PutContent(webAPITestPlanetAsset("1", []byte("slot33")))
	})
	webAPITestErrorCode(t, err, "profile_asset_limit")
	_, err = r.store.Transaction(ctx, WebAPIMutation{"1", "profile-asset-same-replacement"}, "test:assets", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		return struct{}{}, tx.PutContent(webAPITestPlanetAsset("1", []byte("0")))
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.View(ctx, func(tx *WebAPIReadTx) error {
		assets, err := tx.ProfileAssets("1")
		if err != nil || len(assets) != 32 {
			t.Fatal(len(assets), err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebAPIProfileAssetsPreflightRejectsAggregateBeforeBlobDecode(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := context.Background()
	large := webAPITestPlanetAsset("1", bytes.Repeat([]byte{'x'}, WebAPIMaxContentBytes))
	_, err := r.store.Transaction(ctx, WebAPIMutation{"0", "profile-asset-large"}, "test:assets", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		if _, err := tx.CreateProfile(WebAPIProfileInput{Type: "file"}); err != nil {
			return nil, err
		}
		return struct{}{}, tx.PutContent(large)
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.Transaction(ctx, WebAPIMutation{"1", "profile-asset-byte-overflow"}, "test:assets", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		return nil, tx.PutContent(webAPITestPlanetAsset("1", []byte("one more byte")))
	})
	webAPITestErrorCode(t, err, "profile_asset_limit")
	if err = r.store.db.Update(func(tx *bolt.Tx) error {
		key, _, err := webAPIContentKey(large.Key)
		if err != nil {
			return err
		}
		raw := tx.Bucket([]byte("content")).Get([]byte(key))
		return tx.Bucket([]byte("content")).Put([]byte("profile-asset:1:zerotier-planet:"+strings.Repeat("f", 64)), raw)
	}); err != nil {
		t.Fatal(err)
	}
	_, err = r.store.View(ctx, func(tx *WebAPIReadTx) error {
		_, err := tx.ProfileAssets("1")
		webAPITestErrorCode(t, err, "profile_asset_limit")
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err = tx.ProfileAssets("1")
		webAPITestErrorCode(t, err, "profile_asset_limit")
		runtime.ReadMemStats(&after)
		if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 2<<20 {
			t.Fatalf("over-budget asset scan decoded %d bytes before rejection", allocated)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebAPIProfileAssetsDoNotDecodeUnrelatedContent(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := context.Background()
	_, err := r.store.Transaction(ctx, WebAPIMutation{"0", "profile-asset-scope"}, "test:assets", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		if _, err := tx.CreateProfile(WebAPIProfileInput{Type: "file"}); err != nil {
			return nil, err
		}
		return struct{}{}, tx.PutContent(webAPITestPlanetAsset("1", []byte("owned")))
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.store.db.Update(func(tx *bolt.Tx) error {
		for _, key := range []string{"profile-asset:10:garbage", "profile:1:", "mitm-resource:2:broken"} {
			if err := tx.Bucket([]byte("content")).Put([]byte(key), []byte("not JSON")); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err = r.store.View(ctx, func(tx *WebAPIReadTx) error {
		assets, err := tx.ProfileAssets("1")
		if err != nil || len(assets) != 1 || string(assets[0].Bytes) != "owned" {
			t.Fatal(assets, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
