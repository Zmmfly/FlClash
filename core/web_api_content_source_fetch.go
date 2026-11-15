//go:build webpanel && !android

package core

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

func webSourcePurposeValid(p WebAPISourcePurpose) bool {
	switch p {
	case WebAPISourceScriptImport, WebAPISourceRuleProviderImport, WebAPISourceMitmModuleImport, WebAPISourceMitmResourceImport, WebAPISourceIconSetImport, WebAPISourceProfileImport, WebAPISourceProfileRefresh, WebAPISourceScriptRefresh, WebAPISourceRuleProviderRefresh, WebAPISourceMitmModuleRefresh, WebAPISourceMitmResourceRefresh, WebAPISourceIconSetRefresh, WebAPISourceExternalProviderRefresh, WebAPISourceGeoRefresh, WebAPISourceAppRelease, WebAPISourceIPQualityLookup, WebAPISourceIconImage, WebAPISourceDAVProbe, WebAPISourceDAVBackup, WebAPISourceDAVRestore, WebAPISourceCustomProfileImport, WebAPISourceCustomProfileDependency:
		return true
	}
	return false
}

func webSourceOrigin(u *url.URL) (string, string, uint16) {
	port := uint64(80)
	if u.Scheme == "https" {
		port = 443
	}
	if u.Port() != "" {
		port, _ = strconv.ParseUint(u.Port(), 10, 16)
	}
	return u.Scheme, strings.ToLower(u.Hostname()), uint16(port)
}
func webSourceSameOrigin(a, b *url.URL) bool {
	as, ah, ap := webSourceOrigin(a)
	bs, bh, bp := webSourceOrigin(b)
	return as == bs && ah == bh && ap == bp
}

func webSourceCanonical(raw string) (*url.URL, string, error) {
	u, err := webContentURL(raw)
	if err != nil {
		return nil, "", err
	}
	if u.Opaque != "" || strings.ContainsAny(raw, "\x00\r\n") || strings.TrimSpace(raw) != raw {
		return nil, "", webAPIError(400, "invalid_source_url", "invalid source URL")
	}
	scheme, host, port := webSourceOrigin(u)
	u.Scheme = scheme
	u.Host = net.JoinHostPort(host, strconv.Itoa(int(port)))
	if u.Path == "" {
		u.Path = "/"
	}
	return u, webContentDigest([]byte(u.String())), nil
}

func webSourceAddressClass(ip netip.Addr) string {
	ip = ip.Unmap()
	if ip == netip.MustParseAddr("100.100.100.200") || ip == netip.MustParseAddr("fd00:ec2::254") {
		return "forbidden"
	}
	if !ip.IsValid() || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return "forbidden"
	}
	if ip.IsPrivate() || ip.IsLoopback() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip) {
		return "private"
	}
	if webContentPublicAddress(ip) {
		return "public"
	}
	return "forbidden"
}

func webSourceHostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, blocked := range []string{"metadata", "metadata.google.internal", "metadata.goog", "instance-data", "instance-data.ec2.internal", "metadata.azure.internal"} {
		if host == blocked || strings.HasSuffix(host, "."+blocked) {
			return false
		}
	}
	return !strings.ContainsAny(host, "\x00\r\n%")
}

