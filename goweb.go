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
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"runtime"
	"strings"
	"sync"
	"syscall"
)

const version = "9"

var secret = getEnv("GOWEB_ADMIN_TOKEN", "")
var host = getEnv("GOWEB_ADMIN_HOST", "localhost")
var port = getEnv("GOWEB_ADMIN_PORT", "13579")

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

var mu sync.Mutex
var servers []*Server
var confPath *string

func main() {
	v := flag.Bool("v", false, "prints version")
	confPath = flag.String("c", "goweb.json", "configuration file path")
	flag.Parse()
	if *v {
		fmt.Println(version)
		os.Exit(0)
	}
	confBytes, err := os.ReadFile(*confPath)
	if err != nil {
		log.Fatalln(err)
	}

	servers, err = NewConfig(confBytes)
	if err != nil {
		log.Fatalln(err)
	}

	for _, server := range servers {
		err := server.Start()
		if err != nil {
			log.Println(err)
		}
	}

	if secret != "" {
		err = StartAdmin()
		if err != nil {
			log.Fatalln(err)
		}
	}

	Hook(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, server := range servers {
			err := server.Shutdown()
			if err != nil {
				log.Println(err)
			}
		}
	})
}

func (this *Server) Shutdown() error {
	switch this.Type {
	case "https", "http":
		if this.httpServer != nil {
			return this.httpServer.Shutdown(context.Background())
		}
	case "tcp":
		this.tcpListening.Store(false)
		if this.tcpListener != nil {
			this.tcpListener.Close()
			log.Printf("%v: Server closed %v", this.Type, this.Listen)
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
	var fileServers sync.Map
	proxyClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "goweb")
		requestedHost := strings.Split(r.Host, ":")[0]
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
			indexPath := path.Join(host.Path, r.URL.Path)
			if host.DisableDirListing && strings.HasSuffix(r.URL.Path, "/") && indexFileNotExists(indexPath) {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprintf(w, `{"err":"404 page not found"}`)
				return
			}
			fsVal, ok := fileServers.Load(host.Name)
			if !ok {
				fsVal = http.FileServer(http.Dir(host.Path))
				fileServers.Store(host.Name, fsVal)
			}
			fsVal.(http.Handler).ServeHTTP(w, r)
		case "reverse_proxy":
			forwardURLs := strings.Fields(host.ForwardURLs)
			if len(forwardURLs) == 0 {
				log.Printf("No forward URLs configured for host %v", host.Name)
				http.Error(w, `{"err":"no upstream configured"}`, http.StatusBadGateway)
				return
			}
			h := fnv.New32a()
			clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
			h.Write([]byte(clientIP))
			forwardURL := forwardURLs[int(h.Sum32())%len(forwardURLs)]
			requestURL := fmt.Sprintf("%v%v", forwardURL, r.RequestURI)

			defer r.Body.Close()
			req, err := http.NewRequest(r.Method, requestURL, r.Body)
			if err != nil {
				log.Println(err)
				http.Error(w, `{"err":"internal server error"}`, http.StatusInternalServerError)
				return
			}
			// copy request headers
			for k, vs := range r.Header {
				for _, v := range vs {
					req.Header.Add(k, v)
				}
			}

			res, err := proxyClient.Do(req)
			if err != nil {
				log.Println(err)
				http.Error(w, `{"err":"bad gateway"}`, http.StatusBadGateway)
				return
			}
			defer res.Body.Close()

			body, err := io.ReadAll(res.Body)
			if err != nil {
				log.Println(err)
				http.Error(w, `{"err":"internal server error"}`, http.StatusInternalServerError)
				return
			}
			// copy response headers
			for k, vs := range res.Header {
				for _, v := range vs {
					if strings.ToLower(k) == "location" {
						lURL, lErr := url.Parse(v)
						fURL, fErr := url.Parse(forwardURL)
						if lErr != nil {
							log.Println(lErr)
						} else if fErr != nil {
							log.Println(fErr)
						} else if fURL.Scheme == "http" && fURL.Scheme == lURL.Scheme {
							v = strings.ReplaceAll(v, fmt.Sprintf("%v://%v", fURL.Scheme, strings.TrimSuffix(fURL.Host, ":80")), "")
						} else if fURL.Scheme == "https" && fURL.Scheme == lURL.Scheme {
							v = strings.ReplaceAll(v, fmt.Sprintf("%v://%v", fURL.Scheme, strings.TrimSuffix(fURL.Host, ":443")), "")
						} else {
							v = strings.ReplaceAll(v, forwardURL, "")
						}
					}
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(res.StatusCode)
			w.Write(body)
		}
	}

	mux := http.NewServeMux()
	this.hostMap = make(map[string]*Host, len(this.Hosts))
	if this.Type == "https" {
		cfg := &tls.Config{
			MinVersion: tls.VersionTLS12,
		}

		for _, host := range this.Hosts {
			if host.Name == "" {
				host.Status = fmt.Sprintf("Host name is required, server: %v, %v", this.Name, this.Listen)
				return errors.New(host.Status)
			}
			keyPair, err := tls.LoadX509KeyPair(host.CertPath, host.KeyPath)
			if err != nil {
				host.Status = fmt.Sprintf("%v for host: %v, server: %v, %v", err, host.Name, this.Name, this.Listen)
				return errors.New(host.Status)
			}
			cfg.Certificates = append(cfg.Certificates, keyPair)
			this.hostMap[host.Name] = host
		}

		// cfg.BuildNameToCertificate()

		mux.HandleFunc("/", handler)

		srv := http.Server{
			Addr:      this.Listen,
			Handler:   mux,
			TLSConfig: cfg,
		}
		this.httpServer = &srv

		go func() {
			err := srv.ListenAndServeTLS("", "")
			if err != nil {
				log.Println(err, fmt.Sprintf("%v://%v/", this.Type, this.Listen))
			}
		}()
		log.Printf("Listening on %v://%v/\n", this.Type, this.Listen)
	} else if this.Type == "http" {
		for _, host := range this.Hosts {
			if host.Name == "" {
				host.Status = fmt.Sprintf("Host name is required, server: %v, %v", this.Name, this.Listen)
				return errors.New(host.Status)
			}
			this.hostMap[host.Name] = host
		}

		mux.HandleFunc("/", handler)

		srv := http.Server{
			Addr:    this.Listen,
			Handler: mux,
		}
		this.httpServer = &srv

		go func() {
			err := srv.ListenAndServe()
			if err != nil {
				this.Status = fmt.Sprintf("%v for server: %v, %v", err, this.Name, this.Listen)
				log.Println(this.Status)
			}
		}()
		log.Printf("Listening on %v://%v/\n", this.Type, this.Listen)
	} else if this.Type == "tcp" {
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

		listener, err := net.Listen("tcp", this.Listen)
		if err != nil {
			this.Status = fmt.Sprintf("%v for server: %v, %v", err, this.Name, this.Listen)
			return errors.New(this.Status)
		}
		this.tcpListener = listener
		log.Printf("Listening on %v %v\n", this.Type, this.Listen)
		this.tcpListening.Store(true)

		go func() {
			for {
				if !this.tcpListening.Load() {
					break
				}
				connLocal, err := this.tcpListener.Accept()
				if err != nil {
					// log.Println(err)
					continue
				}

				go func() {
					h := fnv.New32a()
					h.Write([]byte(connLocal.RemoteAddr().String()))
					enabledHost := enabledHosts[int(h.Sum32())%len(enabledHosts)]
					connDst, err := net.Dial("tcp", enabledHost.Upstream)
					if err != nil {
						log.Println(err)
						connLocal.Close()
						return
					}
					go pipe(connLocal, connDst, 4096)
					pipe(connDst, connLocal, 4096)
				}()
			}
		}()
	}

	return nil
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

func pipe(connLocal net.Conn, connDst net.Conn, bufSize int) {
	var buffer = make([]byte, bufSize)
	for {
		runtime.Gosched()
		n, err := connLocal.Read(buffer)
		if err != nil {
			connLocal.Close()
			connDst.Close()
			if err != io.EOF {
				log.Println(err)
			}
			break
		}
		if n > 0 {
			_, err := connDst.Write(buffer[0:n])
			if err != nil {
				connLocal.Close()
				connDst.Close()
				log.Println(err)
				break
			}
		}
	}
}

func getEnv(key, def string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return def
}
