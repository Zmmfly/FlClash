//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"
)

func (r *catalogTestRig) cacheRevision() WebAPIRevision {
	r.t.Helper()
	revision, err := r.store.CacheView(context.Background(), func(*WebAPICacheTx) error { return nil })
	if err != nil {
		r.t.Fatal(err)
	}
	return revision
}

func (r *catalogTestRig) seedCache(operation string, apply func(*WebAPICacheTx) error) {
	r.t.Helper()
	_, err := r.store.CacheTransaction(context.Background(), WebAPIMutation{ExpectedRevision: r.cacheRevision(), OperationID: operation}, "test:cache-seed", operation, func(tx *WebAPICacheTx) (any, error) { return nil, apply(tx) })
	if err != nil {
		r.t.Fatal(err)
	}
}

func TestWebAPICacheContractIconHTTPScopesReplayCASAndUnknownOutcome(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	const icon = "https://icons.unit.invalid/a.svg"
	initial := catalogData[[]WebAPIIconRecord](t, r.call("GET", "/api/v1/icon-cache?query=", nil, 200))
	if initial.Data == nil || len(initial.Data) != 0 || initial.Revision != "0" {
		t.Fatal(initial)
	}
	command := catalogMutation("0", "same-scope-operation", map[string]any{"url": icon})
	first := r.call("POST", "/api/v1/icon-cache:put", command, 200)
	created := catalogData[WebAPIIconRecord](t, first)
	if created.Data.LastAccessed != WebAPIInt64(strconv.FormatInt(time.Unix(0, r.now.Load()).UnixMilli(), 10)) {
		t.Fatal(created)
	}
	if r.revision() != "0" || r.commits.Load() != 0 {
		t.Fatal("icon cache churned configuration revision")
	}
	r.call("POST", "/api/v1/scripts:create", catalogMutation("0", "same-scope-operation", map[string]any{"script": map[string]any{"label": "Config"}}), 200)
	r.now.Add(int64(time.Minute))
	beforeQuery := r.cacheRevision()
	listed := catalogData[[]WebAPIIconRecord](t, r.call("GET", "/api/v1/icon-cache?url="+url.QueryEscape(icon), nil, 200))
	if len(listed.Data) != 1 || listed.Data[0].LastAccessed != created.Data.LastAccessed || r.cacheRevision() != beforeQuery {
		t.Fatal("query touched icon access time or revision")
	}
	r.call("GET", "/api/v1/icon-cache?url="+url.QueryEscape(icon)+"&query=a", nil, 400)
	r.call("GET", "/api/v1/icon-cache?table=profiles", nil, 400)
	r.call("POST", "/api/v1/icon-cache:touch", catalogMutation(r.cacheRevision(), "unknown-icon-touch", map[string]any{"url": "https://icons.unit.invalid/missing"}), 404)
	r.call("POST", "/api/v1/icon-cache:put", catalogMutation(r.cacheRevision(), "client-timestamp", map[string]any{"url": icon, "lastAccessed": "1"}), 400)
	r.call("POST", "/api/v1/icon-cache:delete", catalogMutation(r.cacheRevision(), "duplicate-url-delete", map[string]any{"urls": []string{icon, icon}}), 409)
	r.call("POST", "/api/v1/icon-cache:delete", catalogMutation(r.cacheRevision(), "unknown-url-delete", map[string]any{"urls": []string{icon, "https://icons.unit.invalid/missing"}}), 404)
	touched := catalogData[WebAPIIconRecord](t, r.call("POST", "/api/v1/icon-cache:touch", catalogMutation(r.cacheRevision(), "touch-saved-icon", map[string]any{"url": icon}), 200))
	if touched.Data.LastAccessed == created.Data.LastAccessed {
		t.Fatal("touch did not use server clock")
	}
	if !bytes.Equal(first, r.call("POST", "/api/v1/icon-cache:put", command, 200)) {
		t.Fatal("icon replay changed timestamp")
	}
	if receipt := r.call("GET", "/api/v1/cache-operations/same-scope-operation", nil, 200); !bytes.Equal(receipt, first) {
		t.Fatal("cache receipt used the configuration namespace")
	}
	r.call("POST", "/api/v1/icon-cache:put", catalogMutation("0", "same-scope-operation", map[string]any{"url": "https://icons.unit.invalid/changed"}), 409)
	cas := r.cacheRevision()
	var wait sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			request := catalogMutation(cas, fmt.Sprintf("cache-cas-%d", i), map[string]any{"url": fmt.Sprintf("https://icons.unit.invalid/%d.svg", i)})
			body := r.callIfAny("POST", "/api/v1/icon-cache:put", request)
			codes <- body
		}(i)
	}
	wait.Wait()
	close(codes)
	count := map[int]int{}
	for code := range codes {
		count[code]++
	}
	if count[200] != 1 || count[409] != 1 {
		t.Fatal(count)
	}
	lost := catalogMutation(r.cacheRevision(), "cache-lost-response", map[string]any{"url": "https://icons.unit.invalid/lost.svg"})
	r.drop.Store(true)
	if body := r.call("POST", "/api/v1/icon-cache:put", lost, 200); len(body) != 0 {
		t.Fatal("response was not dropped")
	}
	committed := r.cacheRevision()
	recovered := r.call("POST", "/api/v1/icon-cache:put", lost, 200)
	if r.cacheRevision() != committed || !bytes.Equal(recovered, r.call("GET", "/api/v1/cache-operations/cache-lost-response", nil, 200)) {
		t.Fatal("lost response committed twice")
	}
	r.call("POST", "/api/v1/cache-operations/not-sent-operation:resolve", map[string]any{"intent": "resolve-unknown-result"}, 409)
	r.call("POST", "/api/v1/icon-cache:put", catalogMutation(r.cacheRevision(), "not-sent-operation", map[string]any{"url": icon}), 409)
	if r.cacheRevision() != committed {
		t.Fatal("operation tombstone churned cache revision")
	}
	if r.commits.Load() != 1 || r.revision() != "1" {
		t.Fatal("cache transaction notified configuration commits")
	}
}

