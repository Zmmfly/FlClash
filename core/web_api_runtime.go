//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

type WebAPIRuntime struct {
	store        *WebAPIStore
	backend      WebAPIRuntimeBackend
	routes       map[string]webRuntimeHTTPRoute
	Events       *WebAPIEvents
	ctx          context.Context
	cancel       context.CancelFunc
	wake         chan struct{}
	done         chan struct{}
	closeOnce    sync.Once
	backendMu    sync.RWMutex
	workspace    webRuntimeWorkspace
	effect       webRuntimeEffectLifecycle
	phaseMu      sync.Mutex
	phaseChanged chan struct{}
	preparations chan struct{}
	providers    webRuntimeProviderRegistry
	applied      webRuntimeAppliedProvenance
}
type webRuntimeHTTPRoute struct {
	method string
	serve  http.HandlerFunc
}

func NewWebAPIRuntime(store *WebAPIStore, backend WebAPIRuntimeBackend) (*WebAPIRuntime, error) {
	if store == nil {
		return nil, errors.New("runtime requires the authoritative store")
	}
	var meta WebAPIStoreMeta
	if _, err := store.View(context.Background(), func(tx *WebAPIReadTx) error { var err error; meta, err = tx.Meta(); return err }); err != nil {
		return nil, err
	}
	needsReconcile, err := store.recoverRuntime()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	rt := &WebAPIRuntime{store: store, backend: backend, routes: map[string]webRuntimeHTTPRoute{}, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), phaseChanged: make(chan struct{}), preparations: make(chan struct{}, 2)}
	rt.effect.needsReconcile.Store(needsReconcile)
	rt.Events = NewWebAPIEvents(meta.StoreID, store, backend.Subscribe)
	rt.registerRoutes()
	rt.routes["/api/v1/runtime/applied-counts"] = webRuntimeHTTPRoute{http.MethodGet, rt.appliedCountsHTTP}
	rt.routes["/api/v1/runtime/external-providers"] = webRuntimeHTTPRoute{http.MethodGet, rt.externalProvidersHTTP}
	rt.routes["/api/v1/runtime/geo"] = webRuntimeHTTPRoute{http.MethodGet, rt.geoHTTP}
	rt.routes["/api/v1/runtime/geo:update"] = webRuntimeHTTPRoute{http.MethodPost, rt.geoUpdateHTTP}
	rt.routes["/api/v1/runtime/provider-sources"] = webRuntimeHTTPRoute{http.MethodGet, rt.providerSourcesHTTP}
	rt.routes["/api/v1/runtime/provider-sources:refresh"] = webRuntimeHTTPRoute{http.MethodPost, rt.refreshProviderHTTP}
	rt.routes["/api/v1/runtime:apply"] = webRuntimeHTTPRoute{http.MethodPost, rt.applyHTTP}
	rt.routes["/api/v1/runtime:reconcile"] = webRuntimeHTTPRoute{http.MethodPost, rt.reconcileHTTP}
	rt.routes["/api/v1/profiles:build"] = webRuntimeHTTPRoute{http.MethodPost, rt.buildHTTP}
	go rt.run()
	return rt, nil
}

func (rt *WebAPIRuntime) Close() {
	rt.closeOnce.Do(rt.closeEffects)
}

func (rt *WebAPIRuntime) OnCommit(commit WebAPICommit) {
	rt.Events.Commit(commit)
	rt.notifyPhaseChange()
	rt.invalidateProviderSources()
	select {
	case rt.wake <- struct{}{}:
	default:
	}
}

