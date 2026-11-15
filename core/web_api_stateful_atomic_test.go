//go:build webpanel && !android

package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	bolt "github.com/metacubex/bbolt"
)

func TestWebAPIStatefulContractCommitUncertaintyHasNoFilesystemEffect(t *testing.T) {
	f := newWebStatefulFixture(t)
	plan := webStatefulTestPlan(t, f.profile.ID, webStatefulTestDefinition("tailscale"))
	mutation := f.mutation()
	commit := f.store.commit
	f.store.commit = func(tx *bolt.Tx) error {
		if err := commit(tx); err != nil {
			return err
		}
		return errors.New("synthetic uncertain commit acknowledgment")
	}
	_, err := f.store.Transaction(f.ctx, mutation, "runtime:apply", plan, func(tx *WebAPIWriteTx) (any, error) { return webStatefulRecord(tx, mutation.OperationID, plan) })
	webAPITestErrorCode(t, err, "durability_unknown")
	_, err = f.store.StatefulRuntimeBindings(f.ctx, mutation.OperationID)
	webAPITestErrorCode(t, err, "durability_unknown")
	if entries, err := os.ReadDir(f.opts.DataDir); err != nil || len(entries) != 0 {
		t.Fatal("uncertain commit caused a state directory effect")
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = OpenWebAPIStore(f.opts)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := f.store.StatefulRuntimeBindings(f.ctx, mutation.OperationID)
	if err != nil || len(bound.Bindings) != 1 {
		t.Fatal("reopen could not reconcile committed state binding", err)
	}
	if _, err := f.store.Receipt(f.ctx, mutation.OperationID); err != nil {
		t.Fatal("state binding survived without its receipt", err)
	}
	if _, err := f.store.recoverRuntime(); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.View(f.ctx, func(tx *WebAPIReadTx) error {
		operation, err := tx.RuntimeOperation(mutation.OperationID)
		if err == nil && operation.Phase != WebAPIRuntimeUnknown {
			t.Fatal("restart retried the uncertain effect instead of retaining Unknown")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.opts.DataDir, webStatefulDirectory)); !os.IsNotExist(err) {
		t.Fatal("recovery automatically created state")
	}
}

func TestWebAPIStatefulContractRejectedDetachIsAtomic(t *testing.T) {
	f := newWebStatefulFixture(t)
	plan := webStatefulTestPlan(t, f.profile.ID, webStatefulTestDefinition("zerotier"))
	before := f.bind(plan)
	mutation := f.mutation()
	_, err := f.store.Transaction(f.ctx, mutation, "synthetic-restore", mutation, func(tx *WebAPIWriteTx) (any, error) {
		if err := tx.DetachAllStatefulResources(); err != nil {
			return nil, err
		}
		return nil, errors.New("synthetic restore validation rejection")
	})
	if err == nil {
		t.Fatal("rejected restore unexpectedly committed")
	}
	after := f.bind(plan)
	if before.Bindings[0].Identity != after.Bindings[0].Identity {
		t.Fatal("failed restore detached the current device identity")
	}
}

func TestWebAPIStatefulContractRevokedSessionCannotCreateBinding(t *testing.T) {
	f := newWebStatefulFixture(t)
	plan := webStatefulTestPlan(t, f.profile.ID, webStatefulTestDefinition("easytier"))
	mutation := f.mutation()
	ctx := WithWebAPIRequestAuthorization(context.Background(), "synthetic-revoked", func(context.Context) (func(), error) {
		return nil, webAPIError(401, "unauthorized", "synthetic revoked session")
	})
	called := false
	_, err := f.store.Transaction(ctx, mutation, "runtime:apply", plan, func(tx *WebAPIWriteTx) (any, error) {
		called = true
		return webStatefulRecord(tx, mutation.OperationID, plan)
	})
	webAPITestErrorCode(t, err, "unauthorized")
	if called {
		t.Fatal("revoked session reached the binding callback")
	}
	_, err = f.store.StatefulRuntimeBindings(f.ctx, mutation.OperationID)
	if err == nil {
		t.Fatal("revoked session left a state binding")
	}
}