func (r *catalogTestRig) callIfAny(method, path string, value any) int {
	body, err := json.Marshal(value)
	if err != nil {
		r.t.Fatal(err)
	}
	status, _ := r.raw(method, path, body)
	return status
}

func TestWebAPICacheContractCapacityTTLAndRollbackHTTP(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	now := time.Unix(0, r.now.Load()).UTC()
	r.seedCache("seed-icon-capacity", func(tx *WebAPICacheTx) error {
		for i := 0; i < webAPIIconCapacity; i++ {
			if err := tx.PutIconRecord(WebAPIIconRecord{URL: fmt.Sprintf("https://icons.unit.invalid/%04d", i), LastAccessed: WebAPIInt64(strconv.FormatInt(now.Add(time.Duration(i-webAPIIconCapacity)*time.Second).UnixMilli(), 10))}); err != nil {
				return err
			}
		}
		return nil
	})
	r.call("POST", "/api/v1/icon-cache:put", catalogMutation(r.cacheRevision(), "overflow-icon-capacity", map[string]any{"url": "https://icons.unit.invalid/new"}), 200)
	icons := catalogData[[]WebAPIIconRecord](t, r.call("GET", "/api/v1/icon-cache", nil, 200)).Data
	if len(icons) != webAPIIconCapacity {
		t.Fatal(len(icons))
	}
	for _, icon := range icons {
		if icon.URL == "https://icons.unit.invalid/0000" {
			t.Fatal("LRU record was retained")
		}
	}
	r.seedCache("seed-ip-ttl-capacity", func(tx *WebAPICacheTx) error {
		for i := 0; i < webAPIIPQualityCapacity; i++ {
			ip := fmt.Sprintf("10.%d.%d.%d", i>>16, (i>>8)&255, i&255)
			checked := now.Add(-time.Duration(i) * time.Second)
			if err := tx.PutIPQuality(WebAPIIPQualityRecord{IP: ip, Quality: WebAPIIPQuality{IP: ip, Source: "ipApiIs", Type: "hosting"}, CheckedAt: checked.Format(time.RFC3339Nano)}); err != nil {
				return err
			}
		}
		for i, checked := range []time.Time{now.Add(-webAPIIPQualityTTL - time.Second), now.Add(time.Hour)} {
			ip := fmt.Sprintf("192.0.2.%d", i+1)
			if err := tx.PutIPQuality(WebAPIIPQualityRecord{IP: ip, Quality: WebAPIIPQuality{IP: ip, Source: "identMe", Type: "residential"}, CheckedAt: checked.Format(time.RFC3339Nano)}); err != nil {
				return err
			}
		}
		return nil
	})
	for _, ip := range []string{"192.0.2.1", "192.0.2.2"} {
		got := catalogData[[]WebAPIIPQualityRecord](t, r.call("GET", "/api/v1/ip-quality-cache?ip="+ip, nil, 200))
		if len(got.Data) != 0 {
			t.Fatal("stale/future cache record was served", got)
		}
	}
	quality := map[string]any{"ip": "203.0.113.7", "source": "ipApiIs", "type": "residential", "isVpn": true}
	command := catalogMutation(r.cacheRevision(), "ip-quality-put", map[string]any{"quality": quality})
	body := r.call("POST", "/api/v1/ip-quality-cache:put", command, 200)
	put := catalogData[WebAPIIPQualityRecord](t, body)
	if put.Data.CheckedAt != now.Format(time.RFC3339Nano) || !put.Data.Quality.IsVPN {
		t.Fatal(put)
	}
	_, err := r.store.CacheView(context.Background(), func(tx *WebAPICacheTx) error {
		records, err := tx.IPQualities()
		if len(records) != webAPIIPQualityCapacity {
			t.Fatal(len(records))
		}
		for _, record := range records {
			if record.IP == "192.0.2.1" || record.IP == "192.0.2.2" {
				t.Fatal("stale/future record survived prune")
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	listed := catalogData[[]WebAPIIPQualityRecord](t, r.call("GET", "/api/v1/ip-quality-cache?ip=203.0.113.7", nil, 200))
	if len(listed.Data) != 1 {
		t.Fatal(listed)
	}
	before := r.cacheRevision()
	r.call("POST", "/api/v1/ip-quality-cache:put", catalogMutation(before, "client-checked-at", map[string]any{"quality": quality, "checkedAt": "2000-01-01T00:00:00Z"}), 400)
	bad := map[string]any{"ip": "203.0.113.7", "source": "not-source", "type": "hosting"}
	r.call("POST", "/api/v1/ip-quality-cache:put", catalogMutation(before, "invalid-quality-source", map[string]any{"quality": bad}), 400)
	r.call("POST", "/api/v1/ip-quality-cache:delete", catalogMutation(before, "duplicate-quality-delete", map[string]any{"ips": []string{"203.0.113.7", "203.0.113.7"}}), 409)
	r.call("POST", "/api/v1/ip-quality-cache:delete", catalogMutation(before, "unknown-quality-delete", map[string]any{"ips": []string{"203.0.113.7", "198.51.100.9"}}), 404)
	if r.cacheRevision() != before {
		t.Fatal("invalid cache mutation committed")
	}
	if !bytes.Equal(body, r.call("POST", "/api/v1/ip-quality-cache:put", command, 200)) {
		t.Fatal("IP replay changed checkedAt")
	}
	r.now.Add(int64(webAPIIPQualityTTL + time.Second))
	stale := catalogData[[]WebAPIIPQualityRecord](t, r.call("GET", "/api/v1/ip-quality-cache?ip=203.0.113.7", nil, 200))
	if len(stale.Data) != 0 || stale.Revision != before {
		t.Fatal(stale)
	}
	r.call("POST", "/api/v1/ip-quality-cache:delete", catalogMutation(before, "delete-ip-quality", map[string]any{"ips": []string{"203.0.113.7"}}), 200)
	if r.revision() != "0" || r.commits.Load() != 0 {
		t.Fatal("cache lifecycle changed configuration")
	}
}

func TestWebAPICacheContractIPCASAndLostResponseHTTP(t *testing.T) {
	r := newCatalogTestRig(t, nil)
	quality := func(ip string) map[string]any {
		return map[string]any{"ip": ip, "source": "identMe", "type": "residential"}
	}
	var wait sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			codes <- r.callIfAny("POST", "/api/v1/ip-quality-cache:put", catalogMutation("0", fmt.Sprintf("ip-cas-writer-%d", i), map[string]any{"quality": quality(fmt.Sprintf("203.0.113.%d", i+1))}))
		}(i)
	}
	wait.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal(counts)
	}
	lost := catalogMutation(r.cacheRevision(), "ip-cache-lost-response", map[string]any{"quality": quality("203.0.113.99")})
	r.drop.Store(true)
	if body := r.call("POST", "/api/v1/ip-quality-cache:put", lost, 200); len(body) != 0 {
		t.Fatal("response was not lost")
	}
	committed := r.cacheRevision()
	r.now.Add(int64(time.Hour))
	recovered := r.call("POST", "/api/v1/ip-quality-cache:put", lost, 200)
	if r.cacheRevision() != committed || !bytes.Equal(recovered, r.call("GET", "/api/v1/cache-operations/ip-cache-lost-response", nil, 200)) {
		t.Fatal("IP unknown outcome replay changed the committed record")
	}
	queried := catalogData[[]WebAPIIPQualityRecord](t, r.call("GET", "/api/v1/ip-quality-cache?ip=203.0.113.99", nil, 200))
	if len(queried.Data) != 1 || queried.Data[0].CheckedAt != catalogData[WebAPIIPQualityRecord](t, recovered).Data.CheckedAt {
		t.Fatal(queried)
	}
	if r.revision() != "0" || r.commits.Load() != 0 {
		t.Fatal("IP cache churned configuration")
	}
}
