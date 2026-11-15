//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWebLegacySQLiteSerializeCopiesSurviveCloseAndPrivateReadOnlyImport(t *testing.T) {
	for i := 0; i < 50; i++ {
		ctx := context.Background()
		writer, closeWriter, err := webNativeOpen(ctx, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = writer.ExecContext(ctx, "CREATE TABLE fixture(value TEXT NOT NULL); INSERT INTO fixture VALUES('synthetic-owned-value')"); err != nil {
			closeWriter()
			t.Fatal(err)
		}
		first, err := webNativeSerialize(writer)
		if err != nil {
			closeWriter()
			t.Fatal(err)
		}
		first[0] = 0
		image, err := webNativeSerialize(writer)
		if err != nil {
			closeWriter()
			t.Fatal(err)
		}
		if !bytes.HasPrefix(image, []byte("SQLite format 3\x00")) {
			closeWriter()
			t.Fatal("Serialize did not return an independent copy")
		}
		closeWriter()
		before := webContentDigest(image)
		reader, closeReader, err := webNativeOpen(ctx, image, true)
		if err != nil {
			t.Fatal(err)
		}
		var seq int
		var schema, name string
		if err = reader.QueryRowContext(ctx, "PRAGMA database_list").Scan(&seq, &schema, &name); err != nil {
			closeReader()
			t.Fatal(err)
		}
		if filepath.Base(name) != "archive.sqlite" {
			closeReader()
			t.Fatal("reader did not use its fixed private file name")
		}
		fileInfo, err := os.Stat(name)
		if err != nil {
			closeReader()
			t.Fatal(err)
		}
		dirInfo, err := os.Stat(filepath.Dir(name))
		if err != nil {
			closeReader()
			t.Fatal(err)
		}
		if fileInfo.Mode().Perm() != 0o600 || dirInfo.Mode().Perm() != 0o700 {
			closeReader()
			t.Fatal("native archive staging was not private")
		}
		var value string
		if err = reader.QueryRowContext(ctx, "SELECT value FROM fixture").Scan(&value); err != nil {
			closeReader()
			t.Fatal(err)
		}
		if value != "synthetic-owned-value" {
			closeReader()
			t.Fatal("image changed after writer close")
		}
		if _, err = reader.ExecContext(ctx, "INSERT INTO fixture VALUES('forbidden')"); err == nil {
			closeReader()
			t.Fatal("readonly archive accepted a write")
		}
		closeReader()
		if _, err = os.Stat(filepath.Dir(name)); !os.IsNotExist(err) {
			t.Fatal("native archive staging directory survived close")
		}
		if webContentDigest(image) != before {
			t.Fatal("reader modified the uploaded image")
		}
	}
}
