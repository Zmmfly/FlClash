//go:build webpanel && !android

package core

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	C "github.com/metacubex/mihomo/constant"
)

const webStatefulDirectory = ".api-proxy-state"
const webStatefulVersion = "v1"
const webStatefulTreeEntries = 65536
const webStatefulTreeDepth = 64

type WebAPIStatefulManager struct {
	mu      sync.Mutex
	dataDir string
	closed  bool
}

func NewWebAPIStatefulManager(dataDir string) (*WebAPIStatefulManager, error) {
	if !filepath.IsAbs(dataDir) || strings.ContainsRune(dataDir, 0) {
		return nil, webStatefulFileError()
	}
	return &WebAPIStatefulManager{dataDir: filepath.Clean(dataDir)}, nil
}

func webStatefulFileError() *WebAPIError {
	return webAPIError(503, "stateful_storage_unsafe", "private protocol state storage is unavailable or unsafe")
}

func webStatefulOpenChild(parent *os.Root, name string, create, private bool) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) && create {
		if err = parent.Mkdir(name, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		before, err = parent.Lstat(name)
	}
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || private && before.Mode().Perm() != 0o700 {
		return nil, webStatefulFileError()
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := child.Stat(".")
	current, currentErr := parent.Lstat(name)
	if err != nil || currentErr != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, after) || !os.SameFile(after, current) {
		child.Close()
		return nil, webStatefulFileError()
	}
	return child, nil
}

func webStatefulOpenData(path string) (*os.Root, error) {
	volume := filepath.VolumeName(path)
	root, err := os.OpenRoot(volume + string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(path[len(volume):], string(filepath.Separator)), string(filepath.Separator))
	for _, part := range parts {
		if part == "" {
			continue
		}
		next, err := webStatefulOpenChild(root, part, false, false)
		root.Close()
		if err != nil {
			return nil, err
		}
		root = next
	}
	info, err := root.Stat(".")
	if err != nil || info.Mode().Perm()&0o022 != 0 {
		root.Close()
		return nil, webStatefulFileError()
	}
	return root, nil
}

func webStatefulSingleLink(info os.FileInfo) bool {
	value := reflect.ValueOf(info.Sys())
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() || value.Elem().Kind() != reflect.Struct {
		return false
	}
	links := value.Elem().FieldByName("Nlink")
	return links.IsValid() && links.CanUint() && links.Uint() == 1
}

func webStatefulCheckTree(root *os.Root) error {
	type directory struct {
		root  *os.Root
		depth int
	}
	queue := []directory{{root, 0}}
	defer func() {
		for _, pending := range queue {
			if pending.root != root {
				pending.root.Close()
			}
		}
	}()
	remaining := webStatefulTreeEntries
	for len(queue) > 0 {
		current := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		err := func() error {
			if current.root != root {
				defer current.root.Close()
			}
			file, err := current.root.Open(".")
			if err != nil {
				return err
			}
			defer file.Close()
			for {
				names, readErr := file.Readdirnames(128)
				for _, name := range names {
					remaining--
					if remaining < 0 || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
						return webStatefulFileError()
					}
					info, err := current.root.Lstat(name)
					if err != nil || info.Mode()&os.ModeSymlink != 0 {
						return webStatefulFileError()
					}
					if info.IsDir() {
						if current.depth >= webStatefulTreeDepth {
							return webStatefulFileError()
						}
						child, err := webStatefulOpenChild(current.root, name, false, false)
						if err != nil {
							return err
						}
						queue = append(queue, directory{child, current.depth + 1})
						continue
					}
					if !info.Mode().IsRegular() || !webStatefulSingleLink(info) {
						return webStatefulFileError()
					}
				}
				if readErr == io.EOF {
					return nil
				}
				if readErr != nil {
					return readErr
				}
			}
		}()
		if err != nil {
			return err
		}
	}
	return nil
}

func (manager *WebAPIStatefulManager) Materialize(bound WebAPIStatefulBindings) ([]WebAPIStatefulMaterialized, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return nil, webAPIError(503, "stateful_storage_closed", "private protocol state manager is closed")
	}
	if !bound.committed {
		return nil, webAPIError(409, "stateful_intent_required", "state directories require a committed runtime binding")
	}
	if err := webStatefulValidateBindings(bound); err != nil {
		return nil, err
	}
	result := make([]WebAPIStatefulMaterialized, 0, len(bound.Bindings))
	if len(bound.Bindings) == 0 {
		return result, nil
	}
	base := filepath.Join(manager.dataDir, webStatefulDirectory, webStatefulVersion)
	for _, binding := range bound.Bindings {
		if !C.Path.IsSafePath(filepath.Join(base, binding.Identity)) {
			return nil, webStatefulFileError()
		}
	}
	data, err := webStatefulOpenData(manager.dataDir)
	if err != nil {
		return nil, webStatefulFileError()
	}
	defer data.Close()
	private, err := webStatefulOpenChild(data, webStatefulDirectory, true, true)
	if err != nil {
		return nil, webStatefulFileError()
	}
	defer private.Close()
	version, err := webStatefulOpenChild(private, webStatefulVersion, true, true)
	if err != nil {
		return nil, webStatefulFileError()
	}
	defer version.Close()
	for _, binding := range bound.Bindings {
		identity, err := webStatefulOpenChild(version, binding.Identity, true, true)
		if err != nil {
			return nil, webStatefulFileError()
		}
		err = webStatefulCheckTree(identity)
		identity.Close()
		if err != nil {
			return nil, webStatefulFileError()
		}
		result = append(result, WebAPIStatefulMaterialized{Resource: binding.Resource, StateDir: filepath.Join(base, binding.Identity)})
	}
	return result, nil
}

func (manager *WebAPIStatefulManager) Close() {
	manager.mu.Lock()
	manager.closed = true
	manager.mu.Unlock()
}