func webSourceResolve(ctx context.Context, u *url.URL, options WebAPISourceFetchOptions) ([]netip.Addr, bool, error) {
	if !webSourceHostAllowed(u.Hostname()) {
		return nil, false, webAPIError(403, "source_address_forbidden", "source host is reserved for machine metadata")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		class := webSourceAddressClass(ip)
		if class == "forbidden" {
			return nil, false, webAPIError(403, "source_address_forbidden", "source resolved to a forbidden address")
		}
		return []netip.Addr{ip.Unmap()}, class == "private", nil
	}
	lookup := options.LookupNetIP
	if lookup == nil {
		lookup = net.DefaultResolver.LookupNetIP
	}
	addresses, err := lookup(ctx, "ip", u.Hostname())
	if err != nil {
		return nil, false, webSourceNetworkError(ctx, err, "source_dns_failed", "source DNS resolution failed")
	}
	if len(addresses) == 0 {
		return nil, false, webAPIError(502, "source_dns_failed", "source DNS resolution failed")
	}
	if len(addresses) > 64 {
		return nil, false, webAPIError(400, "source_address_limit", "source returned too many addresses")
	}
	private := false
	seen := map[netip.Addr]bool{}
	result := []netip.Addr{}
	for _, ip := range addresses {
		ip = ip.Unmap()
		class := webSourceAddressClass(ip)
		if class == "forbidden" {
			return nil, false, webAPIError(403, "source_address_forbidden", "source resolved to a forbidden address")
		}
		private = private || class == "private"
		if !seen[ip] {
			seen[ip] = true
			result = append(result, ip)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Compare(result[j]) < 0 })
	return result, private, nil
}

func webSourceAddressStrings(addresses []netip.Addr) []string {
	result := make([]string, len(addresses))
	for i, ip := range addresses {
		result[i] = ip.Unmap().String()
	}
	sort.Strings(result)
	return result
}

func webSourceValidateGrant(ctx context.Context, request WebAPISourceRequest, u *url.URL, digest string, addresses []netip.Addr, grant *WebAPISourceGrant, now time.Time) error {
	if grant == nil {
		return webAPIError(403, "source_authorization_required", "this source requires explicit server-network authorization")
	}
	scheme, host, port := webSourceOrigin(u)
	expires, err := time.Parse(time.RFC3339Nano, grant.ExpiresAt)
	if err != nil || !now.Before(expires) || grant.ID != request.AuthorizationID || grant.ImportJobID != request.ImportJobID || grant.SourceReference != request.SourceReference || grant.RequestDigest != request.RequestDigest || grant.PolicyVersion != WebAPISourcePolicyVersion || grant.Purpose != request.Purpose || grant.URLDigest != digest || grant.Scheme != scheme || grant.Host != host || grant.Port != port || grant.SessionID != WebAPIRequestSessionID(ctx) || !reflect.DeepEqual(grant.Key, request.Key) {
		return webAPIError(409, "source_authorization_invalid", "source authorization no longer matches the resource")
	}
	approved := append([]string{}, grant.Addresses...)
	sort.Strings(approved)
	if !reflect.DeepEqual(approved, webSourceAddressStrings(addresses)) {
		return webAPIError(409, "source_target_changed", "source DNS target changed; prepare authorization again")
	}
	return nil
}

type webSourceRoundTrip func(*http.Request) (*http.Response, error)

