package main

import (
	"crypto/tls"
	"log"
	"net/http"
	"strings"
)

func main() {
	cfg := &tls.Config{}

	cert0, err := tls.LoadX509KeyPair(
		"/Users/qianchen/Desktop/certs/example.com/example.com.pem",
		"/Users/qianchen/Desktop/certs/example.com/example.com-key.pem",
	)
	if err != nil {
		log.Fatal(err)
	}

	cert1, err := tls.LoadX509KeyPair(
		"/Users/qianchen/Desktop/certs/test.example.com/test.example.com.pem",
		"/Users/qianchen/Desktop/certs/test.example.com/test.example.com-key.pem",
	)
	if err != nil {
		log.Fatal(err)
	}

	cfg.Certificates = append(cfg.Certificates, cert0)
	cfg.Certificates = append(cfg.Certificates, cert1)

	cfg.BuildNameToCertificate()

	handler := func(w http.ResponseWriter, r *http.Request) {
		http.FileServer(http.Dir(strings.Split(r.Host, ":")[0])).ServeHTTP(w, r)
	}

	http.HandleFunc("/", handler)

	server := http.Server{
		Addr:      "127.0.0.1:1443",
		TLSConfig: cfg,
	}

	server.ListenAndServeTLS("", "")
}
