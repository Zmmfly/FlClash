//go:build webpanel && !android

package core

// WEBPANEL.md resource bounds apply to authenticated clients, not just unauthenticated traffic.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"io/fs"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func TestWebSessionConnectionAndRPCBounds(t *testing.T) {
	s := newWebTestServer(t)
	cookie := webLogin(t, s)
	server := httptest.NewServer(s)
	defer server.Close()
	clients := []net.Conn{}
	defer func() {
		for _, conn := range clients {
			conn.Close()
		}
	}()
	for i := 0; i < webMaxClients; i++ {
		conn, _, err := webDial(t, server, cookie, server.URL)
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, conn)
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		if err := wsutil.WriteClientMessage(conn, ws.OpText, []byte(`{"type":"subscribe","topics":[]}`)); err != nil {
			t.Fatal(err)
		}
		response, _, err := wsutil.ReadServerData(conn)
		if err != nil || !bytes.Contains(response, []byte(`"type":"snapshot"`)) {
			t.Fatalf("event snapshot: %s %v", response, err)
		}
		if err := wsutil.WriteClientMessage(conn, ws.OpText, []byte(`{"type":"ping"}`)); err != nil {
			t.Fatal(err)
		}
		response, _, err = wsutil.ReadServerData(conn)
		if err != nil || !bytes.Contains(response, []byte(`"type":"pong"`)) {
			t.Fatalf("event ping: %s %v", response, err)
		}
	}
	if conn, _, err := webDial(t, server, cookie, server.URL); err == nil {
		conn.Close()
		t.Fatal("WebSocket connection cap exceeded")
	}
	releases := []func(){}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for _, control := range []bool{false, true} {
		capacity, endpoint, lane := webAPIHeavyCapacity, "/api/v1/profiles", "heavy"
		if control {
			capacity, endpoint, lane = webAPIControlCapacity, "/api/v1/meta", "control"
		}
		for i := 0; i < capacity; i++ {
			release, err := s.acquireAPIAdmission(context.Background(), control)
			if err != nil {
				t.Fatal(err)
			}
			releases = append(releases, release)
		}
		webAdmissionBusy(t, webRequest(s, "GET", endpoint, "", cookie), lane)
	}
	for _, release := range releases {
		release()
	}
	if err := wsutil.WriteClientMessage(clients[0], ws.OpText, []byte(`{"id":"retired","method":"core.shutdown"}`)); err != nil {
		t.Fatal(err)
	}
	header, err := ws.ReadHeader(clients[0])
	if err != nil || header.OpCode != ws.OpClose || header.Length < 2 || header.Length > 256 {
		t.Fatalf("events did not reject RPC with a bounded close frame: %+v %v", header, err)
	}
	body := make([]byte, header.Length)
	if _, err := io.ReadFull(clients[0], body); err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint16(body[:2]) != uint16(ws.StatusPolicyViolation) {
		t.Fatalf("RPC close code: %d", binary.BigEndian.Uint16(body[:2]))
	}
	s.mu.Lock()
	for len(s.sessions) < webMaxSessions {
		key := sha256.Sum256([]byte{byte(len(s.sessions)), 7})
		s.sessions[key] = &webSession{expires: time.Now().Add(time.Hour), clients: make(map[*webClient]struct{})}
	}
	s.mu.Unlock()
	w := webRequest(s, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse"}`, nil)
	if w.Code != 503 {
		t.Fatal("session capacity was not bounded")
	}
}

func TestWebGeoAssetSeedingKeepsPersistentFiles(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "GEOIP.dat"), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	assets := fstest.MapFS{"assets/assets/data/GEOSITE.dat": &fstest.MapFile{Data: []byte("embedded")}, "assets/data/GEOIP.dat": &fstest.MapFile{Data: []byte("replacement")}}
	s, err := NewWebServer(WebServerOptions{DataDir: dataDir, AuthFile: filepath.Join(t.TempDir(), "auth.json"), Username: "admin", Password: "password", EmbeddedFS: assets})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if data, err := s.data.ReadFile("GEOIP.dat"); err != nil || string(data) != "existing" {
		t.Fatal("persistent geodata was overwritten")
	}
	if data, err := s.data.ReadFile("GEOSITE.dat"); err != nil || string(data) != "embedded" {
		t.Fatal("embedded geodata was not seeded")
	}
	if _, err := fs.Stat(s.data.FS(), "profiles"); err != nil {
		t.Fatal("profile data directory missing")
	}
}
