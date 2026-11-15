//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type webDAVTransport struct {
	dav     WebAPISettingsDAV
	origin  *url.URL
	request WebAPISourceRequest
	grant   *WebAPISourceGrant
	options WebAPIDAVOptions
	digest  *webDAVDigest
	before  func(string, *WebAPIDAVResult) error
}

type webDAVResponse struct {
	target *url.URL
	status int
	header http.Header
	bytes  []byte
}

func webDAVTupleDigest(dav WebAPISettingsDAV) string {
	body, _ := json.Marshal(dav)
	return webContentDigest(body)
}

func webDAVBase(dav WebAPISettingsDAV) (*url.URL, string, error) {
	u, digest, err := webSourceCanonical(dav.URI)
	if err != nil {
		return nil, "", webAPIError(400, "dav_configuration_invalid", "committed DAV endpoint is invalid")
	}
	if u.User != nil || u.Opaque != "" || !utf8.ValidString(dav.FileName) || dav.FileName == "" || len(dav.FileName) > 256 || dav.FileName == "." || dav.FileName == ".." || strings.ContainsAny(dav.FileName, "/\\\x00\r\n") || !webDAVHeaderText(dav.User, 4096) || !webDAVHeaderText(dav.Password, 4096) {
		return nil, "", webAPIError(400, "dav_configuration_invalid", "committed DAV configuration is invalid")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." || strings.ContainsAny(segment, "\\\x00\r\n") {
			return nil, "", webAPIError(400, "dav_configuration_invalid", "committed DAV base path is invalid")
		}
	}
	return u, digest, nil
}

func newWebDAVTransport(dav WebAPISettingsDAV, action WebAPIDAVAction, authorizationID string, grant *WebAPISourceGrant, options WebAPIDAVOptions, before func(string, *WebAPIDAVResult) error) (*webDAVTransport, error) {
	base, _, err := webDAVBase(dav)
	if err != nil {
		return nil, err
	}
	purpose := WebAPISourcePurpose("dav-" + string(action))
	if action == WebAPIDAVPrepareRestoreAction || action == WebAPIDAVReconcileAction {
		purpose = "dav-restore"
	}
	request := WebAPISourceRequest{Purpose: purpose, Key: &WebAPISourceResource{Kind: "dav", ID: "settings"}, URL: dav.URI, AuthorizationID: authorizationID, RequestDigest: webDAVTupleDigest(dav)}
	return &webDAVTransport{dav: dav, origin: base, request: request, grant: grant, options: options, before: before}, nil
}

func (t *webDAVTransport) location(collection, root bool) *url.URL {
	u := *t.origin
	segments, escaped := []string{}, []string{}
	for _, segment := range strings.Split(u.EscapedPath(), "/") {
		if segment == "" {
			continue
		}
		decoded, _ := url.PathUnescape(segment)
		segments = append(segments, decoded)
		escaped = append(escaped, url.PathEscape(decoded))
	}
	if !root {
		segments = append(segments, "FlClash")
		escaped = append(escaped, "FlClash")
		if !collection {
			segments = append(segments, t.dav.FileName)
			escaped = append(escaped, url.PathEscape(t.dav.FileName))
		}
	}
	u.Path, u.RawPath = "/"+strings.Join(segments, "/"), "/"+strings.Join(escaped, "/")
	if collection && !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
		u.RawPath += "/"
	}
	u.RawQuery, u.ForceQuery = "", false
	return &u
}

func (t *webDAVTransport) authorize(ctx context.Context, target *url.URL) ([]netip.Addr, error) {
	addresses, private, err := webSourceResolve(ctx, target, WebAPISourceFetchOptions{LookupNetIP: t.options.LookupNetIP})
	if err != nil {
		return nil, err
	}
	if private && !webSourceSameOrigin(t.origin, target) {
		return nil, webAPIError(403, "source_redirect_authorization_required", "DAV redirect requires separate private-network authorization")
	}
	if private || t.grant != nil && webSourceSameOrigin(t.origin, target) {
		now := time.Now()
		if t.options.Now != nil {
			now = t.options.Now()
		}
		_, digest, err := webSourceCanonical(t.dav.URI)
		if err != nil {
			return nil, err
		}
		if err = webSourceValidateGrant(ctx, t.request, t.origin, digest, addresses, t.grant, now); err != nil {
			return nil, err
		}
	}
	return addresses, nil
}

