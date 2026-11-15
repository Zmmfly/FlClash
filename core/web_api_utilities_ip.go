//go:build webpanel && !android

package core

import (
	"context"
	"net/http"
	"sort"
	"time"
)

type WebAPIUtilityIPLookup struct {
	IP      string `json:"ip"`
	Refresh bool   `json:"refresh,omitempty"`
}

type webUtilityIPSourceResult struct {
	source string
	answer webUtilityIPAnswer
	err    error
}

func webUtilityIPUnavailable(failures []WebAPIUtilityIPFailure) error {
	err := webAPIError(502, "ip_quality_unavailable", "IP quality providers did not return a classification")
	err.Details = map[string]any{"failures": failures}
	return err
}

func (s *WebAPIUtilityService) ipCached(ctx context.Context, ip string) (*WebAPIIPQualityRecord, error) {
	var record *WebAPIIPQualityRecord
	_, err := s.store.CacheView(ctx, func(tx *WebAPICacheTx) error {
		rows, err := webAPIQueryIPQuality(tx, ip, s.now())
		if len(rows) > 0 {
			record = &rows[0]
		}
		return err
	})
	return record, err
}

func (s *WebAPIUtilityService) lookupIP(ctx context.Context, command WebAPIUtilityIPLookup) (WebAPIIPQualityRecord, error) {
	if _, err := webUtilityIPURL("ipQuery", command.IP); err != nil {
		return WebAPIIPQualityRecord{}, err
	}
	cached, err := s.ipCached(ctx, command.IP)
	if err != nil {
		return WebAPIIPQualityRecord{}, err
	}
	if cached != nil && !command.Refresh {
		return *cached, nil
	}
	if err := s.remoteAllowed(); err != nil {
		return WebAPIIPQualityRecord{}, err
	}
	s.mu.Lock()
	if cooldown, found := s.ipCooldowns[command.IP]; found && !command.Refresh && s.now().Before(cooldown.until) {
		s.mu.Unlock()
		return WebAPIIPQualityRecord{}, webUtilityIPUnavailable(cooldown.failures)
	}
	if flight := s.ipFlights[command.IP]; flight != nil {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return WebAPIIPQualityRecord{}, ctx.Err()
		case <-flight.done:
			return flight.record, flight.err
		}
	}
	if len(s.ipFlights) >= 64 {
		s.mu.Unlock()
		return WebAPIIPQualityRecord{}, webAPIError(429, "ip_quality_busy", "too many pending IP quality lookups")
	}
	flight := &webUtilityIPFlight{done: make(chan struct{})}
	s.ipFlights[command.IP] = flight
	s.mu.Unlock()
	quality, failures, err := s.queryIP(ctx, command.IP)
	var record WebAPIIPQualityRecord
	if err == nil {
		checkedAt := s.now()
		_, err = s.store.DerivedCacheUpdate(ctx, func(tx *WebAPICacheTx) (bool, error) {
			current, err := webAPIQueryIPQuality(tx, command.IP, checkedAt)
			if err != nil {
				return false, err
			}
			if len(current) > 0 {
				checked, _ := time.Parse(time.RFC3339Nano, current[0].CheckedAt)
				if !checked.Before(checkedAt) {
					record = current[0]
					return false, nil
				}
			}
			record, err = webAPIPutIPQuality(tx, quality, checkedAt)
			return err == nil, err
		})
	}
	s.mu.Lock()
	if err == nil {
		delete(s.ipCooldowns, command.IP)
	} else if len(failures) > 0 {
		for ip, failure := range s.ipCooldowns {
			if !s.now().Before(failure.until) {
				delete(s.ipCooldowns, ip)
			}
		}
		if len(s.ipCooldowns) >= webAPIIPQualityCapacity {
			oldestIP, oldestTime := "", s.now().Add(24*time.Hour)
			for ip, cooldown := range s.ipCooldowns {
				if cooldown.until.Before(oldestTime) {
					oldestIP, oldestTime = ip, cooldown.until
				}
			}
			delete(s.ipCooldowns, oldestIP)
		}
		s.ipCooldowns[command.IP] = webUtilityIPCooldown{until: s.now().Add(5 * time.Minute), failures: failures}
	}
	flight.record, flight.err = record, err
	delete(s.ipFlights, command.IP)
	close(flight.done)
	s.mu.Unlock()
	return record, err
}

func (s *WebAPIUtilityService) queryIP(ctx context.Context, ip string) (WebAPIIPQuality, []WebAPIUtilityIPFailure, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	waves := [][]string{{"identMe", "ipApiCom", "ipQuery"}, {"ipLocate"}, {"ipApiIs"}, {"proxyCheck"}}
	results := make(chan webUtilityIPSourceResult, len(webUtilityIPSources))
	wave, active := 0, 0
	failures := []WebAPIUtilityIPFailure{}
	var fallback *WebAPIIPQuality
	launch := func() {
		for _, source := range waves[wave] {
			active++
			safeGo("web utility IP source", func() {
				queryCtx, stop := context.WithTimeout(ctx, 8*time.Second)
				defer stop()
				result := webUtilityIPSourceResult{source: source}
				target, err := webUtilityIPURL(source, ip)
				if err == nil {
					var response WebAPISourceFetchResult
					response, err = WebAPIFetchSource(queryCtx, s.store, WebAPISourceRequest{Purpose: WebAPISourcePurpose("ip-quality-lookup"), URL: target}, s.options.SourceOptions)
					if err == nil {
						result.answer, err = webUtilityParseIP(source, ip, response.Bytes)
					}
				}
				result.err = err
				results <- result
			})
		}
		wave++
	}
	launch()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		if active == 0 {
			if wave == len(waves) {
				if fallback != nil {
					return *fallback, nil, nil
				}
				sort.Slice(failures, func(i, j int) bool {
					return webUtilityIPSourceIndex(failures[i].Source) < webUtilityIPSourceIndex(failures[j].Source)
				})
				return WebAPIIPQuality{}, failures, webUtilityIPUnavailable(failures)
			}
			launch()
			timer.Reset(time.Second)
		}
		select {
		case <-ctx.Done():
			return WebAPIIPQuality{}, nil, ctx.Err()
		case <-timer.C:
			if wave < len(waves) {
				launch()
				timer.Reset(time.Second)
			}
		case result := <-results:
			active--
			if result.err != nil {
				failures = append(failures, WebAPIUtilityIPFailure{Source: result.source, Status: webUtilityIPFailureStatus(result.err)})
			} else if result.answer.inferred {
				if fallback == nil {
					fallback = &result.answer.quality
				}
			} else {
				return result.answer.quality, nil, nil
			}
		}
	}
}

func webUtilityIPSourceIndex(source string) int {
	for index, candidate := range webUtilityIPSources {
		if candidate == source {
			return index
		}
	}
	return len(webUtilityIPSources)
}

func (s *WebAPIUtilityService) ipLookupHTTP(w http.ResponseWriter, r *http.Request) {
	if !webUtilityPost(w, r) {
		return
	}
	var command WebAPIUtilityIPLookup
	if err := WebAPIDecodeJSON(w, r, &command); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	result, err := s.lookupIP(r.Context(), command)
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	revision, err := s.revision(r.Context())
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	release, err := WebAPIAcquireCommitAuthorization(r.Context())
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	defer release()
	WebAPIWriteSuccess(w, revision, result)
}
