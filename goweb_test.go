package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain stands up the loggers, since only main() calls setupLogging:
// diagnostics go nowhere, and the access logger needs a destination before any
// server runs with AccessLog on. Tests that assert on log output swap these and
// restore them.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	accessLog = slog.New(slog.NewTextHandler(io.Discard, nil))
	os.Exit(m.Run())
}

// ---- shared helpers --------------------------------------------------------

// newTestClient returns a client that reaches addr whatever host name the
// request URL carries, so tests can drive virtual host routing without DNS.
func newTestClient(addr string, useTLS bool) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, addr)
		},
	}
	if useTLS {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &http.Client{
		Transport: transport,
		// redirects are the thing under test in several cases, so keep them
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
}

// startTestServer starts an http/https server on its configured ephemeral port
// and returns a client pointed at it.
func startTestServer(t *testing.T, server *Server) *http.Client {
	t.Helper()
	if err := server.Start(); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}
	t.Cleanup(func() { server.Shutdown() })
	return newTestClient(server.listener.Addr().String(), server.Type == "https")
}

// get issues a request and fails the test if it cannot be made at all.
func get(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %v: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// bodyString reads a response body as a string.
func bodyString(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	return string(b)
}

// errField returns the "err" member of a JSON error response.
func errField(t *testing.T, resp *http.Response) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal([]byte(bodyString(t, resp)), &body); err != nil {
		t.Fatalf("decoding error response: %v", err)
	}
	return body["err"]
}

// syncBuffer collects log output written from connection goroutines.
type syncBuffer struct {
	mu    sync.Mutex
	lines []string
}

func (this *syncBuffer) Write(p []byte) (int, error) {
	this.mu.Lock()
	defer this.mu.Unlock()
	this.lines = append(this.lines, strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func (this *syncBuffer) records() []string {
	this.mu.Lock()
	defer this.mu.Unlock()
	return append([]string(nil), this.lines...)
}

// captureAccessLog redirects the access log into a buffer as JSON, so tests can
// assert on individual fields.
func captureAccessLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	old := accessLog
	t.Cleanup(func() { accessLog = old })
	accessLog = slog.New(slog.NewJSONHandler(buf, nil))
	return buf
}

// parseRecord decodes one JSON log record.
func parseRecord(t *testing.T, line string) map[string]any {
	t.Helper()
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("parsing log record %q: %v", line, err)
	}
	return record
}

// waitFor polls until cond holds, for records written on a goroutine the test
// does not synchronise with.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %v", what)
}

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

// redirectHost is the simplest host type to stand up: no files, no upstream.
func redirectHost(name string) *Host {
	return &Host{Name: name, Type: "301_redirect", RedirectURL: "https://elsewhere.example.com"}
}

// ---- certificates ----------------------------------------------------------

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

// A disabled host is never served, so a certificate it can no longer load is
// expected rather than an error, and must not stop the server.
func TestHTTPSStartsWithDisabledHostMissingCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSignedCert(t, dir, "good.example.com")
	server := &Server{
		Name:   "test-https",
		Type:   "https",
		Listen: "127.0.0.1:0",
		Hosts: []*Host{
			{Name: "good.example.com", Type: "301_redirect", RedirectURL: "https://elsewhere.example.com", CertPath: certPath, KeyPath: keyPath},
			{Name: "gone.example.com", Type: "301_redirect", Disabled: true, CertPath: filepath.Join(dir, "missing.crt"), KeyPath: filepath.Join(dir, "missing.key")},
		},
	}

	if err := server.Start(); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}
	server.Shutdown()
}

// ---- host and address helpers ----------------------------------------------

