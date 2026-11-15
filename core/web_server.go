//go:build webpanel && !android

package core

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
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
	SecureCookie bool
}

type webSession struct {
	expires time.Time
	clients map[*webClient]struct{}
}

type WebServer struct {
	opts                        WebServerOptions
	username, password          [32]byte
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
	if strings.TrimSpace(opts.Username) == "" {
		return nil, errors.New("web username is required")
	}
	if opts.Password != "" && opts.PasswordFile != "" {
		return nil, errors.New("web password environment and password file are mutually exclusive")
	}
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
	if opts.PasswordFile != "" {
		file, err := filepath.EvalSymlinks(opts.PasswordFile)
		if err == nil {
			file, err = filepath.Abs(file)
		}
		if err != nil {
			return nil, errors.New("cannot resolve web password file")
		}
		if webWithin(dataDir, file) || (webDir != "" && webWithin(webDir, file)) {
			return nil, errors.New("web password file must be outside data and asset directories")
		}
		initial, err := os.Stat(file)
		if err != nil || !initial.Mode().IsRegular() || initial.Size() > 4096 {
			return nil, errors.New("invalid web password file")
		}
		f, err := os.Open(file)
		if err != nil {
			return nil, errors.New("cannot read web password file")
		}
		info, statErr := f.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
			f.Close()
			return nil, errors.New("invalid web password file")
		}
		data, readErr := io.ReadAll(io.LimitReader(f, 4097))
		f.Close()
		if readErr != nil || len(data) > 4096 {
			return nil, errors.New("cannot read web password file")
		}
		opts.Password = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	}
	if opts.Password == "" {
		return nil, errors.New("web password is required")
	}
	s := &WebServer{opts: opts, username: sha256.Sum256([]byte(opts.Username)), password: sha256.Sum256([]byte(opts.Password)), sessions: make(map[[32]byte]*webSession), now: time.Now, rpcSlots: make(chan struct{}, webMaxRPC)}
	s.opts.Password = ""
	s.data, err = os.OpenRoot(dataDir)
	if err != nil {
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
	for _, root := range []*os.Root{s.staticRoot, s.temporary, s.data} {
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
		_, _ = io.WriteString(w, webLoginHTML)
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
			webJSON(w, map[string]bool{"ok": true})
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

func (s *WebServer) login(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	now := s.now()
	if now.Sub(s.loginWindow) >= time.Minute {
		s.loginWindow = now
		s.loginAttempts = 0
	}
	limited := s.loginAttempts >= 20
	if !limited {
		s.loginAttempts++
	}
	s.mu.Unlock()
	var credentials struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 8193))
	err := decoder.Decode(&credentials)
	validJSON := err == nil && decoder.Decode(&struct{}{}) == io.EOF
	user := sha256.Sum256([]byte(credentials.Username))
	password := sha256.Sum256([]byte(credentials.Password))
	valid := subtle.ConstantTimeCompare(user[:], s.username[:]) & subtle.ConstantTimeCompare(password[:], s.password[:])
	if limited || !validJSON || valid != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	value := base64.RawURLEncoding.EncodeToString(token)
	key := sha256.Sum256([]byte(value))
	s.mu.Lock()
	s.purgeLocked()
	if len(s.sessions) >= webMaxSessions {
		s.mu.Unlock()
		http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	if old, err := r.Cookie(webSessionCookie); err == nil {
		s.revokeLocked(sha256.Sum256([]byte(old.Value)))
	}
	s.sessions[key] = &webSession{expires: now.Add(webSessionTTL), clients: make(map[*webClient]struct{})}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: webSessionCookie, Value: value, Path: "/", MaxAge: int(webSessionTTL.Seconds()), HttpOnly: true, Secure: s.opts.SecureCookie, SameSite: http.SameSiteStrictMode})
	webJSON(w, map[string]bool{"ok": true})
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

const webLoginHTML = `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>FlClash · Login / 登录</title><style>body{font:16px system-ui;background:#f4f5f7;color:#20242b;display:grid;place-items:center;min-height:95vh}main{width:min(330px,85vw)}input,button{box-sizing:border-box;width:100%;padding:12px;margin:8px 0;font:inherit}button{cursor:pointer}#error{color:#b42318}</style><main><h1>FlClash</h1><h2>Login / 登录</h2><form id="login"><label>Username / 用户名<input name="username" autocomplete="username" required></label><label>Password / 密码<input name="password" type="password" autocomplete="current-password" required></label><button>Sign in / 登录</button><p id="error" role="alert"></p></form></main><script>document.getElementById('login').onsubmit=async e=>{e.preventDefault();const f=e.target,b=f.querySelector('button');b.disabled=true;try{const r=await fetch('/api/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({username:f.username.value,password:f.password.value})});if(!r.ok)throw Error();location.replace('/')}catch(_){document.getElementById('error').textContent='Unable to sign in / 登录失败'}finally{f.password.value='';b.disabled=false}};</script></html>`
