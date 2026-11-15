//go:build webpanel && !android

package core

// WEBPANEL.md resource bounds apply to authenticated clients, not just unauthenticated traffic.

import (
	"bytes"
	"crypto/sha256"
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
		if err := wsutil.WriteClientMessage(conn, ws.OpText, []byte(`{"id":"ready","method":"core.shutdown"}`)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := wsutil.ReadServerData(conn); err != nil {
			t.Fatal(err)
		}
	}
	if conn, _, err := webDial(t, server, cookie, server.URL); err == nil {
		conn.Close()
		t.Fatal("WebSocket connection cap exceeded")
	}
	for i := 0; i < cap(s.rpcSlots); i++ {
		s.rpcSlots <- struct{}{}
	}
	if err := wsutil.WriteClientMessage(clients[0], ws.OpText, []byte(`{"id":"busy","method":"core.shutdown"}`)); err != nil {
		t.Fatal(err)
	}
	response, _, err := wsutil.ReadServerData(clients[0])
	for i := 0; i < cap(s.rpcSlots); i++ {
		<-s.rpcSlots
	}
	if err != nil || !bytes.Contains(response, []byte(`"code":"busy"`)) {
		t.Fatalf("RPC cap: %s %v", response, err)
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
	s, err := NewWebServer(WebServerOptions{DataDir: dataDir, Username: "admin", Password: "password", EmbeddedFS: assets})
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
