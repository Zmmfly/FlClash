//go:build webpanel && !android

package core

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/metacubex/mihomo/common/convert"
	"github.com/metacubex/mihomo/tunnel/statistic"
)

type WebAPIRuntimeMITMBackend interface {
	Certificate(context.Context) (*WebAPIRuntimeMITMCertificate, error)
	GenerateCertificate(context.Context) (*WebAPIRuntimeMITMCertificate, error)
	Validate(context.Context, WebAPIRuntimeMITMRules) (map[string][]ValidationIssue, error)
	IssueCertificate(context.Context, string) (string, error)
	RunScript(context.Context, string) (*WebAPIRuntimeMITMScriptOutput, error)
	Exchanges(context.Context, string) (*WebAPIRuntimeMITMExchangeList, error)
	Exchange(context.Context, WebAPIID) (*WebAPIRuntimeMITMExchange, error)
	Body(context.Context, WebAPIID, bool) (*WebAPIRuntimeMITMBody, error)
}

type WebAPIRuntimeBackend struct {
	Native                  bool
	InstallGeo              func(context.Context, string, []byte) error
	SafeMode                bool
	Apply                   func(context.Context, WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error)
	Reconcile               func(context.Context, *WebAPIID, UpdateParams) (WebAPIRuntimeApplyResult, error)
	State                   func(context.Context) (WebAPIRuntimeCoreState, error)
	NetworkInterfaces       func(context.Context) ([]WebAPIRuntimeNetworkInterface, error)
	Memory                  func(context.Context) (MemoryStats, error)
	Proxies                 func(context.Context) (ProxiesData, error)
	ExternalProviders       func(context.Context) ([]WebAPIRuntimeExternalProviderMetadata, error)
	ProxyDefinition         func(context.Context, string) (map[string]any, error)
	ProxyDefinitionProvider func(context.Context, string) (string, error)
	SelectProxy             func(context.Context, string, string) (*ChangeProxyResult, error)
	Delay                   func(context.Context, WebAPIRuntimeDelayRequest) (*Delay, error)
	Connections             func(context.Context) (*statistic.Snapshot, error)
	ConnectionCount         func(context.Context) (int, error)
	Traffic                 func(context.Context, bool, bool) (Traffic, error)
	CloseConnection         func(context.Context, string) error
	CloseConnections        func(context.Context) error
	ResetConnections        func(context.Context) error
	ResetTraffic            func(context.Context) error
	ClearDNS                func(context.Context) error
	NetworkChanged          func(context.Context) error
	SetListener             func(context.Context, bool) error
	CollectMemory           func(context.Context) error
	GeoResources            func(context.Context) ([]WebAPIRuntimeGeoResource, error)
	GeoMetadata             func(context.Context, string) (WebAPIRuntimeGeoMetadata, error)
	Route                   func(context.Context) (RouteState, error)
	CheckRoutes             func(context.Context, CheckRoutesParams) (*RouteCheck, error)
	OutboundIP              func(context.Context, WebAPIRuntimeOutboundIPRequest) (*OutboundIpResult, error)
	Services                func(context.Context, WebAPIRuntimeServicesRequest) ([]ServiceCheckItem, error)
	Subscribe               func(logs, route bool)
	MITM                    WebAPIRuntimeMITMBackend
}