func (t *webDAVTransport) roundTrip(request *http.Request, addresses []netip.Addr) (*http.Response, error) {
	dial := t.options.DialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: 8 * time.Second}).DialContext
	}
	_, _, port := webSourceOrigin(request.URL)
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: t.options.TLSRootCAs}, TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 15 * time.Second, MaxResponseHeaderBytes: 32 << 10, ExpectContinueTimeout: time.Second}
	defer transport.CloseIdleConnections()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		for _, ip := range addresses {
			connection, err := dial(ctx, network, net.JoinHostPort(ip.String(), strconv.Itoa(int(port))))
			if err == nil {
				return connection, nil
			}
			if ctx.Err() != nil {
				break
			}
		}
		return nil, errors.New("DAV connection failed")
	}
	return transport.RoundTrip(request)
}

func webDAVFixedMethod(method string) bool {
	switch method {
	case http.MethodOptions, http.MethodHead, http.MethodGet, http.MethodPut, "MKCOL":
		return true
	}
	return false
}

func webDAVTransportError(err error, uncertain bool) error {
	if uncertain {
		return webAPIError(409, "dav_effect_unknown", "DAV write outcome is unknown; automatic upload retry is prohibited")
	}
	var problem *WebAPIError
	if errors.As(err, &problem) {
		return problem
	}
	return webAPIError(502, "dav_request_failed", "DAV request failed")
}

