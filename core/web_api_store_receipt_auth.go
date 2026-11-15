//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"

	bolt "github.com/metacubex/bbolt"
)

func webAPIDAVReceiptStateError() *WebAPIError {
	return webAPIError(409, "dav_receipt_state_invalid", "DAV receipt and private intent state do not agree")
}

func webAPIIsDAVReceipt(tx *bolt.Tx, saved webAPIStoredReceipt) bool {
	return saved.Class == "dav" || tx.Bucket([]byte("dav-jobs")).Get([]byte(saved.OperationID)) != nil
}

// Authorization is already leased by write paths; read paths acquire it before calling.
func webAPIAuthorizeDAVReceipt(ctx context.Context, tx *bolt.Tx, saved webAPIStoredReceipt) error {
	if saved.Class != "" && saved.Class != "dav" {
		return webAPIDAVReceiptStateError()
	}
	if !webAPIIsDAVReceipt(tx, saved) {
		return nil
	}
	raw := tx.Bucket([]byte("dav-jobs")).Get([]byte(saved.OperationID))
	if raw == nil {
		return webAPIDAVReceiptStateError()
	}
	var record WebAPIDAVRecord
	if err := json.Unmarshal(raw, &record); err != nil || record.Job.OperationID != saved.OperationID || record.SessionID == "" || record.Job.DesiredRevision != saved.Revision {
		return webAPIDAVReceiptStateError()
	}
	if WebAPIRequestSessionID(ctx) == "" || WebAPIRequestSessionID(ctx) != record.SessionID {
		return webAPIError(403, "dav_session_mismatch", "DAV receipt belongs to another session")
	}
	return nil
}

func webAPIMissingDAVReceipt(ctx context.Context, tx *bolt.Tx, operationID string) error {
	raw := tx.Bucket([]byte("dav-jobs")).Get([]byte(operationID))
	if raw == nil {
		return nil
	}
	var record WebAPIDAVRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return webAPIDAVReceiptStateError()
	}
	if record.SessionID == "" || WebAPIRequestSessionID(ctx) != record.SessionID {
		return webAPIError(403, "dav_session_mismatch", "DAV receipt belongs to another session")
	}
	return webAPIDAVReceiptStateError()
}
