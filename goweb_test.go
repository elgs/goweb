package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSelfSignedCert writes a self-signed certificate and key for name into
// dir and returns their paths.
func writeSelfSignedCert(t *testing.T, dir, name string) (certPath, keyPath string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	certPath = filepath.Join(dir, name+".crt")
	keyPath = filepath.Join(dir, name+".key")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

// A host whose certificate files are missing must degrade that one host, not
// take the listener down with it: the server starts and the remaining hosts
// keep serving. This is the regression where one certificate deleted by a
// renewal turned into an outage of every site on the port.
func TestHTTPSStartsWithOneBadCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSignedCert(t, dir, "good.example.com")

	goodHost := &Host{
		Name:        "good.example.com",
		Type:        "301_redirect",
		RedirectURL: "https://elsewhere.example.com",
		CertPath:    certPath,
		KeyPath:     keyPath,
	}
	badHost := &Host{
		Name:        "bad.example.com",
		Type:        "301_redirect",
		RedirectURL: "https://elsewhere.example.com",
		CertPath:    filepath.Join(dir, "missing.crt"),
		KeyPath:     filepath.Join(dir, "missing.key"),
	}
	server := &Server{
		Name:   "test-https",
		Type:   "https",
		Listen: "127.0.0.1:0",
		Hosts:  []*Host{goodHost, badHost},
	}

	if err := server.Start(); err != nil {
		t.Fatalf("Start() = %v, want nil: one bad certificate must not fail the server", err)
	}
	defer server.Shutdown()

	if badHost.Status == "" {
		t.Error("bad host Status is empty, want the certificate error recorded")
	}
	if server.Status != "" {
		t.Errorf("server Status = %q, want empty", server.Status)
	}

	addr := server.listener.Addr().String()
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			Dial: func(network, _ string) (net.Conn, error) {
				return net.Dial(network, addr)
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Get("https://good.example.com/some/path")
	if err != nil {
		t.Fatalf("request to remaining host failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Errorf("status = %v, want %v", resp.StatusCode, http.StatusMovedPermanently)
	}
	if got, want := resp.Header.Get("Location"), "https://elsewhere.example.com/some/path"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// With no loadable certificate at all the server must fail before the port is
// bound — otherwise ServeTLS fails after the listen and clients hang in the
// accept backlog instead of being refused.
func TestHTTPSFailsWithNoUsableCertificate(t *testing.T) {
	dir := t.TempDir()
	server := &Server{
		Name:   "test-https",
		Type:   "https",
		Listen: "127.0.0.1:0",
		Hosts: []*Host{{
			Name:        "only.example.com",
			Type:        "301_redirect",
			RedirectURL: "https://elsewhere.example.com",
			CertPath:    filepath.Join(dir, "missing.crt"),
			KeyPath:     filepath.Join(dir, "missing.key"),
		}},
	}

	err := server.Start()
	if err == nil {
		server.Shutdown()
		t.Fatal("Start() = nil, want error when no certificate is usable")
	}
	if !strings.Contains(err.Error(), "No usable certificate") {
		t.Errorf("error = %q, want it to mention the missing certificates", err)
	}
	if server.listener != nil {
		t.Error("listener is non-nil, want the port left unbound on failure")
	}
}
