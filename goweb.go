package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"

	"github.com/elgs/gostrgen"
)

var servers []*Server

func main() {
	confPath := flag.String("c", "goweb.json", "configration file path")
	flag.Parse()
	confBytes, err := ioutil.ReadFile(*confPath)
	if err != nil {
		fmt.Println(err)
		return
	}
	servers, err = NewConfig(confBytes)
	if err != nil {
		fmt.Println(err)
		return
	}

	for _, server := range servers {
		err := server.Start()
		if err != nil {
			log.Fatal(err)
		}
	}

	err = StartAdmin()
	if err != nil {
		log.Fatal(err)
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
	if this.server != nil {
		return this.server.Shutdown(context.Background())
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
		if host.HttpRedirectPort > 0 {
			redirectUrl := fmt.Sprintf("https://%v:%v%v", host.Name, host.HttpRedirectPort, r.RequestURI)
			http.Redirect(w, r, redirectUrl, http.StatusMovedPermanently)
		} else {
			indexPath := path.Join(host.Path, r.URL.Path)
			if host.DisableDirListing && strings.HasSuffix(r.URL.Path, "/") && indexFileNotExists(indexPath) {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, "404 page not found")
				return
			}
			http.FileServer(http.Dir(host.Path)).ServeHTTP(w, r)
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
		this.server = &srv

		go func() {
			err := srv.ListenAndServeTLS("", "")
			if err != nil {
				log.Println(err, fmt.Sprintf("%v://%v/", this.Type, this.Listen))
			}
		}()
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
		this.server = &srv

		go func() {
			err := srv.ListenAndServe()
			if err != nil {
				log.Println(err, fmt.Sprintf("%v://%v/", this.Type, this.Listen))
			}
		}()
	}
	log.Println(fmt.Sprintf("Listening on %v://%v/", this.Type, this.Listen))
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
