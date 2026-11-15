//go:build webpanel && !android

package core

import (
	"context"
	"errors"
	"math"
	"strconv"

	bolt "github.com/metacubex/bbolt"
)

var webAPIDerivedCacheUnchanged = errors.New("derived cache update made no change")

func (s *WebAPIStore) DerivedCacheUpdate(ctx context.Context, apply func(*WebAPICacheTx) (bool, error)) (WebAPIRevision, error) {
	var revision WebAPIRevision
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return revision, err
	}
	err := s.write(ctx, func(tx *bolt.Tx) error {
		var err error
		revision, err = webAPIRevision(tx, "cache-meta")
		if err != nil {
			return err
		}
		changed, err := apply(&WebAPICacheTx{tx})
		if err != nil {
			return err
		}
		if !changed {
			return webAPIDerivedCacheUnchanged
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		current, _ := strconv.ParseInt(string(revision), 10, 64)
		if current == math.MaxInt64 {
			return webAPIError(409, "revision_exhausted", "cache revision space is exhausted")
		}
		revision = WebAPIRevision(strconv.FormatInt(current+1, 10))
		return tx.Bucket([]byte("cache-meta")).Put([]byte("revision"), []byte(revision))
	})
	if errors.Is(err, webAPIDerivedCacheUnchanged) {
		return revision, nil
	}
	if err != nil {
		return "", err
	}
	return revision, nil
}