func TestNormalizeHost(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"example.com", "example.com"},
		{"Example.COM", "example.com"},
		{"example.com:8080", "example.com"},
		{"EXAMPLE.com:443", "example.com"},
		{"[::1]:443", "::1"},
		{"[::1]", "::1"},
		{"[2001:DB8::1]:80", "2001:db8::1"},
		{"127.0.0.1:80", "127.0.0.1"},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeHost(c.in); got != c.want {
			t.Errorf("normalizeHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"127.0.0.1:54321", "127.0.0.1"},
		{"[::1]:54321", "::1"},
		{"10.0.0.1", "10.0.0.1"}, // no port: use it as-is rather than dropping it
		{"", ""},
	}
	for _, c := range cases {
		if got := clientIP(c.in); got != c.want {
			t.Errorf("clientIP(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHashIndex(t *testing.T) {
	// A client must always land on the same slot: that is what makes load
	// balancing sticky per client rather than per connection.
	for _, s := range []string{"", "127.0.0.1", "::1", "10.0.0.7"} {
		for n := 1; n <= 8; n++ {
			got := hashIndex(s, n)
			if got < 0 || got >= n {
				t.Errorf("hashIndex(%q, %v) = %v, want within [0, %v)", s, n, got, n)
			}
			if again := hashIndex(s, n); again != got {
				t.Errorf("hashIndex(%q, %v) = %v then %v, want a stable result", s, n, got, again)
			}
		}
	}
	// Different clients must spread over the slots, or every one of them would
	// pile onto a single upstream.
	seen := map[int]bool{}
	for i := 0; i < 200; i++ {
		seen[hashIndex(fmt.Sprintf("10.0.0.%v", i), 4)] = true
	}
	if len(seen) != 4 {
		t.Errorf("200 clients covered %v of 4 slots, want all of them", len(seen))
	}
}

func TestCanonicalHostPort(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"http://example.com", "example.com:80"},
		{"https://example.com", "example.com:443"},
		{"http://example.com:8080", "example.com:8080"},
		{"https://EXAMPLE.com:8443", "example.com:8443"},
		{"http://[::1]:8080", "[::1]:8080"},
	}
	for _, c := range cases {
		u, err := url.Parse(c.in)
		if err != nil {
			t.Fatalf("parsing %q: %v", c.in, err)
		}
		if got := canonicalHostPort(u); got != c.want {
			t.Errorf("canonicalHostPort(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRewriteLocation(t *testing.T) {
	cases := []struct {
		name     string
		target   string
		location string
		want     string
	}{
		{"upstream absolute becomes relative", "http://up.internal:8080", "http://up.internal:8080/next?q=1#f", "/next?q=1#f"},
		{"bare upstream origin becomes root", "http://up.internal:8080", "http://up.internal:8080", "/"},
		{"default port matches explicit port", "http://up.internal", "http://up.internal:80/next", "/next"},
		{"host case is ignored", "http://up.internal:8080", "http://UP.Internal:8080/next", "/next"},
		{"credentials are dropped with the origin", "http://up.internal:8080", "http://user:pass@up.internal:8080/next", "/next"},
		{"another host is left alone", "http://up.internal:8080", "http://other.example.com/next", "http://other.example.com/next"},
		{"another port is left alone", "http://up.internal:8080", "http://up.internal:9090/next", "http://up.internal:9090/next"},
		{"relative location is left alone", "http://up.internal:8080", "/already/relative", "/already/relative"},
		{"unparseable location is left alone", "http://up.internal:8080", "://nope", "://nope"},
		{"absent location stays absent", "http://up.internal:8080", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target, err := url.Parse(c.target)
			if err != nil {
				t.Fatalf("parsing target: %v", err)
			}
			res := &http.Response{Header: http.Header{}}
			if c.location != "" {
				res.Header.Set("Location", c.location)
			}
			rewriteLocation(res, target)
			if got := res.Header.Get("Location"); got != c.want {
				t.Errorf("Location = %q, want %q", got, c.want)
			}
		})
	}
}

func TestIndexFileNotExists(t *testing.T) {
	dir := t.TempDir()
	withIndex := filepath.Join(dir, "with")
	withoutIndex := filepath.Join(dir, "without")
	indexIsDir := filepath.Join(dir, "weird")
	for _, d := range []string{withIndex, withoutIndex, filepath.Join(indexIsDir, "index.html")} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(withIndex, "index.html"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		dir  string
		want bool
	}{
		{"index file present", withIndex, false},
		{"index file absent", withoutIndex, true},
		{"index is a directory", indexIsDir, true},
		{"directory does not exist", filepath.Join(dir, "nope"), true},
	}
	for _, c := range cases {
		if got := indexFileNotExists(c.dir); got != c.want {
			t.Errorf("%v: indexFileNotExists() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestGetEnv(t *testing.T) {
	if got := getEnv("GOWEB_TEST_UNSET_VAR", "fallback"); got != "fallback" {
		t.Errorf("getEnv(unset) = %q, want %q", got, "fallback")
	}
	t.Setenv("GOWEB_TEST_VAR", "value")
	if got := getEnv("GOWEB_TEST_VAR", "fallback"); got != "value" {
		t.Errorf("getEnv(set) = %q, want %q", got, "value")
	}
	// An explicitly empty variable is a choice, not an absence.
	t.Setenv("GOWEB_TEST_VAR", "")
	if got := getEnv("GOWEB_TEST_VAR", "fallback"); got != "" {
		t.Errorf("getEnv(set to empty) = %q, want %q", got, "")
	}
}

// ---- server lifecycle ------------------------------------------------------

func TestStartRejectsInvalidServers(t *testing.T) {
	cases := []struct {
		name   string
		server *Server
		want   string
	}{
		{
			name:   "no name",
			server: &Server{Type: "http", Listen: "127.0.0.1:0"},
			want:   "name is required",
		},
		{
			name:   "unknown type",
			server: &Server{Name: "odd", Type: "gopher", Listen: "127.0.0.1:0"},
			want:   "Unknown server type",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.server.Start()
			if err == nil {
				c.server.Shutdown()
				t.Fatal("Start() = nil, want an error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, want it to mention %q", err, c.want)
			}
			if c.server.Status != err.Error() {
				t.Errorf("Status = %q, want it to match the error %q", c.server.Status, err)
			}
			if c.server.listener != nil {
				t.Error("listener is non-nil, want no port bound for an invalid server")
			}
		})
	}
}

func TestStartSkipsDisabledServer(t *testing.T) {
	server := &Server{Name: "off", Type: "http", Listen: "127.0.0.1:0", Disabled: true, Hosts: []*Host{redirectHost("a.example.com")}}
	if err := server.Start(); err != nil {
		t.Fatalf("Start() = %v, want nil for a disabled server", err)
	}
	if server.listener != nil {
		t.Error("listener is non-nil, want a disabled server to bind nothing")
	}
}

func TestStartRejectsInvalidHosts(t *testing.T) {
	cases := []struct {
		name string
		host *Host
		want string
	}{
		{"no name", &Host{Type: "serve_static", Path: "."}, "name is required"},
		{"unknown type", &Host{Name: "a.example.com", Type: "carrier_pigeon"}, "Unknown host type"},
		{"reverse proxy without upstreams", &Host{Name: "a.example.com", Type: "reverse_proxy"}, "no forward URLs"},
		{"reverse proxy with an unusable upstream", &Host{Name: "a.example.com", Type: "reverse_proxy", ForwardURLs: "ftp://files.example.com"}, "invalid forward URL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := &Server{Name: "edge", Type: "http", Listen: "127.0.0.1:0", Hosts: []*Host{c.host}}
			err := server.Start()
			if err == nil {
				server.Shutdown()
				t.Fatal("Start() = nil, want an error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, want it to mention %q", err, c.want)
			}
			if c.host.Status == "" {
				t.Error("host Status is empty, want the reason recorded for the admin UI")
			}
			// Host configuration is validated before the listen, so a rejected
			// config must not leave a port bound behind it.
			if server.listener != nil {
				t.Error("listener is non-nil, want no port bound for an invalid host")
			}
		})
	}
}

// A disabled host is not prepared at all, so even nonsense in its configuration
// must not keep the rest of the server from starting.
func TestStartIgnoresDisabledHostConfiguration(t *testing.T) {
	server := &Server{
		Name:   "edge",
		Type:   "http",
		Listen: "127.0.0.1:0",
		Hosts: []*Host{
			redirectHost("live.example.com"),
			{Name: "off.example.com", Type: "carrier_pigeon", Disabled: true},
		},
	}
	client := startTestServer(t, server)
	resp := get(t, client, "http://live.example.com/")
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Errorf("status = %v, want %v", resp.StatusCode, http.StatusMovedPermanently)
	}
}

func TestStartFailsWhenAddressIsTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()

	for _, serverType := range []string{"http", "tcp"} {
		t.Run(serverType, func(t *testing.T) {
			host := redirectHost("a.example.com")
			host.Upstream = "10.0.0.1:9999" // for the tcp case
			server := &Server{Name: "dup", Type: serverType, Listen: taken.Addr().String(), Hosts: []*Host{host}}
			err := server.Start()
			if err == nil {
				server.Shutdown()
				t.Fatal("Start() = nil, want an error when the address is already in use")
			}
			if server.Status == "" {
				t.Error("Status is empty, want the bind failure recorded")
			}
		})
	}
}

// The admin rollback path shuts servers down that may never have started, so
// Shutdown has to tolerate a server with nothing running.
func TestShutdownIsSafeOnUnstartedServer(t *testing.T) {
	for _, serverType := range []string{"http", "https", "tcp", "gopher"} {
		server := &Server{Name: "never-started", Type: serverType, Listen: "127.0.0.1:0"}
		if err := server.Shutdown(); err != nil {
			t.Errorf("type %v: Shutdown() = %v, want nil", serverType, err)
		}
	}
}

func TestShutdownStopsServing(t *testing.T) {
	server := &Server{Name: "edge", Type: "http", Listen: "127.0.0.1:0", Hosts: []*Host{redirectHost("a.example.com")}}
	client := startTestServer(t, server)
	get(t, client, "http://a.example.com/") // serving before the shutdown

	addr := server.listener.Addr().String()
	if err := server.Shutdown(); err != nil {
		t.Fatalf("Shutdown() = %v, want nil", err)
	}
	if _, err := client.Get("http://a.example.com/"); err == nil {
		t.Error("request succeeded after Shutdown, want it refused")
	}
	if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		conn.Close()
		t.Error("port still accepts connections after Shutdown")
	}
}

// A server must be restartable after a shutdown: the admin API rolls back by
// starting the very servers it just stopped.
func TestServerCanBeRestarted(t *testing.T) {
	server := &Server{Name: "edge", Type: "http", Listen: "127.0.0.1:0", Hosts: []*Host{redirectHost("a.example.com")}}
	startTestServer(t, server)
	if err := server.Shutdown(); err != nil {
		t.Fatalf("Shutdown() = %v, want nil", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start() after Shutdown = %v, want nil", err)
	}
	defer server.Shutdown()

	client := newTestClient(server.listener.Addr().String(), false)
	resp := get(t, client, "http://a.example.com/")
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Errorf("status = %v, want %v", resp.StatusCode, http.StatusMovedPermanently)
	}
}

// ---- request handling ------------------------------------------------------

func TestHandleUnknownHost(t *testing.T) {
	server := &Server{Name: "edge", Type: "http", Listen: "127.0.0.1:0", Hosts: []*Host{redirectHost("known.example.com")}}
	client := startTestServer(t, server)

	resp := get(t, client, "http://Unknown.Example.COM/some/path")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %v, want %v", resp.StatusCode, http.StatusBadRequest)
	}
	if got, want := resp.Header.Get("Content-Type"), "application/json; charset=utf-8"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got, want := errField(t, resp), "Host 'unknown.example.com' not found"; got != want {
		t.Errorf("err = %q, want %q", got, want)
	}
}

