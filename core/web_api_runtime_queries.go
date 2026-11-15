//go:build webpanel && !android

package core

import (
	"context"
	"time"
)

var webRuntimeRouteSlots = make(slots, 4)

func webRuntimeDelay(ctx context.Context, p WebAPIRuntimeDelayRequest) (*Delay, error) {
	url := p.TestURL
	if url == "" {
		url = currentTestURL()
	}
	result := &Delay{Name: p.ProxyName, Url: url, Value: delayFailed}
	proxy := lookupProxy(p.ProxyName)
	if proxy == nil {
		return result, nil
	}
	timeout := time.Duration(p.TimeoutMillis) * time.Millisecond
	queue, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if !delayTestSlots.acquire(queue) {
		return nil, webAPIError(503, "probe_busy", "delay probe could not acquire its bounded queue slot")
	}
	defer delayTestSlots.release()
	probe, stop := context.WithTimeout(ctx, timeout)
	defer stop()
	delay, err := proxy.URLTest(probe, url, anyDelayTestStatus)
	if err != nil {
		delay = 0
	}
	result.Value = delayValue(delay, probe.Err() != nil)
	return result, nil
}

func webRuntimeRouteExit(ctx context.Context, host, proxy string) (string, error) {
	return webRuntimeRouteExitUsing(ctx, host, proxy, resolveExit)
}

func webRuntimeRouteExitUsing(ctx context.Context, host, proxy string, resolve func(string, string) string) (string, error) {
	if !webRuntimeRouteSlots.acquire(ctx) {
		return "", webAPIError(504, "query_timeout", "route resolution exceeded its bounded budget")
	}
	type resolution struct {
		value string
		err   error
	}
	result := make(chan resolution, 1)
	go func() {
		defer webRuntimeRouteSlots.release()
		defer func() {
			if recover() != nil {
				result <- resolution{err: webAPIError(422, "route_resolution_failed", "route resolution failed without a usable result")}
			}
		}()
		result <- resolution{value: resolve(host, proxy)}
	}()
	select {
	case value := <-result:
		return value.value, value.err
	case <-ctx.Done():
		return "", webAPIError(504, "query_timeout", "route resolution exceeded its bounded budget")
	}
}

func webRuntimeCheckRoutes(ctx context.Context, p CheckRoutesParams) (*RouteCheck, error) {
	epoch, version := routeStamp()
	held := make([]bool, len(p.Routes))
	exits := map[RouteHit]string{}
	for i, route := range p.Routes {
		held[i] = len(route) > 0
		for _, hit := range route {
			if err := ctx.Err(); err != nil {
				return nil, webAPIError(504, "query_timeout", "route check exceeded its bounded budget")
			}
			key := RouteHit{Host: hit.Host, Proxy: hit.Proxy}
			exit, known := exits[key]
			if !known {
				var err error
				exit, err = webRuntimeRouteExit(ctx, hit.Host, hit.Proxy)
				if err != nil {
					return nil, err
				}
				exits[key] = exit
			}
			if exit != hit.Exit {
				held[i] = false
				break
			}
		}
	}
	return &RouteCheck{CoreEpoch: epoch, RouteVersion: version, Held: held}, nil
}

func webRuntimeValidateDefinitions(ctx context.Context, p ProxiesParams) ([]ValidationIssue, error) {
	issues := []ValidationIssue{}
	for i, mapping := range p.Proxies {
		if err := ctx.Err(); err != nil {
			return nil, webAPIError(504, "query_timeout", "proxy validation exceeded its bounded budget")
		}
		stateful, err := WebAPIValidateStatefulDefinition(mapping)
		if err != nil {
			return nil, err
		}
		if stateful {
			// Native stateful constructors can open files or start protocol services.
			// They are reserved for a committed apply worker, never validation/preview.
			continue
		}
		for _, issue := range handleValidateProxies(ProxiesParams{Proxies: []map[string]any{mapping}}) {
			issue.Index = i
			issues = append(issues, issue)
		}
	}
	return issues, nil
}