func (rt *WebAPIRuntime) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if WebAPIRequestSessionID(r.Context()) == "" {
		WebAPIWriteError(w, webAPIError(401, "unauthorized", "an authenticated API session is required"))
		return
	}
	select {
	case <-rt.ctx.Done():
		WebAPIWriteError(w, webAPIError(503, "runtime_closed", "runtime is closed"))
		return
	default:
	}
	defer func() {
		if recover() != nil {
			WebAPIWriteError(w, webAPIError(500, "runtime_failed", "runtime request failed"))
		}
	}()
	ctx, cancel := context.WithTimeout(r.Context(), 75*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(15 * time.Second))
	route, ok := rt.routes[r.URL.Path]
	if !ok && strings.HasPrefix(r.URL.Path, "/api/v1/runtime/provider-sources/") {
		route = webRuntimeHTTPRoute{r.Method, rt.providerContentRoute}
		ok = true
	}
	if !ok && strings.HasPrefix(r.URL.Path, "/api/v1/runtime/operations/") {
		route = webRuntimeHTTPRoute{http.MethodGet, rt.operation}
		ok = true
	}
	if !ok && strings.HasPrefix(r.URL.Path, "/api/v1/profiles/") {
		route = webRuntimeHTTPRoute{http.MethodGet, rt.profileDerivedHTTP}
		ok = true
	}
	if !ok {
		WebAPIWriteError(w, webAPIError(404, "not_found", "runtime resource or command does not exist"))
		return
	}
	if r.Method != route.method {
		w.Header().Set("Allow", route.method)
		WebAPIWriteError(w, webAPIError(405, "method_not_allowed", "incorrect method for this runtime resource"))
		return
	}
	if r.Method == http.MethodGet && r.ContentLength != 0 {
		WebAPIWriteError(w, webAPIError(400, "unexpected_body", "GET requests cannot contain a body"))
		return
	}
	if webRuntimeRequiresAdmission(r.URL.Path) {
		release, err := rt.acquirePreparation(r.Context())
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		defer release()
	}
	route.serve(w, r)
}

func (rt *WebAPIRuntime) get(path string, read func(context.Context, *http.Request) (any, error)) {
	rt.routes["/api/v1/runtime/"+path] = webRuntimeHTTPRoute{http.MethodGet, func(w http.ResponseWriter, r *http.Request) {
		data, err := func() (any, error) {
			if path != "state" && path != "listener" && path != "logs" && path != "network-interfaces" {
				if !rt.backendMu.TryRLock() {
					return nil, webAPIError(503, "runtime_busy", "a runtime effect is still in flight")
				}
				defer rt.backendMu.RUnlock()
			}
			return read(r.Context(), r)
		}()
		if err != nil {
			WebAPIWriteError(w, webRuntimeFailure(err))
			return
		}
		rt.answer(w, r, data)
	}}
}

func (rt *WebAPIRuntime) answer(w http.ResponseWriter, r *http.Request, data any) {
	rev, err := rt.store.View(r.Context(), func(*WebAPIReadTx) error { return nil })
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	release, err := WebAPIAcquireCommitAuthorization(r.Context())
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	release()
	WebAPIWriteSuccess(w, rev, data)
}

func webRuntimeQuery[P any](rt *WebAPIRuntime, path string, run func(context.Context, P) (any, error)) {
	rt.routes["/api/v1/runtime/"+path] = webRuntimeHTTPRoute{http.MethodPost, func(w http.ResponseWriter, r *http.Request) {
		if _, err := WebAPIQuery(r); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		if rt.backend.SafeMode && (path == "proxies:delay" || path == "route:check" || strings.HasPrefix(path, "probes/")) {
			WebAPIWriteError(w, webRuntimeSafeMode())
			return
		}
		var p P
		if err := WebAPIDecodeJSONLimit(w, r, &p, WebAPIRuntimeBodyLimit(r.URL.Path)); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		data, err := func() (any, error) {
			if !rt.backendMu.TryRLock() {
				return nil, webAPIError(503, "runtime_busy", "a runtime effect is still in flight")
			}
			defer rt.backendMu.RUnlock()
			return run(r.Context(), p)
		}()
		if err != nil {
			WebAPIWriteError(w, webRuntimeFailure(err))
			return
		}
		rt.answer(w, r, data)
	}}
}

