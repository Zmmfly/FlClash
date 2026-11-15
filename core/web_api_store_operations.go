//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"

	bolt "github.com/metacubex/bbolt"
)

func webAPINotCommitted() *WebAPIError {
	e := webAPIError(409, "operation_not_committed", "the unresolved operation was durably closed without a commit")
	e.Details = map[string]any{"state": "not_committed"}
	return e
}

func (s *WebAPIStore) ResolveOperation(ctx context.Context, operationID string) (WebAPIReceipt, error) {
	return s.resolveOperation(ctx, operationID, false)
}

func (s *WebAPIStore) ResolveCacheOperation(ctx context.Context, operationID string) (WebAPIReceipt, error) {
	return s.resolveOperation(ctx, operationID, true)
}

func (s *WebAPIStore) resolveOperation(ctx context.Context, operationID string, cache bool) (WebAPIReceipt, error) {
	var result WebAPIReceipt
	if !webAPIOperationID(operationID) {
		return result, webAPIError(400, "invalid_operation_id", "invalid operationId")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	meta, receipts, tombstones := "meta", "receipts", "operation-tombstones"
	if cache {
		meta, receipts, tombstones = "cache-meta", "cache-receipts", "cache-operation-tombstones"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	notCommitted := false
	err := s.write(ctx, func(tx *bolt.Tx) error {
		if raw := tx.Bucket([]byte(receipts)).Get([]byte(operationID)); raw != nil {
			var saved webAPIStoredReceipt
			if err := json.Unmarshal(raw, &saved); err != nil {
				return err
			}
			if !cache {
				if err := webAPIAuthorizeDAVReceipt(ctx, tx, saved); err != nil {
					return err
				}
			}
			result = WebAPIReceipt{Revision: saved.Revision, OperationID: saved.OperationID, Body: append(json.RawMessage{}, saved.Body...), Replayed: true}
			return nil
		}
		if !cache {
			if err := webAPIMissingDAVReceipt(ctx, tx, operationID); err != nil {
				return err
			}
		}
		revision, err := webAPIRevision(tx, meta)
		if err != nil {
			return err
		}
		result.Revision = revision
		notCommitted = true
		return tx.Bucket([]byte(tombstones)).Put([]byte(operationID), []byte("not_committed"))
	})
	if err != nil {
		return WebAPIReceipt{}, err
	}
	if notCommitted {
		e := webAPINotCommitted()
		e.Revision = result.Revision
		return WebAPIReceipt{}, e
	}
	return result, nil
}
