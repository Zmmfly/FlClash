//go:build webpanel && !android

package core

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func TestWebRuntimeOpenVPNInlineCAValidationRemainsAvailableWithoutFilesystemAuthority(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic inline OpenVPN CA"}, NotBefore: time.Unix(0, 0), NotAfter: time.Unix(2000000000, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}))
	s := newWebRuntimeTestRig(t, WebAPIRuntimeBackend{})
	definition := map[string]any{"name": "inline-openvpn", "type": "openvpn", "server": "192.0.2.1", "port": 1194, "ca": ca, "username": "synthetic-user", "password": "synthetic-password"}
	body, err := json.Marshal(map[string]any{"proxies": []map[string]any{definition}})
	if err != nil {
		t.Fatal(err)
	}
	code, result := s.request(t, "POST", "/api/v1/runtime/proxies:validate", string(body), s.cookie, "")
	var envelope WebAPIEnvelope[[]ValidationIssue]
	if code != 200 || json.Unmarshal(result, &envelope) != nil || len(envelope.Data) != 0 {
		t.Fatalf("legal inline OpenVPN was removed or constructed a filesystem dependency %d %s", code, result)
	}
	definition["ca"] = "/etc/forbidden-host-ca.pem"
	body, _ = json.Marshal(map[string]any{"proxies": []map[string]any{definition}})
	if code, result := s.request(t, "POST", "/api/v1/runtime/proxies:validate", string(body), s.cookie, ""); code != 422 || !bytes.Contains(result, []byte("resource_required")) {
		t.Fatalf("OpenVPN CA host path was admitted %d %s", code, result)
	}
}