func webRuntimeCommand[P any](rt *WebAPIRuntime, path string, action WebAPIRuntimeAction, available bool, mutation func(P) WebAPIMutation, validate func(P) error) {
	rt.routes["/api/v1/runtime/"+path] = webRuntimeHTTPRoute{http.MethodPost, func(w http.ResponseWriter, r *http.Request) {
		if _, err := WebAPIQuery(r); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		var p P
		if err := WebAPIDecodeJSONLimit(w, r, &p, WebAPIRuntimeBodyLimit(r.URL.Path)); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		m := mutation(p)
		if receipt, found, err := rt.preflightApply(r.Context(), m, "runtime/"+path, p); err != nil {
			WebAPIWriteError(w, err)
			return
		} else if found {
			WebAPIWriteReceipt(w, receipt)
			return
		}
		if rt.backend.SafeMode {
			WebAPIWriteError(w, webRuntimeSafeMode())
			return
		}
		if !available {
			WebAPIWriteError(w, webRuntimeUnavailable())
			return
		}
		if validate != nil {
			if err := validate(p); err != nil {
				WebAPIWriteError(w, err)
				return
			}
		}
		receipt, err := rt.store.Transaction(r.Context(), m, "runtime/"+path, p, func(tx *WebAPIWriteTx) (any, error) { return tx.RecordRuntimeIntent(m.OperationID, action, p) })
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		if !receipt.Replayed {
			select {
			case rt.wake <- struct{}{}:
			default:
			}
		}
		WebAPIWriteReceipt(w, receipt)
	}}
}

func WebAPIRuntimeBodyLimit(path string) int64 {
	if strings.HasPrefix(path, "/api/v1/runtime/provider-sources/") && (strings.HasSuffix(path, ":write") || strings.HasSuffix(path, ":sideload")) {
		return 48 << 20
	}
	switch path {
	case "/api/v1/runtime/proxies:convert", "/api/v1/runtime/proxies:validate", "/api/v1/runtime/share-links:encode", "/api/v1/runtime/share-links:decode", "/api/v1/runtime/filters:validate", "/api/v1/runtime:apply":
		return 48 << 20
	}
	return webAPIMaxJSONBytes
}

func webRuntimeSafeMode() *WebAPIError {
	return webAPIError(409, "safe_mode", "runtime effects are disabled by the immutable server safety policy")
}

func webRuntimeUnavailable() *WebAPIError {
	return webAPIError(501, "not_implemented", "this runtime capability is not available in the current core")
}
func webRuntimeNoQuery(r *http.Request) error { _, err := WebAPIQuery(r); return err }
func webRuntimeBool(q map[string]string, name string) (bool, error) {
	v, ok := q[name]
	if !ok {
		return false, nil
	}
	if v == "true" {
		return true, nil
	}
	if v == "false" {
		return false, nil
	}
	return false, webAPIError(400, "invalid_query", "boolean query parameters require true or false")
}
func webRuntimeSimpleMutation(p WebAPIMutation) WebAPIMutation { return p }

