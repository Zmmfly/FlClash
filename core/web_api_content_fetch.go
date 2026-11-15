//go:build webpanel && !android

package core

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func webContentURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 8192 || u.Hostname() == "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, webAPIError(400, "invalid_source_url", "source must be an HTTP(S) URL without a fragment")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, webAPIError(400, "invalid_source_url", "source URL has an invalid port")
		}
	}
	return u, nil
}

func webContentPublicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	for _, block := range []string{"0.0.0.0/8", "100.64.0.0/10", "169.254.0.0/16", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "::/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/32", "2001:db8::/32", "2002::/16", "fec0::/10"} {
		if netip.MustParsePrefix(block).Contains(ip) {
			return false
		}
	}
	return true
}

func webContentHTTPClient() *http.Client {
	transport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 32 << 10, IdleConnTimeout: 30 * time.Second, MaxIdleConns: 4, MaxConnsPerHost: 4}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("invalid source address")
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(addresses) == 0 {
			return nil, errors.New("source name could not be resolved")
		}
		for _, ip := range addresses {
			if !webContentPublicAddress(ip) {
				return nil, webAPIError(400, "source_address_forbidden", "source resolved to a non-public address")
			}
		}
		dialer := net.Dialer{Timeout: 10 * time.Second}
		for _, ip := range addresses {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
		}
		return nil, errors.New("source connection failed")
	}
	return &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many source redirects")
		}
		if len(via) > 0 && (r.URL.Host != via[0].URL.Host || r.URL.Scheme != via[0].URL.Scheme) {
			r.Header.Del("Authorization")
		}
		r.Header.Del("Cookie")
		if r.URL.User != nil {
			return errors.New("redirect credentials are not accepted")
		}
		_, err := webContentURL(r.URL.String())
		return err
	}}
}

type webContentFetched struct {
	bytes        []byte
	subscription *WebAPISubscriptionInfo
}

func webContentFetch(ctx context.Context, client *http.Client, source string, limit int64) (webContentFetched, error) {
	var out webContentFetched
	u, err := webContentURL(source)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	user := u.User
	u.User = nil
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return out, webAPIError(400, "invalid_source_url", "invalid source URL")
	}
	if user != nil {
		password, _ := user.Password()
		req.SetBasicAuth(user.Username(), password)
	}
	req.Header.Set("User-Agent", "FlClash/typed-content")
	resp, err := client.Do(req)
	if err != nil {
		return out, webAPIError(502, "source_fetch_failed", "content source request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return out, webAPIError(502, "source_fetch_failed", "content source returned an unsuccessful status")
	}
	if resp.ContentLength > limit {
		return out, webAPIError(413, "content_too_large", "content source exceeds the resource limit")
	}
	out.bytes, err = webContentReadLimit(resp.Body, limit)
	if err != nil {
		return out, err
	}
	out.subscription = webContentSubscription(resp.Header.Get("subscription-userinfo"))
	return out, nil
}

func webContentSubscription(s string) *WebAPISubscriptionInfo {
	if s == "" || len(s) > 8192 {
		return nil
	}
	values := map[string]WebAPIInt64{}
	for _, field := range strings.Split(s, ";") {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || n < 0 {
			continue
		}
		values[key] = WebAPIInt64(strconv.FormatInt(n, 10))
	}
	result := &WebAPISubscriptionInfo{Upload: "0", Download: "0", Total: "0", Expire: "0"}
	if v, ok := values["upload"]; ok {
		result.Upload = v
	}
	if v, ok := values["download"]; ok {
		result.Download = v
	}
	if v, ok := values["total"]; ok {
		result.Total = v
	}
	if v, ok := values["expire"]; ok {
		result.Expire = v
	}
	return result
}

func webContentDecompress(data []byte) ([]byte, error) {
	var reader io.ReadCloser
	var err error
	switch {
	case bytes.HasPrefix(data, []byte{0x1f, 0x8b}):
		reader, err = gzip.NewReader(bytes.NewReader(data))
	case len(data) > 1 && data[0] == 0x78 && (uint16(data[0])<<8|uint16(data[1]))%31 == 0:
		reader, err = zlib.NewReader(bytes.NewReader(data))
	default:
		return data, nil
	}
	if err != nil {
		return nil, webAPIError(400, "invalid_compressed_content", "content has invalid compression")
	}
	defer reader.Close()
	return webContentReadLimit(reader, WebAPIMaxContentBytes)
}
