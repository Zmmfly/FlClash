//go:build webpanel && !android

package core

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/metacubex/mihomo/component/age"
	"go.yaml.in/yaml/v3"
)

func (s *WebServer) corePath(value string, relative bool) (string, error) {
	if strings.HasPrefix(value, s.opts.DataDir+string(filepath.Separator)) {
		value = "/data/" + filepath.ToSlash(strings.TrimPrefix(value, s.opts.DataDir+string(filepath.Separator)))
	}
	if relative && !strings.HasPrefix(value, "/") {
		value = "/data/" + value
	}
	p, err := s.virtualPath(value, false)
	if err != nil {
		return "", err
	}
	prefix := ""
	for _, part := range strings.Split(p.name, "/") {
		prefix = path.Join(prefix, part)
		info, err := p.root.Lstat(prefix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&fs.ModeSymlink != 0 {
			return "", errors.New("core paths must not contain symbolic links")
		}
	}
	return filepath.Join(p.root.Name(), filepath.FromSlash(p.name)), nil
}

func (s *WebServer) checkCoreTree() error {
	count := 0
	return fs.WalkDir(s.data.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		count++
		if err != nil || count > 32768 {
			return errors.New("core data tree unavailable or too large")
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return errors.New("core data tree must not contain symbolic links")
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return errors.New("core data tree contains a non-regular file")
		}
		return nil
	})
}

func (s *WebServer) readRPCPath(value string) ([]byte, error) {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	p, err := s.virtualPath(value, false)
	if err != nil {
		return nil, err
	}
	data, _, err := webReadFile(p, webMaxBody)
	return data, err
}

func (s *WebServer) withRPCFile(value string, run func(string) (any, error)) (any, error) {
	data, err := s.readRPCPath(value)
	if err != nil {
		return nil, err
	}
	return webWithBuffer(data, run)
}

func webWithBuffer(data []byte, run func(string) (any, error)) (any, error) {
	dir, err := os.MkdirTemp("", "flclash-web-rpc-")
	if err != nil {
		return nil, errors.New("RPC temporary file unavailable")
	}
	defer os.RemoveAll(dir)
	filename := filepath.Join(dir, "profile.yaml")
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		return nil, errors.New("RPC temporary file unavailable")
	}
	return run(filename)
}

func (s *WebServer) rewriteWebResponse(data []byte) []byte {
	tmp, _ := json.Marshal(filepath.ToSlash(s.temporary.Name()))
	root, _ := json.Marshal(filepath.ToSlash(s.opts.DataDir))
	if len(tmp) > 2 {
		data = bytes.ReplaceAll(data, tmp[1:len(tmp)-1], []byte("/tmp"))
	}
	if len(root) > 2 {
		data = bytes.ReplaceAll(data, root[1:len(root)-1], []byte("/data"))
	}
	return data
}

func webRPCError(id, code, message string) []byte {
	return encodeResponse(MethodResponse{ID: id, Error: &MethodError{Code: code, Message: message}})
}

func (s *WebServer) webRPC(call *MethodCall) (response []byte) {
	defer func() {
		if recover() != nil {
			response = webRPCError(call.ID, "internal_error", "web RPC failed")
		}
	}()
	result, err := s.runWebRPC(call)
	if err != nil {
		if isDependencyPolicyError(err) {
			return encodeResponse(MethodResponse{ID: call.ID, Error: asMethodError(err)})
		}
		var methodErr *MethodError
		if errors.As(err, &methodErr) {
			return encodeResponse(MethodResponse{ID: call.ID, Error: methodErr})
		}
		return webRPCError(call.ID, "invalid_arguments", err.Error())
	}
	return s.rewriteWebResponse(encodeResponse(MethodResponse{ID: call.ID, Result: result}))
}

