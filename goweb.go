package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"
)

const version = "9"

const (
	shutdownTimeout   = 10 * time.Second
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 2 * time.Minute
)

var secret = getEnv("GOWEB_ADMIN_TOKEN", "")
var host = getEnv("GOWEB_ADMIN_HOST", "localhost")
var port = getEnv("GOWEB_ADMIN_PORT", "13579")

var mu sync.Mutex
var servers []*Server
var confPath *string

func main() {
	setupLogging()
	v := flag.Bool("v", false, "prints version")
	confPath = flag.String("c", "goweb.json", "configuration file path")
	flag.Parse()
	if *v {
		fmt.Println(version)
		os.Exit(0)
	}
	confBytes, err := os.ReadFile(*confPath)
	if err != nil {
		slog.Error("Failed to read config file", "err", err)
		os.Exit(1)
	}

	servers, err = NewConfig(confBytes)
	if err != nil {
		slog.Error("Failed to parse config file", "path", *confPath, "err", err)
		os.Exit(1)
	}

	for _, server := range servers {
		err := server.Start()
		if err != nil {
			slog.Error("Failed to start server", "server", server.Name, "err", err)
		}
	}

	if secret != "" {
		err = StartAdmin()
		if err != nil {
			slog.Error("Failed to start admin server", "err", err)
		}
	}

	Hook(func() {
		slog.Info("Shutting down")
		mu.Lock()
		defer mu.Unlock()
		for _, server := range servers {
			err := server.Shutdown()
			if err != nil {
				slog.Error("Failed to shut down server", "server", server.Name, "err", err)
			}
		}
	})
}

func (this *Server) Shutdown() error {
	switch this.Type {
	case "https", "http":
		var err error
		if this.httpServer != nil {
			ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			err = this.httpServer.Shutdown(ctx)
			if err != nil {
				// graceful shutdown failed or timed out; force-close
				this.httpServer.Close()
			}
			slog.Info("Server stopped", "server", this.Name, "type", this.Type, "listen", this.Listen)
		}
		if this.listener != nil {
			// usually already closed by httpServer.Shutdown; this covers the
			// window where the serve goroutine has not picked it up yet
			this.listener.Close()
		}
		return err
	case "tcp":
		if this.listener != nil {
			this.listener.Close()
			slog.Info("Server stopped", "server", this.Name, "type", this.Type, "listen", this.Listen)
		}
	}
	return nil
}

func indexFileNotExists(dir string) bool {
	indexPath := path.Join(dir, "index.html")
	stats, err := os.Stat(indexPath)
	if err != nil || stats.IsDir() {
		return true
	}
	return false
}

func (this *Server) Start() error {
	if this.Name == "" {
		this.Status = "Server name is required"
		return errors.New(this.Status)
	}
	if this.Disabled {
		return nil
	}
	switch this.Type {
	case "http", "https":
		return this.startHTTP()
	case "tcp":
		return this.startTCP()
	default:
		this.Status = fmt.Sprintf("Unknown server type '%v' for server: %v, %v", this.Type, this.Name, this.Listen)
		return errors.New(this.Status)
	}
}