func DefaultWebAPIRuntimeBackend(safeMode bool) WebAPIRuntimeBackend {
	var memoryMu sync.Mutex
	var startMu sync.Mutex
	var startTime int64
	var activeProfile *WebAPIID
	var subscriptionMu sync.Mutex
	var logsSubscribed, routeSubscribed bool
	return WebAPIRuntimeBackend{
		Native: true,
		InstallGeo: func(ctx context.Context, kind string, data []byte) error {
			if safeMode {
				return webRuntimeSafeMode()
			}
			return webRuntimeInstallGeo(ctx, kind, data)
		},
		SafeMode: safeMode,
		Apply: func(ctx context.Context, p WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
			if safeMode {
				return WebAPIRuntimeApplyResult{}, webRuntimeSafeMode()
			}
			result, err := webRuntimeApplyNative(ctx, p)
			startMu.Lock()
			if result.ActualConfig == "candidate" {
				activeProfile = p.ProfileID
			} else if result.ActualConfig == "default-fallback" {
				activeProfile = nil
			}
			startMu.Unlock()
			return result, err
		},
		Reconcile: func(ctx context.Context, id *WebAPIID, p UpdateParams) (WebAPIRuntimeApplyResult, error) {
			if safeMode {
				return WebAPIRuntimeApplyResult{}, webRuntimeSafeMode()
			}
			startMu.Lock()
			matches := webAPIOwner(activeProfile, id)
			startMu.Unlock()
			if !matches {
				return WebAPIRuntimeApplyResult{}, webAPIError(409, "profile_not_applied", "the desired profile is not the currently applied profile")
			}
			if err := ctx.Err(); err != nil {
				return WebAPIRuntimeApplyResult{}, err
			}
			err := updateConfig(&p)
			return WebAPIRuntimeApplyResult{ActualConfig: "patched", ProfileID: id, Route: routeState(), ListenerEnabled: isRunning.Load()}, err
		},
		State: func(context.Context) (WebAPIRuntimeCoreState, error) {
			startMu.Lock()
			defer startMu.Unlock()
			return WebAPIRuntimeCoreState{Initialized: isInit.Load(), ListenerEnabled: isRunning.Load(), SafeMode: safeMode, StartTimeMillis: startTime, ProfileID: activeProfile, RuntimeGeneration: WebAPIRevision(strconv.FormatUint(routeState().CoreEpoch, 10))}, nil
		},
		NetworkInterfaces: func(context.Context) ([]WebAPIRuntimeNetworkInterface, error) {
			interfaces, err := net.Interfaces()
			if err != nil {
				return nil, err
			}
			out := make([]WebAPIRuntimeNetworkInterface, 0, len(interfaces))
			for _, item := range interfaces {
				addrs, err := item.Addrs()
				if err != nil {
					return nil, err
				}
				entry := WebAPIRuntimeNetworkInterface{Name: item.Name, Addresses: []string{}}
				for _, address := range addrs {
					entry.Addresses = append(entry.Addresses, address.String())
				}
				out = append(out, entry)
			}
			return out, nil
		},
		Memory: func(context.Context) (MemoryStats, error) {
			memoryMu.Lock()
			defer memoryMu.Unlock()
			return handleGetMemoryStats(), nil
		},
		Proxies:           func(context.Context) (ProxiesData, error) { return handleGetProxies(), nil },
		ExternalProviders: webRuntimeNativeExternalProviders,
		ProxyDefinition: func(_ context.Context, name string) (map[string]any, error) {
			return handleGetProxyDefinition(NameParams{Name: name})
		},
		ProxyDefinitionProvider: func(_ context.Context, name string) (string, error) {
			if proxy := lookupProxy(name); proxy != nil {
				return proxy.ProxyInfo().ProviderName, nil
			}
			return "", nil
		},
		SelectProxy: func(_ context.Context, group, name string) (*ChangeProxyResult, error) {
			r := handleChangeProxy(&ChangeProxyParams{GroupName: group, ProxyName: name})
			if r.Message != "" {
				return nil, webAPIError(422, "selection_rejected", "the running group rejected this selection")
			}
			return r, nil
		},
		Delay: func(ctx context.Context, p WebAPIRuntimeDelayRequest) (*Delay, error) {
			if safeMode {
				return nil, webRuntimeSafeMode()
			}
			return webRuntimeDelay(ctx, p)
		},
		Connections:     func(context.Context) (*statistic.Snapshot, error) { return handleGetConnections(), nil },
		ConnectionCount: func(context.Context) (int, error) { return handleGetConnectionCount(), nil },
		Traffic: func(_ context.Context, total, only bool) (Traffic, error) {
			if total {
				return handleGetTotalTraffic(TrafficParams{OnlyProxy: only}), nil
			}
			return handleGetTraffic(TrafficParams{OnlyProxy: only}), nil
		},
		CloseConnection:  func(_ context.Context, id string) error { return handleCloseConnection(IDParams{ID: id}) },
		CloseConnections: func(context.Context) error { handleCloseConnections(); return nil },
		ResetConnections: func(context.Context) error { handleResetConnections(); return nil },
		ResetTraffic:     func(context.Context) error { handleResetTraffic(); return nil },
		ClearDNS:         func(context.Context) error { handleClearDnsCache(); return nil },
		NetworkChanged:   func(context.Context) error { handleNetworkChanged(); return nil },
		SetListener: func(_ context.Context, enabled bool) error {
			if safeMode {
				return webAPIError(409, "safe_mode", "listener transitions are disabled in safe mode")
			}
			if isRunning.Load() == enabled {
				return nil
			}
			if enabled {
				handleStartListener()
			} else {
				handleStopListener()
			}
			startMu.Lock()
			if enabled {
				startTime = time.Now().UnixMilli()
			} else {
				startTime = 0
			}
			startMu.Unlock()
			return nil
		},
		CollectMemory: func(context.Context) error { handleForceGC(); return nil },
		GeoMetadata:   webRuntimeReadGeoMetadata,
		GeoResources: func(context.Context) ([]WebAPIRuntimeGeoResource, error) {
			items := make([]WebAPIRuntimeGeoResource, 0, len(geoResources))
			for _, kind := range []string{"MMDB", "ASN", "GEOIP", "GEOSITE"} {
				items = append(items, WebAPIRuntimeGeoResource{Type: kind, Updating: webRuntimeGeoUpdating(kind)})
			}
			return items, nil
		},
		Route: func(context.Context) (RouteState, error) { return routeState(), nil },
		CheckRoutes: func(ctx context.Context, p CheckRoutesParams) (*RouteCheck, error) {
			if safeMode {
				return nil, webRuntimeSafeMode()
			}
			return webRuntimeCheckRoutes(ctx, p)
		},
		OutboundIP: func(_ context.Context, p WebAPIRuntimeOutboundIPRequest) (*OutboundIpResult, error) {
			return handleOutboundIp(&OutboundIpParams{ProxyName: p.ProxyName, Urls: p.URLs, Timeout: p.TimeoutMillis}), nil
		},
		Services: func(_ context.Context, p WebAPIRuntimeServicesRequest) ([]ServiceCheckItem, error) {
			return handleServiceCheck(&ServiceCheckParams{ProxyName: p.ProxyName, Names: p.Names, Timeout: p.TimeoutMillis}), nil
		},
		Subscribe: func(logs, route bool) {
			if safeMode {
				return
			}
			subscriptionMu.Lock()
			defer subscriptionMu.Unlock()
			if logs != logsSubscribed {
				if logs {
					handleStartLog()
				} else {
					handleStopLog()
				}
				logsSubscribed = logs
			}
			if route != routeSubscribed {
				handleWatchRoute(route, methodCallSeq.Add(1))
				routeSubscribed = route
			}
		}}
}