func (rt *WebAPIRuntime) registerRoutes() {
	b := rt.backend
	rt.get("state", func(ctx context.Context, r *http.Request) (any, error) {
		if err := webRuntimeNoQuery(r); err != nil {
			return nil, err
		}
		if b.State == nil {
			return nil, webRuntimeUnavailable()
		}
		state, err := b.State(ctx)
		state.MITM = b.MITM != nil
		return state, err
	})
	rt.get("network-interfaces", func(ctx context.Context, r *http.Request) (any, error) {
		if err := webRuntimeNoQuery(r); err != nil {
			return nil, err
		}
		if b.NetworkInterfaces == nil {
			return nil, webRuntimeUnavailable()
		}
		return b.NetworkInterfaces(ctx)
	})
	rt.get("memory", func(ctx context.Context, r *http.Request) (any, error) {
		if err := webRuntimeNoQuery(r); err != nil {
			return nil, err
		}
		if b.Memory == nil {
			return nil, webRuntimeUnavailable()
		}
		return b.Memory(ctx)
	})
	rt.get("proxies", func(ctx context.Context, r *http.Request) (any, error) {
		if err := webRuntimeNoQuery(r); err != nil {
			return nil, err
		}
		if b.Proxies == nil {
			return nil, webRuntimeUnavailable()
		}
		proxies, err := b.Proxies(ctx)
		if err != nil {
			return nil, err
		}
		if b.State == nil {
			return nil, webRuntimeUnavailable()
		}
		state, err := b.State(ctx)
		return WebAPIRuntimeProxySnapshot{ProxiesData: proxies, RuntimeGeneration: state.RuntimeGeneration, ProfileID: state.ProfileID}, err
	})
	rt.get("proxies/definition", func(ctx context.Context, r *http.Request) (any, error) {
		q, err := WebAPIQuery(r, "name")
		if err != nil {
			return nil, err
		}
		if err = webRuntimeName(q["name"], false); err != nil {
			return nil, err
		}
		return rt.proxyDefinition(ctx, q["name"])
	})
	rt.routes["/api/v1/runtime/proxies:select"] = webRuntimeHTTPRoute{http.MethodPost, rt.selectHTTP}
	webRuntimeQuery(rt, "proxies:delay", func(ctx context.Context, p WebAPIRuntimeDelayRequest) (any, error) {
		if err := webRuntimeName(p.ProxyName, false); err != nil {
			return nil, err
		}
		if err := webRuntimeTimeout(p.TimeoutMillis, 30000); err != nil {
			return nil, err
		}
		if err := webRuntimeURL(p.TestURL, true); err != nil {
			return nil, err
		}
		if b.Delay == nil {
			return nil, webRuntimeUnavailable()
		}
		return b.Delay(ctx, p)
	})
	webRuntimeQuery(rt, "proxies:validate", func(ctx context.Context, p ProxiesParams) (any, error) {
		if err := webRuntimeDefinitions(p.Proxies); err != nil {
			return nil, err
		}
		return webRuntimeValidateDefinitions(ctx, p)
	})
	webRuntimeQuery(rt, "proxies:convert", func(_ context.Context, p WebAPIRuntimeConvertRequest) (any, error) {
		return webRuntimeConvert(p.Content)
	})
	webRuntimeQuery(rt, "share-links:encode", func(_ context.Context, p ProxiesParams) (any, error) {
		if err := webRuntimeDefinitions(p.Proxies); err != nil {
			return nil, err
		}
		return handleEncodeShareLinks(p), nil
	})
	webRuntimeQuery(rt, "share-links:decode", func(_ context.Context, p LinesParams) (any, error) {
		if p.Lines == nil {
			return nil, webAPIError(400, "invalid_lines", "share-link lines are required")
		}
		out := handleDecodeShareLinks(p)
		for _, proxies := range out {
			if err := webRuntimeDefinitions(proxies); err != nil {
				return nil, err
			}
		}
		return out, nil
	})
	webRuntimeQuery(rt, "filters:validate", func(_ context.Context, p FiltersParams) (any, error) {
		if p.Filters == nil {
			return nil, webAPIError(400, "invalid_filters", "filter lines are required")
		}
		return handleValidateFilters(p), nil
	})
	rt.get("connections", func(ctx context.Context, r *http.Request) (any, error) {
		if err := webRuntimeNoQuery(r); err != nil {
			return nil, err
		}
		if b.Connections == nil {
			return nil, webRuntimeUnavailable()
		}
		return b.Connections(ctx)
	})
	rt.get("connections/count", func(ctx context.Context, r *http.Request) (any, error) {
		if err := webRuntimeNoQuery(r); err != nil {
			return nil, err
		}
		if b.ConnectionCount == nil {
			return nil, webRuntimeUnavailable()
		}
		return b.ConnectionCount(ctx)
	})
	webRuntimeCommand(rt, "connections:close", webRuntimeCloseConnection, b.CloseConnection != nil, func(p WebAPIRuntimeCloseRequest) WebAPIMutation { return p.WebAPIMutation }, func(p WebAPIRuntimeCloseRequest) error {
		if len(p.ID) != 36 || strings.ContainsAny(p.ID, "/\\\x00\r\n") {
			return webAPIError(400, "invalid_connection_id", "a connection UUID is required")
		}
		return nil
	})
	webRuntimeCommand(rt, "connections:close-all", webRuntimeCloseConnections, b.CloseConnections != nil, webRuntimeSimpleMutation, nil)
	webRuntimeCommand(rt, "connections:reset", webRuntimeResetConnections, b.ResetConnections != nil, webRuntimeSimpleMutation, nil)
	rt.get("traffic", func(ctx context.Context, r *http.Request) (any, error) {
		q, err := WebAPIQuery(r, "total", "onlyProxy")
		if err != nil {
			return nil, err
		}
		total, err := webRuntimeBool(q, "total")
		if err != nil {
			return nil, err
		}
		only, err := webRuntimeBool(q, "onlyProxy")
		if err != nil {
			return nil, err
		}
		if b.Traffic == nil {
			return nil, webRuntimeUnavailable()
		}
		return b.Traffic(ctx, total, only)
	})
	webRuntimeCommand(rt, "traffic:reset", webRuntimeResetTraffic, b.ResetTraffic != nil, webRuntimeSimpleMutation, nil)
	webRuntimeCommand(rt, "dns:clear-cache", webRuntimeClearDNS, b.ClearDNS != nil, webRuntimeSimpleMutation, nil)
	webRuntimeCommand(rt, "network:changed", webRuntimeNetworkChanged, b.NetworkChanged != nil, webRuntimeSimpleMutation, nil)
	rt.get("listener", func(ctx context.Context, r *http.Request) (any, error) {
		if err := webRuntimeNoQuery(r); err != nil {
			return nil, err
		}
		if b.State == nil {
			return nil, webRuntimeUnavailable()
		}
		state, err := b.State(ctx)
		return struct {
			Enabled  bool `json:"enabled"`
			SafeMode bool `json:"safeMode"`
		}{state.ListenerEnabled, state.SafeMode}, err
	})
	webRuntimeCommand(rt, "listener:set-enabled", webRuntimeListener, b.SetListener != nil, func(p WebAPIRuntimeListenerRequest) WebAPIMutation { return p.WebAPIMutation }, func(p WebAPIRuntimeListenerRequest) error {
		if p.Enabled == nil {
			return webAPIError(400, "invalid_listener_state", "enabled is required")
		}
		return nil
	})
	webRuntimeCommand(rt, "core:gc", webRuntimeGC, b.CollectMemory != nil, webRuntimeSimpleMutation, nil)
	rt.get("logs", func(_ context.Context, r *http.Request) (any, error) {
		if err := webRuntimeNoQuery(r); err != nil {
			return nil, err
		}
		return WebAPIRuntimeLogState{Subscribed: rt.Events.HasTopic("log")}, nil
	})
	rt.get("geo", func(ctx context.Context, r *http.Request) (any, error) {
		if err := webRuntimeNoQuery(r); err != nil {
			return nil, err
		}
		if b.GeoResources == nil {
			return nil, webRuntimeUnavailable()
		}
		return b.GeoResources(ctx)
	})
	rt.get("route", func(ctx context.Context, r *http.Request) (any, error) {
		if err := webRuntimeNoQuery(r); err != nil {
			return nil, err
		}
		if b.Route == nil {
			return nil, webRuntimeUnavailable()
		}
		return b.Route(ctx)
	})
	webRuntimeQuery(rt, "route:check", func(ctx context.Context, p CheckRoutesParams) (any, error) {
		if len(p.Routes) > 256 {
			return nil, webAPIError(400, "invalid_routes", "route check exceeds the batch limit")
		}
		hits := 0
		for _, route := range p.Routes {
			hits += len(route)
			for _, hit := range route {
				if err := webRuntimeName(hit.Host, false); err != nil {
					return nil, err
				}
				if err := webRuntimeName(hit.Proxy, true); err != nil {
					return nil, err
				}
			}
		}
		if hits > 1024 {
			return nil, webAPIError(400, "invalid_routes", "route check exceeds the hit limit")
		}
		if b.CheckRoutes == nil {
			return nil, webRuntimeUnavailable()
		}
		return b.CheckRoutes(ctx, p)
	})
	webRuntimeQuery(rt, "probes/outbound-ip", func(ctx context.Context, p WebAPIRuntimeOutboundIPRequest) (any, error) {
		if err := webRuntimeTimeout(p.TimeoutMillis, 10000); err != nil {
			return nil, err
		}
		if len(p.URLs) < 1 || len(p.URLs) > 4 {
			return nil, webAPIError(400, "invalid_sources", "one to four known IP sources are required")
		}
		for _, u := range p.URLs {
			switch u {
			case "https://www.cloudflare.com/cdn-cgi/trace", "https://api.ip.sb/geoip", "https://ipinfo.io/json", "https://ipwho.is":
			default:
				return nil, webAPIError(400, "invalid_sources", "unknown outbound IP source")
			}
		}
		if b.OutboundIP == nil {
			return nil, webRuntimeUnavailable()
		}
		return b.OutboundIP(ctx, p)
	})
	webRuntimeQuery(rt, "probes/services", func(ctx context.Context, p WebAPIRuntimeServicesRequest) (any, error) {
		if err := webRuntimeTimeout(p.TimeoutMillis, 10000); err != nil {
			return nil, err
		}
		if len(p.Names) > len(serviceCheckers) {
			return nil, webAPIError(400, "invalid_services", "service batch exceeds the limit")
		}
		for _, n := range p.Names {
			if len(selectedCheckers([]string{n})) != 1 {
				return nil, webAPIError(400, "invalid_services", "unknown service check")
			}
		}
		if b.Services == nil {
			return nil, webRuntimeUnavailable()
		}
		return b.Services(ctx, p)
	})
	rt.registerMITM()
}