func (t *webDAVTransport) send(ctx context.Context, method string, target *url.URL, body []byte, condition string, createOnly bool, stage string) (webDAVResponse, error) {
	var result webDAVResponse
	if !webDAVFixedMethod(method) {
		return result, webAPIError(400, "dav_method_invalid", "unsupported DAV operation")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	u := *target
	credentials := webSourceSameOrigin(t.origin, target)
	redirects, authRetries := 0, 0
	for {
		if t.before != nil {
			if err := t.before(stage, nil); err != nil {
				return result, err
			}
		}
		addresses, err := t.authorize(ctx, &u)
		if err != nil {
			return result, err
		}
		request, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
		if err != nil {
			return result, webAPIError(400, "dav_configuration_invalid", "invalid DAV request")
		}
		request.GetBody = nil
		request.Header.Set("User-Agent", "FlClash/typed-dav")
		request.Header.Set("Accept-Encoding", "identity")
		if method == http.MethodPut {
			request.Header.Set("Content-Type", "application/zip")
			if condition != "" {
				request.Header.Set("If-Match", condition)
			}
			if createOnly {
				request.Header.Set("If-None-Match", "*")
			}
		}
		if credentials && webSourceSameOrigin(t.origin, &u) && (t.dav.User != "" || t.dav.Password != "") {
			if t.digest == nil {
				request.SetBasicAuth(t.dav.User, t.dav.Password)
			} else {
				authorization, err := t.digest.authorization(t.dav.User, t.dav.Password, method, &u)
				if err != nil {
					return result, err
				}
				request.Header.Set("Authorization", authorization)
			}
		}
		if t.before != nil {
			if err = t.before(stage, nil); err != nil {
				return result, err
			}
		}
		release, err := WebAPIAcquireCommitAuthorization(ctx)
		if err != nil {
			return result, err
		}
		release()
		response, err := t.roundTrip(request, addresses)
		if err != nil {
			return result, webDAVTransportError(err, method == http.MethodPut || method == "MKCOL")
		}
		status := response.StatusCode
		header := response.Header.Clone()
		if status == http.StatusUnauthorized && credentials && webSourceSameOrigin(t.origin, &u) && authRetries < 2 {
			digest := webDAVDigestChallenge(header.Values("WWW-Authenticate"))
			if digest != nil && (t.digest == nil || digest.nonce != t.digest.nonce || digest.realm != t.digest.realm || digest.algorithm != t.digest.algorithm) && (authRetries == 0 || digest.stale) {
				response.Body.Close()
				t.digest = digest
				authRetries++
				continue
			}
		}
		if status == 301 || status == 302 || status == 303 || status == 307 || status == 308 {
			response.Body.Close()
			if redirects >= 5 {
				return result, webAPIError(502, "dav_redirect_limit", "DAV redirect limit exceeded")
			}
			location := header.Get("Location")
			if location == "" || len(location) > 8192 {
				return result, webAPIError(502, "dav_redirect_invalid", "invalid DAV redirect")
			}
			next, err := u.Parse(location)
			if err != nil {
				return result, webAPIError(502, "dav_redirect_invalid", "invalid DAV redirect")
			}
			if next.User != nil || next.Fragment != "" || next.Opaque != "" {
				return result, webAPIError(403, "dav_redirect_forbidden", "DAV redirect carries unsupported authority")
			}
			if u.Scheme == "https" && next.Scheme != "https" {
				return result, webAPIError(403, "dav_downgrade_forbidden", "DAV HTTPS downgrade is forbidden")
			}
			canonical, _, err := webSourceCanonical(next.String())
			if err != nil {
				return result, webAPIError(403, "dav_redirect_forbidden", "DAV redirect target is invalid")
			}
			if method == http.MethodPut && condition != "" && canonical.String() != u.String() {
				return result, webAPIError(409, "dav_remote_conflict", "DAV conditional upload target changed after its entity tag was observed")
			}
			if !webSourceSameOrigin(t.origin, canonical) {
				credentials = false
			}
			u = *canonical
			redirects++
			continue
		}
		finalURL := u
		result.target = &finalURL
		result.status, result.header = status, header
		if method != http.MethodGet || status != 200 && status != 206 {
			response.Body.Close()
			return result, nil
		}
		if value := header.Get("Content-Encoding"); value != "" && !strings.EqualFold(value, "identity") {
			response.Body.Close()
			return result, webAPIError(400, "dav_encoding_unsupported", "DAV archive encoding is unsupported")
		}
		if response.ContentLength > WebAPIDAVMaxArchiveBytes {
			response.Body.Close()
			return result, webAPIError(413, "dav_archive_too_large", "DAV archive exceeds the transfer limit")
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, WebAPIDAVMaxArchiveBytes+1))
		response.Body.Close()
		if err != nil {
			return result, webDAVTransportError(err, false)
		}
		if len(data) > WebAPIDAVMaxArchiveBytes {
			return result, webAPIError(413, "dav_archive_too_large", "DAV archive exceeds the transfer limit")
		}
		if status == 206 && !webDAVWholeRange(header.Get("Content-Range"), len(data)) {
			return result, webAPIError(502, "dav_partial_archive", "DAV returned an incomplete archive")
		}
		result.bytes = data
		return result, nil
	}
}

func webDAVWholeRange(value string, length int) bool {
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bytes") {
		return false
	}
	rangePart, totalText, ok := strings.Cut(parts[1], "/")
	if !ok {
		return false
	}
	startText, endText, ok := strings.Cut(rangePart, "-")
	if !ok {
		return false
	}
	start, e1 := strconv.ParseUint(startText, 10, 64)
	end, e2 := strconv.ParseUint(endText, 10, 64)
	total, e3 := strconv.ParseUint(totalText, 10, 64)
	return e1 == nil && e2 == nil && e3 == nil && start == 0 && total > 0 && end == total-1 && total == uint64(length)
}

func webDAVStatus(response webDAVResponse, acceptable ...int) error {
	for _, code := range acceptable {
		if response.status == code {
			return nil
		}
	}
	if response.status == 401 || response.status == 403 {
		return webAPIError(502, "dav_auth_failed", "DAV authentication or access was rejected")
	}
	if response.status == 409 || response.status == 412 {
		return webAPIError(409, "dav_remote_conflict", "DAV remote state changed; upload was not confirmed")
	}
	return webAPIError(502, "dav_status_failed", "DAV server returned an unsuccessful status")
}