func TestHandleDisabledHost(t *testing.T) {
	server := &Server{
		Name:   "edge",
		Type:   "http",
		Listen: "127.0.0.1:0",
		Hosts:  []*Host{{Name: "off.example.com", Type: "301_redirect", RedirectURL: "https://elsewhere.example.com", Disabled: true}},
	}
	client := startTestServer(t, server)

	resp := get(t, client, "http://off.example.com/")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %v, want %v", resp.StatusCode, http.StatusBadRequest)
	}
	if got, want := errField(t, resp), "Host 'off.example.com' is disabled"; got != want {
		t.Errorf("err = %q, want %q", got, want)
	}
}

// Host names are matched on the name alone: browsers send the port for
// non-default ports, and the case of a host name carries no meaning.
func TestHandleMatchesHostIgnoringCaseAndPort(t *testing.T) {
	server := &Server{Name: "edge", Type: "http", Listen: "127.0.0.1:0", Hosts: []*Host{
		{Name: "Mixed.Example.COM", Type: "301_redirect", RedirectURL: "https://elsewhere.example.com"},
	}}
	client := startTestServer(t, server)

	req, err := http.NewRequest(http.MethodGet, "http://placeholder/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "mixed.example.com:8080"
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Errorf("status = %v, want %v: the host should have matched", resp.StatusCode, http.StatusMovedPermanently)
	}
}