func (s *WebServer) runWebRPC(call *MethodCall) (any, error) {
	switch call.Method {
	case "core.shutdown", "shutdown":
		return true, nil
	case coreCrashMethod:
		return nil, &MethodError{Code: "unsupported", Message: "core.crash is unavailable in web mode"}
	case configReadMethod, configValidateMethod, configDecryptMethod, profileBaseMethod, profileOutboundsMethod, ruleSetDumpMethod, proxyConvertMethod:
		var params DecryptConfigParams
		if err := call.decodeArguments(&params); err != nil {
			return nil, errors.New("invalid file arguments")
		}
		if call.Method == configDecryptMethod {
			data, err := s.readRPCPath(params.Path)
			if err != nil {
				return nil, err
			}
			decrypted, err := age.DecryptBytes(data, params.AgeSecretKey)
			return string(decrypted), err
		}
		return s.withRPCFile(params.Path, func(filename string) (any, error) {
			p := PathParams{Path: filename}
			switch call.Method {
			case configReadMethod:
				return handleReadConfig(p)
			case configValidateMethod:
				return handleValidateConfig(p), nil
			case profileBaseMethod:
				return handleGetProfileBase(p)
			case ruleSetDumpMethod:
				return handleDumpRuleSet(p)
			case proxyConvertMethod:
				return handleConvertProxies(p)
			default:
				return handleGetProfileOutbounds(p)
			}
		})
	case profileBuildMethod:
		var base SetupBase
		if call.decodeArguments(&base) != nil {
			return nil, errors.New("invalid profile arguments")
		}
		return s.withRPCFile(base.Path, func(filename string) (any, error) {
			base.Path = filename
			document, err := mergeProfileBase(&base)
			if err != nil {
				if isDependencyPolicyError(err) {
					return nil, err
				}
				return nil, errors.New("cannot build profile")
			}
			data, err := yaml.Marshal(document)
			if err != nil || len(data) > webMaxBody {
				return nil, errors.New("profile too large or invalid")
			}
			if _, err := s.sanitizeConfig(data); err != nil {
				return nil, err
			}
			token := make([]byte, 16)
			if _, err := rand.Read(token); err != nil {
				return nil, err
			}
			name := hex.EncodeToString(token) + ".yaml"
			s.ioMu.Lock()
			_, err = webWriteFile(webPath{s.temporary, name}, data, false)
			s.ioMu.Unlock()
			return "/tmp/" + name, err
		})
	}
	if !s.coreEnabled.Load() {
		return nil, &MethodError{Code: "unavailable", Message: "web core is not attached"}
	}
	if call.Method == coreInitMethod {
		s.ioMu.Lock()
		defer s.ioMu.Unlock()
		var params InitParams
		if call.decodeArguments(&params) != nil || params.HomeDir != "/data" {
			return nil, errors.New("core home-dir must be /data")
		}
		if err := s.checkCoreTree(); err != nil {
			return nil, err
		}
		if isInit.Load() {
			return nil, nil
		}
		params.HomeDir = s.opts.DataDir
		return nil, handleInit(&params)
	}
	if call.Method == coreInitializedMethod {
		return handleInitialized(), nil
	}
	if !isInit.Load() {
		return nil, errNotInitialized
	}
	if call.Method == configSetupMethod {
		var params SetupParams
		if call.decodeArguments(&params) != nil {
			return nil, errors.New("invalid setup arguments")
		}
		if params.Base != nil && params.ConfigPath != "" {
			return nil, dependencyPolicy("invalid_setup_source", "base", "config-path")
		}
		var data []byte
		var err error
		if params.Base != nil {
			_, err = s.withRPCFile(params.Base.Path, func(filename string) (any, error) {
				base := *params.Base
				base.Path = filename
				document, err := mergeProfileBase(&base)
				if err != nil {
					return nil, err
				}
				data, err = yaml.Marshal(document)
				return nil, err
			})
		} else if params.ConfigPath != "" {
			data, err = s.readRPCPath(params.ConfigPath)
		} else {
			data, err = s.readRPCPath("/data/config.yaml")
		}
		if err != nil {
			if isDependencyPolicyError(err) {
				return nil, err
			}
			return nil, errors.New("setup profile unavailable or invalid")
		}
		data, err = s.sanitizeConfig(data)
		if err != nil {
			return nil, err
		}
		if _, err := s.corePath("/data/config.yaml", false); err != nil {
			return nil, err
		}
		return webWithBuffer(data, func(filename string) (any, error) {
			params.Base = nil
			params.ConfigPath = filename
			return nil, handleSetupConfig(&params)
		})
	}
	if call.Method == configUpdateMethod {
		var params UpdateParams
		if call.decodeArguments(&params) != nil {
			return nil, errors.New("invalid update arguments")
		}
		if params.ExternalController != nil && *params.ExternalController != "" {
			return nil, &MethodError{Code: "unsupported", Message: "external controller is unavailable in web mode"}
		}
	}
	if call.Method == providerSideLoadMethod {
		var params SideLoadParams
		if call.decodeArguments(&params) != nil {
			return nil, errors.New("invalid provider arguments")
		}
		data, err := s.sanitizeProviderData(params.Data)
		if err != nil {
			return nil, err
		}
		params.Data = data
		call.Arguments, _ = json.Marshal(params)
	}
	if call.Method == proxyValidateMethod {
		var value any
		if json.Unmarshal(call.Arguments, &value) != nil {
			return nil, errors.New("invalid arguments")
		}
		if err := s.checkConfigValue(value, ""); err != nil {
			return nil, err
		}
		call.Arguments, _ = json.Marshal(value)
	}
	if call.Method == ruleSetCompileMethod {
		var params CompileRuleSetParams
		if call.decodeArguments(&params) != nil {
			return nil, errors.New("invalid rule set arguments")
		}
		filename, ok := ruleSetCachePath(params.Name)
		if !ok {
			return nil, errors.New("invalid rule set name")
		}
		for _, suffix := range []string{"", ".mrs", ".tmp", ".mrs.tmp"} {
			if _, err := s.corePath(filename+suffix, false); err != nil {
				return nil, err
			}
		}
	}
	if call.Method == profileClearEffectMethod {
		var params ClearEffectParams
		if call.decodeArguments(&params) != nil {
			return nil, errors.New("invalid profile arguments")
		}
		_, filename := providerPaths(s.opts.DataDir, params.ProfileID)
		if _, err := s.corePath(filename, false); err != nil {
			return nil, err
		}
	}
	handler, ok := methodHandlers[call.Method]
	if !ok {
		return nil, &MethodError{Code: "not_implemented", Message: "unknown method"}
	}
	result, err := handler(call)
	if err == nil {
		s.mu.Lock()
		if call.Method == listenerStartMethod {
			s.startTimeMs = time.Now().UnixMilli()
		}
		if call.Method == listenerStopMethod {
			s.startTimeMs = 0
		}
		s.mu.Unlock()
	}
	return result, err
}

