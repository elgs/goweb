package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
)

type Server struct {
	Name     string `json:"name"`
	Disabled bool   `json:"disabled"`
	Type     string `json:"type"`
	Listen   string `json:"listen"`
	Hosts    []Host `json:"hosts"`
	hostMap  map[string]*Host
	server   *http.Server
}

func (this *Server) Shutdown() error {
	return this.server.Shutdown(context.Background())
}

func indexFileNotExists(dir string) bool {
	indexPath := path.Join(dir, "index.html")
	if stats, err := os.Stat(indexPath); errors.Is(err, os.ErrNotExist) || stats.IsDir() {
		// Please don't hack with index.html/index.html, you will get 404 if you request index.html or index.html/.
		return true
	}
	return false
}

func (this *Server) Start() error {
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

		for hostIndex := range this.Hosts {
			host := this.Hosts[hostIndex]
			if host.Disabled {
				continue
			}
			keyPair, err := tls.LoadX509KeyPair(host.CertPath, host.KeyPath)
			if err != nil {
				return err
			}
			cfg.Certificates = append(cfg.Certificates, keyPair)
			this.hostMap[host.Name] = &host
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
			log.Fatal(srv.ListenAndServeTLS("", ""))
		}()
	} else if this.Type == "http" {
		for hostIndex := range this.Hosts {
			host := this.Hosts[hostIndex]
			if host.Disabled {
				continue
			}
			this.hostMap[host.Name] = &host
		}

		mux.HandleFunc("/", handler)

		srv := http.Server{
			Addr:    this.Listen,
			Handler: mux,
		}
		this.server = &srv

		go func() {
			log.Fatal(srv.ListenAndServe())
		}()
	}
	log.Println(fmt.Sprintf("Listening on %v://%v/", this.Type, this.Listen))
	return nil
}

type Host struct {
	Disabled          bool    `json:"disabled"`
	DisableDirListing bool    `json:"disable_dir_listing"`
	Name              string  `json:"name"`
	Path              string  `json:"path"`
	HttpRedirectPort  float64 `json:"https_redirect_port"`
	CertPath          string  `json:"cert_path"`
	KeyPath           string  `json:"key_path"`
}

func NewConfig(confBytes []byte) ([]Server, error) {
	var servers []Server
	err := json.Unmarshal(confBytes, &servers)
	return servers, err
}
