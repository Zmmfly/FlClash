//go:build webpanel && !android

package core

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

const webAPIEventMaxInput = 16 << 10
const webAPIEventMaxBytes = 1 << 20
const webAPIEventQueueBytes = 4 << 20
const webAPIEventStateCapacity = 32
const webAPIEventQueueCapacity = 128

type WebAPIEvent struct {
	Type        string         `json:"type"`
	StoreID     string         `json:"storeId,omitempty"`
	Revision    WebAPIRevision `json:"revision,omitempty"`
	OperationID string         `json:"operationId,omitempty"`
	Resources   []string       `json:"resources,omitempty"`
	Data        any            `json:"data,omitempty"`
	Invalidate  bool           `json:"invalidate,omitempty"`
}

type WebAPIEvents struct {
	storeID     string
	store       *WebAPIStore
	mu          sync.Mutex
	subscribeMu sync.Mutex
	clients     map[*webAPIEventClient]struct{}
	subscribe   func(bool, bool)
	logs, route bool
	closed      bool
}

type webAPIEventClient struct {
	hub                   *WebAPIEvents
	carrier               *webClient
	mu                    sync.Mutex
	topics                map[string]bool
	state, priority, bulk [][]byte
	bytes                 int
	priorityRun           int
	wake                  chan struct{}
}

type webAPIEventCommand struct {
	Type     string          `json:"type"`
	Topics   []string        `json:"topics,omitempty"`
	StoreID  *string         `json:"storeId,omitempty"`
	Revision *WebAPIRevision `json:"revision,omitempty"`
}

func NewWebAPIEvents(storeID string, store *WebAPIStore, subscribe func(bool, bool)) *WebAPIEvents {
	return &WebAPIEvents{storeID: storeID, store: store, clients: map[*webAPIEventClient]struct{}{}, subscribe: subscribe}
}

func (h *WebAPIEvents) Commit(commit WebAPICommit) {
	h.publish(WebAPIEvent{Type: "commit", StoreID: h.storeID, Revision: commit.Revision, OperationID: commit.OperationID}, "state")
}
func (h *WebAPIEvents) PublishRuntime(state WebAPIRuntimeOperation) {
	h.publish(WebAPIEvent{Type: "runtime", Data: state}, "state")
}
func (h *WebAPIEvents) HasTopic(topic string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		c.mu.Lock()
		yes := c.topics[topic]
		c.mu.Unlock()
		if yes {
			return true
		}
	}
	return false
}

func (h *WebAPIEvents) publish(event WebAPIEvent, tier string) {
	body, err := json.Marshal(event)
	if err != nil || len(body) > webAPIEventMaxBytes {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	for c := range h.clients {
		c.enqueue(event.Type, body, tier)
	}
}

func webAPIEventFromCore(frame []byte, emit func(WebAPIEvent, string)) bool {
	var envelope struct {
		Method    CoreMethod `json:"method"`
		Arguments []struct {
			Type MessageType     `json:"type"`
			Data json.RawMessage `json:"data"`
		} `json:"arguments"`
	}
	if len(frame) > webMaxBody || json.Unmarshal(frame, &envelope) != nil || envelope.Method != messageMethod {
		return false
	}
	for _, message := range envelope.Arguments {
		kind, tier := "", "priority"
		switch message.Type {
		case LogMessage:
			kind, tier = "log", "bulk"
		case DelayMessage:
			kind = "delay"
		case RequestMessage:
			kind, tier = "connections", "bulk"
		case DnsMessage:
			kind, tier = "dns", "dns"
		case LoadedMessage:
			kind, tier = "loaded", "state"
		case GeoUpdateMessage:
			kind, tier = "geo", "state"
		case RouteChangedMessage:
			kind, tier = "route", "state"
		case DialerLoopMessage:
			kind = "dialerLoop"
		case RequestTrafficMessage:
			kind, tier = "traffic", "bulk"
		default:
			continue
		}
		emit(WebAPIEvent{Type: kind, Data: message.Data}, tier)
	}
	return true
}

func (h *WebAPIEvents) PublishCoreFrame(frame []byte) bool {
	return webAPIEventFromCore(frame, h.publish)
}

func (c *webAPIEventClient) enqueue(topic string, data []byte, tier string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if topic != "snapshot" && topic != "pong" && !c.topics[topic] {
		return
	}
	if len(data) > webAPIEventMaxBytes {
		c.carrier.close()
		return
	}
	var queue *[][]byte
	limit := webAPIEventQueueCapacity
	switch tier {
	case "state":
		queue = &c.state
		limit = webAPIEventStateCapacity
	case "priority":
		queue = &c.priority
	default:
		queue = &c.bulk
	}
	if tier == "dns" && (len(*queue) >= limit || c.bytes+len(data) > webAPIEventQueueBytes) {
		return
	}
	if tier == "state" && (len(*queue) >= limit || c.bytes+len(data) > webAPIEventQueueBytes) {
		c.carrier.close()
		return
	}
	for len(*queue) > 0 && (len(*queue) >= limit || c.bytes+len(data) > webAPIEventQueueBytes) {
		c.bytes -= len((*queue)[0])
		(*queue)[0] = nil
		*queue = (*queue)[1:]
	}
	if c.bytes+len(data) > webAPIEventQueueBytes {
		if tier == "priority" {
			c.carrier.close()
		}
		return
	}
	*queue = append(*queue, data)
	c.bytes += len(data)
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *webAPIEventClient) next() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	var queue *[][]byte
	switch {
	case len(c.state) > 0:
		queue = &c.state
	case len(c.priority) > 0 && (c.priorityRun < 8 || len(c.bulk) == 0):
		queue = &c.priority
		c.priorityRun++
	case len(c.bulk) > 0:
		queue = &c.bulk
		c.priorityRun = 0
	default:
		return nil
	}
	data := (*queue)[0]
	(*queue)[0] = nil
	*queue = (*queue)[1:]
	c.bytes -= len(data)
	return data
}

