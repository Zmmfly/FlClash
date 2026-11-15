//go:build webpanel && !android

package core

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"time"
)

type webServerAPI struct {
	store     *WebAPIStore
	runtime   *WebAPIRuntime
	evaluator *WebAPIEvaluator
	custom    *WebAPICustomSourceService
	dav       *WebAPIDAVService
	scheduler *WebAPIScheduler
	authority *webAPIServiceAuthority
	handler   http.Handler
}

func (s *WebServer) ensureAPI() (result *webServerAPI, err error) {
	s.apiMu.Lock()
	defer s.apiMu.Unlock()
	if s.apiClosed {
		return nil, webAPIError(503, "server_closed", "The API server is closed")
	}
	if s.api != nil {
		return s.api, nil
	}
	managed := []string{}
	if s.opts.WebDir != "" {
		managed = append(managed, s.opts.WebDir)
	}
	var runtime *WebAPIRuntime
	var evaluator *WebAPIEvaluator
	var custom *WebAPICustomSourceService
	var dav *WebAPIDAVService
	var scheduler *WebAPIScheduler
	var authority *webAPIServiceAuthority
	var store *WebAPIStore
	complete := false
	defer func() {
		if !complete {
			if custom != nil {
				custom.Close()
			}
			if scheduler != nil {
				scheduler.Close()
			}
			if dav != nil {
				dav.Close()
			}
			if authority != nil {
				authority.close()
			}
			if evaluator != nil {
				evaluator.Close()
			}
			if runtime != nil {
				runtime.Close()
			}
			if store != nil {
				store.Close()
			}
		}
		if recover() != nil {
			result = nil
			err = errors.New("cannot initialize the private API services")
		}
	}()
	store, err = OpenWebAPIStore(WebAPIStoreOptions{
		DataDir: s.opts.DataDir, StoreDir: s.opts.APIStoreDir, ManagedRoots: managed, Now: s.now,
		OnCommit: func(commit WebAPICommit) {
			if runtime != nil {
				runtime.OnCommit(commit)
			}
		},
	})
	if err != nil {
		return nil, err
	}
	for _, privateFile := range []string{s.opts.AuthFile, s.opts.PasswordFile} {
		if privateFile != "" && webWithin(store.root.Name(), privateFile) {
			store.Close()
			return nil, errors.New("private store must not contain authentication files")
		}
	}
	backend := DefaultWebAPIRuntimeBackend(s.opts.SafeMode)
	if s.opts.RuntimeBackend != nil {
		backend = *s.opts.RuntimeBackend
	}
	fetchOptions := s.opts.SourceFetchOptions
	if fetchOptions.Now == nil {
		fetchOptions.Now = s.now
	}
	if s.opts.SafeMode {
		if fetchOptions.LookupNetIP == nil {
			fetchOptions.LookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
				return nil, webAPIError(409, "safe_mode", "Remote resolution is disabled by the server safety policy")
			}
		}
		if fetchOptions.DialContext == nil {
			fetchOptions.DialContext = func(context.Context, string, string) (net.Conn, error) {
				return nil, webAPIError(409, "safe_mode", "Remote access is disabled by the server safety policy")
			}
		}
	}
	if fetchOptions.ResolveRuntimeSourceIdentity == nil {
		fetchOptions.ResolveRuntimeSourceIdentity = func(ctx context.Context, tx *WebAPIReadTx, key WebAPISourceResource) (WebAPICommittedSource, error) {
			if runtime == nil {
				return WebAPICommittedSource{}, webAPIError(503, "runtime_source_unavailable", "Runtime source authority is unavailable")
			}
			return runtime.ResolveRuntimeSourceIdentity(ctx, tx, key)
		}
	}
	stateful, err := NewWebAPIStatefulManager(s.opts.DataDir)
	if err != nil {
		return nil, err
	}
	runtime, err = NewWebAPIRuntimeWithOptions(store, backend, WebAPIRuntimeOptions{WorkspaceRoot: s.opts.DataDir, SourceFetchOptions: fetchOptions, StatefulManager: stateful})
	if err != nil {
		stateful.Close()
		return nil, err
	}
	mux := http.NewServeMux()
	RegisterWebAPIResourceRoutes(mux, store)
	RegisterWebAPICatalogRoutesWithOptions(mux, store, WebAPICatalogOptions{SourceFetchOptions: fetchOptions, Now: s.now})
	RegisterWebAPISettingsRoutes(mux, store)
	content := RegisterWebAPIContentRoutes(mux, store, WebAPIContentOptions{SourceOptions: fetchOptions, Now: s.now})
	custom = NewWebAPICustomSourceService(store, WebAPICustomSourceOptions{SourceOptions: fetchOptions, Now: s.now})
	content.CustomSources = custom
	custom.RegisterRoutes(mux)
	RegisterWebAPISourceRoutes(mux, store, WebAPISourceOptions{
		WebAPISourceFetchOptions:     fetchOptions,
		ResolveRuntimeSource:         runtime.ResolveRuntimeSource,
		ResolveRuntimeSourceIdentity: runtime.ResolveRuntimeSourceIdentity,
		ResolveImportJobSource:       content.PendingSource,
		ResolveCustomImportJobSource: custom.PendingSource,
		ResolveUtilitySource:         ResolveWebAPIUtilitySource,
	})
	RegisterWebAPIUtilityRoutes(mux, store, WebAPIUtilityOptions{SourceOptions: fetchOptions, Now: s.now, RemoteEnabled: !s.opts.SafeMode})
	evaluator, err = RegisterWebAPIEvaluateRoutes(mux, store)
	if err != nil {
		return nil, err
	}
	backup := RegisterWebAPIBackupRoutes(mux, store, WebAPIBackupOptions{Now: s.now})
	authority = newWebAPIServiceAuthority()
	dav, err = RegisterWebAPIDAVRoutes(mux, store, backup, WebAPIDAVOptions{
		StatusContext: authority.ctx, LookupNetIP: fetchOptions.LookupNetIP,
		DialContext: fetchOptions.DialContext, TLSRootCAs: fetchOptions.TLSRootCAs, Now: s.now,
	})
	if err != nil {
		return nil, err
	}
	scheduler, err = NewWebAPIScheduler(store, WebAPISchedulerOptions{
		InternalContext: authority.ctx, SafeMode: s.opts.SafeMode,
		Backend: WebAPISchedulerServiceBackend(content, runtime, fetchOptions),
	})
	if err != nil {
		return nil, err
	}
	RegisterWebAPISchedulerRoutes(mux, scheduler)
	if err = scheduler.Start(); err != nil {
		return nil, err
	}
	mux.Handle("/api/v1/runtime/", runtime)
	mux.Handle("/api/v1/runtime:apply", runtime)
	mux.Handle("/api/v1/runtime:reconcile", runtime)
	mux.Handle("/api/v1/profiles/", runtime)
	mux.Handle("/api/v1/profiles:build", runtime)
	s.api = &webServerAPI{store: store, runtime: runtime, evaluator: evaluator, custom: custom, dav: dav, scheduler: scheduler, authority: authority, handler: WebAPIRuntimeSessionHandler(s, mux)}
	complete = true
	return s.api, nil
}

