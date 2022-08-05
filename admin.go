package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"

	"github.com/elgs/gostrgen"
)

var dev = false

//go:embed webadmin
var webadmin embed.FS

func StartAdmin() error {

	secret, err := gostrgen.RandGen(32, gostrgen.LowerDigit, "", "")
	if err != nil {
		return err
	}

	port := rand.Intn(10000) + 50000
	if dev {
		port = 2022
	}
	listen := fmt.Sprintf("[::]:%v", port)

	mux := http.NewServeMux()
	sub, err := fs.Sub(webadmin, "webadmin")
	if err != nil {
		log.Fatal(err)
	}

	mux.HandleFunc("/admin/", func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("access_token")
		if !dev && token != secret {
			fmt.Fprintln(w, "Invalid access token.")
			return
		}
		http.StripPrefix("/admin/", http.FileServer(http.FS(sub))).ServeHTTP(w, r)
	})

	mux.HandleFunc("/api/servers/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		token := r.URL.Query().Get("access_token")
		if !dev && token != secret {
			w.WriteHeader(http.StatusBadRequest)
			err := errors.New("Invalid access token.")
			fmt.Fprintln(w, fmt.Sprintf(`{"err":"%v"}`, err))
			log.Println(err)
			return
		}

		if r.Method == http.MethodPost {
			body, err := ioutil.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, fmt.Sprintf(`{"err":"%v"}`, err))
				log.Println(err)
				return
			}
			var bodyData []*Server
			err = json.Unmarshal(body, &bodyData)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, fmt.Sprintf(`{"err":"%v"}`, err))
				log.Println(err)
				return
			}
			for _, server := range servers {
				err := server.Shutdown()
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, fmt.Sprintf(`{"err":"%v"}`, err))
					log.Println(err)
					return
				}
			}
			for _, server := range bodyData {
				err := server.Start()
				if err != nil {
					for _, server := range bodyData {
						err := server.Shutdown()
						if err != nil {
							w.WriteHeader(http.StatusBadRequest)
							fmt.Fprint(w, fmt.Sprintf(`{"err":"%v"}`, err))
							log.Println(err)
							return
						}
					}
					for _, server := range servers {
						err := server.Shutdown()
						if err != nil {
							w.WriteHeader(http.StatusBadRequest)
							fmt.Fprint(w, fmt.Sprintf(`{"err":"%v"}`, err))
							log.Println(err)
							return
						}
					}
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, fmt.Sprintf(`{"err":"%v"}`, err))
					log.Println(err)
					return
				}
			}
			servers = bodyData
			fmt.Fprint(w, "{}")
		} else if r.Method == http.MethodGet {
			b, err := json.Marshal(servers)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprintln(w, fmt.Sprintf(`{"err":"%v"}`, err))
				log.Println(err)
				return
			}
			fmt.Fprint(w, string(b))
		}
	})

	mux.HandleFunc("/api/server/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		token := r.URL.Query().Get("access_token")
		if !dev && token != secret {
			w.WriteHeader(http.StatusBadRequest)
			err := errors.New("Invalid access token.")
			fmt.Fprintln(w, fmt.Sprintf(`{"err":"%v"}`, err))
			log.Println(err)
			return
		}

		if r.Method == http.MethodPost {
			body, err := ioutil.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, fmt.Sprintf(`{"err":"%v"}`, err))
				log.Println(err)
				return
			}
			var bodyData *Server
			err = json.Unmarshal(body, &bodyData)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, fmt.Sprintf(`{"err":"%v"}`, err))
				log.Println(err)
				return
			}

			if bodyData.RuntimeId == "" {
				err := bodyData.Start()
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, fmt.Sprintf(`{"err":"%v"}`, err))
					log.Println(err)
					return
				}
				servers = append(servers, bodyData)
			} else {
				for serverIndex, server := range servers {
					if server.RuntimeId == bodyData.RuntimeId {
						err := server.Shutdown()
						if err != nil {
							w.WriteHeader(http.StatusBadRequest)
							fmt.Fprint(w, fmt.Sprintf(`{"err":"%v"}`, err))
							log.Println(err)
							return
						}
						err = bodyData.Start()
						if err != nil {
							w.WriteHeader(http.StatusBadRequest)
							fmt.Fprint(w, fmt.Sprintf(`{"err":"%v"}`, err))
							log.Println(err)
							return
						}
						servers[serverIndex] = bodyData
					}
				}
			}
			fmt.Fprint(w, "{}")
		}
	})

	go func() {
		err := http.ListenAndServe(listen, mux)
		if err != nil {
			log.Fatal(err)
		}
	}()
	fmt.Println(fmt.Sprintf("Web admin url: http://%v/admin", listen))
	fmt.Println(fmt.Sprintf("Access token: %v", secret))
	return nil
}
