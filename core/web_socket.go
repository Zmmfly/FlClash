//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

const webMaxWS = 4 << 20

type webClient struct {
	conn    net.Conn
	queue   chan []byte
	done    chan struct{}
	once    sync.Once
	writeMu sync.Mutex
}

func (c *webClient) close() { c.once.Do(func() { close(c.done); _ = c.conn.Close() }) }

func (c *webClient) enqueue(data []byte) {
	if len(data) > webMaxBody {
		c.close()
		return
	}
	select {
	case <-c.done:
		return
	default:
	}
	select {
	case c.queue <- data:
	default:
		c.close()
	}
}

func (c *webClient) write(op ws.OpCode, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return wsutil.WriteServerMessage(c.conn, op, data)
}

func (s *WebServer) serveWebSocket(w http.ResponseWriter, r *http.Request, key [32]byte) {
	s.mu.Lock()
	s.purgeLocked()
	session := s.sessions[key]
	count := 0
	for _, item := range s.sessions {
		count += len(item.clients)
	}
	if session == nil {
		s.mu.Unlock()
		http.Error(w, "unauthorized", 401)
		return
	}
	if count >= webMaxClients {
		s.mu.Unlock()
		http.Error(w, "too many connections", 503)
		return
	}
	s.mu.Unlock()
	connection, buffer, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		return
	}
	client := &webClient{conn: connection, queue: make(chan []byte, webClientQueue), done: make(chan struct{})}
	s.mu.Lock()
	s.purgeLocked()
	count = 0
	for _, item := range s.sessions {
		count += len(item.clients)
	}
	if s.sessions[key] != session || count >= webMaxClients {
		s.mu.Unlock()
		client.close()
		return
	}
	session.clients[client] = struct{}{}
	expires := session.expires
	s.mu.Unlock()
	defer func() { client.close(); s.mu.Lock(); delete(session.clients, client); s.mu.Unlock() }()
	go func() {
		timer := time.NewTimer(time.Until(expires))
		defer timer.Stop()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		defer client.close()
		for {
			select {
			case <-client.done:
				return
			case <-timer.C:
				return
			case <-ticker.C:
				if client.write(ws.OpPing, nil) != nil {
					return
				}
			case data := <-client.queue:
				if client.write(ws.OpText, data) != nil {
					return
				}
			}
		}
	}()
	var source io.Reader = connection
	if buffer != nil {
		source = buffer.Reader
	}
	reader := wsutil.Reader{Source: source, State: ws.StateServerSide, CheckUTF8: true, MaxFrameSize: webMaxWS}
	reader.OnIntermediate = func(header ws.Header, body io.Reader) error {
		data, err := io.ReadAll(io.LimitReader(body, 126))
		if err != nil {
			return err
		}
		if header.OpCode == ws.OpPing {
			return client.write(ws.OpPong, data)
		}
		if header.OpCode == ws.OpClose {
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
		data, err := io.ReadAll(io.LimitReader(&reader, webMaxWS+1))
		if err != nil || len(data) > webMaxWS {
			return
		}
		if header.OpCode == ws.OpClose {
			return
		}
		if header.OpCode == ws.OpPing {
			if client.write(ws.OpPong, data) != nil {
				return
			}
			continue
		}
		if header.OpCode != ws.OpText && header.OpCode != ws.OpBinary {
			continue
		}
		var call MethodCall
		if json.Unmarshal(data, &call) != nil {
			client.enqueue(encodeResponse(MethodResponse{Error: &MethodError{Code: "invalid_arguments", Message: "invalid RPC frame"}}))
			continue
		}
		s.mu.Lock()
		s.purgeLocked()
		active := s.sessions[key] == session
		s.mu.Unlock()
		if !active {
			return
		}
		select {
		case s.rpcSlots <- struct{}{}:
		default:
			client.enqueue(encodeResponse(MethodResponse{ID: call.ID, Error: &MethodError{Code: "busy", Message: "too many pending calls"}}))
			continue
		}
		call.seq = methodCallSeq.Add(1)
		go func(call MethodCall) {
			defer func() { <-s.rpcSlots }()
			client.enqueue(s.webRPC(&call))
		}(call)
	}
}

func (s *WebServer) broadcast(data []byte) {
	data = s.rewriteWebResponse(data)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked()
	for _, session := range s.sessions {
		for client := range session.clients {
			client.enqueue(data)
		}
	}
}

type webEventSink struct {
	server   *WebServer
	mu       sync.Mutex
	buffer   bytes.Buffer
	expected int
}

func (*webEventSink) Read([]byte) (int, error)         { return 0, io.EOF }
func (*webEventSink) Close() error                     { return nil }
func (*webEventSink) SetWriteDeadline(time.Time) error { return nil }
func (sink *webEventSink) Write(data []byte) (int, error) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.buffer.Len()+len(data) > maxIPCFrameSize+4 {
		sink.buffer.Reset()
		sink.expected = 0
		return 0, errors.New("web event too large")
	}
	sink.buffer.Write(data)
	for {
		if sink.expected == 0 {
			if sink.buffer.Len() < 4 {
				break
			}
			var size [4]byte
			_, _ = sink.buffer.Read(size[:])
			sink.expected = int(binary.LittleEndian.Uint32(size[:]))
			if sink.expected > maxIPCFrameSize {
				sink.buffer.Reset()
				sink.expected = 0
				return 0, errors.New("web event too large")
			}
			if sink.expected == 0 {
				continue
			}
		}
		if sink.buffer.Len() < sink.expected {
			break
		}
		frame := make([]byte, sink.expected)
		_, _ = sink.buffer.Read(frame)
		sink.expected = 0
		sink.server.broadcast(frame)
	}
	return len(data), nil
}

func (s *WebServer) attachCore() (func(), error) {
	connMu.Lock()
	defer connMu.Unlock()
	if conn != nil || isInit.Load() {
		return nil, errors.New("web core requires an exclusive process")
	}
	sink := &webEventSink{server: s}
	conn = sink
	s.coreEnabled.Store(true)
	return func() {
		connMu.Lock()
		if conn == sink {
			conn = nil
		}
		connMu.Unlock()
		s.ioMu.Lock()
		defer s.ioMu.Unlock()
		s.coreEnabled.Store(false)
		handleShutdown()
	}, nil
}
