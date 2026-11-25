package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// restoreLoggers puts the loggers TestMain set up back after a test has called
// setupLogging.
func restoreLoggers(t *testing.T) {
	t.Helper()
	oldDefault, oldAccess := slog.Default(), accessLog
	t.Cleanup(func() {
		slog.SetDefault(oldDefault)
		accessLog = oldAccess
	})
}

// captureStdio points os.Stderr and os.Stdout at files: setupLogging writes to
// them directly, so this both keeps its output out of the test output and lets
// tests read it back.
func captureStdio(t *testing.T) (stderr, stdout *os.File) {
	t.Helper()
	dir := t.TempDir()
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	stdout, err = os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	oldStderr, oldStdout := os.Stderr, os.Stdout
	t.Cleanup(func() {
		os.Stderr, os.Stdout = oldStderr, oldStdout
		stderr.Close()
		stdout.Close()
	})
	os.Stderr, os.Stdout = stderr, stdout
	return stderr, stdout
}

func contents(t *testing.T, f *os.File) string {
	t.Helper()
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("reading %v: %v", f.Name(), err)
	}
	return string(b)
}

// setEnv sets a variable for the duration of the test, or removes it when value
// is empty, which is what an unconfigured goweb sees.
func setEnv(t *testing.T, key, value string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	t.Cleanup(func() {
		if had {
			os.Setenv(key, old)
		} else {
			os.Unsetenv(key)
		}
	})
	if value == "" {
		os.Unsetenv(key)
	} else {
		os.Setenv(key, value)
	}
}

func TestSetupLogging(t *testing.T) {
	cases := []struct {
		name        string
		level       string
		format      string
		enabled     []slog.Level
		disabled    []slog.Level
		wantJSON    bool
		wantWarning string
	}{
		{
			name:     "defaults to info and text",
			enabled:  []slog.Level{slog.LevelInfo, slog.LevelError},
			disabled: []slog.Level{slog.LevelDebug},
		},
		{
			name:    "debug",
			level:   "debug",
			enabled: []slog.Level{slog.LevelDebug, slog.LevelInfo},
		},
		{
			name:     "error",
			level:    "error",
			enabled:  []slog.Level{slog.LevelError},
			disabled: []slog.Level{slog.LevelWarn, slog.LevelInfo},
		},
		{
			name:     "json",
			format:   "json",
			wantJSON: true,
			enabled:  []slog.Level{slog.LevelInfo},
		},
		{
			name:        "unknown level falls back to info",
			level:       "loud",
			enabled:     []slog.Level{slog.LevelInfo},
			disabled:    []slog.Level{slog.LevelDebug},
			wantWarning: "Invalid GOWEB_LOG_LEVEL",
		},
		{
			name:        "unknown format falls back to text",
			format:      "yaml",
			enabled:     []slog.Level{slog.LevelInfo},
			wantWarning: "Invalid GOWEB_LOG_FORMAT",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			restoreLoggers(t)
			stderr, _ := captureStdio(t)
			setEnv(t, "GOWEB_LOG_LEVEL", c.level)
			setEnv(t, "GOWEB_LOG_FORMAT", c.format)

			setupLogging()

			for _, level := range c.enabled {
				if !slog.Default().Enabled(context.Background(), level) {
					t.Errorf("%v is disabled, want it logged", level)
				}
			}
			for _, level := range c.disabled {
				if slog.Default().Enabled(context.Background(), level) {
					t.Errorf("%v is enabled, want it dropped", level)
				}
			}
			if accessLog == nil {
				t.Fatal("accessLog is nil, want a logger")
			}
			for name, handler := range map[string]slog.Handler{"default": slog.Default().Handler(), "access": accessLog.Handler()} {
				_, isJSON := handler.(*slog.JSONHandler)
				if isJSON != c.wantJSON {
					t.Errorf("%v handler json = %v, want %v", name, isJSON, c.wantJSON)
				}
			}
			// The access log carries a record per request whatever the
			// diagnostic level is: it is data, not a diagnostic.
			if !accessLog.Enabled(context.Background(), slog.LevelInfo) {
				t.Error("access log drops info records, want them all kept")
			}
			if got := contents(t, stderr); !strings.Contains(got, c.wantWarning) {
				t.Errorf("stderr = %q, want it to mention %q", got, c.wantWarning)
			} else if c.wantWarning == "" && strings.Contains(got, "Invalid") {
				t.Errorf("stderr = %q, want no complaint about a valid configuration", got)
			}
		})
	}
}

