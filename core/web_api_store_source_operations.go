//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"errors"

	bolt "github.com/metacubex/bbolt"
)

type webAPISourceTombstone struct {
	SessionID string         `json:"sessionId"`
	Revision  WebAPIRevision `json:"revision"`
}

func webAPICheckSourceTombstone(tx *bolt.Tx, operationID, sessionID string) error {
	raw := tx.Bucket([]byte("source-operation-tombstones")).Get([]byte(operationID))
	if raw == nil {
		return nil
	}
	var saved webAPISourceTombstone
	if err := json.Unmarshal(raw, &saved); err != nil {
		return err
	}
	if sessionID != "" && saved.SessionID != sessionID {
		return webAPIError(403, "source_authorization_mismatch", "source operation belongs to another session")
	}
	n, err := webAPIInteger(string(saved.Revision), false)
	if err != nil || n < 0 {
		return errors.New("invalid stored source operation revision")
	}
	problem := webAPINotCommitted()
	problem.Revision = saved.Revision
	return problem
}

func webAPIReadSourceReceipt(tx *bolt.Tx, operationID, sessionID string) (WebAPISourceAuthorizationReceipt, error) {
	var result WebAPISourceAuthorizationReceipt
	raw := tx.Bucket([]byte("source-receipts")).Get([]byte(operationID))
	if raw == nil {
		if err := webAPICheckSourceTombstone(tx, operationID, sessionID); err != nil {
			return result, err
		}
		return result, webAPIError(404, "not_found", "source operation does not exist")
	}
	var saved webAPIStoredSourceReceipt
	if err := json.Unmarshal(raw, &saved); err != nil {
		return result, err
	}
	if sessionID != "" && saved.SessionID != sessionID {
		return result, webAPIError(403, "source_authorization_mismatch", "source authorization receipt belongs to another session")
	}
	n, err := webAPIInteger(string(saved.Revision), false)
	if err != nil || n < 0 {
		return result, errors.New("invalid stored source receipt revision")
	}
	return WebAPISourceAuthorizationReceipt{Revision: saved.Revision, OperationID: saved.OperationID, Body: append(json.RawMessage{}, saved.Body...), Replayed: true}, nil
}

func (s *WebAPIStore) ResolveSourceOperation(ctx context.Context, operationID string) (WebAPISourceAuthorizationReceipt, error) {
	var result WebAPISourceAuthorizationReceipt
	if !webAPIOperationID(operationID) {
		return result, webAPIError(400, "invalid_operation_id", "invalid operationId")
	}
	sessionID := WebAPIRequestSessionID(ctx)
	if sessionID == "" {
		return result, webAPIError(401, "unauthorized", "source operation resolution requires an authenticated session")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var notCommitted *WebAPIError
	err := s.write(ctx, func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("source-receipts")).Get([]byte(operationID)) != nil {
			var err error
			result, err = webAPIReadSourceReceipt(tx, operationID, sessionID)
			return err
		}
		if err := webAPICheckSourceTombstone(tx, operationID, sessionID); err != nil {
			return err
		}
		revision, err := webAPIRevision(tx, "meta")
		if err != nil {
			return err
		}
		if err = webAPIPut(tx, "source-operation-tombstones", operationID, webAPISourceTombstone{SessionID: sessionID, Revision: revision}); err != nil {
			return err
		}
		notCommitted = webAPINotCommitted()
		notCommitted.Revision = revision
		return nil
	})
	if err != nil {
		return WebAPISourceAuthorizationReceipt{}, err
	}
	if notCommitted != nil {
		return WebAPISourceAuthorizationReceipt{}, notCommitted
	}
	return result, nil
}
