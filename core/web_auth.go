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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/pbkdf2"
)

const webPasswordIterations = 600000

type webCredentials struct {
	Version  int    `json:"version"`
	Username string `json:"username"`
	Salt     string `json:"salt"`
	Hash     string `json:"hash"`
}

type webAuthState struct {
	record webCredentials
	salt   []byte
	hash   []byte
}

func webDerivePassword(password string, salt []byte) []byte {
	return pbkdf2.Key([]byte(password), salt, webPasswordIterations, 32, sha256.New)
}

func webValidUsername(username string) bool {
	return username != "" && !strings.ContainsAny(username, "\r\n") && strings.TrimSpace(username) == username && utf8.ValidString(username) && utf8.RuneCountInString(username) <= 64
}

func webNewCredentials(username, password string, derive func(string, []byte) []byte) (*webAuthState, error) {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, errors.New("cannot generate web credentials")
	}
	hash := derive(password, salt)
	return &webAuthState{record: webCredentials{Version: 1, Username: username, Salt: base64.RawStdEncoding.EncodeToString(salt), Hash: base64.RawStdEncoding.EncodeToString(hash)}, salt: salt, hash: hash}, nil
}

func webResolvePrivatePath(filename, dataDir, webDir string) (string, error) {
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return "", errors.New("cannot resolve web credential path")
	}
	current := absolute
	var missing []string
	for {
		_, err = os.Lstat(current)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", errors.New("cannot resolve web credential path")
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("cannot resolve web credential path")
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", errors.New("cannot resolve web credential path")
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
	}
	if webWithin(dataDir, resolved) || (webDir != "" && webWithin(webDir, resolved)) {
		return "", errors.New("web credential files must be outside data and asset directories")
	}
	return resolved, nil
}

func webLoadCredentials(root *os.Root, name string) (*webAuthState, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 4096 {
		return nil, errors.New("invalid web credential store")
	}
	data, _, err := webReadFile(webPath{root, name}, 4096)
	if err != nil {
		return nil, errors.New("cannot read web credential store")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("invalid web credential store")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, errors.New("invalid web credential store")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, errors.New("invalid web credential store")
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("invalid web credential store")
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, errors.New("invalid web credential store")
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, errors.New("invalid web credential store")
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("invalid web credential store")
	}
	if len(fields) != 4 {
		return nil, errors.New("invalid web credential store")
	}
	var record webCredentials
	if json.Unmarshal(fields["version"], &record.Version) != nil || json.Unmarshal(fields["username"], &record.Username) != nil || json.Unmarshal(fields["salt"], &record.Salt) != nil || json.Unmarshal(fields["hash"], &record.Hash) != nil {
		return nil, errors.New("invalid web credential store")
	}
	salt, saltErr := base64.RawStdEncoding.DecodeString(record.Salt)
	hash, hashErr := base64.RawStdEncoding.DecodeString(record.Hash)
	if record.Version != 1 || !webValidUsername(record.Username) || saltErr != nil || hashErr != nil || len(salt) != 32 || len(hash) != 32 || base64.RawStdEncoding.EncodeToString(salt) != record.Salt || base64.RawStdEncoding.EncodeToString(hash) != record.Hash {
		return nil, errors.New("invalid web credential store")
	}
	return &webAuthState{record: record, salt: salt, hash: hash}, nil
}

func (s *WebServer) prepareCredentials(record webCredentials) (string, error) {
	data, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	name := ".credentials-" + base64.RawURLEncoding.EncodeToString(random) + ".tmp"
	file, err := s.authRoot.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			s.authRoot.Remove(name)
		}
	}()
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return "", err
	}
	if err := s.syncAuthFile(file); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	ok = true
	return name, nil
}

