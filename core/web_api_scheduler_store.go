//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	bolt "github.com/metacubex/bbolt"
	"time"
)

type webSchedulerAttempt struct {
	Epoch                string         `json:"epoch"`
	Fingerprint          string         `json:"fingerprint"`
	OperationID          string         `json:"operationId"`
	ExpectedRevision     WebAPIRevision `json:"expectedRevision"`
	Phase                string         `json:"phase"`
	LastAttempt          time.Time      `json:"lastAttempt"`
	LastSuccess          time.Time      `json:"lastSuccess"`
	NextAttempt          time.Time      `json:"nextAttempt"`
	Failures             int            `json:"failures"`
	ErrorCode            string         `json:"errorCode,omitempty"`
	RequiresApply        bool           `json:"requiresApply"`
	ConfirmationRequired bool           `json:"confirmationRequired"`
}

func webSchedulerReadAttempts(tx *WebAPIReadTx) (map[string]webSchedulerAttempt, error) {
	out := map[string]webSchedulerAttempt{}
	bucket := tx.tx.Bucket([]byte(webSchedulerBucket))
	if bucket == nil {
		return out, nil
	}
	err := bucket.ForEach(func(k, v []byte) error {
		if len(out) >= webSchedulerMaxTargets {
			return webAPIError(413, "scheduler_target_limit", "scheduler attempt budget exceeded")
		}
		var record webSchedulerAttempt
		if err := json.Unmarshal(v, &record); err != nil {
			return err
		}
		out[string(k)] = record
		return nil
	})
	return out, err
}

func (s *WebAPIScheduler) saveAttempt(ctx context.Context, key string, record webSchedulerAttempt) error {
	return s.store.updateRuntime(ctx, func(tx *bolt.Tx) error {
		if (&WebAPIReadTx{tx}).SchedulerEpoch() != record.Epoch {
			return webAPIError(409, "scheduler_epoch_changed", "background target belongs to a prior application-data generation")
		}
		bucket, err := tx.CreateBucketIfNotExists([]byte(webSchedulerBucket))
		if err != nil {
			return err
		}
		if bucket.Get([]byte(key)) == nil && bucket.Stats().KeyN >= webSchedulerMaxTargets {
			return webAPIError(413, "scheduler_target_limit", "scheduler attempt budget exceeded")
		}
		return webAPIPut(tx, webSchedulerBucket, key, record)
	})
}

func (s *WebAPIScheduler) recoverAttempts(ctx context.Context, snapshot webSchedulerSnapshot) error {
	return s.store.updateRuntime(ctx, func(tx *bolt.Tx) error {
		if (&WebAPIReadTx{tx}).SchedulerEpoch() != snapshot.epoch {
			return webAPIError(409, "scheduler_epoch_changed", "application data changed before scheduler recovery")
		}
		bucket, err := tx.CreateBucketIfNotExists([]byte(webSchedulerBucket))
		if err != nil {
			return err
		}
		valid := map[string]string{}
		for _, target := range snapshot.targets {
			valid[target.key()] = target.fingerprint
		}
		for key, record := range snapshot.attempts {
			if valid[key] != record.Fingerprint {
				if err = bucket.Delete([]byte(key)); err != nil {
					return err
				}
			} else if record.Phase == "inflight" {
				record.Phase, record.ErrorCode = "unknown", "effect_unknown"
				record.NextAttempt = time.Time{}
				if err = webAPIPut(tx, webSchedulerBucket, key, record); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