func TestHandleRedirect(t *testing.T) {
	server := &Server{Name: "edge", Type: "http", Listen: "127.0.0.1:0", Hosts: []*Host{
		{Name: "old.example.com", Type: "301_redirect", RedirectURL: "https://new.example.com"},
	}}
	client := startTestServer(t, server)

	resp := get(t, client, "http://old.example.com/a/b?c=d")
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Errorf("status = %v, want %v", resp.StatusCode, http.StatusMovedPermanently)
	}
	// The whole request URI, query included, has to survive the redirect.
	if got, want := resp.Header.Get("Location"), "https://new.example.com/a/b?c=d"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	if got, want := resp.Header.Get("Server"), "goweb"; got != want {
		t.Errorf("Server = %q, want %q", got, want)
	}
}

// staticRoot builds a small tree for the serve_static cases.
func staticRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "withindex"), 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"index.html":           "root index",
		"file.txt":             "hello",
		"sub/note.txt":         "note",
		"withindex/index.html": "sub index",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestHandleStatic(t *testing.T) {
	root := staticRoot(t)
	server := &Server{Name: "edge", Type: "http", Listen: "127.0.0.1:0", Hosts: []*Host{
		{Name: "static.example.com", Type: "serve_static", Path: root},
	}}
	client := startTestServer(t, server)

	cases := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{"file", "/file.txt", http.StatusOK, "hello"},
		{"index for the root", "/", http.StatusOK, "root index"},
		{"index for a directory", "/withindex/", http.StatusOK, "sub index"},
		{"listing for a directory without an index", "/sub/", http.StatusOK, "note.txt"},
		{"missing file", "/nope.txt", http.StatusNotFound, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := get(t, client, "http://static.example.com"+c.path)
			if resp.StatusCode != c.wantStatus {
				t.Errorf("status = %v, want %v", resp.StatusCode, c.wantStatus)
			}
			if body := bodyString(t, resp); !strings.Contains(body, c.wantBody) {
				t.Errorf("body = %q, want it to contain %q", body, c.wantBody)
			}
		})
	}
}