func (rt *WebAPIRuntime) registerMITM() {
	m := rt.backend.MITM
	rt.get("mitm/certificate", func(ctx context.Context, r *http.Request) (any, error) {
		if err := webRuntimeNoQuery(r); err != nil {
			return nil, err
		}
		if m == nil {
			return nil, webRuntimeUnavailable()
		}
		return m.Certificate(ctx)
	})
	webRuntimeCommand(rt, "mitm/certificate:generate", webRuntimeGenerateCertificate, m != nil, webRuntimeSimpleMutation, nil)
	webRuntimeQuery(rt, "mitm:validate", func(ctx context.Context, p WebAPIRuntimeMITMRules) (any, error) {
		if m == nil {
			return nil, webRuntimeUnavailable()
		}
		return m.Validate(ctx, p)
	})
	webRuntimeQuery(rt, "mitm/certificate:issue", func(ctx context.Context, p WebAPIRuntimeIssueRequest) (any, error) {
		if err := webRuntimeName(p.Host, false); err != nil {
			return nil, err
		}
		if strings.ContainsAny(p.Host, "/\\ :\t") {
			return nil, webAPIError(400, "invalid_host", "a certificate hostname is required")
		}
		if m == nil {
			return nil, webRuntimeUnavailable()
		}
		return m.IssueCertificate(ctx, p.Host)
	})
	webRuntimeCommand(rt, "mitm/scripts:run", webRuntimeRunScript, m != nil, func(p WebAPIRuntimeScriptRequest) WebAPIMutation { return p.WebAPIMutation }, func(p WebAPIRuntimeScriptRequest) error {
		if err := webRuntimeName(p.ScriptName, false); err != nil {
			return err
		}
		if strings.ContainsAny(p.ScriptName, "/\\") {
			return webAPIError(400, "invalid_script", "a registered script name is required")
		}
		return nil
	})
	rt.get("mitm/captures", func(ctx context.Context, r *http.Request) (any, error) {
		q, err := WebAPIQuery(r, "connectionId")
		if err != nil {
			return nil, err
		}
		if err = webRuntimeName(q["connectionId"], false); err != nil {
			return nil, err
		}
		if m == nil {
			return nil, webRuntimeUnavailable()
		}
		return m.Exchanges(ctx, q["connectionId"])
	})
	rt.get("mitm/capture", func(ctx context.Context, r *http.Request) (any, error) {
		q, err := WebAPIQuery(r, "id")
		if err != nil {
			return nil, err
		}
		if _, err = webAPIInteger(q["id"], true); err != nil {
			return nil, webAPIError(400, "invalid_id", "a decimal string capture identity is required")
		}
		if m == nil {
			return nil, webRuntimeUnavailable()
		}
		return m.Exchange(ctx, WebAPIID(q["id"]))
	})
	rt.get("mitm/body", func(ctx context.Context, r *http.Request) (any, error) {
		q, err := WebAPIQuery(r, "id", "response")
		if err != nil {
			return nil, err
		}
		if _, err = webAPIInteger(q["id"], true); err != nil {
			return nil, webAPIError(400, "invalid_id", "a decimal string capture identity is required")
		}
		response, err := webRuntimeBool(q, "response")
		if err != nil {
			return nil, err
		}
		if m == nil {
			return nil, webRuntimeUnavailable()
		}
		return m.Body(ctx, WebAPIID(q["id"]), response)
	})
}

