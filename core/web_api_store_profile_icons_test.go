//go:build webpanel && !android

package core

import (
	"context"
	"testing"

	bolt "github.com/metacubex/bbolt"
)

func TestWebAPIProfileIconSourceUsesOnlyAvailableCatalogAndDropsOldURLs(t *testing.T) {
	r := newWebAPITestRig(t)
	ctx := context.Background()
	rawURL := "https://icons.example.invalid/quoted%20icon.png?variant=1"
	_, err := r.store.Transaction(ctx, WebAPIMutation{"0", "profile-icons-seed"}, "test:icons", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		p, err := tx.CreateProfile(WebAPIProfileInput{Type: "file"})
		if err != nil {
			return nil, err
		}
		icons := []string{rawURL, rawURL}
		if err := tx.SetProfileCatalog(p.ID, WebAPIProfileCatalog{IconURLs: icons}); err != nil {
			return nil, err
		}
		icons[0] = "https://changed.invalid/alias"
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.store.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("content")).Put([]byte("profile:1:"), []byte("not a content document"))
	}); err != nil {
		t.Fatal(err)
	}
	_, err = r.store.View(ctx, func(tx *WebAPIReadTx) error {
		resolved, err := tx.ProfileIconSource(WebAPIUtilityIconSourceID(rawURL))
		if err != nil || resolved != rawURL {
			t.Fatal(resolved, err)
		}
		catalog, err := tx.ProfileCatalogState("1")
		if err != nil || len(catalog.IconURLs) != 1 {
			t.Fatal(catalog, err)
		}
		catalog.IconURLs[0] = "https://mutated.invalid/reader"
		resolved, err = tx.ProfileIconSource(WebAPIUtilityIconSourceID(rawURL))
		if err != nil || resolved != rawURL {
			t.Fatal("reader slice aliased stored index", resolved, err)
		}
		_, err = tx.ProfileIconSource("0")
		webAPITestErrorCode(t, err, "icon_image_unregistered")
		_, err = tx.ProfileIconSource("01")
		webAPITestErrorCode(t, err, "invalid_source_resource")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.store.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("content")).Delete([]byte("profile:1:")) }); err != nil {
		t.Fatal(err)
	}
	_, err = r.store.Transaction(ctx, WebAPIMutation{"1", "profile-icons-unavailable"}, "test:icons", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		return struct{}{}, tx.SetProfileCatalog("1", WebAPIProfileCatalog{UnavailableReason: WebAPICatalogContentUnavailable})
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.View(ctx, func(tx *WebAPIReadTx) error {
		_, err := tx.ProfileIconSource(WebAPIUtilityIconSourceID(rawURL))
		webAPITestErrorCode(t, err, "icon_image_unregistered")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	newURL := "http://new.example.invalid/icon.png"
	_, err = r.store.Transaction(ctx, WebAPIMutation{"2", "profile-icons-replaced"}, "test:icons", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
		return struct{}{}, tx.SetProfileCatalog("1", WebAPIProfileCatalog{IconURLs: []string{newURL}})
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.View(ctx, func(tx *WebAPIReadTx) error {
		_, err := tx.ProfileIconSource(WebAPIUtilityIconSourceID(rawURL))
		webAPITestErrorCode(t, err, "icon_image_unregistered")
		value, err := tx.ProfileIconSource(WebAPIUtilityIconSourceID(newURL))
		if value != newURL {
			t.Fatal(value)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r.call("POST", "/api/v1/profiles:delete", webAPITestMutation("3", "profile-icons-delete", map[string]any{"id": "1"}), 200)
	_, err = r.store.View(ctx, func(tx *WebAPIReadTx) error {
		_, err := tx.ProfileIconSource(WebAPIUtilityIconSourceID(newURL))
		webAPITestErrorCode(t, err, "icon_image_unregistered")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