func TestHandleStaticWithDirListingDisabled(t *testing.T) {
	root := staticRoot(t)
	server := &Server{Name: "edge", Type: "http", Listen: "127.0.0.1:0", Hosts: []*Host{
		{Name: "static.example.com", Type: "serve_static", Path: root, DisableDirListing: true},
	}}
	client := startTestServer(t, server)

	// A directory with no index must not leak its file names.
	resp := get(t, client, "http://static.example.com/sub/")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %v, want %v", resp.StatusCode, http.StatusNotFound)
	}
	if body := bodyString(t, resp); strings.Contains(body, "note.txt") {
		t.Errorf("body = %q, want no file names in it", body)
	}

	// Directories that do have an index, and plain files, are unaffected.
	for path, want := range map[string]string{"/": "root index", "/withindex/": "sub index", "/file.txt": "hello"} {
		resp := get(t, client, "http://static.example.com"+path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%v: status = %v, want %v", path, resp.StatusCode, http.StatusOK)
		}
		if got := bodyString(t, resp); got != want {
			t.Errorf("%v: body = %q, want %q", path, got, want)
		}
	}
}

func TestHandleAllowedOrigins(t *testing.T) {
	server := &Server{Name: "edge", Type: "http", Listen: "127.0.0.1:0", Hosts: []*Host{
		{Name: "cors.example.com", Type: "301_redirect", RedirectURL: "https://elsewhere.example.com", AllowedOrigins: "https://app.example.com"},
		redirectHost("plain.example.com"),
	}}
	client := startTestServer(t, server)

	resp := get(t, client, "http://cors.example.com/")
	if got, want := resp.Header.Get("Access-Control-Allow-Origin"), "https://app.example.com"; got != want {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, want)
	}
	resp = get(t, client, "http://plain.example.com/")
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want it unset when no origins are configured", got)
	}
}