func (rt *WebAPIRuntime) operation(w http.ResponseWriter, r *http.Request) {
	if _, err := WebAPIQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/runtime/operations/")
	var data WebAPIRuntimeOperation
	rev, err := rt.store.View(r.Context(), func(tx *WebAPIReadTx) error { var err error; data, err = tx.RuntimeOperation(id); return err })
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	release, err := WebAPIAcquireCommitAuthorization(r.Context())
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	release()
	WebAPIWriteSuccess(w, rev, data)
}

func (rt *WebAPIRuntime) run() {
	defer close(rt.done)
	for {
		select {
		case <-rt.ctx.Done():
			return
		case <-rt.wake:
		}
		_ = rt.sweepProviderSources(rt.ctx)
		_ = rt.sweepGeoSources(rt.ctx)
		records, err := rt.store.runtimeRecords(rt.ctx)
		if err != nil {
			continue
		}
		if len(records) > 0 {
			select {
			case rt.wake <- struct{}{}:
			default:
			}
		}
		for _, record := range records {
			if record.State.Phase != WebAPIRuntimePending {
				continue
			}
			select {
			case <-rt.ctx.Done():
				return
			default:
			}
			state, err := rt.store.transitionRuntime(record.State.OperationID, WebAPIRuntimePending, WebAPIRuntimeApplying, nil, nil)
			if err != nil {
				continue
			}
			rt.publishRuntimeState(state)
			if !rt.runEffect(record) {
				return
			}
		}
	}
}