func (s *WebServer) sanitizeConfig(data []byte) ([]byte, error) {
	var config map[string]any
	if yaml.Unmarshal(data, &config) != nil {
		return nil, errors.New("invalid configuration")
	}
	for _, key := range []string{"external-controller", "external-controller-tls", "external-controller-unix", "external-controller-pipe", "external-ui", "external-ui-url"} {
		if value, ok := config[key]; ok && value != nil && value != "" {
			return nil, &MethodError{Code: "unsupported", Message: "external controller and external UI are unavailable in web mode"}
		}
	}
	if err := s.checkConfigValue(config, ""); err != nil {
		return nil, err
	}
	return yaml.Marshal(config)
}

func (s *WebServer) checkConfigValue(value any, context string) error {
	switch value := value.(type) {
	case []any:
		for _, item := range value {
			if err := s.checkConfigValue(item, context); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, item := range value {
			text, isText := item.(string)
			filePath := key == "state-dir" || key == "planet" || key == "config-file"
			if key == "path" && (strings.HasPrefix(context, "/proxy-providers/") || strings.HasPrefix(context, "/rule-providers/")) {
				filePath = true
			}
			if key == "certificate" || key == "private-key" || key == "ech-key" || key == "client-auth-cert" {
				kind, _ := value["type"].(string)
				filePath = isText && !strings.Contains(text, "-----BEGIN ") && !(key == "private-key" && (kind == "wireguard" || kind == "masque"))
			}
			if filePath && isText && text != "" {
				resolved, err := s.corePath(text, true)
				if err != nil {
					return errors.New("configuration file path must remain within virtual roots without symlinks")
				}
				value[key] = resolved
			}
			if err := s.checkConfigValue(item, context+"/"+key); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *WebServer) sanitizeProviderData(data string) (string, error) {
	var value map[string]any
	if yaml.Unmarshal([]byte(data), &value) != nil {
		return data, nil
	}
	_, proxies := value["proxies"]
	_, groups := value["proxy-groups"]
	if !proxies && !groups {
		return data, nil
	}
	if err := s.checkConfigValue(value, ""); err != nil {
		return "", err
	}
	rewritten, err := yaml.Marshal(value)
	return string(rewritten), err
}
