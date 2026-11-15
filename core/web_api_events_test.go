//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func webRuntimeDialEvents(t *testing.T, s *webRuntimeTestRig, cookie *http.Cookie, origin string) (net.Conn, error) {
	t.Helper()
	header := http.Header{}
	if cookie != nil {
		header.Set("Cookie", cookie.String())
	}
	if origin != "" {
		header.Set("Origin", origin)
	}
	dialer := ws.Dialer{Timeout: time.Second, Header: ws.HandshakeHeaderHTTP(header)}
	conn, _, _, err := dialer.Dial(context.Background(), "ws"+strings.TrimPrefix(s.server.URL, "http")+"/ws/v1/events")
	if err == nil {
		conn.SetDeadline(time.Now().Add(3 * time.Second))
	}
	return conn, err
}
func webRuntimeReadEvent(t *testing.T, conn net.Conn) WebAPIEvent {
	t.Helper()
	data, _, err := wsutil.ReadServerData(conn)
	if err != nil {
		t.Fatal(err)
	}
	var event WebAPIEvent
	if err = json.Unmarshal(data, &event); err != nil {
		t.Fatalf("event %s: %v", data, err)
	}
	return event
}
func webRuntimeSubscribe(t *testing.T, conn net.Conn, body string) WebAPIEvent {
	t.Helper()
	if err := wsutil.WriteClientMessage(conn, ws.OpText, []byte(body)); err != nil {
		t.Fatal(err)
	}
	event := webRuntimeReadEvent(t, conn)
	if event.Type != "snapshot" {
		t.Fatalf("expected snapshot, got %+v", event)
	}
	return event
}