// The two streams are separate so they can be collected or filtered apart.
func TestSetupLoggingSplitsStreams(t *testing.T) {
	restoreLoggers(t)
	stderr, stdout := captureStdio(t)
	setEnv(t, "GOWEB_LOG_LEVEL", "info")
	setEnv(t, "GOWEB_LOG_FORMAT", "text")

	setupLogging()
	slog.Info("a diagnostic")
	accessLog.Info("access")

	if got := contents(t, stderr); !strings.Contains(got, "a diagnostic") || strings.Contains(got, "access") {
		t.Errorf("stderr = %q, want the diagnostic and nothing else", got)
	}
	if got := contents(t, stdout); !strings.Contains(got, "access") || strings.Contains(got, "a diagnostic") {
		t.Errorf("stdout = %q, want the access record and nothing else", got)
	}
}

// The http server's own messages — handshake failures, port scanner noise —
// have to reach the same place as everything else rather than bypass slog.
func TestHTTPErrorLogRoutesThroughSlog(t *testing.T) {
	restoreLoggers(t)
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))

	httpErrorLog().Printf("http: TLS handshake error from %v", "10.0.0.1:1234")

	record := parseRecord(t, strings.TrimSpace(buf.String()))
	if got, want := record["level"], "WARN"; got != want {
		t.Errorf("level = %v, want %v", got, want)
	}
	if got, want := record["msg"], "http: TLS handshake error from 10.0.0.1:1234"; got != want {
		t.Errorf("msg = %q, want %q", got, want)
	}
}

func TestLogAccessRecord(t *testing.T) {
	logs := captureAccessLog(t)
	server := &Server{Name: "edge", AccessLog: true}
	handler := server.logAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		fmt.Fprint(w, "hello")
	}))

	req := httptest.NewRequest(http.MethodGet, "/a/b?c=d", nil)
	req.Host = "Example.COM:8080"
	req.RemoteAddr = "10.1.2.3:44444"
	req.Header.Set("Referer", "https://ref.example.com/from")
	req.Header.Set("User-Agent", "goweb-test/1")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	records := logs.records()
	if len(records) != 1 {
		t.Fatalf("wrote %v access records, want 1", len(records))
	}
	record := parseRecord(t, records[0])
	want := map[string]any{
		"msg":        "access",
		"server":     "edge",
		"host":       "example.com",
		"client":     "10.1.2.3",
		"method":     "GET",
		"uri":        "/a/b?c=d",
		"proto":      "HTTP/1.1",
		"status":     float64(http.StatusTeapot),
		"bytes":      float64(len("hello")),
		"referer":    "https://ref.example.com/from",
		"user_agent": "goweb-test/1",
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

func TestLogAccessMasksCheckoutReferences(t *testing.T) {
	logs := captureAccessLog(t)
	const uri = "/thanks?session_id=first&session_id=second&item=one"
	const referer = "https://shop.example/thanks?square_order=third&%54oken=fourth#private-fragment"
	req := httptest.NewRequest(http.MethodGet, uri, nil)
	req.Header.Set("Referer", referer)
	handler := (&Server{}).logAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI != uri || r.Referer() != referer {
			t.Error("handler did not receive the original checkout URL and referrer")
		}
	}))
	handler.ServeHTTP(httptest.NewRecorder(), req)
	record := parseRecord(t, logs.records()[0])
	if got := record["uri"]; got != "/thanks?session_id=REDACTED&session_id=REDACTED&item=one" {
		t.Errorf("logged URI = %v", got)
	}
	if got := record["referer"]; got != "https://shop.example/thanks?square_order=REDACTED&%54oken=REDACTED" {
		t.Errorf("logged referrer = %v", got)
	}
}