// ---- reverse proxy ---------------------------------------------------------

// echoServer reports what it received, so proxy behaviour can be asserted from
// the upstream's point of view.
func echoServer(t *testing.T, name string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"upstream": name,
			"host":     r.Host,
			"path":     r.URL.Path,
			"query":    r.URL.RawQuery,
			"xff":      r.Header.Get("X-Forwarded-For"),
			"xfh":      r.Header.Get("X-Forwarded-Host"),
			"xfp":      r.Header.Get("X-Forwarded-Proto"),
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBuildProxiesRejectsBadForwardURLs(t *testing.T) {
	cases := []struct {
		name, forwardURLs, want string
	}{
		{"empty", "", "no forward URLs"},
		{"blank", "   ", "no forward URLs"},
		{"unparseable", "://nope", "invalid forward URL"},
		{"wrong scheme", "ftp://files.example.com", "invalid forward URL"},
		{"no host", "http://", "invalid forward URL"},
		{"one bad among good", "http://a.example.com gopher://b.example.com", "invalid forward URL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			host := &Host{Name: "proxy.example.com", Type: "reverse_proxy", ForwardURLs: c.forwardURLs}
			err := host.buildProxies()
			if err == nil {
				t.Fatal("buildProxies() = nil, want an error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, want it to mention %q", err, c.want)
			}
		})
	}
}

// Each upstream gets its own proxy, and they must stay in the configured order:
// a shared loop variable here would send every request to the last upstream.
func TestBuildProxiesOneProxyPerUpstream(t *testing.T) {
	one, two := echoServer(t, "one"), echoServer(t, "two")
	host := &Host{Name: "proxy.example.com", Type: "reverse_proxy", ForwardURLs: one.URL + "  " + two.URL}
	if err := host.buildProxies(); err != nil {
		t.Fatalf("buildProxies() = %v, want nil", err)
	}
	if got, want := len(host.forwardProxies), 2; got != want {
		t.Fatalf("built %v proxies, want %v", got, want)
	}
	for i, want := range []string{"one", "two"} {
		rec := httptest.NewRecorder()
		host.forwardProxies[i].ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("proxy %v: decoding response: %v", i, err)
		}
		if body["upstream"] != want {
			t.Errorf("proxy %v reached upstream %q, want %q", i, body["upstream"], want)
		}
	}
}

