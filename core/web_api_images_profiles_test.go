//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	bolt "github.com/metacubex/bbolt"
)

func webImageProfileYAML(icon string) []byte {
	line := ""
	if icon != "" {
		line = fmt.Sprintf("    icon: %q\n", icon)
	}
	return []byte("proxies: []\nproxy-groups:\n  - name: G\n    type: select\n    proxies: [DIRECT]\n" + line + "rules:\n  - MATCH,G\n")
}

func webImageImportOrdinaryProfile(t *testing.T, f *webUtilityHTTPFixture, kind, label, revision, operation, icon string) WebAPIProfile {
	t.Helper()
	source := WebAPIContentImportSource{Kind: "file", Bytes: webImageProfileYAML(icon)}
	if kind == "url" {
		source = WebAPIContentImportSource{Kind: "url", URL: "http://icons.test/profile.yaml"}
	}
	command := WebAPIProfileImport{WebAPIMutation: WebAPIMutation{ExpectedRevision: WebAPIRevision(revision), OperationID: operation}, Label: label, Source: source}
	status, body := f.request(t, f.clients[0], "POST", "/api/v1/profiles:import", command)
	if status != 200 {
		t.Fatalf("ordinary profile import %d: %s", status, body)
	}
	var result WebAPIEnvelope[WebAPIProfile]
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.Type != kind {
		t.Fatalf("ordinary import changed profile kind: %s", result.Data.Type)
	}
	return result.Data
}

func webImageRemoveProfileIcon(t *testing.T, f *webUtilityHTTPFixture, profile WebAPIProfile, revision, operation string) {
	t.Helper()
	command := WebAPIContentWrite{WebAPIMutation: WebAPIMutation{ExpectedRevision: WebAPIRevision(revision), OperationID: operation}, Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: profile.ID}, Bytes: webImageProfileYAML("")}
	if status, body := f.request(t, f.clients[0], "POST", "/api/v1/content:write", command); status != 200 {
		t.Fatalf("ordinary profile content change %d: %s", status, body)
	}
}

func TestWebImageHTTPOrdinaryFileAndURLProfileFirstLoadAndSourceRemoval(t *testing.T) {
	for _, kind := range []string{"file", "url"} {
		t.Run(kind, func(t *testing.T) {
			icon := "http://icons.test/ordinary.png"
			png := webImageTestPNG(t)
			f := newWebUtilityHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/profile.yaml" {
					w.Write(webImageProfileYAML(icon))
					return
				}
				w.Write(png)
			})
			first := webImageImportOrdinaryProfile(t, f, kind, "First", "0", "ordinary-icon-import-first", icon)
			status, body := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:load", WebAPIImageLoad{URL: icon})
			if status != 200 {
				t.Fatalf("first ordinary icon load without preview/history %d: %s", status, body)
			}
			var image WebAPIEnvelope[WebAPIImage]
			if err := json.Unmarshal(body, &image); err != nil || !bytes.Equal(image.Data.Bytes, png) {
				t.Fatalf("ordinary image bytes invalid: %v", err)
			}
			cacheRevision, err := f.store.CacheView(context.Background(), func(tx *WebAPICacheTx) error {
				rows, err := tx.IconRecords()
				if len(rows) != 0 {
					t.Error("background ordinary icon created history/preview authorization")
				}
				return err
			})
			if err != nil || cacheRevision != "0" {
				t.Fatalf("ordinary icon changed metadata cache %s %v", cacheRevision, err)
			}
			second := webImageImportOrdinaryProfile(t, f, "file", "Second", "1", "ordinary-icon-import-second", icon)
			webImageRemoveProfileIcon(t, f, first, "2", "ordinary-icon-remove-first")
			if status, body := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:load", WebAPIImageLoad{URL: icon}); status != 200 {
				t.Fatalf("same URL still owned by another profile %d: %s", status, body)
			}
			webImageRemoveProfileIcon(t, f, second, "3", "ordinary-icon-remove-second")
			before := f.totalFetches()
			if status, body := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:load", WebAPIImageLoad{URL: icon}); status != 404 {
				t.Fatalf("removed ordinary icon escaped through byte LRU %d: %s", status, body)
			}
			if f.totalFetches() != before {
				t.Fatal("removed icon performed another source fetch")
			}
		})
	}
}

func TestWebImageHTTPOrdinaryProfileSourceUsesCatalogWithoutReadingBlob(t *testing.T) {
	icon := "http://icons.test/no-blob.png"
	png := webImageTestPNG(t)
	f := newWebUtilityHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) { w.Write(png) })
	profile := webImageImportOrdinaryProfile(t, f, "file", "Catalog projection", "0", "ordinary-no-blob-import", icon)
	name, _, err := webAPIContentKey(WebAPIContentKey{Kind: WebAPIContentProfile, ID: profile.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.write(context.Background(), func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("content")).Put([]byte(name), []byte("not a content blob"))
	}); err != nil {
		t.Fatal(err)
	}
	if status, body := f.request(t, f.clients[1], "POST", "/api/v1/icon-images:load", WebAPIImageLoad{URL: icon}); status != 200 {
		t.Fatalf("metadata-only source projection attempted blob decoding %d: %s", status, body)
	}
}

func TestWebImageHTTPOrdinaryPrivateProfileIconNeverAutoAuthorizes(t *testing.T) {
	icon := "http://lan.test/private-profile.png"
	f := newWebUtilityHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("ordinary private icon was fetched without explicit source consent")
		w.Write(webImageTestPNG(t))
	})
	webImageImportOrdinaryProfile(t, f, "file", "Private icon", "0", "ordinary-private-icon-import", icon)
	if status, body := f.request(t, f.clients[0], "POST", "/api/v1/icon-images:load", WebAPIImageLoad{URL: icon}); status != 403 {
		t.Fatalf("private ordinary icon was hidden or autoauthorized %d: %s", status, body)
	}
	if f.totalFetches() != 0 {
		t.Fatal("private icon used a background authorization/fetch")
	}
}
