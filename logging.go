package main

import (
	"bufio"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
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
			"uri", r.RequestURI,
			"proto", r.Proto,
			"status", status,
			"bytes", rec.bytes,
			"duration_ms", durationMs(start),
			"referer", r.Referer(),
			"user_agent", r.UserAgent(),
		)
	})
}

// durationMs returns the time elapsed since start as milliseconds with
// microsecond precision, so it reads the same in text and json output.
func durationMs(start time.Time) float64 {
	return float64(time.Since(start).Round(time.Microsecond)) / float64(time.Millisecond)
}