func webRuntimeGeoUpdating(kind string) bool {
	updateClaims.Lock()
	defer updateClaims.Unlock()
	_, active := updateClaims.fromHook[geoUpdateScope+kind]
	return active
}

func webRuntimeName(value string, empty bool) error {
	if (!empty && value == "") || len(value) > 1024 || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
		return webAPIError(400, "invalid_name", "invalid runtime name")
	}
	return nil
}

func webRuntimeTimeout(value int64, max int64) error {
	if value < 1 || value > max {
		return webAPIError(400, "invalid_timeout", "timeoutMillis is outside the endpoint budget")
	}
	return nil
}

func webRuntimeURL(value string, empty bool) error {
	if value == "" && empty {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(value) > 4096 {
		return webAPIError(400, "invalid_url", "an HTTP(S) URL without credentials is required")
	}
	return nil
}

func webRuntimeDefinitions(proxies []map[string]any) error {
	if proxies == nil {
		return webAPIError(400, "invalid_proxies", "proxy definitions are required")
	}
	for _, p := range proxies {
		if _, err := WebAPIValidateStatefulDefinition(p); err != nil {
			return err
		}
		if err := webRuntimePathPolicy(p, ""); err != nil {
			return err
		}
	}
	return nil
}

func webRuntimeEmptyField(value any) bool {
	if value == nil {
		return true
	}
	s, ok := value.(string)
	return ok && s == ""
}

func webRuntimeConvert(content string) ([]map[string]any, error) {
	if len(content) > WebAPIMaxContentBytes || !utf8.ValidString(content) {
		return nil, webAPIError(413, "content_too_large", "proxy input exceeds the bounded conversion limit")
	}
	proxies, err := convert.ConvertsV2Ray([]byte(content))
	if err != nil {
		return nil, webAPIError(422, "conversion_failed", "proxy input cannot be converted")
	}
	if err := webRuntimeDefinitions(proxies); err != nil {
		return nil, err
	}
	return proxies, nil
}

func webRuntimeFailure(err error) *WebAPIError {
	if isDependencyPolicyError(err) {
		method := asMethodError(err)
		problem := webAPIError(422, method.Code, "candidate outbound graph is invalid")
		if details, ok := method.Details.(map[string]any); ok {
			problem.Details = details
		}
		return problem
	}
	var api *WebAPIError
	if errors.As(err, &api) {
		return api
	}
	var method *MethodError
	if errors.As(err, &method) {
		return webAPIError(422, method.Code, "the core rejected this runtime operation")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return webAPIError(504, "effect_unknown", "runtime execution was interrupted")
	}
	return webAPIError(500, "effect_unknown", "runtime result could not be confirmed")
}
