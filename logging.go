package main

import (
	"bufio"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// accessLog carries one record per request or connection on stdout, while the
// default slog logger carries diagnostics on stderr, so the two streams can be
// collected or filtered separately.
var accessLog *slog.Logger

// setupLogging configures both loggers from GOWEB_LOG_LEVEL (debug, info,
// warn, error) and GOWEB_LOG_FORMAT (text, json).
func setupLogging() {
	levelText := getEnv("GOWEB_LOG_LEVEL", "info")
	format := getEnv("GOWEB_LOG_FORMAT", "text")

	var level slog.Level
	levelErr := level.UnmarshalText([]byte(levelText))
	if levelErr != nil {
		level = slog.LevelInfo
	}

	newHandler := func(w io.Writer, opts *slog.HandlerOptions) slog.Handler {
		if format == "json" {
			return slog.NewJSONHandler(w, opts)
		}
		return slog.NewTextHandler(w, opts)
	}

	slog.SetDefault(slog.New(newHandler(os.Stderr, &slog.HandlerOptions{
		Level:     level,
		AddSource: level <= slog.LevelDebug,
	})))
	accessLog = slog.New(newHandler(os.Stdout, &slog.HandlerOptions{}))

	if levelErr != nil {
		slog.Warn("Invalid GOWEB_LOG_LEVEL, using info", "value", levelText)
	}
	if format != "text" && format != "json" {
		slog.Warn("Invalid GOWEB_LOG_FORMAT, using text", "value", format)
	}
}

// httpErrorLog routes an http.Server's internal messages (TLS handshake
// failures, handler panics, port scanner noise) through slog.
func httpErrorLog() *log.Logger {
	return slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn)
}

// responseRecorder captures the status and body bytes of a response while
// delegating everything else to the wrapped ResponseWriter. Unwrap keeps
// http.ResponseController working, and Hijack must delegate explicitly so the
// reverse proxy's websocket upgrades keep working and can be recorded.
type responseRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
	hijacked    bool
}

func (this *responseRecorder) WriteHeader(status int) {
	if !this.wroteHeader {
		this.wroteHeader = true
		this.status = status
	}
	this.ResponseWriter.WriteHeader(status)
}

func (this *responseRecorder) Write(b []byte) (int, error) {
	n, err := this.ResponseWriter.Write(b)
	this.bytes += int64(n)
	return n, err
}

func (this *responseRecorder) Flush() {
	http.NewResponseController(this.ResponseWriter).Flush()
}

func (this *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(this.ResponseWriter).Hijack()
	if err == nil {
		this.hijacked = true
	}
	return conn, rw, err
}

func (this *responseRecorder) Unwrap() http.ResponseWriter {
	return this.ResponseWriter
}

// requestURIForLog masks credentials in the query of a request line. Checkout
// providers put bearer credentials in return URLs, and those URLs can also
// reach the access log as the Referer of a later request.
//
// Masking works pair by pair, so the rest of the query is logged as the client
// sent it. A "#" is not a fragment in a request line: the server keeps it in
// the path or query and the proxy forwards it, so it is logged too.
func requestURIForLog(raw string) string {
	q := strings.IndexByte(raw, '?')
	if q < 0 {
		return raw
	}
	var b strings.Builder
	copied := 0 // how much of raw is already in b
	for start := q + 1; start <= len(raw); {
		end := len(raw)
		if i := strings.IndexByte(raw[start:], '&'); i >= 0 {
			end = start + i
		}
		if keep, mask := maskPair(raw[start:end]); mask != "" {
			b.WriteString(raw[copied : start+keep])
			b.WriteString(mask)
			copied = end
		}
		start = end + 1
	}
	if copied == 0 {
		// nothing was masked
		return raw
	}
	b.WriteString(raw[copied:])
	return b.String()
}

// urlForLog does the same for a URL that can carry a fragment, such as a
// Referer or an upstream URL. The fragment is dropped.
func urlForLog(raw string) string {
	raw, _, _ = strings.Cut(raw, "#")
	return requestURIForLog(raw)
}

// maskPair looks for a credential in one "&"-separated pair. When it finds
// one it returns how much of the pair to keep and the mask to write in place
// of the rest; otherwise the mask is empty.
//
// Go rejects a pair that holds a ";", but other servers split on it, so each
// piece is checked. Whatever follows a masked piece goes with it, because a
// server that does not split there reads all of it as the value. A piece
// whose key cannot be decoded is masked whole.
func maskPair(pair string) (keep int, mask string) {
	for start := 0; ; {
		piece, _, more := strings.Cut(pair[start:], ";")
		key, _, _ := strings.Cut(piece, "=")
		name, err := url.QueryUnescape(key)
		if err != nil {
			return start, "REDACTED"
		}
		switch strings.ToLower(name) {
		case "session_id", "square_order", "token":
			return start + len(key), "=REDACTED"
		}
		if !more {
			return len(pair), ""
		}
		start += len(piece) + 1
	}
}

// logAccess wraps next and writes one access record per request.
func (this *Server) logAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		status := rec.status
		if rec.hijacked {
			// upgraded connections write past the recorder; ServeHTTP
			// returns when the tunneled session ends
			status = http.StatusSwitchingProtocols
		}
		accessLog.Info("access",
			"server", this.Name,
			"host", normalizeHost(r.Host),
			"client", clientIP(r.RemoteAddr),
			"method", r.Method,
			"uri", requestURIForLog(r.RequestURI),
			"proto", r.Proto,
			"status", status,
			"bytes", rec.bytes,
			"duration_ms", durationMs(start),
			"referer", urlForLog(r.Referer()),
			"user_agent", r.UserAgent(),
		)
	})
}

// durationMs returns the time elapsed since start as milliseconds with
// microsecond precision, so it reads the same in text and json output.
func durationMs(start time.Time) float64 {
	return float64(time.Since(start).Round(time.Microsecond)) / float64(time.Millisecond)
}