func (s *WebServer) initializeCredentials(opts WebServerOptions) error {
	filename := opts.AuthFile
	if filename == "" {
		filename = opts.DataDir + ".auth.json"
	}
	if info, err := os.Lstat(filename); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("web credential store must not be a symbolic link")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect web credential store")
	}
	resolved, err := webResolvePrivatePath(filename, opts.DataDir, opts.WebDir)
	if err != nil {
		return err
	}
	if opts.PasswordFile != "" {
		if _, err := webResolvePrivatePath(opts.PasswordFile, opts.DataDir, opts.WebDir); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o700); err != nil {
		return errors.New("cannot create web credential directory")
	}
	s.authRoot, err = os.OpenRoot(filepath.Dir(resolved))
	if err != nil {
		return errors.New("cannot open web credential directory")
	}
	if s.syncAuthFile == nil {
		s.syncAuthFile = func(file *os.File) error { return file.Sync() }
	}
	if s.syncAuthDirectory == nil {
		s.syncAuthDirectory = webSyncAuthDirectory
	}
	s.authName = filepath.Base(resolved)
	s.opts.AuthFile = resolved
	stored, err := webLoadCredentials(s.authRoot, s.authName)
	if err == nil {
		s.auth = stored
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return errors.New("invalid or unreadable web credential store")
	}
	if strings.ContainsAny(opts.Username, "\r\n") {
		return errors.New("web username must not contain line breaks")
	}
	username := strings.TrimSpace(opts.Username)
	if !webValidUsername(username) {
		return errors.New("web username is required and must not exceed 64 characters")
	}
	if opts.Password != "" && opts.PasswordFile != "" {
		return errors.New("web password environment and password file are mutually exclusive")
	}
	password := opts.Password
	if opts.PasswordFile != "" {
		file, err := webResolvePrivatePath(opts.PasswordFile, opts.DataDir, opts.WebDir)
		if err != nil {
			return err
		}
		info, err := os.Stat(file)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
			return errors.New("invalid web password file")
		}
		parent, err := os.OpenRoot(filepath.Dir(file))
		if err != nil {
			return errors.New("cannot read web password file")
		}
		data, _, readErr := webReadFile(webPath{parent, filepath.Base(file)}, 4096)
		parent.Close()
		if readErr != nil {
			return errors.New("cannot read web password file")
		}
		password = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	}
	if password == "" {
		return errors.New("web password is required")
	}
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) > 256 || strings.ContainsAny(password, "\r\n") {
		return errors.New("web bootstrap password must contain 1 to 256 valid Unicode characters without line breaks")
	}
	created, err := webNewCredentials(username, password, s.derivePassword)
	if err != nil {
		return err
	}
	temporary, err := s.prepareCredentials(created.record)
	if err != nil {
		return errors.New("cannot initialize web credential store")
	}
	defer s.authRoot.Remove(temporary)
	// Publishing a new store must not replace a constructor that won the initialization race.
	if err := s.authRoot.Link(temporary, s.authName); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return errors.New("cannot initialize web credential store")
		}
		stored, err := webLoadCredentials(s.authRoot, s.authName)
		if err != nil {
			return errors.New("invalid or unreadable web credential store")
		}
		s.auth = stored
		return nil
	}
	s.auth = created
	if err := s.syncAuthDirectory(s.authRoot); err != nil {
		return errors.New("web credential store published but directory synchronization failed")
	}
	return nil
}

func (s *WebServer) takeAuthAttempt(credentials bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	window, attempts := &s.loginWindow, &s.loginAttempts
	limit := 20
	if credentials {
		window, attempts = &s.credentialsWindow, &s.credentialsAttempts
		limit = 10
	}
	if s.now().Sub(*window) >= time.Minute {
		*window = s.now()
		*attempts = 0
	}
	if *attempts >= limit {
		return false
	}
	*attempts++
	return true
}

func (s *WebServer) passwordMatches(state *webAuthState, password string) bool {
	return subtle.ConstantTimeCompare(s.derivePassword(password, state.salt), state.hash) == 1
}

func (s *WebServer) login(w http.ResponseWriter, r *http.Request) {
	if !s.takeAuthAttempt(false) {
		http.Error(w, "unauthorized", 401)
		return
	}
	var credentials struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 8193))
	if decoder.Decode(&credentials) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "unauthorized", 401)
		return
	}
	select {
	case s.authWork <- struct{}{}:
		defer func() { <-s.authWork }()
	default:
		http.Error(w, "unauthorized", 401)
		return
	}
	s.mu.Lock()
	snapshot := s.auth
	s.mu.Unlock()
	passwordOK := s.passwordMatches(snapshot, credentials.Password)
	user := sha256.Sum256([]byte(credentials.Username))
	expected := sha256.Sum256([]byte(snapshot.record.Username))
	if subtle.ConstantTimeCompare(user[:], expected[:]) != 1 || !passwordOK {
		http.Error(w, "unauthorized", 401)
		return
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		http.Error(w, "authentication unavailable", 503)
		return
	}
	value := base64.RawURLEncoding.EncodeToString(token)
	key := sha256.Sum256([]byte(value))
	status := s.commitLogin(r, snapshot, key)
	if status != 200 {
		message := "unauthorized"
		if status == 503 {
			message = "authentication unavailable"
		}
		http.Error(w, message, status)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: webSessionCookie, Value: value, Path: "/", MaxAge: int(webSessionTTL.Seconds()), HttpOnly: true, Secure: s.opts.SecureCookie, SameSite: http.SameSiteStrictMode})
	webJSON(w, map[string]bool{"ok": true})
}

