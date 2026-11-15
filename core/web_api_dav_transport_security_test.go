//go:build webpanel && !android

package core

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func webDAVTLSFixture(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic DAV fixture"}, DNSNames: []string{"dav.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return cert, pool
}

func TestWebDAVTLSVerificationAndDowngradeHardDenial(t *testing.T) {
	cert, roots := webDAVTLSFixture(t)
	for _, trusted := range []bool{false, true} {
		var requests, dials atomic.Int32
		options := WebAPIDAVOptions{LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}, DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
			dials.Add(1)
			if address != "8.8.8.8:443" {
				t.Error("HTTPS redirect reached plaintext dial")
			}
			client, server := net.Pipe()
			go func() {
				defer server.Close()
				secure := tls.Server(server, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
				request, err := http.ReadRequest(bufio.NewReader(secure))
				if err != nil {
					return
				}
				requests.Add(1)
				response := webDAVTestResponse(307, http.Header{"Location": {"http://dav.test/downgrade"}}, nil)
				response.Request = request
				_ = response.Write(secure)
			}()
			return client, nil
		}}
		if trusted {
			options.TLSRootCAs = roots
		}
		transport, err := newWebDAVTransport(WebAPISettingsDAV{URI: "https://dav.test/base", User: "synthetic-user", Password: "synthetic-password", FileName: "backup.zip"}, WebAPIDAVProbeAction, "", nil, options, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = transport.probe(t.Context())
		if trusted {
			webDAVErrorCode(t, err, "dav_downgrade_forbidden")
			if requests.Load() != 1 {
				t.Fatal("TLS fixture did not receive probe")
			}
		} else {
			webDAVErrorCode(t, err, "dav_request_failed")
			if requests.Load() != 0 {
				t.Fatal("untrusted TLS received credentials")
			}
		}
		if dials.Load() != 1 {
			t.Fatal("redirect or TLS failure caused another dial")
		}
	}
}

func TestWebDAVPUTDeadlineIsUnknownWithoutRetry(t *testing.T) {
	entered, finish := make(chan struct{}), make(chan struct{})
	puts := 0
	wire := &webDAVTestWire{handle: func(r *http.Request, _ []byte) *http.Response {
		if r.Method == "HEAD" {
			return webDAVTestResponse(404, nil, nil)
		}
		if r.Method == "PUT" {
			puts++
			close(entered)
			<-finish
			return nil
		}
		return webDAVTestResponse(204, nil, nil)
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := webDAVTestTransport(t, wire, WebAPIDAVBackupAction).backup(ctx, []byte("archive"))
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(finish)
		t.Fatal("PUT did not enter")
	}
	select {
	case err := <-done:
		webDAVErrorCode(t, err, "dav_effect_unknown")
	case <-time.After(3 * time.Second):
		close(finish)
		t.Fatal("PUT cancellation did not finish")
	}
	close(finish)
	if puts != 1 {
		t.Fatal("deadline retried upload")
	}
}

func TestWebDAVDownloadExact32MiBBudgetAndStreamingOverrun(t *testing.T) {
	for _, size := range []int{WebAPIDAVMaxArchiveBytes, WebAPIDAVMaxArchiveBytes + 1} {
		wire := &webDAVTestWire{handle: func(*http.Request, []byte) *http.Response {
			response := webDAVTestResponse(200, nil, nil)
			response.ContentLength = -1
			response.Body = io.NopCloser(io.LimitReader(webDAVZeroReader{}, int64(size)))
			return response
		}}
		data, _, err := webDAVTestTransport(t, wire, WebAPIDAVPrepareRestoreAction).download(t.Context())
		if size == WebAPIDAVMaxArchiveBytes {
			if err != nil || len(data) != size {
				t.Fatalf("32MiB capability: %d %v", len(data), err)
			}
		} else {
			webDAVErrorCode(t, err, "dav_archive_too_large")
		}
	}
}

type webDAVZeroReader struct{}

func (webDAVZeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
