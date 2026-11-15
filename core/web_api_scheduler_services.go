//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
)

func WebAPISchedulerContentBackend(content *WebAPIContentService, sourceOptions WebAPISourceFetchOptions) WebAPISchedulerBackend {
	if content == nil {
		return WebAPISchedulerBackend{}
	}
	refresh := func(ctx context.Context, task WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
		if err := webSchedulerValidateTask(ctx, content.store, task); err != nil {
			return WebAPISchedulerEffect{}, err
		}
		key := WebAPIContentKey{Kind: WebAPIContentKind(task.Kind), ID: WebAPIID(task.ID)}
		if task.Kind != WebAPISchedulerProfile && task.Kind != WebAPISchedulerScript && task.Kind != WebAPISchedulerRuleProvider {
			return WebAPISchedulerEffect{}, webAPIError(400, "invalid_scheduler_target", "unsupported content target")
		}
		receipt, err := content.Refresh(ctx, WebAPIContentRefresh{WebAPIMutation: task.Mutation, Key: key, Purpose: "source"})
		if err != nil {
			return WebAPISchedulerEffect{}, webSchedulerOriginalError(ctx, content.store, task, err)
		}
		return webSchedulerContentReceipt(task, receipt)
	}
	module := func(ctx context.Context, task WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
		if task.Kind != WebAPISchedulerModule {
			return WebAPISchedulerEffect{}, webAPIError(400, "invalid_scheduler_target", "unsupported module target")
		}
		if err := webSchedulerValidateTask(ctx, content.store, task); err != nil {
			return WebAPISchedulerEffect{}, err
		}
		key := WebAPIContentKey{Kind: WebAPIContentMitmModule, ID: WebAPIID(task.ID)}
		var remote bool
		var manifest WebAPIContent
		_, err := content.store.View(ctx, func(tx *WebAPIReadTx) error {
			v, err := tx.MitmModule(key.ID)
			if err != nil {
				return err
			}
			remote = v.URL != nil && *v.URL != ""
			if !remote {
				manifest, err = tx.Content(key)
			}
			return err
		})
		if err != nil {
			return WebAPISchedulerEffect{}, err
		}
		var receipt WebAPIReceipt
		if remote {
			receipt, err = content.Refresh(ctx, WebAPIContentRefresh{WebAPIMutation: task.Mutation, Key: key, Purpose: "source"})
		} else {
			receipt, err = content.Write(ctx, WebAPIContentWrite{WebAPIMutation: task.Mutation, Key: key, Bytes: manifest.Bytes})
		}
		if err != nil {
			return WebAPISchedulerEffect{}, webSchedulerOriginalError(ctx, content.store, task, err)
		}
		return webSchedulerContentReceipt(task, receipt)
	}
	icon := func(ctx context.Context, task WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
		if task.Kind != WebAPISchedulerIconSet {
			return WebAPISchedulerEffect{}, webAPIError(400, "invalid_scheduler_target", "unsupported icon-set target")
		}
		if err := webSchedulerValidateTask(ctx, content.store, task); err != nil {
			return WebAPISchedulerEffect{}, err
		}
		receipt, err := webAPIRefreshIconSet(ctx, content, webAPIIconSetRefreshCommand{WebAPIMutation: task.Mutation, ID: WebAPIID(task.ID)}, sourceOptions)
		if err != nil {
			return WebAPISchedulerEffect{}, err
		}
		var envelope WebAPIEnvelope[WebAPIIconSet]
		if err = json.Unmarshal(receipt.Body, &envelope); err != nil || string(envelope.Data.ID) != task.ID {
			return WebAPISchedulerEffect{}, webAPIError(503, "effect_unknown", "icon-set refresh receipt could not be reconciled")
		}
		return WebAPISchedulerEffect{Revision: receipt.Revision, OperationID: receipt.OperationID, Phase: "completed"}, nil
	}
	return WebAPISchedulerBackend{RefreshContent: refresh, RefreshModule: module, RefreshIconSet: icon}
}

func webSchedulerValidateTask(ctx context.Context, store *WebAPIStore, task WebAPISchedulerTask) error {
	if !WebAPIInternalAuthorized(ctx) || WebAPIRequestSessionID(ctx) != "" {
		return webAPIError(403, "background_authority_required", "typed background callback requires service-owned authority")
	}
	probe := &WebAPIScheduler{store: store, clock: webSchedulerClock{}, ctx: ctx, options: WebAPISchedulerOptions{Backend: WebAPISchedulerBackend{Synthetic: true}}}
	snapshot, err := probe.snapshot(ctx)
	if err != nil {
		return err
	}
	if snapshot.revision != task.Mutation.ExpectedRevision {
		e := webAPIError(409, "revision_conflict", "committed scheduler target changed before fetch")
		e.Revision = snapshot.revision
		return e
	}
	for _, target := range snapshot.targets {
		if target.status.Kind == task.Kind && target.status.ID == task.ID && target.status.Name == task.Name && target.fingerprint == task.SourceFingerprint {
			return nil
		}
	}
	return webAPIError(409, "source_target_changed", "scheduler source identity changed")
}

func webSchedulerContentReceipt(task WebAPISchedulerTask, receipt WebAPIReceipt) (WebAPISchedulerEffect, error) {
	var envelope WebAPIEnvelope[WebAPIContentMutationResult]
	if err := json.Unmarshal(receipt.Body, &envelope); err != nil || string(envelope.Data.Content.Key.ID) != task.ID || string(envelope.Data.Content.Key.Kind) != string(task.Kind) || len(envelope.Data.Content.SHA256) != 64 {
		return WebAPISchedulerEffect{}, webAPIError(503, "effect_unknown", "content refresh receipt could not be reconciled")
	}
	confirmation := envelope.Data.MitmModule != nil && !envelope.Data.MitmModule.Enabled && envelope.Data.MitmModule.Scripts > 0
	return WebAPISchedulerEffect{Revision: receipt.Revision, OperationID: receipt.OperationID, Phase: "completed", RequiresApply: true, ConfirmationRequired: confirmation}, nil
}

func webSchedulerOriginalError(ctx context.Context, store *WebAPIStore, task WebAPISchedulerTask, err error) error {
	revision, readErr := store.View(ctx, func(*WebAPIReadTx) error { return nil })
	if readErr == nil && revision != task.Mutation.ExpectedRevision {
		conflict := webAPIError(409, "revision_conflict", "committed inputs changed while background content was fetched")
		conflict.Revision = revision
		return conflict
	}
	return err
}