func (this *Server) startHTTP() error {
	var tlsConfig *tls.Config
	if this.Type == "https" {
		tlsConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}

	this.hostMap = make(map[string]*Host, len(this.Hosts))
	for _, host := range this.Hosts {
		if host.Name == "" {
			host.Status = fmt.Sprintf("Host name is required, server: %v, %v", this.Name, this.Listen)
			return errors.New(host.Status)
		}
		if !host.Disabled {
			switch host.Type {
			case "serve_static":
				host.fileServer = http.FileServer(http.Dir(host.Path))
			case "301_redirect":
				// nothing to prepare
			case "reverse_proxy":
				if err := host.buildProxies(); err != nil {
					host.Status = fmt.Sprintf("%v, server: %v, %v", err, this.Name, this.Listen)
					return errors.New(host.Status)
				}
			default:
				host.Status = fmt.Sprintf("Unknown host type '%v' for host: %v, server: %v, %v", host.Type, host.Name, this.Name, this.Listen)
				return errors.New(host.Status)
			}
		}
		if this.Type == "https" {
			keyPair, err := tls.LoadX509KeyPair(host.CertPath, host.KeyPath)
			if err != nil {
				host.Status = fmt.Sprintf("%v for host: %v, server: %v, %v", err, host.Name, this.Name, this.Listen)
				return errors.New(host.Status)
			}
			tlsConfig.Certificates = append(tlsConfig.Certificates, keyPair)
		}
		this.hostMap[normalizeHost(host.Name)] = host
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", this.handle)
	var handler http.Handler = mux
	if this.AccessLog {
		handler = this.logAccess(mux)
	}

	listener, err := net.Listen("tcp", this.Listen)
	if err != nil {
		this.Status = fmt.Sprintf("%v for server: %v, %v", err, this.Name, this.Listen)
		return errors.New(this.Status)
	}
	this.listener = listener

	srv := &http.Server{
		Handler:           handler,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          httpErrorLog(),
	}
	this.httpServer = srv

	go func() {
		var err error
		if this.Type == "https" {
			err = srv.ServeTLS(listener, "", "")
		} else {
			err = srv.Serve(listener)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			this.Status = fmt.Sprintf("%v for server: %v, %v", err, this.Name, this.Listen)
			slog.Error("Server failed", "server", this.Name, "listen", this.Listen, "err", err)
		}
	}()
	slog.Info("Server listening", "server", this.Name, "type", this.Type, "listen", this.Listen)
	return nil
}

func (this *Server) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Server", "goweb")
	requestedHost := normalizeHost(r.Host)
	host := this.hostMap[requestedHost]
	if host == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"err": fmt.Sprintf("Host '%v' not found", requestedHost)})
		return
	}
	if host.Disabled {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"err": fmt.Sprintf("Host '%v' is disabled", requestedHost)})
		return
	}

	if host.AllowedOrigins != "" {
		w.Header().Set("Access-Control-Allow-Origin", host.AllowedOrigins)
	}

	switch host.Type {
	case "301_redirect":
		http.Redirect(w, r, fmt.Sprintf("%v%v", host.RedirectURL, r.RequestURI), http.StatusMovedPermanently)
	case "serve_static":
		dirPath := path.Join(host.Path, r.URL.Path)
		if host.DisableDirListing && strings.HasSuffix(r.URL.Path, "/") && indexFileNotExists(dirPath) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"err":"404 page not found"}`)
			return
		}
		host.fileServer.ServeHTTP(w, r)
	case "reverse_proxy":
		proxy := host.forwardProxies[hashIndex(clientIP(r.RemoteAddr), len(host.forwardProxies))]
		proxy.ServeHTTP(w, r)
	default:
		// unreachable: host types are validated in startHTTP
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"err": fmt.Sprintf("Unknown host type '%v'", host.Type)})
	}
}

// buildProxies parses the space separated forward URLs and builds one reverse
// proxy per upstream. The proxies stream request and response bodies, strip
// hop-by-hop headers, set X-Forwarded-For/Host/Proto and support upgrades
// such as websockets.
func (host *Host) buildProxies() error {
	forwardURLs := strings.Fields(host.ForwardURLs)
	if len(forwardURLs) == 0 {
		return fmt.Errorf("no forward URLs configured for host: %v", host.Name)
	}
	proxies := make([]*httputil.ReverseProxy, 0, len(forwardURLs))
	for _, forwardURL := range forwardURLs {
		target, err := url.Parse(forwardURL)
		if err != nil {
			return fmt.Errorf("invalid forward URL '%v' for host: %v: %v", forwardURL, host.Name, err)
		}
		if (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
			return fmt.Errorf("invalid forward URL '%v' for host: %v", forwardURL, host.Name)
		}
		proxies = append(proxies, &httputil.ReverseProxy{
			Rewrite: func(r *httputil.ProxyRequest) {
				r.SetURL(target)
				r.SetXForwarded()
				r.Out.Host = r.In.Host
			},
			ModifyResponse: func(res *http.Response) error {
				rewriteLocation(res, target)
				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				level := slog.LevelError
				if errors.Is(err, context.Canceled) {
					// the client went away mid-request, not an upstream failure
					level = slog.LevelDebug
				}
				slog.Log(r.Context(), level, "Proxy error",
					"host", host.Name,
					"upstream", target.String(),
					"method", r.Method,
					"uri", r.RequestURI,
					"client", clientIP(r.RemoteAddr),
					"err", err)
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprint(w, `{"err":"bad gateway"}`)
			},
		})
	}
	host.forwardProxies = proxies
	return nil
}

// rewriteLocation makes upstream Location headers relative when they point
// back at the upstream itself, so redirects keep clients on this proxy.
func rewriteLocation(res *http.Response, target *url.URL) {
	location := res.Header.Get("Location")
	if location == "" {
		return
	}
	u, err := url.Parse(location)
	if err != nil || u.Host == "" {
		return
	}
	if canonicalHostPort(u) != canonicalHostPort(target) {
		return
	}
	u.Scheme = ""
	u.Host = ""
	u.User = nil
	rel := u.String()
	if rel == "" {
		rel = "/"
	}
	res.Header.Set("Location", rel)
}

// canonicalHostPort returns the URL's host:port with the scheme's default
// port filled in when absent.
func canonicalHostPort(u *url.URL) string {
	hostPort := u.Host
	if u.Port() == "" {
		switch u.Scheme {
		case "http":
			hostPort += ":80"
		case "https":
			hostPort += ":443"
		}
	}
	return strings.ToLower(hostPort)
}

// normalizeHost lowercases a host name and strips an optional port, handling
// IPv6 literals such as [::1]:443.
func normalizeHost(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = strings.Trim(hostport, "[]")
	}
	return strings.ToLower(host)
}

// clientIP returns the IP part of an ip:port remote address, so load
// balancing is sticky per client instead of per connection.
func clientIP(remoteAddr string) string {
	ip, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return ip
}

// hashIndex maps s onto [0, n) with an fnv-1a hash. The modulo is done in
// uint32 so the result is valid on 32-bit platforms too.
func hashIndex(s string, n int) int {
	h := fnv.New32a()
	h.Write([]byte(s))
	return int(h.Sum32() % uint32(n))
}

func (this *Server) startTCP() error {
	enabledHosts := make([]*Host, 0, len(this.Hosts))
	for _, host := range this.Hosts {
		if !host.Disabled {
			enabledHosts = append(enabledHosts, host)
		}
	}
	if len(enabledHosts) == 0 {
		this.Status = fmt.Sprintf("No enabled hosts for server: %v, %v", this.Name, this.Listen)
		return errors.New(this.Status)
	}
	for _, host := range enabledHosts {
		if _, _, err := net.SplitHostPort(host.Upstream); err != nil {
			host.Status = fmt.Sprintf("Invalid upstream '%v' for host: %v, server: %v: %v", host.Upstream, host.Name, this.Name, err)
			return errors.New(host.Status)
		}
	}

	listener, err := net.Listen("tcp", this.Listen)
	if err != nil {
		this.Status = fmt.Sprintf("%v for server: %v, %v", err, this.Name, this.Listen)
		return errors.New(this.Status)
	}
	this.listener = listener
	slog.Info("Server listening", "server", this.Name, "type", this.Type, "listen", this.Listen)

	go this.acceptTCP(listener, enabledHosts)
	return nil
}

func (this *Server) acceptTCP(listener net.Listener, enabledHosts []*Host) {
	logger := slog.With("server", this.Name, "listen", this.Listen)
	var delay time.Duration
	for {
		connLocal, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// back off so persistent errors (e.g. EMFILE) don't spin the CPU
			if delay == 0 {
				delay = 5 * time.Millisecond
			} else {
				delay *= 2
			}
			if delay > time.Second {
				delay = time.Second
			}
			logger.Warn("Accept failed", "err", err)
			time.Sleep(delay)
			continue
		}
		delay = 0

		go func() {
			client := connLocal.RemoteAddr().String()
			enabledHost := enabledHosts[hashIndex(clientIP(client), len(enabledHosts))]
			connLogger := logger.With("host", enabledHost.Name, "upstream", enabledHost.Upstream, "client", client)
			connDst, err := net.Dial("tcp", enabledHost.Upstream)
			if err != nil {
				connLogger.Error("Failed to connect to upstream", "err", err)
				connLocal.Close()
				return
			}
			connLogger.Debug("Connection opened")
			start := time.Now()
			sent, received := pipe(connLocal, connDst, connLogger)
			if this.AccessLog {
				accessLog.Info("connection",
					"server", this.Name,
					"host", enabledHost.Name,
					"client", client,
					"upstream", enabledHost.Upstream,
					"duration_ms", durationMs(start),
					"bytes_sent", sent,
					"bytes_received", received,
				)
			}
		}()
	}
}

func Hook(clean func()) {
	sigs := make(chan os.Signal, 1)
	done := make(chan bool, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigs
		if clean != nil {
			clean()
		}
		done <- true
	}()
	<-done
}

// pipe copies data between client and upstream in both directions,
// half-closing each write side when the opposite read side reaches EOF, and
// closes both connections when both directions are done. It returns the bytes
// sent to and received from the client.
func pipe(client, upstream net.Conn, logger *slog.Logger) (sent, received int64) {
	defer client.Close()
	defer upstream.Close()
	done := make(chan struct{})
	go func() {
		received = copyHalf(upstream, client, logger)
		close(done)
	}()
	sent = copyHalf(client, upstream, logger)
	<-done
	return sent, received
}

func copyHalf(dst, src net.Conn, logger *slog.Logger) int64 {
	n, err := io.Copy(dst, src)
	if err != nil && !errors.Is(err, net.ErrClosed) {
		if errors.Is(err, syscall.ECONNRESET) {
			// routine teardown: one side dropped without a clean close
			logger.Debug("Connection reset", "err", err)
		} else {
			logger.Warn("Copy failed", "err", err)
		}
	}
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	} else {
		dst.Close()
	}
	return n
}

func getEnv(key, def string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return def
}
