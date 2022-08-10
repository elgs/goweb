package main

import (
	"context"
	"crypto/tls"
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
	"syscall"

	"github.com/elgs/gostrgen"
)

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	fmt.Println("v3")
}

var servers []*Server
var confPath *string

func main() {
	confPath = flag.String("c", "goweb.json", "configration file path")
	startAdmin := flag.Bool("admin", false, "start admin web interface")
	flag.Parse()
	confBytes, err := os.ReadFile(*confPath)
	if err != nil {
		if *startAdmin {
			servers = []*Server{}
			log.Println(err)
		} else {
			log.Fatalln(err)
		}
	} else {
		servers, err = NewConfig(confBytes)
		if err != nil {
			log.Fatalln(err)
		}
	}

	for _, server := range servers {
		err := server.Start()
		if err != nil {
			log.Fatalln(err)
		}
	}

	if dev {
		*startAdmin = true
	}

	if *startAdmin {
		err = StartAdmin()
		if err != nil {
			log.Fatalln(err)
		}
	}

	Hook(func() {
		for _, server := range servers {
			err := server.Shutdown()
			if err != nil {
				log.Println(err)
			}
		}
	})
}

func (this *Server) Shutdown() error {
	if this.Type == "https" || this.Type == "http" {
		if this.httpServer != nil {
			return this.httpServer.Shutdown(context.Background())
		}
	} else if this.Type == "tcp" {
		this.tcpListening = false
		if this.tcpListener != nil {
			this.tcpListener.Close()
			log.Printf("%v: Server closed %v", this.Type, this.Listen)
		}
	}
	return nil
}

func indexFileNotExists(dir string) bool {
	indexPath := path.Join(dir, "index.html")
	if stats, err := os.Stat(indexPath); errors.Is(err, os.ErrNotExist) || stats.IsDir() {
		return true
	}
	return false
}

func (this *Server) Start() error {
	if this.RuntimeId == "" {
		this.RuntimeId, _ = gostrgen.RandGen(32, gostrgen.LowerDigit, "", "")
	}
	if this.Disabled {
		return nil
	}
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "goweb")
		requestedHost := strings.Split(r.Host, ":")[0]
		host := this.hostMap[requestedHost]
		if host == nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			fmt.Fprint(w, fmt.Sprintf(`{"err":"Host '%v' is disabled."}`, requestedHost))
			return
		}
		if host.Type == "301_redirect" {
			http.Redirect(w, r, fmt.Sprintf("%v%v", host.RedirectURL, r.RequestURI), http.StatusMovedPermanently)
		} else if host.Type == "serve_static" {
			indexPath := path.Join(host.Path, r.URL.Path)
			if host.DisableDirListing && strings.HasSuffix(r.URL.Path, "/") && indexFileNotExists(indexPath) {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, "404 page not found")
				return
			}
			http.FileServer(http.Dir(host.Path)).ServeHTTP(w, r)
		} else if host.Type == "reverse_proxy" {
			forwardURLs := strings.Fields(host.ForwardURLs)
			h := fnv.New32a()
			h.Write([]byte(r.Host))
			forwardURL := forwardURLs[int(h.Sum32())%len(forwardURLs)]
			requestURL := fmt.Sprintf("%v%v", forwardURL, r.RequestURI)

			client := &http.Client{
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					return http.ErrUseLastResponse
				},
			}
			req, err := http.NewRequest(r.Method, requestURL, r.Body)
			defer r.Body.Close()
			if err != nil {
				log.Println(err)
			}
			// copy request headers
			for k, vs := range r.Header {
				for _, v := range vs {
					req.Header.Set(k, v)
				}
			}

			res, err := client.Do(req)
			if err != nil {
				log.Println(err)
			}

			body, err := io.ReadAll(res.Body)
			defer res.Body.Close()
			if err != nil {
				log.Println(err)
			}
			// copy response headers
			for k, vs := range res.Header {
				for _, v := range vs {
					if strings.ToLower(k) == "location" {
						lURL, err := url.Parse(v)
						if err != nil {
							log.Println(err)
						}
						fURL, err := url.Parse(forwardURL)
						if err != nil {
							log.Println(err)
						}
						if fURL.Scheme == "http" && fURL.Scheme == lURL.Scheme {
							v = strings.ReplaceAll(v, fmt.Sprintf("%v://%v", fURL.Scheme, strings.TrimSuffix(fURL.Host, ":80")), "")
						} else if fURL.Scheme == "https" && fURL.Scheme == lURL.Scheme {
							v = strings.ReplaceAll(v, fmt.Sprintf("%v://%v", fURL.Scheme, strings.TrimSuffix(fURL.Host, ":443")), "")
						} else {
							v = strings.ReplaceAll(v, forwardURL, "")
						}
					}
					w.Header().Set(k, v)
				}
			}
			w.WriteHeader(res.StatusCode)
			w.Write(body)
		}
	}

	mux := http.NewServeMux()
	this.hostMap = make(map[string]*Host, len(this.Hosts))
	if this.Type == "https" {
		cfg := &tls.Config{}

		for _, host := range this.Hosts {
			if host.RuntimeId == "" {
				host.RuntimeId, _ = gostrgen.RandGen(32, gostrgen.LowerDigit, "", "")
			}
			if host.Disabled {
				continue
			}
			keyPair, err := tls.LoadX509KeyPair(host.CertPath, host.KeyPath)
			if err != nil {
				return fmt.Errorf("%v for host: %v, server: %v, %v", err, host.Name, this.Name, this.Listen)
			}
			cfg.Certificates = append(cfg.Certificates, keyPair)
			this.hostMap[host.Name] = host
		}

		cfg.BuildNameToCertificate()

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
		log.Println(fmt.Sprintf("Listening on %v://%v/", this.Type, this.Listen))
	} else if this.Type == "http" {
		for _, host := range this.Hosts {
			if host.RuntimeId == "" {
				host.RuntimeId, _ = gostrgen.RandGen(32, gostrgen.LowerDigit, "", "")
			}
			if host.Disabled {
				continue
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
				log.Println(err, fmt.Sprintf("%v://%v/", this.Type, this.Listen))
			}
		}()
		log.Println(fmt.Sprintf("Listening on %v://%v/", this.Type, this.Listen))
	} else if this.Type == "tcp" {
		enabledHosts := make([]*Host, 0, len(this.Hosts))
		for _, host := range this.Hosts {
			if !host.Disabled {
				enabledHosts = append(enabledHosts, host)
			}
		}

		listener, err := net.Listen("tcp", this.Listen)
		this.tcpListener = listener
		if err != nil {
			log.Println(err)
		}
		log.Println(fmt.Sprintf("Listening on %v %v", this.Type, this.Listen))
		this.tcpListening = true

		go func() {
			for {
				if !this.tcpListening {
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
		select {
		case <-sigs:
			if clean != nil {
				clean()
			}
			done <- true
		}
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
			log.Println(err)
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
