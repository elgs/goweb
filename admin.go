package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math/rand"
	"net/http"

	"github.com/elgs/gostrgen"
)

var dev = false

//go:embed examples/web/example.com
var examples embed.FS

func StartAdmin() error {

	secret, err := gostrgen.RandGen(32, gostrgen.LowerDigit, "", "")
	if err != nil {
		return err
	}

	port := rand.Intn(10000) + 50000
	if dev {
		port = 2022
	}
	listen := fmt.Sprintf("127.0.0.1:%v", port)

	// adminServer := &Server{
	// 	Name:   "AdminServer",
	// 	Type:   "http",
	// 	Listen: listen,
	// 	Hosts: []*Host{&Host{
	// 		DisableDirListing: true,
	// 		// Name: ,
	// 	}},
	// }

	mux := http.NewServeMux()
	sub, err := fs.Sub(examples, "examples/web/example.com")
	if err != nil {
		log.Fatal(err)
	}

	mux.HandleFunc("/admin/", func(w http.ResponseWriter, r *http.Request) {
		// token := r.URL.Query().Get("access_token")
		// if token != secret {
		// 	fmt.Fprintln(w, "Invalid access token.")
		// 	return
		// }
		http.StripPrefix("/admin/", http.FileServer(http.FS(sub))).ServeHTTP(w, r)
	})

	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("access_token")
		if token != secret {
			w.WriteHeader(http.StatusBadRequest)
			err := errors.New("Invalid access token.")
			fmt.Fprintln(w, fmt.Sprintf(`{"err":"%v"}`, err))
			log.Println(err)
			return
		}
		b, err := json.Marshal(servers)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintln(w, fmt.Sprintf(`{"err":"%v"}`, err))
			log.Println(err)
			return
		}
		fmt.Fprint(w, string(b))
	})

	go func() {
		err := http.ListenAndServe(listen, mux)
		if err != nil {
			log.Fatal(err)
		}
	}()
	log.Println(fmt.Sprintf("Web admin url: http://%v/api?access_token=%v", listen, secret))
	return nil
}