func webAPIEventTopic(topic string) bool {
	switch topic {
	case "commit", "runtime", "log", "delay", "connections", "dns", "loaded", "geo", "route", "dialerLoop", "traffic", "mitm":
		return true
	}
	return false
}

func (h *WebAPIEvents) reconcile() {
	h.subscribeMu.Lock()
	defer h.subscribeMu.Unlock()
	h.mu.Lock()
	logs, route := false, false
	for c := range h.clients {
		c.mu.Lock()
		logs = logs || c.topics["log"]
		route = route || c.topics["route"]
		c.mu.Unlock()
	}
	changed := h.logs != logs || h.route != route
	h.logs, h.route = logs, route
	callback := h.subscribe
	h.mu.Unlock()
	if changed && callback != nil {
		callback(logs, route)
	}
}

func (h *WebAPIEvents) Close() {
	h.mu.Lock()
	h.closed = true
	for c := range h.clients {
		c.carrier.close()
	}
	clear(h.clients)
	h.mu.Unlock()
	h.reconcile()
}

func (h *WebAPIEvents) ServeHTTP(auth *WebServer, w http.ResponseWriter, r *http.Request) {
	if auth == nil || !auth.sameOrigin(r) {
		WebAPIWriteError(w, webAPIError(401, "unauthorized", "an authenticated same-origin session is required"))
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		WebAPIWriteError(w, webAPIError(405, "method_not_allowed", "GET is required"))
		return
	}
	if _, err := WebAPIQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	key, session := auth.session(r)
	if session == nil {
		WebAPIWriteError(w, webAPIError(401, "unauthorized", "an authenticated session is required"))
		return
	}
	auth.mu.Lock()
	auth.purgeLocked()
	count := 0
	for _, s := range auth.sessions {
		count += len(s.clients)
	}
	active := auth.sessions[key] == session
	auth.mu.Unlock()
	if !active || count >= webMaxClients {
		WebAPIWriteError(w, webAPIError(503, "events_busy", "event connections are unavailable"))
		return
	}
	connection, buffer, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		return
	}
	carrier := &webClient{conn: connection, queue: make(chan []byte, webClientQueue), done: make(chan struct{})}
	client := &webAPIEventClient{hub: h, carrier: carrier, topics: map[string]bool{}, wake: make(chan struct{}, 1)}
	auth.mu.Lock()
	auth.purgeLocked()
	count = 0
	for _, s := range auth.sessions {
		count += len(s.clients)
	}
	if auth.sessions[key] != session || count >= webMaxClients {
		auth.mu.Unlock()
		carrier.close()
		return
	}
	session.clients[carrier] = struct{}{}
	expires := session.expires
	auth.mu.Unlock()
	defer func() {
		carrier.close()
		h.mu.Lock()
		delete(h.clients, client)
		h.mu.Unlock()
		h.reconcile()
		auth.mu.Lock()
		delete(session.clients, carrier)
		auth.mu.Unlock()
	}()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		carrier.close()
		return
	}
	h.clients[client] = struct{}{}
	h.mu.Unlock()

	go client.writeLoop(expires)
	var source io.Reader = connection
	if buffer != nil {
		source = buffer.Reader
	}
	reader := wsutil.Reader{Source: source, State: ws.StateServerSide, CheckUTF8: true, MaxFrameSize: webAPIEventMaxInput}
	reader.OnIntermediate = func(header ws.Header, body io.Reader) error {
		data, err := io.ReadAll(io.LimitReader(body, 126))
		if err != nil {
			return err
		}
		switch header.OpCode {
		case ws.OpPing:
			return carrier.write(ws.OpPong, data)
		case ws.OpClose:
			return io.EOF
		}
		return nil
	}
	for {
		_ = connection.SetReadDeadline(time.Now().Add(90 * time.Second))
		header, err := reader.NextFrame()
		if err != nil {
			return
		}
		data, err := io.ReadAll(io.LimitReader(&reader, webAPIEventMaxInput+1))
		if err != nil || len(data) > webAPIEventMaxInput {
			return
		}
		auth.mu.Lock()
		auth.purgeLocked()
		active = auth.sessions[key] == session
		auth.mu.Unlock()
		if !active {
			return
		}
		switch header.OpCode {
		case ws.OpClose:
			return
		case ws.OpPong:
			continue
		case ws.OpPing:
			if carrier.write(ws.OpPong, data) != nil {
				return
			}
			continue
		case ws.OpText:
		default:
			client.reject()
			return
		}
		var command webAPIEventCommand
		if webAPIStrictJSON(data, &command) != nil {
			client.reject()
			return
		}
		switch command.Type {
		case "ping":
			if command.Topics != nil || command.StoreID != nil || command.Revision != nil {
				client.reject()
				return
			}
			client.enqueue("pong", []byte(`{"type":"pong"}`), "priority")
		case "subscribe":
			if command.Topics == nil || len(command.Topics) > 12 {
				client.reject()
				return
			}
			topics := map[string]bool{}
			for _, topic := range command.Topics {
				if !webAPIEventTopic(topic) || topics[topic] {
					client.reject()
					return
				}
				topics[topic] = true
			}
			client.mu.Lock()
			client.topics = topics
			client.mu.Unlock()
			h.reconcile()
			rev, err := h.store.View(r.Context(), func(*WebAPIReadTx) error { return nil })
			if err != nil {
				client.reject()
				return
			}
			invalidate := command.StoreID == nil || *command.StoreID != h.storeID || command.Revision == nil || *command.Revision != rev
			body, _ := json.Marshal(WebAPIEvent{Type: "snapshot", StoreID: h.storeID, Revision: rev, Invalidate: invalidate})
			client.enqueue("snapshot", body, "state")
		default:
			client.reject()
			return
		}
	}
}