func TestWebAPIEventsAuthenticationLogoutExpiryAndRecredential(t *testing.T) {
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	if conn, err := webRuntimeDialEvents(t, s, nil, s.server.URL); err == nil {
		conn.Close()
		t.Fatal("unauthenticated events accepted")
	}
	if conn, err := webRuntimeDialEvents(t, s, s.cookie, "http://evil.test"); err == nil {
		conn.Close()
		t.Fatal("cross-origin events accepted")
	}
	conn, err := webRuntimeDialEvents(t, s, s.cookie, s.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	webRuntimeSubscribe(t, conn, `{"type":"subscribe","topics":["commit"]}`)
	second := s.login(t)
	other, err := webRuntimeDialEvents(t, s, second, s.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	webRuntimeSubscribe(t, other, `{"type":"subscribe","topics":[]}`)
	if code, _ := s.request(t, "POST", "/api/auth/logout", "", s.cookie, ""); code != 204 {
		t.Fatal(code)
	}
	if _, _, err := wsutil.ReadServerData(conn); err == nil {
		t.Fatal("logout did not close the matching events connection")
	}
	if err := wsutil.WriteClientMessage(other, ws.OpText, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatal(err)
	}
	if event := webRuntimeReadEvent(t, other); event.Type != "pong" {
		t.Fatal("one session logout closed another session")
	}
	if code, _ := s.request(t, "POST", "/api/auth/credentials", `{"username":"admin","currentPassword":"correct horse","newPassword":"another synthetic password"}`, second, ""); code != 200 {
		t.Fatal(code)
	}
	if _, _, err := wsutil.ReadServerData(other); err == nil {
		t.Fatal("recredential did not close all event connections")
	}
	if code, _ := s.request(t, "GET", "/api/v1/runtime/state", "", second, ""); code != 401 {
		t.Fatal("revoked session read runtime state")
	}

	expiring := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	key := sha256.Sum256([]byte(expiring.cookie.Value))
	expiring.auth.mu.Lock()
	expiring.auth.sessions[key].expires = time.Now().Add(60 * time.Millisecond)
	expiring.auth.mu.Unlock()
	idle, err := webRuntimeDialEvents(t, expiring, expiring.cookie, expiring.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	if _, _, err := wsutil.ReadServerData(idle); err == nil {
		t.Fatal("absolute expiry did not close idle event socket")
	}
}

func TestWebAPIEventsRejectEveryNativeMethod(t *testing.T) {
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	protocol, err := os.ReadFile("protocol.go")
	if err != nil {
		t.Fatal(err)
	}
	methods := regexp.MustCompile(`CoreMethod\s*=\s*"([^"]+)"`).FindAllSubmatch(protocol, -1)
	if len(methods) < 50 {
		t.Fatal("incomplete native method inventory")
	}
	for _, entry := range methods {
		method := string(entry[1])
		t.Run(method, func(t *testing.T) {
			conn, err := webRuntimeDialEvents(t, s, s.cookie, s.server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			frame, _ := json.Marshal(map[string]any{"id": "native-deny", "method": method, "arguments": map[string]any{}})
			if err := wsutil.WriteClientMessage(conn, ws.OpText, frame); err != nil {
				t.Fatal(err)
			}
			if body, _, err := wsutil.ReadServerData(conn); err == nil {
				t.Fatalf("native method %s accepted by events: %s", method, body)
			}
			if code, body := s.request(t, "POST", "/api/v1/runtime/rpc", string(frame), s.cookie, ""); code != 404 {
				t.Fatalf("renamed RPC accepted %s: %d %s", method, code, body)
			}
		})
	}
	for _, frame := range []string{`{"type":"subscribe","topics":["log"],"method":"core.init"}`, `{"Type":"ping"}`, `{"type":"ping","type":"ping"}`, `{"type":"subscribe","topics":["unknown"]}`, `{"type":"subscribe","topics":["log","log"]}`, `{"type":"ping","arguments":{}}`} {
		conn, err := webRuntimeDialEvents(t, s, s.cookie, s.server.URL)
		if err != nil {
			t.Fatal(err)
		}
		if err = wsutil.WriteClientMessage(conn, ws.OpText, []byte(frame)); err != nil {
			t.Fatal(err)
		}
		_, _, err = wsutil.ReadServerData(conn)
		conn.Close()
		if err == nil {
			t.Errorf("invalid frame accepted: %s", frame)
		}
	}
}

func TestWebAPIEventsCommitBeforeHTTPAndReconnectInvalidation(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{SelectProxy: func(context.Context, string, string) (*ChangeProxyResult, error) {
		started <- struct{}{}
		<-release
		return &ChangeProxyResult{Changed: true}, nil
	}})
	conn, err := webRuntimeDialEvents(t, s, s.cookie, s.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	snapshot := webRuntimeSubscribe(t, conn, `{"type":"subscribe","topics":["commit"],"storeId":"old-store","revision":"0"}`)
	if !snapshot.Invalidate || snapshot.StoreID == "" || snapshot.Revision != "1" {
		t.Fatalf("initial snapshot %+v", snapshot)
	}
	status, body := s.request(t, "POST", "/api/v1/runtime/proxies:select", `{"expectedRevision":"1","operationId":"event-before-http","groupName":"G","proxyName":"P","runtimeGeneration":"7"}`, s.cookie, "")
	if status != 200 {
		t.Fatalf("command %d %s", status, body)
	}
	event := webRuntimeReadEvent(t, conn)
	if event.Type != "commit" || event.OperationID != "event-before-http" || event.Revision != "2" || event.StoreID != snapshot.StoreID {
		t.Fatalf("commit event %+v", event)
	}
	<-started
	code, receipt := s.request(t, "GET", "/api/v1/operations/event-before-http", "", s.cookie, "")
	if code != 200 || !bytes.Contains(receipt, []byte(`"phase":"pending"`)) {
		t.Fatalf("commit event could not recover original pending receipt: %d %s", code, receipt)
	}
	close(release)
	if state := s.phase(t, "event-before-http"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state.Phase)
	}
	conn.Close()
	reconnected, err := webRuntimeDialEvents(t, s, s.cookie, s.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close()
	command, _ := json.Marshal(map[string]any{"type": "subscribe", "topics": []string{"commit"}, "storeId": snapshot.StoreID, "revision": "0"})
	if snap := webRuntimeSubscribe(t, reconnected, string(command)); !snap.Invalidate || snap.Revision != "2" {
		t.Fatalf("reconnect failed to invalidate: %+v", snap)
	}
	command, _ = json.Marshal(map[string]any{"type": "subscribe", "topics": []string{"commit"}, "storeId": snapshot.StoreID, "revision": "2"})
	if snap := webRuntimeSubscribe(t, reconnected, string(command)); snap.Invalidate {
		t.Fatal("matching snapshot was incorrectly invalidated")
	}
}

func TestWebAPIEventsSubscriptionsArePerConnection(t *testing.T) {
	var on, off atomic.Int32
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{Subscribe: func(logs, route bool) {
		if logs && route {
			on.Add(1)
		}
		if !logs && !route {
			off.Add(1)
		}
	}})
	a, err := webRuntimeDialEvents(t, s, s.cookie, s.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := webRuntimeDialEvents(t, s, s.cookie, s.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	webRuntimeSubscribe(t, a, `{"type":"subscribe","topics":["log","route"]}`)
	webRuntimeSubscribe(t, b, `{"type":"subscribe","topics":["log","route"]}`)
	if on.Load() != 1 {
		t.Fatalf("repeated tab subscription restarted global pumps %d", on.Load())
	}
	webRuntimeSubscribe(t, a, `{"type":"subscribe","topics":[]}`)
	if off.Load() != 0 {
		t.Fatal("one tab stopped another tab's subscriptions")
	}
	webRuntimeSubscribe(t, b, `{"type":"subscribe","topics":[]}`)
	if off.Load() != 1 {
		t.Fatal("last unsubscribe left native pumps running")
	}
	if rev, err := s.store.View(context.Background(), func(*WebAPIReadTx) error { return nil }); err != nil || rev != "0" {
		t.Fatal("ephemeral subscriptions changed canonical revision")
	}
}

func TestWebAPIEventsTierBackpressureAndNoRPCOutput(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	carrier := &webClient{conn: a, queue: make(chan []byte, 1), done: make(chan struct{})}
	client := &webAPIEventClient{carrier: carrier, topics: map[string]bool{"log": true, "dns": true, "route": true, "delay": true}, wake: make(chan struct{}, 1)}
	for i := 0; i < webAPIEventQueueCapacity; i++ {
		client.enqueue("log", []byte("old"), "bulk")
	}
	client.enqueue("dns", []byte("dns"), "dns")
	if len(client.bulk) != webAPIEventQueueCapacity || string(client.bulk[0]) != "old" {
		t.Fatal("DNS evicted request/log bulk records")
	}
	client.enqueue("log", []byte("new"), "bulk")
	if string(client.bulk[len(client.bulk)-1]) != "new" {
		t.Fatal("bulk did not evict its own oldest record")
	}
	for i := 0; i < webAPIEventStateCapacity; i++ {
		client.enqueue("route", []byte("state"), "state")
	}
	select {
	case <-carrier.done:
		t.Fatal("queue closed before state capacity")
	default:
	}
	client.enqueue("route", []byte("lost-state"), "state")
	select {
	case <-carrier.done:
	default:
		t.Fatal("state loss did not force reconnect/snapshot invalidation")
	}
	called := 0
	if webAPIEventFromCore([]byte(`{"id":"response","result":"sensitive"}`), func(WebAPIEvent, string) { called++ }) || called != 0 {
		t.Fatal("native MethodResponse crossed event-only transport")
	}
	if !webAPIEventFromCore([]byte(`{"method":"message","arguments":[{"type":"routeChanged","data":{"core-epoch":1,"route-version":2}},{"type":"unknown","data":"no"}]}`), func(e WebAPIEvent, tier string) {
		called++
		if e.Type != "route" || tier != "state" {
			t.Fatal("core event mapping was not typed")
		}
	}) || called != 1 {
		t.Fatal("typed event batch was not mapped")
	}
}

func TestWebRuntimeLogoutBeforeCommitDoesNotMutate(t *testing.T) {
	var applied atomic.Int32
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{ResetTraffic: func(context.Context) error { applied.Add(1); return nil }})
	cookie := s.cookie
	if code, _ := s.request(t, "POST", "/api/auth/logout", "", cookie, ""); code != 204 {
		t.Fatal(code)
	}
	if code, _ := s.request(t, "POST", "/api/v1/runtime/traffic:reset", `{"expectedRevision":"0","operationId":"after-logout"}`, cookie, ""); code != 401 {
		t.Fatal("revoked session committed a command")
	}
	if applied.Load() != 0 {
		t.Fatal("logout did not linearize against commit")
	}
}
