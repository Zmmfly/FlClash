//go:build webpanel && !android

package core

import (
	"fmt"
	"testing"
	"time"
)

func TestWebImageByteCacheLRUHasIndependentSizeEntryTTLAndIdentityBounds(t *testing.T) {
	clock := time.Now()
	s := newWebImageService(&WebAPIUtilityService{now: func() time.Time { return clock }})
	body := make([]byte, 24<<20)
	for _, key := range []string{"one", "two", "three"} {
		s.remember(key, WebAPIImage{Bytes: body})
	}
	if s.bytes != 48<<20 || len(s.entries) != 2 || s.entries["one"] != nil {
		t.Fatal("byte LRU did not bound or evict oldest")
	}
	s.remember("three", WebAPIImage{Bytes: []byte{1}})
	if s.bytes != (24<<20)+1 || len(s.entries) != 2 {
		t.Fatal("replacement double counted image bytes")
	}
	for index := 0; index < webImageCacheEntries+1; index++ {
		s.remember(fmt.Sprint(index), WebAPIImage{Bytes: []byte{1}})
	}
	if len(s.entries) != webImageCacheEntries || s.lru.Len() != webImageCacheEntries || s.bytes != webImageCacheEntries {
		t.Fatal("entry budget not bounded")
	}
	if s.entries["0"] != nil || s.entries["1"] == nil {
		t.Fatal("entry LRU discarded wrong image")
	}
	for _, element := range s.entries {
		if element.Value.(webImageEntry).expires != clock.Add(12*time.Hour) {
			t.Fatal("byte TTL differs from internal server clock")
		}
	}
}
