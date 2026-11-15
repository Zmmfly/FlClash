//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type webProxyRequest struct {
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	Headers         map[string]string `json:"headers"`
	BodyBase64      string            `json:"bodyBase64"`
	TimeoutMs       int               `json:"timeoutMs"`
	ProxyURL        string            `json:"proxyUrl"`
	FollowRedirects *bool             `json:"followRedirects"`
	SavePath        string            `json:"savePath"`
}

type webProxyResponse struct {
	OK          bool                `json:"ok"`
	StatusCode  int                 `json:"statusCode"`
	StatusText  string              `json:"statusText"`
	Headers     map[string][]string `json:"headers"`
	BodyBase64  string              `json:"bodyBase64,omitempty"`
	SavedSize   int64               `json:"savedSize,omitempty"`
	SavedMtime  int64               `json:"savedMtime,omitempty"`
	InlinedBody bool                `json:"inlinedBody"`
	Error       string              `json:"error,omitempty"`
}

func (s *WebServer) serveProxy(w http.ResponseWriter, r *http.Request) {
	var params webProxyRequest
	if json.NewDecoder(r.Body).Decode(&params) != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	upstream, err := url.Parse(params.URL)
	if err != nil || (upstream.Scheme != "http" && upstream.Scheme != "https") || upstream.Host == "" || upstream.User != nil {
		http.Error(w, "invalid upstream URL", 400)
		return
	}
	if params.SavePath != "" {
		if _, err := s.virtualPath(params.SavePath, true); err != nil {
			http.Error(w, "invalid virtual path", 400)
			return
		}
	}
	body, err := base64.StdEncoding.DecodeString(params.BodyBase64)
	if err != nil {
		http.Error(w, "invalid body", 400)
		return
	}
	method := strings.ToUpper(params.Method)
	if method == "" {
		method = http.MethodGet
	}
	timeout := 60 * time.Second
	if params.TimeoutMs > 0 && params.TimeoutMs < 60000 {
		timeout = time.Duration(params.TimeoutMs) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, upstream.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	for key, value := range params.Headers {
		switch strings.ToLower(key) {
		case "cookie", "proxy-authorization", "host", "origin", "connection", "upgrade", "transfer-encoding":
			continue
		}
		request.Header.Set(key, value)
	}
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, MaxResponseHeaderBytes: 1 << 20}
	defer transport.CloseIdleConnections()
	if params.ProxyURL != "" && !strings.EqualFold(params.ProxyURL, "DIRECT") {
		raw := strings.TrimSpace(params.ProxyURL)
		if strings.HasPrefix(strings.ToUpper(raw), "PROXY ") {
			raw = strings.TrimSpace(strings.Split(raw[6:], ";")[0])
		}
		if !strings.Contains(raw, "://") {
			raw = "http://" + raw
		}
		proxy, err := url.Parse(raw)
		if err != nil || proxy.Host == "" || (proxy.Scheme != "http" && proxy.Scheme != "https" && proxy.Scheme != "socks5" && proxy.Scheme != "socks5h") {
			http.Error(w, "invalid proxy URL", 400)
			return
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	client := &http.Client{Transport: transport, Timeout: timeout}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if params.FollowRedirects != nil && !*params.FollowRedirects {
			return http.ErrUseLastResponse
		}
		if len(via) >= 10 {
			return http.ErrUseLastResponse
		}
		if request.URL.Scheme != "http" && request.URL.Scheme != "https" {
			return http.ErrUseLastResponse
		}
		return nil
	}
	response, err := client.Do(request)
	if err != nil {
		webJSON(w, webProxyResponse{Error: "upstream request failed"})
		return
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, webMaxBody+1))
	if err != nil || len(data) > webMaxBody {
		webJSON(w, webProxyResponse{Error: "upstream response too large or unreadable"})
		return
	}
	headers := map[string][]string{}
	for key, values := range response.Header {
		headers[strings.ToLower(key)] = values
	}
	result := webProxyResponse{OK: true, StatusCode: response.StatusCode, StatusText: response.Status, Headers: headers}
	if params.SavePath != "" {
		s.ioMu.Lock()
		p, err := s.virtualPath(params.SavePath, true)
		if err == nil {
			info, writeErr := webWriteFile(p, data, false)
			err = writeErr
			if info != nil {
				result.SavedSize = info.Size()
				result.SavedMtime = info.ModTime().UnixMilli()
			}
		}
		s.ioMu.Unlock()
		if err != nil {
			webJSON(w, webProxyResponse{Error: "destination unavailable"})
			return
		}
		if webInline(params.SavePath, int64(len(data))) {
			result.BodyBase64 = base64.StdEncoding.EncodeToString(data)
			result.InlinedBody = true
		}
	} else {
		result.BodyBase64 = base64.StdEncoding.EncodeToString(data)
		result.InlinedBody = true
	}
	webJSON(w, result)
}
