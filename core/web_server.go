//go:build webpanel && !android

package core

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	webSessionCookie = "flclash_session"
	webSessionTTL    = 12 * time.Hour
	webMaxBody       = 32 << 20
	webMaxSessions   = 128
	webMaxClients    = 32
	webMaxRPC        = 64
	webClientQueue   = 128
)

type WebServerOptions struct {
	ListenAddr   string
	DataDir      string
	WebDir       string
	EmbeddedFS   fs.FS
	Username     string
	Password     string
	PasswordFile string
	AuthFile     string
	SecureCookie bool
}

type webSession struct {
	expires time.Time
	clients map[*webClient]struct{}
}

type WebServer struct {
	opts                        WebServerOptions
	auth                        *webAuthState
	authMu                      sync.Mutex
	authRoot                    *os.Root
	authName                    string
	authWork                    chan struct{}
	derivePassword              func(string, []byte) []byte
	syncAuthDirectory           func(*os.Root) error
	syncAuthFile                func(*os.File) error
	credentialsWindow           time.Time
	credentialsAttempts         int
	data, temporary, staticRoot *os.Root
	static                      fs.FS
	mu                          sync.Mutex
	sessions                    map[[32]byte]*webSession
	loginWindow                 time.Time
	loginAttempts               int
	now                         func() time.Time
	ioMu                        sync.Mutex
	rpcSlots                    chan struct{}
	coreEnabled                 atomic.Bool
	startTimeMs                 int64
}