// The access log is also the record of what a client sent, so a request line
// keeps everything the masking has no reason to touch, in the order and
// encoding it arrived in. Otherwise one malformed pair or a "#" would let a
// client keep the rest of its request out of the log.
func TestLogAccessKeepsTheRestOfTheRequestLine(t *testing.T) {
	logs := captureAccessLog(t)
	handler := (&Server{}).logAccess(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	uris := []string{
		"/search?q=1%27%20OR%201=1--&bad=%zz",
		"/search?q=ok#&id=1%20UNION%20SELECT",
		"/hello.txt#anything",
	}
	for _, uri := range uris {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, uri, nil))
	}
	records := logs.records()
	if len(records) != len(uris) {
		t.Fatalf("wrote %v access records, want %v", len(records), len(uris))
	}
	for i, uri := range uris {
		if got := parseRecord(t, records[i])["uri"]; got != uri {
			t.Errorf("logged URI = %v, want %v", got, uri)
		}
	}
}

func TestRequestURIForLog(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/plain", "/plain"},
		{"/?b=two%20words&a=1", "/?b=two%20words&a=1"},
		// only the credential changes
		{"/?b=2&Session_ID=private&a=%2F%20x", "/?b=2&Session_ID=REDACTED&a=%2F%20x"},
		{"/?token", "/?token=REDACTED"},
		{"/?&&token=private&", "/?&&token=REDACTED&"},
		// a malformed pair does not hide its neighbours
		{"/?bad=%zz&token=private", "/?bad=%zz&token=REDACTED"},
		{"/?q=kept&%zz=private", "/?q=kept&REDACTED"},
		// servers that split on ";" would see these keys
		{"/?token=private;page=1", "/?token=REDACTED"},
		{"/?page=1;token=private&q=kept", "/?page=1;token=REDACTED&q=kept"},
		{"/?page=1;%zz=private;q=dropped", "/?page=1;REDACTED"},
		// "#" is part of a request line, not the start of a fragment
		{"/?q=kept#&id=kept", "/?q=kept#&id=kept"},
		{"/path#?token=private", "/path#?token=REDACTED"},
	} {
		if got := requestURIForLog(tc.in); got != tc.want {
			t.Errorf("requestURIForLog(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestURLForLog(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://shop.example/#token=private", "https://shop.example/"},
		{"https://shop.example/?a=1&token=private#fragment", "https://shop.example/?a=1&token=REDACTED"},
	} {
		if got := urlForLog(tc.in); got != tc.want {
			t.Errorf("urlForLog(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestProxyErrorMasksCheckoutReferences(t *testing.T) {
	restoreLoggers(t)
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	host := &Host{Name: "shop.example", ForwardURLs: "http://upstream.example/?token=upstream-secret"}
	if err := host.buildProxies(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/thanks?square_order=request-secret#&item=one", nil)
	err := &url.Error{Op: "Get", URL: "http://upstream.example/?session_id=error-secret", Err: io.EOF}
	host.forwardProxies[0].ErrorHandler(httptest.NewRecorder(), req, err)
	record := parseRecord(t, buf.String())
	if record["uri"] != "/thanks?square_order=REDACTED&item=one" || record["upstream"] != "http://upstream.example/?token=REDACTED" {
		t.Errorf("proxy URL fields = %v / %v", record["uri"], record["upstream"])
	}
	if strings.Contains(buf.String(), "-secret") || !strings.Contains(buf.String(), "EOF") {
		t.Error("proxy diagnostic should retain the cause and mask each checkout reference")
	}
}

// A handler that writes a body without setting a status has sent 200.
func TestLogAccessDefaultsToOK(t *testing.T) {
	logs := captureAccessLog(t)
	server := &Server{Name: "edge", AccessLog: true}
	handler := server.logAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	record := parseRecord(t, logs.records()[0])
	if got, want := record["status"], float64(http.StatusOK); got != want {
		t.Errorf("status = %v, want %v", got, want)
	}
}

// hijackableRecorder stands in for a connection that can be taken over, the way
// a websocket upgrade through the reverse proxy does.
type hijackableRecorder struct {
	*httptest.ResponseRecorder
	conn net.Conn
}

func (this *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return this.conn, bufio.NewReadWriter(bufio.NewReader(this.conn), bufio.NewWriter(this.conn)), nil
}

// An upgraded connection writes past the recorder, so the status it reports is
// the upgrade itself rather than the 200 the recorder started with.
func TestLogAccessRecordsUpgradeAs101(t *testing.T) {
	logs := captureAccessLog(t)
	client, upstream := net.Pipe()
	defer client.Close()
	defer upstream.Close()

	server := &Server{Name: "edge", AccessLog: true}
	handler := server.logAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := http.NewResponseController(w).Hijack(); err != nil {
			t.Errorf("Hijack() = %v, want the takeover to be passed through", err)
		}
	}))
	w := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder(), conn: upstream}
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ws", nil))

	record := parseRecord(t, logs.records()[0])
	if got, want := record["status"], float64(http.StatusSwitchingProtocols); got != want {
		t.Errorf("status = %v, want %v", got, want)
	}
}