func (s *WebServer) closeAPI() error {
	s.apiMu.Lock()
	s.apiClosed = true
	api := s.api
	s.api = nil
	s.apiMu.Unlock()
	if api == nil {
		return nil
	}
	api.custom.Close()
	api.scheduler.Close()
	davError := api.dav.Close()
	api.authority.close()
	api.runtime.Close()
	return errors.Join(davError, api.evaluator.Close(), api.store.Close())
}

func (s *WebServer) serveAPI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/ws/v1/events" {
		api, err := s.ensureAPI()
		if err != nil {
			WebAPIWriteError(w, webAPIError(503, "api_unavailable", "The private API store is unavailable"))
			return
		}
		api.runtime.ServeEvents(s, w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Origin") == "" {
		WebAPIWriteError(w, webAPIError(403, "origin_required", "A same-origin browser request is required"))
		return
	}
	release, err := s.acquireAPIAdmission(r.Context(), webAPIControlRequest(r))
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	defer release()
	api, err := s.ensureAPI()
	if err != nil {
		WebAPIWriteError(w, webAPIError(503, "api_unavailable", "The private API store is unavailable"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	api.handler.ServeHTTP(w, r.WithContext(ctx))
}

func webAPIRequestBodyLimit(path string) int64 {
	switch path {
	case "/api/v1/scripts:preview":
		return WebAPIEvaluatePreviewBodyLimit
	case "/api/v1/profiles:validate", "/api/v1/profiles:upload-asset", "/api/v1/profiles:create", "/api/v1/profiles:update", "/api/v1/content:prepare-write", "/api/v1/content:write", "/api/v1/content:create", "/api/v1/profiles:import", "/api/v1/profiles:import-custom", "/api/v1/backups:restore", "/api/v1/backups:prepare", "/api/v1/mitm-modules:attach":
		return WebAPIMaxContentJSONBytes
	}
	if strings.HasPrefix(path, "/api/v1/runtime") {
		return WebAPIRuntimeBodyLimit(path)
	}
	return WebAPIMaxBulkJSONBytes
}

func (s *WebServer) initializeAPICore() error {
	if err := s.checkCoreTree(); err != nil {
		return errors.New("runtime resources are unavailable")
	}
	if err := handleInit(&InitParams{HomeDir: filepath.Clean(s.opts.DataDir)}); err != nil {
		return errors.New("cannot initialize the server runtime")
	}
	return nil
}
