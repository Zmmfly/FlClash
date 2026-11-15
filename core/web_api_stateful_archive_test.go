//go:build webpanel && !android

package core

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestWebAPIStatefulContractArchiveExcludesLedgerAndIncarnations(t *testing.T) {
	f := newWebStatefulFixture(t)
	bound := f.bind(webStatefulTestPlan(t, f.profile.ID, webStatefulTestDefinition("tailscale")))
	service := NewWebAPIBackupService(f.store, WebAPIBackupOptions{})
	_, body, err := service.Export(f.ctx, "flclash-v1", false)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	private := []string{bound.StoreID, bound.OperationID, bound.ProfileIncarnation, bound.Bindings[0].Identity, bound.Bindings[0].Incarnation, webStatefulBucket}
	for _, entry := range archive.File {
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range private {
			if strings.Contains(entry.Name, value) || bytes.Contains(data, []byte(value)) {
				t.Fatal("archive exposed private state authority")
			}
		}
	}
}