func (c *webAPIEventClient) reject() {
	_ = c.carrier.write(ws.OpClose, ws.NewCloseFrameBody(ws.StatusPolicyViolation, "only typed subscribe and ping are accepted"))
}
func (c *webAPIEventClient) writeLoop(expires time.Time) {
	timer := time.NewTimer(time.Until(expires))
	defer timer.Stop()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	defer c.carrier.close()
	for {
		select {
		case <-c.carrier.done:
			return
		case <-timer.C:
			return
		default:
		}
		if data := c.next(); data != nil {
			if c.carrier.write(ws.OpText, data) != nil {
				return
			}
			continue
		}
		select {
		case <-c.carrier.done:
			return
		case <-timer.C:
			return
		case <-ticker.C:
			if c.carrier.write(ws.OpPing, nil) != nil {
				return
			}
		case <-c.wake:
		case frame := <-c.carrier.queue:
			webAPIEventFromCore(frame, func(event WebAPIEvent, tier string) {
				body, err := json.Marshal(event)
				if err == nil {
					c.enqueue(event.Type, body, tier)
				}
			})
		}
	}
}

func (rt *WebAPIRuntime) ServeEvents(auth *WebServer, w http.ResponseWriter, r *http.Request) {
	rt.Events.ServeHTTP(auth, w, r)
}

func WebAPIRuntimeSessionHandler(auth *WebServer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth == nil || !auth.sameOrigin(r) {
			WebAPIWriteError(w, webAPIError(401, "unauthorized", "an authenticated same-origin session is required"))
			return
		}
		key, session := auth.session(r)
		if session == nil {
			WebAPIWriteError(w, webAPIError(401, "unauthorized", "an authenticated session is required"))
			return
		}
		ctx := WithWebAPIRequestAuthorization(r.Context(), hex.EncodeToString(key[:]), func(ctx context.Context) (func(), error) {
			auth.mu.Lock()
			auth.purgeLocked()
			if auth.sessions[key] != session || ctx.Err() != nil {
				auth.mu.Unlock()
				return nil, webAPIError(401, "unauthorized", "the session is no longer authorized")
			}
			return auth.mu.Unlock, nil
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
