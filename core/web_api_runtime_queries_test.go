//go:build webpanel && !android

package core

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebRuntimeRouteResolutionPanicIsFailureNotEmptyHeldRoute(t *testing.T) {
	_, err := webRuntimeRouteExitUsing(context.Background(), "example.test", "P", func(string, string) string { panic("fixture panic") })
	if err == nil || webRuntimeFailure(err).Code != "route_resolution_failed" {
		t.Fatal("route resolver panic masqueraded as the empty actual exit")
	}
}

func TestWebRuntimeRouteResolutionHasAtMostFourUnderlyingWorkers(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	returned := make(chan struct{}, 4)
	var once sync.Once
	defer once.Do(func() { close(release) })
	var active, peak atomic.Int32
	resolve := func(string, string) string {
		current := active.Add(1)
		for {
			old := peak.Load()
			if current <= old || peak.CompareAndSwap(old, current) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		active.Add(-1)
		returned <- struct{}{}
		return "DIRECT"
	}
	var done []chan error
	for range 4 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		result := make(chan error, 1)
		done = append(done, result)
		go func() { _, err := webRuntimeRouteExitUsing(ctx, "example.test", "P", resolve); result <- err }()
	}
	for range 4 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("route workers were not admitted")
		}
	}
	for _, result := range done {
		select {
		case err := <-result:
			if err == nil {
				t.Fatal("uncooperative route resolver ignored its deadline")
			}
		case <-time.After(time.Second):
			t.Fatal("route query waited beyond its bounded context")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := webRuntimeRouteExitUsing(ctx, "example.test", "P", resolve); err == nil || peak.Load() != 4 || active.Load() != 4 {
		t.Fatal("fifth native resolver escaped the bounded worker slots")
	}
	once.Do(func() { close(release) })
	for range 4 {
		select {
		case <-returned:
		case <-time.After(time.Second):
			t.Fatal("route worker did not finish")
		}
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if value, err := webRuntimeRouteExitUsing(ctx, "example.test", "P", func(string, string) string { return "DIRECT" }); err != nil || value != "DIRECT" {
		t.Fatalf("completed native route workers leaked their slots: %s %v", value, err)
	}
}