func (rt *WebAPIRuntime) applySafely(ctx context.Context, record webRuntimeRecord) (result any, failure *WebAPIError) {
	defer func() {
		if recover() != nil {
			result = nil
			failure = webAPIError(500, "effect_unknown", "runtime execution failed after acceptance")
		}
	}()
	result, err := rt.apply(ctx, record)
	if err != nil {
		return result, webRuntimeFailure(err)
	}
	return result, nil
}

func (rt *WebAPIRuntime) apply(ctx context.Context, r webRuntimeRecord) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b := rt.backend
	if b.SafeMode {
		return nil, webRuntimeSafeMode()
	}
	if b.State != nil && r.State.Action != webRuntimeRefreshProvider && r.State.Action != webRuntimeWriteProvider && r.State.Action != webRuntimeSideloadProvider {
		state, err := b.State(ctx)
		if err != nil {
			return nil, err
		}
		if !state.Initialized {
			return nil, webAPIError(503, "core_unavailable", "core is not initialized")
		}
	}
	switch r.State.Action {
	case webRuntimeWriteProvider, webRuntimeSideloadProvider:
		var desired webRuntimeDesiredProviderWrite
		if err := json.Unmarshal(r.Desired, &desired); err != nil {
			return nil, err
		}
		return rt.writeProvider(ctx, desired)
	case webRuntimeRefreshProvider:
		var desired webRuntimeDesiredProviderRefresh
		if err := json.Unmarshal(r.Desired, &desired); err != nil {
			return nil, err
		}
		return rt.refreshProvider(ctx, desired)
	case webRuntimeApply:
		var desired webRuntimeDesiredApply
		if err := json.Unmarshal(r.Desired, &desired); err != nil {
			return nil, err
		}
		prepared, err := rt.materializeStatefulApply(ctx, r.State.OperationID, desired)
		if err != nil {
			return nil, err
		}
		return b.Apply(ctx, prepared)
	case webRuntimeReconcile:
		var desired webRuntimeDesiredReconcile
		if err := json.Unmarshal(r.Desired, &desired); err != nil {
			return nil, err
		}
		return b.Reconcile(ctx, desired.ProfileID, desired.Patch)
	case webRuntimeSelect:
		var p WebAPIRuntimeSelectRequest
		if err := json.Unmarshal(r.Desired, &p); err != nil {
			return nil, err
		}
		state, err := b.State(ctx)
		if err != nil {
			return nil, err
		}
		if state.RuntimeGeneration != p.RuntimeGeneration {
			return nil, webAPIError(409, "runtime_generation_conflict", "the accepted selection was superseded by a new runtime generation")
		}
		return b.SelectProxy(ctx, p.GroupName, *p.ProxyName)
	case webRuntimeCloseConnection:
		var p WebAPIRuntimeCloseRequest
		if err := json.Unmarshal(r.Desired, &p); err != nil {
			return nil, err
		}
		return nil, b.CloseConnection(ctx, p.ID)
	case webRuntimeCloseConnections:
		return nil, b.CloseConnections(ctx)
	case webRuntimeResetConnections:
		return nil, b.ResetConnections(ctx)
	case webRuntimeResetTraffic:
		return nil, b.ResetTraffic(ctx)
	case webRuntimeClearDNS:
		return nil, b.ClearDNS(ctx)
	case webRuntimeNetworkChanged:
		return nil, b.NetworkChanged(ctx)
	case webRuntimeListener:
		var p WebAPIRuntimeListenerRequest
		if err := json.Unmarshal(r.Desired, &p); err != nil {
			return nil, err
		}
		return nil, b.SetListener(ctx, *p.Enabled)
	case webRuntimeGC:
		return nil, b.CollectMemory(ctx)
	case webRuntimeGeoUpdate:
		var desired webRuntimeDesiredGeo
		if err := json.Unmarshal(r.Desired, &desired); err != nil {
			return nil, err
		}
		return rt.geoUpdate(ctx, desired)
	case webRuntimeGenerateCertificate:
		return b.MITM.GenerateCertificate(ctx)
	case webRuntimeRunScript:
		var p WebAPIRuntimeScriptRequest
		if err := json.Unmarshal(r.Desired, &p); err != nil {
			return nil, err
		}
		return b.MITM.RunScript(ctx, p.ScriptName)
	default:
		return nil, webRuntimeUnavailable()
	}
}