// The access log middleware is only in the chain when the server asks for it.
func TestAccessLogIsOptional(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("access_log=%v", enabled), func(t *testing.T) {
			logs := captureAccessLog(t)
			server := &Server{
				Name:      "edge",
				Type:      "http",
				Listen:    "127.0.0.1:0",
				AccessLog: enabled,
				Hosts:     []*Host{redirectHost("a.example.com")},
			}
			client := startTestServer(t, server)
			get(t, client, "http://a.example.com/")
			// the record is written on the request's own goroutine; a graceful
			// shutdown waits for it to finish
			if err := server.Shutdown(); err != nil {
				t.Fatalf("Shutdown() = %v, want nil", err)
			}

			records := logs.records()
			if enabled && len(records) != 1 {
				t.Fatalf("wrote %v access records, want 1", len(records))
			}
			if !enabled && len(records) != 0 {
				t.Fatalf("wrote %v access records, want none", len(records))
			}
		})
	}
}

func TestResponseRecorder(t *testing.T) {
	inner := httptest.NewRecorder()
	rec := &responseRecorder{ResponseWriter: inner, status: http.StatusOK}

	rec.WriteHeader(http.StatusServiceUnavailable)
	rec.WriteHeader(http.StatusOK) // a second call cannot change what was sent
	rec.Write([]byte("abc"))
	rec.Write([]byte("de"))
	rec.Flush()

	if got, want := rec.status, http.StatusServiceUnavailable; got != want {
		t.Errorf("status = %v, want %v", got, want)
	}
	if got, want := rec.bytes, int64(5); got != want {
		t.Errorf("bytes = %v, want %v", got, want)
	}
	if got, want := inner.Body.String(), "abcde"; got != want {
		t.Errorf("body written through = %q, want %q", got, want)
	}
	if got, want := inner.Code, http.StatusServiceUnavailable; got != want {
		t.Errorf("status written through = %v, want %v", got, want)
	}
	if !inner.Flushed {
		t.Error("Flush did not reach the wrapped writer, want streaming responses to keep flowing")
	}
	// Unwrap is what keeps http.ResponseController working through the wrapper.
	if rec.Unwrap() != http.ResponseWriter(inner) {
		t.Error("Unwrap did not return the wrapped writer")
	}
	if rec.hijacked {
		t.Error("hijacked = true, want false when nothing took the connection over")
	}
}

func TestDurationMs(t *testing.T) {
	if got := durationMs(time.Now()); got < 0 {
		t.Errorf("durationMs(now) = %v, want it not negative", got)
	}
	// The clock only moves forward, so an elapsed 20ms reads as at least 20.
	if got := durationMs(time.Now().Add(-20 * time.Millisecond)); got < 20 {
		t.Errorf("durationMs(20ms ago) = %v, want at least 20", got)
	}
	// It has to serialise as a number rather than a duration string, so the
	// two output formats agree.
	b, err := json.Marshal(durationMs(time.Now().Add(-time.Millisecond)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(string(b), `"ms`) {
		t.Errorf("durationMs serialised as %s, want a plain number", b)
	}
}
