//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
)

type webPath struct {
	root *os.Root
	name string
}

func (s *WebServer) virtualPath(value string, writable bool) (webPath, error) {
	if strings.ContainsAny(value, "\\\x00") || !strings.HasPrefix(value, "/") {
		return webPath{}, errors.New("invalid virtual path")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return webPath{}, errors.New("invalid virtual path")
		}
	}
	clean := path.Clean(value)
	base, name, _ := strings.Cut(strings.TrimPrefix(clean, "/"), "/")
	if name == "" {
		name = "."
	}
	var root *os.Root
	switch base {
	case "data":
		root = s.data
		if name == ".tmp" || strings.HasPrefix(name, ".tmp/") {
			return webPath{}, errors.New("invalid virtual path")
		}
	case "tmp":
		root = s.temporary
	default:
		return webPath{}, errors.New("invalid virtual path")
	}
	if writable && name == "." {
		return webPath{}, errors.New("cannot modify virtual root")
	}
	return webPath{root: root, name: name}, nil
}

func webReadFile(p webPath, maxSize int64) ([]byte, fs.FileInfo, error) {
	initial, err := p.root.Stat(p.name)
	if err != nil || !initial.Mode().IsRegular() {
		return nil, nil, errors.New("not a regular file")
	}
	f, err := p.root.Open(p.name)
	if err != nil {
		return nil, nil, errors.New("file unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, errors.New("not a regular file")
	}
	if info.Size() > maxSize {
		return nil, nil, errors.New("file too large")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil || int64(len(data)) > maxSize {
		return nil, nil, errors.New("file too large or unreadable")
	}
	return data, info, nil
}

func webWriteFile(p webPath, data []byte, appendMode bool) (fs.FileInfo, error) {
	if info, err := p.root.Stat(p.name); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := p.root.MkdirAll(path.Dir(p.name), 0o700); err != nil {
		return nil, err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if appendMode {
		flags = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	}
	f, err := p.root.OpenFile(p.name, flags, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	info, err := f.Stat()
	return info, errors.Join(err, f.Close())
}

type webVFSEntry struct {
	Path          string `json:"path"`
	IsDir         bool   `json:"isDir"`
	Size          int64  `json:"size"`
	MtimeMs       int64  `json:"mtimeMs"`
	ContentBase64 string `json:"contentBase64,omitempty"`
}

type webNetInterface struct {
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
}

func webInterfaces() []webNetInterface {
	result := []webNetInterface{}
	interfaces, _ := net.Interfaces()
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := iface.Addrs()
		ips := []string{}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				ips = append(ips, ip.String())
			}
		}
		if len(ips) > 0 {
			result = append(result, webNetInterface{Name: iface.Name, Addresses: ips})
		}
	}
	return result
}

func webInline(name string, size int64) bool {
	lower := strings.ToLower(name)
	return size <= 2<<20 && !strings.HasSuffix(lower, ".metadb") && !strings.HasSuffix(lower, ".mmdb") && !strings.HasSuffix(lower, ".dat")
}

func (s *WebServer) snapshot(w http.ResponseWriter) {
	entries := []webVFSEntry{}
	var bytes int
	err := fs.WalkDir(s.data.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("snapshot unavailable")
		}
		if name == ".tmp" || name == "responses" {
			return fs.SkipDir
		}
		if len(entries) >= 8192 {
			return errors.New("snapshot too large")
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		vpath := "/data"
		if name != "." {
			vpath += "/" + name
		}
		item := webVFSEntry{Path: vpath, IsDir: info.IsDir(), Size: info.Size(), MtimeMs: info.ModTime().UnixMilli()}
		if info.Mode().IsRegular() && webInline(name, info.Size()) {
			data, _, err := webReadFile(webPath{s.data, name}, 2<<20)
			if err != nil {
				return err
			}
			bytes += len(data)
			if bytes > 16<<20 {
				return errors.New("snapshot too large")
			}
			item.ContentBase64 = base64.StdEncoding.EncodeToString(data)
		}
		entries = append(entries, item)
		return nil
	})
	if err != nil {
		http.Error(w, "snapshot unavailable or too large", http.StatusRequestEntityTooLarge)
		return
	}
	s.mu.Lock()
	start := s.startTimeMs
	s.mu.Unlock()
	if !isRunning.Load() {
		start = 0
	}
	webJSON(w, map[string]any{"dataDir": "/data", "isListening": isRunning.Load(), "startTimeMs": start, "networkInterfaces": webInterfaces(), "entries": entries})
}

func (s *WebServer) serveFS(w http.ResponseWriter, r *http.Request) {
	operation := strings.TrimPrefix(r.URL.Path, "/api/fs/")
	methods := map[string][]string{
		"snapshot": {http.MethodGet}, "interfaces": {http.MethodGet}, "stat": {http.MethodGet}, "read": {http.MethodGet},
		"write": {http.MethodPost, http.MethodPut}, "mkdir": {http.MethodPost}, "delete": {http.MethodPost, http.MethodDelete}, "rename": {http.MethodPost}, "copy": {http.MethodPost},
	}
	allowed, ok := methods[operation]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !webMethod(w, r, allowed...) {
		return
	}
	if operation == "write" || operation == "rename" || operation == "copy" {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "request too large or unreadable", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
	}
	buffer := &webFSResponse{header: make(http.Header)}
	s.ioMu.Lock()
	s.serveFSLocked(buffer, r, operation)
	s.ioMu.Unlock()
	for key, values := range buffer.header {
		w.Header()[key] = values
	}
	status := buffer.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(buffer.body.Bytes())
}

type webFSResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *webFSResponse) Header() http.Header { return w.header }
func (w *webFSResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *webFSResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(data)
}

func (s *WebServer) serveFSLocked(w http.ResponseWriter, r *http.Request, operation string) {
	if operation == "snapshot" {
		s.snapshot(w)
		return
	}
	if operation == "interfaces" {
		webJSON(w, map[string]any{"networkInterfaces": webInterfaces()})
		return
	}
	if operation == "rename" || operation == "copy" {
		var params struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if json.NewDecoder(r.Body).Decode(&params) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		from, err := s.virtualPath(params.From, operation == "rename")
		if err != nil {
			http.Error(w, "invalid virtual path", 400)
			return
		}
		to, err := s.virtualPath(params.To, true)
		if err != nil {
			http.Error(w, "invalid virtual path", 400)
			return
		}
		if err := to.root.MkdirAll(path.Dir(to.name), 0o700); err != nil {
			http.Error(w, "destination unavailable", 400)
			return
		}
		if operation == "rename" && from.root == to.root {
			if err := from.root.Rename(from.name, to.name); err != nil {
				http.Error(w, "rename failed", 400)
				return
			}
			webJSON(w, map[string]bool{"ok": true})
			return
		}
		data, _, err := webReadFile(from, webMaxBody)
		if err != nil {
			http.Error(w, "source unavailable", 400)
			return
		}
		info, err := webWriteFile(to, data, false)
		if err != nil {
			http.Error(w, "copy failed", 400)
			return
		}
		if operation == "rename" {
			if err := from.root.Remove(from.name); err != nil {
				http.Error(w, "rename failed", 400)
				return
			}
		}
		webJSON(w, map[string]any{"ok": true, "size": info.Size(), "mtimeMs": info.ModTime().UnixMilli()})
		return
	}
	p, err := s.virtualPath(r.URL.Query().Get("path"), operation != "read" && operation != "stat" && operation != "mkdir")
	if err != nil {
		http.Error(w, "invalid virtual path", 400)
		return
	}
	switch operation {
	case "stat":
		info, err := p.root.Stat(p.name)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				webJSON(w, map[string]bool{"exists": false})
			} else {
				http.Error(w, "path unavailable", 400)
			}
			return
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			http.Error(w, "path unavailable", 400)
			return
		}
		webJSON(w, map[string]any{"exists": true, "isDir": info.IsDir(), "size": info.Size(), "mtimeMs": info.ModTime().UnixMilli()})
	case "read":
		data, info, err := webReadFile(p, webMaxBody)
		if err != nil {
			http.Error(w, "file unavailable", 400)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-File-Mtime", fmt.Sprint(info.ModTime().UnixMilli()))
		_, _ = w.Write(data)
	case "write":
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "request too large or unreadable", 413)
			return
		}
		info, err := webWriteFile(p, data, r.URL.Query().Get("mode") == "append")
		if err != nil {
			http.Error(w, "write failed", 400)
			return
		}
		webJSON(w, map[string]any{"ok": true, "size": info.Size(), "mtimeMs": info.ModTime().UnixMilli()})
	case "mkdir":
		if err := p.root.MkdirAll(p.name, 0o700); err != nil {
			http.Error(w, "mkdir failed", 400)
			return
		}
		webJSON(w, map[string]bool{"ok": true})
	case "delete":
		if err := p.root.RemoveAll(p.name); err != nil {
			http.Error(w, "delete failed", 400)
			return
		}
		webJSON(w, map[string]bool{"ok": true})
	}
}

func (s *WebServer) seedGeoAssets() {
	for _, name := range []string{"GEOIP.metadb", "ASN.mmdb", "GEOIP.dat", "GEOSITE.dat"} {
		if info, err := s.data.Stat(name); err == nil && info.Size() > 0 {
			continue
		}
		var sources []fs.FS
		sources = append(sources, os.DirFS("/usr/share/flclash"))
		if s.static != nil {
			sources = append(sources, s.static)
		}
		seeded := false
		for _, source := range sources {
			for _, candidate := range []string{name, "assets/assets/data/" + name, "assets/data/" + name} {
				f, err := source.Open(candidate)
				if err != nil {
					continue
				}
				info, err := f.Stat()
				if err != nil || !info.Mode().IsRegular() || info.Size() > 128<<20 {
					f.Close()
					continue
				}
				data, err := io.ReadAll(io.LimitReader(f, (128<<20)+1))
				f.Close()
				if err != nil || len(data) == 0 || len(data) > 128<<20 {
					continue
				}
				if _, err := webWriteFile(webPath{s.data, name}, data, false); err == nil {
					seeded = true
					break
				}
			}
			if seeded {
				break
			}
		}
	}
}
