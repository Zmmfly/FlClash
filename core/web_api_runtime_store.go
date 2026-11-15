//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"

	bolt "github.com/metacubex/bbolt"
)

const webRuntimeBucket = "runtime-intents"
const webRuntimeQueueBucket = "runtime-queue"
const webRuntimeMaxPending = 64

type webRuntimeRecord struct {
	State   WebAPIRuntimeOperation `json:"state"`
	Desired json.RawMessage        `json:"desired"`
}

func (tx *WebAPIWriteTx) RecordRuntimeIntent(id string, action WebAPIRuntimeAction, desired any) (WebAPIRuntimeOperation, error) {
	var state WebAPIRuntimeOperation
	bucket, err := tx.tx.CreateBucketIfNotExists([]byte(webRuntimeBucket))
	if err != nil {
		return state, err
	}
	if bucket.Get([]byte(id)) != nil {
		return state, webAPIError(409, "operation_conflict", "runtime operation already exists")
	}
	queue, err := tx.tx.CreateBucketIfNotExists([]byte(webRuntimeQueueBucket))
	if err != nil {
		return state, err
	}
	if queue.Stats().KeyN >= webRuntimeMaxPending {
		return state, webAPIError(503, "runtime_busy", "runtime command queue is full")
	}
	current, err := webAPIRevision(tx.tx, "meta")
	if err != nil {
		return state, err
	}
	n, err := strconv.ParseInt(string(current), 10, 64)
	if err != nil {
		return state, err
	}
	body, err := json.Marshal(desired)
	if err != nil {
		return state, err
	}
	state = WebAPIRuntimeOperation{OperationID: id, DesiredRevision: WebAPIRevision(strconv.FormatInt(n+1, 10)), Action: action, Phase: WebAPIRuntimePending, UpdatedAt: time.Now().UTC()}
	if err = webAPIPut(tx.tx, webRuntimeBucket, id, webRuntimeRecord{State: state, Desired: body}); err != nil {
		return state, err
	}
	return state, webAPIPut(tx.tx, webRuntimeQueueBucket, id, state)
}

func (tx *WebAPIReadTx) RuntimeOperation(id string) (WebAPIRuntimeOperation, error) {
	record, err := tx.runtimeRecord(id)
	return record.State, err
}

func (tx *WebAPIReadTx) runtimeRecord(id string) (webRuntimeRecord, error) {
	var record webRuntimeRecord
	if !webAPIOperationID(id) {
		return record, webAPIError(400, "invalid_operation_id", "invalid operationId")
	}
	if tx.tx.Bucket([]byte(webRuntimeBucket)) == nil {
		return record, webAPIError(404, "not_found", "runtime operation does not exist")
	}
	err := webAPIGet(tx.tx, webRuntimeBucket, id, &record)
	return record, err
}

func (s *WebAPIStore) runtimeRecords(ctx context.Context) ([]webRuntimeRecord, error) {
	records := []webRuntimeRecord{}
	_, err := s.View(ctx, func(tx *WebAPIReadTx) error {
		if tx.tx.Bucket([]byte(webRuntimeQueueBucket)) == nil {
			return nil
		}
		queue, err := webAPIList[WebAPIRuntimeOperation](tx.tx, webRuntimeQueueBucket)
		if err != nil {
			return err
		}
		sort.Slice(queue, func(i, j int) bool {
			a, b := queue[i].DesiredRevision, queue[j].DesiredRevision
			if len(a) != len(b) {
				return len(a) < len(b)
			}
			return a < b
		})
		for _, state := range queue {
			if state.Phase != WebAPIRuntimePending {
				continue
			}
			record, err := tx.runtimeRecord(state.OperationID)
			if err != nil {
				return err
			}
			records = append(records, record)
			break
		}
		return nil
	})
	return records, err
}

func (tx *WebAPIReadTx) runtimeTransitionPending() (bool, error) {
	bucket := tx.tx.Bucket([]byte(webRuntimeQueueBucket))
	if bucket == nil {
		return false, nil
	}
	pending := false
	err := bucket.ForEach(func(_, b []byte) error {
		var state WebAPIRuntimeOperation
		if err := json.Unmarshal(b, &state); err != nil {
			return err
		}
		if webRuntimeChangesGeneration(state.Action) && (state.Phase == WebAPIRuntimePending || state.Phase == WebAPIRuntimeApplying) {
			pending = true
		}
		return nil
	})
	return pending, err
}