func (s *WebServer) commitLogin(r *http.Request, snapshot *webAuthState, key [32]byte) int {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	s.mu.Lock()
	s.purgeLocked()
	if s.auth != snapshot {
		s.mu.Unlock()
		return 401
	}
	if len(s.sessions) >= webMaxSessions {
		s.mu.Unlock()
		return 503
	}
	if old, err := r.Cookie(webSessionCookie); err == nil {
		s.revokeLocked(sha256.Sum256([]byte(old.Value)))
	}
	s.sessions[key] = &webSession{expires: s.now().Add(webSessionTTL), clients: make(map[*webClient]struct{})}
	s.mu.Unlock()
	return 200
}

func webAuthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	webJSON(w, map[string]string{"error": code})
}

func (s *WebServer) updateCredentials(w http.ResponseWriter, r *http.Request, key [32]byte, session *webSession) {
	if !s.takeAuthAttempt(true) {
		webAuthError(w, 429, "rate_limited")
		return
	}
	var params webCredentialChange
	decoder := json.NewDecoder(io.LimitReader(r.Body, 8193))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&params) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		webAuthError(w, 400, "invalid_credentials")
		return
	}
	if strings.ContainsAny(params.Username, "\r\n") {
		webAuthError(w, 400, "invalid_credentials")
		return
	}
	params.Username = strings.TrimSpace(params.Username)
	if !webValidUsername(params.Username) || !utf8.ValidString(params.NewPassword) || strings.ContainsAny(params.NewPassword, "\r\n") || (params.NewPassword != "" && (utf8.RuneCountInString(params.NewPassword) < 8 || utf8.RuneCountInString(params.NewPassword) > 256 || strings.TrimSpace(params.NewPassword) == "")) {
		webAuthError(w, 400, "invalid_credentials")
		return
	}
	select {
	case s.authWork <- struct{}{}:
		defer func() { <-s.authWork }()
	default:
		webAuthError(w, 429, "rate_limited")
		return
	}
	status, code := s.changeCredentials(params, key, session)
	if status != 200 {
		if status == 401 {
			http.Error(w, "unauthorized", 401)
		} else {
			webAuthError(w, status, code)
		}
		return
	}
	http.SetCookie(w, &http.Cookie{Name: webSessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.opts.SecureCookie, SameSite: http.SameSiteStrictMode})
	result := map[string]any{"ok": true}
	if code != "" {
		result["warning"] = code
	}
	webJSON(w, result)
}

type webCredentialChange struct {
	CurrentPassword string `json:"currentPassword"`
	Username        string `json:"username"`
	NewPassword     string `json:"newPassword"`
}

func (s *WebServer) changeCredentials(params webCredentialChange, key [32]byte, session *webSession) (int, string) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	s.mu.Lock()
	s.purgeLocked()
	active := s.sessions[key] == session
	snapshot := s.auth
	s.mu.Unlock()
	if !active {
		return 401, ""
	}
	if !s.passwordMatches(snapshot, params.CurrentPassword) {
		return 403, "invalid_current_password"
	}
	next := &webAuthState{record: snapshot.record, salt: snapshot.salt, hash: snapshot.hash}
	next.record.Username = params.Username
	if params.NewPassword != "" {
		var err error
		next, err = webNewCredentials(params.Username, params.NewPassword, s.derivePassword)
		if err != nil {
			return 500, "credentials_save_failed"
		}
	}
	temporary, err := s.prepareCredentials(next.record)
	if err != nil {
		return 500, "credentials_save_failed"
	}
	defer s.authRoot.Remove(temporary)
	s.mu.Lock()
	s.purgeLocked()
	active = s.sessions[key] == session && s.auth == snapshot
	s.mu.Unlock()
	if !active {
		return 401, ""
	}
	if err := s.authRoot.Rename(temporary, s.authName); err != nil {
		return 500, "credentials_save_failed"
	}
	s.mu.Lock()
	s.auth = next
	for key := range s.sessions {
		s.revokeLocked(key)
	}
	s.mu.Unlock()
	if err := s.syncAuthDirectory(s.authRoot); err != nil {
		return 200, "durability_unconfirmed"
	}
	return 200, ""
}

func webSyncAuthDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
