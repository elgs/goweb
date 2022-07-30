package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func main() {

	confPath := flag.String("c", "goweb.json", "configration file path")
	flag.Parse()
	confBytes, err := ioutil.ReadFile(*confPath)
	if err != nil {
		log.Fatalln(err)
	}
	servers, err := NewConfig(confBytes)
	if err != nil {
		log.Fatalln(err)
	}

	for _, server := range *servers {
		mux := http.NewServeMux()
		server.hostMap = make(map[string]*Host, len(*server.Hosts))
		if server.Type == "https" {
			cfg := &tls.Config{}

			for i, host := range *server.Hosts {
				keyPair, err := tls.LoadX509KeyPair(host.CertPath, host.KeyPath)
				if err != nil {
					log.Fatal(err)
				}
				cfg.Certificates = append(cfg.Certificates, keyPair)
				server.hostMap[host.Name] = &(*server.Hosts)[i]
			}

			cfg.BuildNameToCertificate()

			handler := func(w http.ResponseWriter, r *http.Request) {
				requestedHost := strings.Split(r.Host, ":")[0]
				host := server.hostMap[requestedHost]
				http.FileServer(http.Dir(host.Path)).ServeHTTP(w, r)
			}

			mux.HandleFunc("/", handler)

			srv := http.Server{
				Addr:      server.Listen,
				Handler:   mux,
				TLSConfig: cfg,
			}

			go func() {
				log.Fatal(srv.ListenAndServeTLS("", ""))
			}()
		} else if server.Type == "http" {
			for i, host := range *server.Hosts {
				server.hostMap[host.Name] = &(*server.Hosts)[i]
			}
			handler := func(w http.ResponseWriter, r *http.Request) {
				requestedHost := strings.Split(r.Host, ":")[0]
				host := server.hostMap[requestedHost]
				if host.HttpRedirectPort > 0 {
					redirectUrl := fmt.Sprintf("https://%v:%v", host.Name, host.HttpRedirectPort)
					http.Redirect(w, r, redirectUrl, http.StatusMovedPermanently)
				} else {
					http.FileServer(http.Dir(host.Path)).ServeHTTP(w, r)
				}
			}

			mux.HandleFunc("/", handler)

			srv := http.Server{
				Addr:    server.Listen,
				Handler: mux,
			}

			go func() {
				log.Fatal(srv.ListenAndServe())
			}()
		}
		fmt.Println(fmt.Sprintf("Listening on %v://%v/", server.Type, server.Listen))
	}
	Hook()
}

func Hook() {
	sigs := make(chan os.Signal, 1)
	done := make(chan bool, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		for {
			select {
			case sig := <-sigs:
				fmt.Println(sig)
				// cleanup code here
				done <- true
			}
		}
	}()
	<-done
}