func webDAVETag(header http.Header) (string, error) {
	values := header.Values("ETag")
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 {
		return "", webAPIError(502, "dav_etag_invalid", "DAV returned an invalid entity tag")
	}
	value := strings.TrimSpace(values[0])
	if !webDAVHeaderText(value, 1024) || len(value) < 2 {
		return "", webAPIError(502, "dav_etag_invalid", "DAV returned an invalid entity tag")
	}
	etag := strings.TrimPrefix(value, "W/")
	if len(etag) < 2 || etag[0] != '"' || etag[len(etag)-1] != '"' || strings.ContainsRune(etag[1:len(etag)-1], '"') {
		return "", webAPIError(502, "dav_etag_invalid", "DAV returned an invalid entity tag")
	}
	return value, nil
}

func (t *webDAVTransport) probe(ctx context.Context) (WebAPIDAVResult, error) {
	response, err := t.send(ctx, http.MethodOptions, t.location(true, true), nil, "", false, "probing")
	if err == nil {
		err = webDAVStatus(response, 200, 204)
	}
	return WebAPIDAVResult{Reachable: err == nil}, err
}

func (t *webDAVTransport) backup(ctx context.Context, archive []byte) (WebAPIDAVResult, error) {
	result := WebAPIDAVResult{ArchiveDigest: webContentDigest(archive), ArchiveSize: len(archive)}
	if len(archive) > WebAPIDAVMaxArchiveBytes {
		return result, webAPIError(413, "dav_archive_too_large", "DAV archive exceeds the transfer limit")
	}
	collection, err := t.send(ctx, "MKCOL", t.location(true, false), nil, "", false, "creating-collection")
	if err != nil {
		return result, err
	}
	if err = webDAVStatus(collection, 200, 201, 204, 405); err != nil {
		return result, err
	}
	head, err := t.send(ctx, http.MethodHead, t.location(false, false), nil, "", false, "checking-remote")
	if err != nil {
		return result, err
	}
	if err = webDAVStatus(head, 200, 204, 404, 405, 501); err != nil {
		return result, err
	}
	etag, err := webDAVETag(head.header)
	if err != nil {
		return result, err
	}
	condition := ""
	if (head.status == 200 || head.status == 204) && etag != "" {
		if strings.HasPrefix(etag, "W/") {
			return result, webAPIError(409, "dav_weak_etag", "DAV entity tag cannot safely guard an overwrite")
		}
		condition = etag
	}
	createOnly := head.status == 404
	result.ETag = condition
	result.Conditional = condition != "" || createOnly
	if t.before != nil {
		if err = t.before("put-ready", &result); err != nil {
			return result, err
		}
	}
	response, err := t.send(ctx, http.MethodPut, head.target, archive, condition, createOnly, "put-sending")
	if err != nil {
		return result, err
	}
	if err = webDAVStatus(response, 200, 201, 204); err != nil {
		if response.status >= 500 || response.status == 408 {
			return result, webAPIError(409, "dav_effect_unknown", "DAV server did not establish the write outcome; automatic upload retry is prohibited")
		}
		return result, err
	}
	result.ETag, err = webDAVETag(response.header)
	if err != nil {
		return result, webAPIError(409, "dav_effect_unknown", "DAV write response is incomplete; automatic upload retry is prohibited")
	}
	return result, nil
}

func (t *webDAVTransport) download(ctx context.Context) ([]byte, WebAPIDAVResult, error) {
	response, err := t.send(ctx, http.MethodGet, t.location(false, false), nil, "", false, "downloading")
	if err == nil {
		err = webDAVStatus(response, 200, 206)
	}
	if err != nil {
		return nil, WebAPIDAVResult{}, err
	}
	etag, err := webDAVETag(response.header)
	if err != nil {
		return nil, WebAPIDAVResult{}, err
	}
	return response.bytes, WebAPIDAVResult{ETag: etag, ArchiveDigest: webContentDigest(response.bytes), ArchiveSize: len(response.bytes)}, nil
}
