//go:build webpanel && !android

package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebBackupPowerCutWorker(t *testing.T) {
	dir := os.Getenv("FLCLASH_BACKUP_CRASH_FIXTURE")
	if dir == "" {
		t.Skip("subprocess crash fixture only")
	}
	stage := os.Getenv("FLCLASH_BACKUP_CRASH_STAGE")
	options := WebAPIStoreOptions{DataDir: filepath.Join(dir, "data")}
	if stage == "after-commit" {
		options.OnCommit = func(WebAPICommit) { os.Exit(37) }
	}
	store, err := OpenWebAPIStore(options)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(filepath.Join(dir, "backup.zip"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewWebAPIBackupService(store, WebAPIBackupOptions{Fault: func(current string) error {
		if current == stage {
			os.Exit(37)
		}
		return nil
	}})
	_, err = service.Restore(context.Background(), WebAPIBackupRestore{WebAPIMutation: WebAPIMutation{ExpectedRevision: "1", OperationID: "power-cut-restore"}, Intent: "restore-application-data", Strategy: "override", ArchiveBase64: archive})
	if err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash point not reached")
}

func TestWebBackupPowerCutBeforeAndAfterDurableCommit(t *testing.T) {
	for _, stage := range []string{"after-metadata", "after-content", "after-settings", "after-commit"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			options := WebAPIStoreOptions{DataDir: filepath.Join(dir, "data")}
			store, err := OpenWebAPIStore(options)
			if err != nil {
				t.Fatal(err)
			}
			seed := webBackupFixture()
			seed.Data.Profiles[0].ID = "1"
			seed.Data.Profiles[0].Label = "before"
			seed.Contents[0].Key.ID = "1"
			_, err = store.Transaction(context.Background(), WebAPIMutation{ExpectedRevision: "0", OperationID: "crash-seed"}, "fixture", struct{}{}, func(tx *WebAPIWriteTx) (any, error) {
				if err := tx.InsertProfile(seed.Data.Profiles[0]); err != nil {
					return nil, err
				}
				return nil, tx.PutContent(seed.Contents[0])
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			archive, err := webBackupEncodeTyped(webBackupFixture())
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, "backup.zip"), archive, 0o600); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(executable, "-test.run=^TestWebBackupPowerCutWorker$")
			command.Env = append(os.Environ(), "FLCLASH_BACKUP_CRASH_FIXTURE="+dir, "FLCLASH_BACKUP_CRASH_STAGE="+stage)
			output, err := command.CombinedOutput()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 37 {
				t.Fatalf("worker did not hit power cut: %v %s", err, output)
			}
			reopened, err := OpenWebAPIStore(options)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			revision, err := reopened.View(context.Background(), func(tx *WebAPIReadTx) error {
				profiles, err := tx.Profiles()
				if err != nil {
					return err
				}
				contents, err := tx.Contents()
				if err != nil {
					return err
				}
				if len(profiles) != 1 || len(contents) != 1 {
					t.Fatalf("partial state after crash: %d profiles, %d blobs", len(profiles), len(contents))
				}
				expected := WebAPIID("1")
				if stage == "after-commit" {
					expected = "9007199254740993"
				}
				if profiles[0].ID != expected || contents[0].Key.ID != expected {
					t.Fatal("metadata/blob commit split")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if stage == "after-commit" {
				if revision != "2" {
					t.Fatalf("committed revision %s", revision)
				}
				receipt, err := reopened.Receipt(context.Background(), "power-cut-restore")
				if err != nil || receipt.Revision != "2" {
					t.Fatalf("missing durable receipt: %v", err)
				}
			} else {
				if revision != "1" {
					t.Fatalf("uncommitted revision %s", revision)
				}
				_, err := reopened.Receipt(context.Background(), "power-cut-restore")
				webBackupRequireCode(t, err, "not_found")
			}
		})
	}
}