func TestReverseProxyForwardsRequest(t *testing.T) {
	upstream := echoServer(t, "one")
	server := &Server{Name: "edge", Type: "http", Listen: "127.0.0.1:0", Hosts: []*Host{
		{Name: "proxy.example.com", Type: "reverse_proxy", ForwardURLs: upstream.URL},
	}}
	client := startTestServer(t, server)

	resp := get(t, client, "http://proxy.example.com/a/b?c=d")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %v, want %v", resp.StatusCode, http.StatusOK)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(bodyString(t, resp)), &body); err != nil {
		t.Fatalf("decoding upstream response: %v", err)
	}
	want := map[string]string{
		"path":  "/a/b",
		"query": "c=d",
		// the upstream sees the name the client asked for, not its own
		"host": "proxy.example.com",
		"xff":  "127.0.0.1",
		"xfh":  "proxy.example.com",
		"xfp":  "http",
	}
	for field, wantValue := range want {
		if body[field] != wantValue {
			t.Errorf("upstream saw %v = %q, want %q", field, body[field], wantValue)
		}
	}
}

func TestReverseProxyBadGateway(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // nothing is listening there any more

	server := &Server{Name: "edge", Type: "http", Listen: "127.0.0.1:0", Hosts: []*Host{
		{Name: "proxy.example.com", Type: "reverse_proxy", ForwardURLs: deadURL},
	}}
	client := startTestServer(t, server)

	resp := get(t, client, "http://proxy.example.com/")
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %v, want %v", resp.StatusCode, http.StatusBadGateway)
	}
	if got, want := resp.Header.Get("Content-Type"), "application/json; charset=utf-8"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got, want := errField(t, resp), "bad gateway"; got != want {
		t.Errorf("err = %q, want %q", got, want)
	}
}

// A redirect the upstream writes to itself has to come back relative, or the
// client would leave the proxy and try to reach the upstream directly.
func TestReverseProxyRewritesUpstreamRedirects(t *testing.T) {
	var upstreamURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			http.Redirect(w, r, "https://other.example.com/away", http.StatusFound)
			return
		}
		http.Redirect(w, r, upstreamURL+"/next", http.StatusFound)
	}))
	defer upstream.Close()
	upstreamURL = upstream.URL

	server := &Server{Name: "edge", Type: "http", Listen: "127.0.0.1:0", Hosts: []*Host{
		{Name: "proxy.example.com", Type: "reverse_proxy", ForwardURLs: upstream.URL},
	}}
	client := startTestServer(t, server)

	resp := get(t, client, "http://proxy.example.com/here")
	if got, want := resp.Header.Get("Location"), "/next"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	// Redirects to anywhere else must be passed through untouched.
	resp = get(t, client, "http://proxy.example.com/elsewhere")
	if got, want := resp.Header.Get("Location"), "https://other.example.com/away"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// Load balancing is by client IP, so one client keeps its upstream for every
// request instead of bouncing between them.
func TestReverseProxyIsStickyPerClient(t *testing.T) {
	one, two := echoServer(t, "one"), echoServer(t, "two")
	server := &Server{Name: "edge", Type: "http", Listen: "127.0.0.1:0", Hosts: []*Host{
		{Name: "proxy.example.com", Type: "reverse_proxy", ForwardURLs: one.URL + " " + two.URL},
	}}
	client := startTestServer(t, server)

	want := []string{"one", "two"}[hashIndex("127.0.0.1", 2)]
	for i := 0; i < 5; i++ {
		resp := get(t, client, "http://proxy.example.com/")
		var body map[string]string
		if err := json.Unmarshal([]byte(bodyString(t, resp)), &body); err != nil {
			t.Fatalf("decoding upstream response: %v", err)
		}
		if body["upstream"] != want {
			t.Fatalf("request %v reached upstream %q, want %q every time", i, body["upstream"], want)
		}
	}
}

// ---- tcp -------------------------------------------------------------------

// startEchoServer starts a TCP server that echoes back everything it reads and
// returns its address.
func startEchoServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}

// closedAddr returns an address where nothing is listening.
func closedAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	return addr
}

func TestStartTCPRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name  string
		hosts []*Host
		want  string
	}{
		{"no hosts", nil, "No enabled hosts"},
		{"every host disabled", []*Host{{Name: "a", Upstream: "10.0.0.1:1234", Disabled: true}}, "No enabled hosts"},
		{"upstream without a port", []*Host{{Name: "a", Upstream: "10.0.0.1"}}, "Invalid upstream"},
		{"empty upstream", []*Host{{Name: "a"}}, "Invalid upstream"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := &Server{Name: "tcp-edge", Type: "tcp", Listen: "127.0.0.1:0", Hosts: c.hosts}
			err := server.Start()
			if err == nil {
				server.Shutdown()
				t.Fatal("Start() = nil, want an error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, want it to mention %q", err, c.want)
			}
			// Upstreams are checked before the listen, so a rejected config
			// must not leave a port bound behind it.
			if server.listener != nil {
				t.Error("listener is non-nil, want no port bound for an invalid config")
			}
		})
	}
}

func TestTCPProxyRoundTrip(t *testing.T) {
	logs := captureAccessLog(t)
	upstream := startEchoServer(t)
	server := &Server{
		Name:      "tcp-edge",
		Type:      "tcp",
		Listen:    "127.0.0.1:0",
		AccessLog: true,
		Hosts:     []*Host{{Name: "db", Upstream: upstream}},
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}
	t.Cleanup(func() { server.Shutdown() })

	conn, err := net.Dial("tcp", server.listener.Addr().String())
	if err != nil {
		t.Fatalf("dialling the proxy: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("writing to the proxy: %v", err)
	}
	// Half-close: the echo server only sees EOF, and so only finishes copying
	// back, if the proxy forwards the shutdown instead of holding the write
	// side open.
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("half-closing: %v", err)
	}
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("reading the echo: %v", err)
	}
	if string(got) != "ping" {
		t.Errorf("read %q, want %q", got, "ping")
	}

	waitFor(t, "the access log record", func() bool { return len(logs.records()) > 0 })
	record := parseRecord(t, logs.records()[0])
	want := map[string]any{
		"msg":            "connection",
		"server":         "tcp-edge",
		"host":           "db",
		"upstream":       upstream,
		"bytes_sent":     float64(4),
		"bytes_received": float64(4),
	}
	for field, wantValue := range want {
		if record[field] != wantValue {
			t.Errorf("access record %v = %v, want %v", field, record[field], wantValue)
		}
	}
	if _, ok := record["duration_ms"].(float64); !ok {
		t.Errorf("access record duration_ms = %v, want a number", record["duration_ms"])
	}
}

// When the upstream cannot be reached the client connection is closed rather
// than left hanging.
func TestTCPProxyClosesClientWhenUpstreamIsDown(t *testing.T) {
	server := &Server{
		Name:   "tcp-edge",
		Type:   "tcp",
		Listen: "127.0.0.1:0",
		Hosts:  []*Host{{Name: "db", Upstream: closedAddr(t)}},
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}
	t.Cleanup(func() { server.Shutdown() })

	conn, err := net.Dial("tcp", server.listener.Addr().String())
	if err != nil {
		t.Fatalf("dialling the proxy: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	n, err := conn.Read(make([]byte, 8))
	if err == nil {
		t.Fatalf("read %v bytes with no error, want the connection closed", n)
	}
}

func TestTCPShutdownStopsAccepting(t *testing.T) {
	server := &Server{
		Name:   "tcp-edge",
		Type:   "tcp",
		Listen: "127.0.0.1:0",
		Hosts:  []*Host{{Name: "db", Upstream: startEchoServer(t)}},
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}
	addr := server.listener.Addr().String()
	if err := server.Shutdown(); err != nil {
		t.Fatalf("Shutdown() = %v, want nil", err)
	}
	if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		conn.Close()
		t.Error("port still accepts connections after Shutdown")
	}
}