func (s *WebAPIStore) updateRuntime(ctx context.Context, update func(*bolt.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return webAPIError(503, "store_closed", "store is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.write(ctx, update)
}

func webRuntimeRevisionAfter(a, b WebAPIRevision) bool {
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	return a > b
}

func (s *WebAPIStore) recoverRuntime() (bool, error) {
	needsReconcile := false
	err := s.updateRuntime(context.Background(), func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte(webRuntimeBucket))
		if err != nil {
			return err
		}
		// Recovery scans only state headers. Retaining every pending Desired blob
		// here would multiply a full 32 MiB payload by the queue capacity.
		interrupted := []string{}
		var newestUnknown, newestConfirmed WebAPIRevision
		err = bucket.ForEach(func(_, raw []byte) error {
			var head struct {
				State WebAPIRuntimeOperation `json:"state"`
			}
			if err := json.Unmarshal(raw, &head); err != nil {
				return err
			}
			state := head.State
			if state.Phase == WebAPIRuntimePending || state.Phase == WebAPIRuntimeApplying {
				if len(interrupted) >= webRuntimeMaxPending {
					return webAPIError(503, "runtime_queue_invalid", "runtime recovery exceeds the accepted queue budget")
				}
				interrupted = append(interrupted, state.OperationID)
				state.Phase = WebAPIRuntimeUnknown
			}
			if !webRuntimeChangesGeneration(state.Action) {
				return nil
			}
			if state.Phase == WebAPIRuntimeUnknown && webRuntimeRevisionAfter(state.DesiredRevision, newestUnknown) {
				newestUnknown = state.DesiredRevision
			}
			confirmed := state.Phase == WebAPIRuntimeApplied && (state.Action == webRuntimeApply || state.Action == webRuntimeReconcile)
			if state.Phase == WebAPIRuntimeFailed && state.Action == webRuntimeApply {
				var actual WebAPIRuntimeApplyResult
				confirmed = json.Unmarshal(state.Result, &actual) == nil && actual.ActualConfig == "default-fallback"
			}
			if confirmed && webRuntimeRevisionAfter(state.DesiredRevision, newestConfirmed) {
				newestConfirmed = state.DesiredRevision
			}
			return nil
		})
		if err != nil {
			return err
		}
		needsReconcile = webRuntimeRevisionAfter(newestUnknown, newestConfirmed)
		for _, id := range interrupted {
			var record webRuntimeRecord
			if err := webAPIGet(tx, webRuntimeBucket, id, &record); err != nil {
				return err
			}
			record.State.Phase = WebAPIRuntimeUnknown
			record.State.Error = webAPIError(409, "effect_unknown", "runtime was interrupted; the side effect is not automatically repeated")
			record.State.UpdatedAt = time.Now().UTC()
			if err := webAPIPut(tx, webRuntimeBucket, id, record); err != nil {
				return err
			}
			if err := webRuntimeMarkProviderPhase(&WebAPIWriteTx{WebAPIReadTx: &WebAPIReadTx{tx}}, record, WebAPIRuntimeUnknown); err != nil {
				return err
			}
		}
		if tx.Bucket([]byte(webRuntimeQueueBucket)) != nil {
			if err := tx.DeleteBucket([]byte(webRuntimeQueueBucket)); err != nil {
				return err
			}
		}
		_, err = tx.CreateBucketIfNotExists([]byte(webRuntimeQueueBucket))
		return err
	})
	return needsReconcile, err
}

func (s *WebAPIStore) transitionRuntime(id string, from, to WebAPIRuntimePhase, result any, failure *WebAPIError) (WebAPIRuntimeOperation, error) {
	var state WebAPIRuntimeOperation
	err := s.updateRuntime(context.Background(), func(tx *bolt.Tx) error {
		var record webRuntimeRecord
		if err := webAPIGet(tx, webRuntimeBucket, id, &record); err != nil {
			return err
		}
		if record.State.Phase != from {
			return errors.New("runtime phase changed")
		}
		var data json.RawMessage
		if result != nil {
			var err error
			data, err = json.Marshal(result)
			if err != nil {
				return err
			}
			if len(data) > webAPIMaxReceiptBytes {
				return errors.New("runtime result is too large")
			}
		}
		record.State.Phase = to
		record.State.Result = data
		record.State.Error = failure
		record.State.UpdatedAt = time.Now().UTC()
		state = record.State
		if bucket := tx.Bucket([]byte(webRuntimeQueueBucket)); bucket != nil {
			if to == WebAPIRuntimePending || to == WebAPIRuntimeApplying {
				if err := webAPIPut(tx, webRuntimeQueueBucket, id, state); err != nil {
					return err
				}
			} else if err := bucket.Delete([]byte(id)); err != nil {
				return err
			}
		}
		return webAPIPut(tx, webRuntimeBucket, id, record)
	})
	return state, err
}