func webCanonicalDir(name string) (string, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func webWithin(root, name string) bool {
	rel, err := filepath.Rel(root, name)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

func NewWebServer(opts WebServerOptions) (*WebServer, error) {
	if opts.DataDir == "" {
		return nil, errors.New("web data directory is required")
	}
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return nil, errors.New("cannot create web data directory")
	}
	dataDir, err := webCanonicalDir(opts.DataDir)
	if err != nil {
		return nil, errors.New("cannot resolve web data directory")
	}
	opts.DataDir = dataDir
	var webDir string
	if opts.WebDir != "" {
		webDir, err = webCanonicalDir(opts.WebDir)
		if err != nil {
			return nil, errors.New("cannot resolve web asset directory")
		}
		if webWithin(webDir, dataDir) || webWithin(dataDir, webDir) {
			return nil, errors.New("web data and asset directories must not overlap")
		}
		opts.WebDir = webDir
	}
	s := &WebServer{opts: opts, sessions: make(map[[32]byte]*webSession), now: time.Now, rpcSlots: make(chan struct{}, webMaxRPC), authWork: make(chan struct{}, 4), derivePassword: webDerivePassword}
	if err := s.initializeCredentials(opts); err != nil {
		s.Close()
		return nil, err
	}
	s.opts.Password = ""
	s.opts.PasswordFile = ""
	s.data, err = os.OpenRoot(dataDir)
	if err != nil {
		s.Close()
		return nil, errors.New("cannot open web data directory")
	}
	for _, dir := range []string{"profiles", "providers", ".tmp"} {
		if err = s.data.MkdirAll(dir, 0o700); err != nil {
			s.Close()
			return nil, errors.New("cannot create web data subdirectory")
		}
	}
	s.temporary, err = s.data.OpenRoot(".tmp")
	if err != nil {
		s.Close()
		return nil, errors.New("cannot open web temporary directory")
	}
	if webDir != "" {
		s.staticRoot, err = os.OpenRoot(webDir)
		if err != nil {
			s.Close()
			return nil, errors.New("cannot open web asset directory")
		}
		s.static = s.staticRoot.FS()
	} else {
		s.static = opts.EmbeddedFS
	}
	s.seedGeoAssets()
	return s, nil
}

func (s *WebServer) Close() error {
	s.mu.Lock()
	for key := range s.sessions {
		s.revokeLocked(key)
	}
	s.mu.Unlock()
	var errs []error
	for _, root := range []*os.Root{s.staticRoot, s.temporary, s.data, s.authRoot} {
		if root != nil {
			errs = append(errs, root.Close())
		}
	}
	return errors.Join(errs...)
}

func (s *WebServer) revokeLocked(key [32]byte) {
	if session := s.sessions[key]; session != nil {
		delete(s.sessions, key)
		for client := range session.clients {
			client.close()
		}
	}
}

func (s *WebServer) purgeLocked() {
	for key, session := range s.sessions {
		if !s.now().Before(session.expires) {
			s.revokeLocked(key)
		}
	}
}

func (s *WebServer) session(r *http.Request) ([32]byte, *webSession) {
	var key [32]byte
	cookie, err := r.Cookie(webSessionCookie)
	if err != nil || len(cookie.Value) != 43 {
		return key, nil
	}
	key = sha256.Sum256([]byte(cookie.Value))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked()
	return key, s.sessions[key]
}

func (s *WebServer) sameOrigin(r *http.Request) bool {
	values := r.Header.Values("Origin")
	if len(values) == 0 {
		return true
	}
	if len(values) != 1 {
		return false
	}
	u, err := url.Parse(values[0])
	scheme := "http"
	if r.TLS != nil || s.opts.SecureCookie {
		scheme = "https"
	}
	return err == nil && u.Scheme == scheme && strings.EqualFold(u.Host, r.Host) && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Path == ""
}

func webJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func webMethod(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, method := range methods {
		if r.Method == method {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func (s *WebServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	if !s.sameOrigin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, webMaxBody)
	if r.URL.Path == "/login" {
		if !webMethod(w, r, http.MethodGet) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		page, err := renderWebLogin()
		if err != nil {
			http.Error(w, "login page unavailable", 500)
			return
		}
		_, _ = w.Write(page)
		return
	}
	if r.URL.Path == "/api/auth/login" {
		if webMethod(w, r, http.MethodPost) {
			s.login(w, r)
		}
		return
	}
	key, session := s.session(r)
	if session == nil {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") || r.URL.Path == "/api" || r.URL.Path == "/ws" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		} else {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		}
		return
	}
	switch r.URL.Path {
	case "/api/auth/session":
		if webMethod(w, r, http.MethodGet) {
			s.mu.Lock()
			s.purgeLocked()
			active := s.sessions[key] == session
			username := s.auth.record.Username
			s.mu.Unlock()
			if !active {
				http.Error(w, "unauthorized", 401)
				return
			}
			webJSON(w, map[string]any{"ok": true, "username": username})
		}
	case "/api/auth/credentials":
		if webMethod(w, r, http.MethodPost) {
			s.updateCredentials(w, r, key, session)
		}
	case "/api/auth/logout":
		if !webMethod(w, r, http.MethodPost) {
			return
		}
		s.mu.Lock()
		s.revokeLocked(key)
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: webSessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.opts.SecureCookie, SameSite: http.SameSiteStrictMode})
		w.WriteHeader(http.StatusNoContent)
	case "/ws/core":
		if webMethod(w, r, http.MethodGet) {
			s.serveWebSocket(w, r, key)
		}
	case "/api/http-proxy":
		if webMethod(w, r, http.MethodPost) {
			s.serveProxy(w, r)
		}
	default:
		if strings.HasPrefix(r.URL.Path, "/api/fs/") {
			s.serveFS(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") {
			http.NotFound(w, r)
			return
		}
		s.serveStatic(w, r)
	}
}

func (s *WebServer) serveStatic(w http.ResponseWriter, r *http.Request) {
	if !webMethod(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	if s.static == nil {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" || name == "." {
		name = "index.html"
	}
	if info, err := fs.Stat(s.static, name); err != nil || info.IsDir() {
		name = "index.html"
	} else if !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	f, err := s.static.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(name, ".wasm") {
		w.Header().Set("Content-Type", "application/wasm")
	}
	if seeker, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, name, info.ModTime(), seeker)
		return
	}
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, f)
	}
}

func StartWebServer(opts WebServerOptions) error {
	s, err := NewWebServer(opts)
	if err != nil {
		return err
	}
	defer s.Close()
	address := opts.ListenAddr
	if address == "" {
		address = "127.0.0.1:8080"
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	restore, err := s.attachCore()
	if err != nil {
		return err
	}
	defer restore()
	server := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	defer server.Close()
	return server.Serve(listener)
}