func (f webSourceRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func webSourcePublicUtilityPurpose(p WebAPISourcePurpose) bool {
	return p == WebAPISourceAppRelease || p == WebAPISourceIPQualityLookup
}

func webSourceDAVPurpose(p WebAPISourcePurpose) bool {
	return p == WebAPISourceDAVProbe || p == WebAPISourceDAVBackup || p == WebAPISourceDAVRestore
}

func webSourceValidateFetchRequest(request WebAPISourceRequest) error {
	if !webSourcePurposeValid(request.Purpose) || !webSourceResourcePurpose(request.Key, request.Purpose) || webSourceDAVPurpose(request.Purpose) {
		return webAPIError(400, "invalid_source_purpose", "unsupported fixed GET source purpose")
	}
	if len(request.CommittedHeaders) > 0 && request.Purpose != WebAPISourceExternalProviderRefresh && request.Purpose != WebAPISourceCustomProfileDependency {
		return webAPIError(400, "invalid_source_headers", "this source does not declare committed headers")
	}
	if request.Purpose == WebAPISourceCustomProfileDependency && (!webAPIOperationID(request.ImportJobID) || request.SourceReference != request.Key.Name || !webAPISourceDigest(request.RequestDigest)) {
		return webAPIError(400, "invalid_source_request", "custom dependency requires exact parsed-job bindings")
	}
	if request.Purpose == WebAPISourceCustomProfileImport && request.SourceReference != "" {
		return webAPIError(400, "invalid_source_request", "custom root does not accept a dependency reference")
	}
	if webSourcePublicUtilityPurpose(request.Purpose) {
		if request.AuthorizationID != "" || request.ImportJobID != "" || request.SourceReference != "" || request.RequestDigest != "" {
			return webAPIError(400, "invalid_source_request", "public utility sources do not accept private authorization")
		}
		return ValidateWebAPIUtilitySource(request.Purpose, request.URL)
	}
	return nil
}

func webSourceNetworkError(ctx context.Context, err error, code, message string) error {
	var api *WebAPIError
	if errors.As(err, &api) {
		return api
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return webAPIError(504, "source_timeout", "source request exceeded its deadline")
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return webAPIError(408, "source_canceled", "source request was canceled")
	}
	return webAPIError(502, code, message)
}

func webSourceReadLimit(p WebAPISourcePurpose) int64 {
	switch p {
	case WebAPISourceGeoRefresh:
		return WebAPIMaxGeoSourceBytes
	case WebAPISourceAppRelease:
		return 1 << 20
	case WebAPISourceIPQualityLookup:
		return 256 << 10
	case WebAPISourceMitmResourceImport, WebAPISourceMitmResourceRefresh:
		return 16 << 20
	default:
		return WebAPIMaxContentBytes
	}
}

func webFetchPurposeSource(ctx context.Context, request WebAPISourceRequest, grant *WebAPISourceGrant, options WebAPISourceFetchOptions) (WebAPISourceFetchResult, error) {
	return webFetchPurposeSourceChecked(ctx, request, grant, options, nil)
}

func webFetchPurposeSourceChecked(ctx context.Context, request WebAPISourceRequest, grant *WebAPISourceGrant, options WebAPISourceFetchOptions, check func(context.Context) error) (WebAPISourceFetchResult, error) {
	var result WebAPISourceFetchResult
	if err := webSourceValidateFetchRequest(request); err != nil {
		return result, err
	}
	if webSourcePublicUtilityPurpose(request.Purpose) && grant != nil {
		return result, webAPIError(400, "invalid_source_request", "public utility sources do not accept private authorization")
	}
	original, digest, headers, err := webSourceIdentity(request.URL, request.CommittedHeaders)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	now := options.Now
	if now == nil {
		now = time.Now
	}
	user := original.User
	original.User = nil
	credentials := true
	transport := webSourceRoundTrip(func(r *http.Request) (*http.Response, error) {
		if webSourcePublicUtilityPurpose(request.Purpose) {
			if err := ValidateWebAPIUtilitySource(request.Purpose, r.URL.String()); err != nil {
				return nil, err
			}
			if r.URL.String() != original.String() {
				return nil, webAPIError(403, "source_redirect_forbidden", "utility redirect changed its fixed source")
			}
		}
		addresses, private, err := webSourceResolve(r.Context(), r.URL, options)
		if err != nil {
			return nil, err
		}
		if private && webSourcePublicUtilityPurpose(request.Purpose) {
			return nil, webAPIError(403, "source_address_forbidden", "utility sources require public addresses")
		}
		if private || grant != nil && webSourceSameOrigin(original, r.URL) {
			if !webSourceSameOrigin(original, r.URL) || private && r.URL.String() != original.String() {
				return nil, webAPIError(403, "source_redirect_authorization_required", "redirect destination requires separate authorization")
			}
			if err = webSourceValidateGrant(ctx, request, original, digest, addresses, grant, now()); err != nil {
				return nil, err
			}
		}
		if check != nil {
			if err := check(ctx); err != nil {
				return nil, err
			}
		}
		release, err := WebAPIAcquireCommitAuthorization(ctx)
		if err != nil {
			return nil, err
		}
		release()
		dial := options.DialContext
		if dial == nil {
			dial = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
		}
		_, _, port := webSourceOrigin(r.URL)
		t := &http.Transport{Proxy: nil, DisableKeepAlives: true, ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: options.TLSRootCAs}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 32 << 10, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			lastError := errors.New("source connection failed")
			for _, ip := range addresses {
				connection, err := dial(ctx, network, net.JoinHostPort(ip.String(), strconv.Itoa(int(port))))
				if err == nil {
					return connection, nil
				}
				lastError = err
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
			}
			return nil, lastError
		}}
		defer t.CloseIdleConnections()
		return t.RoundTrip(r)
	})
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return webAPIError(502, "source_redirect_limit", "source redirected too many times")
		}
		if r.URL.User != nil {
			return webAPIError(403, "source_redirect_credentials", "redirect supplied unexpected credentials")
		}
		if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && r.URL.Scheme != "https" {
			return webAPIError(403, "source_downgrade_forbidden", "source HTTPS downgrade is forbidden")
		}
		canonical, _, err := webSourceCanonical(r.URL.String())
		if err != nil {
			return err
		}
		r.URL = canonical
		if !webSourceSameOrigin(original, r.URL) {
			credentials = false
		}
		if !credentials {
			for name := range headers {
				r.Header.Del(name)
			}
			r.Header.Del("Authorization")
			r.Header.Del("Cookie")
		}
		for _, header := range []string{"Proxy-Authorization", "Referer", "Origin"} {
			r.Header.Del(header)
		}
		return nil
	}}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, original.String(), nil)
	if err != nil {
		return result, webAPIError(400, "invalid_source_url", "invalid source URL")
	}
	httpRequest.Header.Set("User-Agent", "FlClash/typed-content")
	for name, values := range headers {
		httpRequest.Header[name] = append([]string(nil), values...)
	}
	if user != nil {
		password, _ := user.Password()
		httpRequest.SetBasicAuth(user.Username(), password)
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return result, webSourceNetworkError(ctx, err, "source_fetch_failed", "content source request failed")
	}
	defer response.Body.Close()
	result.StatusCode = response.StatusCode
	if values := response.Header.Values("Content-Type"); len(values) == 1 && len(values[0]) <= 1024 {
		if media, _, err := mime.ParseMediaType(values[0]); err == nil {
			result.ContentType = media
		}
	}
	if response.StatusCode == http.StatusTooManyRequests {
		return result, webAPIError(429, "source_rate_limited", "source request was rate limited")
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return result, webAPIError(502, "source_fetch_failed", "content source returned an unsuccessful status")
	}
	limit := webSourceReadLimit(request.Purpose)
	if response.ContentLength > limit {
		return result, webAPIError(413, "content_too_large", "source body exceeds its purpose limit")
	}
	result.Bytes, err = io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		result.Bytes = nil
		return result, webSourceNetworkError(ctx, err, "source_fetch_failed", "source body could not be read")
	}
	if int64(len(result.Bytes)) > limit {
		result.Bytes = nil
		return result, webAPIError(413, "content_too_large", "source body exceeds its purpose limit")
	}
	result.Subscription = webContentSubscription(response.Header.Get("subscription-userinfo"))
	return result, nil
}

func webSourceDependencyPurpose(p WebAPISourcePurpose) bool {
	return p == WebAPISourceMitmResourceImport || p == WebAPISourceCustomProfileDependency
}

func webSourceImportPurpose(p WebAPISourcePurpose) bool {
	switch p {
	case WebAPISourceProfileImport, WebAPISourceScriptImport, WebAPISourceRuleProviderImport, WebAPISourceMitmModuleImport, WebAPISourceMitmResourceImport, WebAPISourceIconSetImport, WebAPISourceCustomProfileImport, WebAPISourceCustomProfileDependency:
		return true
	}
	return false
}
