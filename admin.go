package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/rand"
	"net/http"
	"os"

	"github.com/elgs/gostrgen"
)

var dev = os.Getenv("env") == "dev"

//go:embed webadmin
var webadmin embed.FS

func CheckAccessToken(secret string, w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Allow-Methods", r.Header.Get("Access-Control-Request-Method"))
	w.Header().Set("Access-Control-Allow-Headers", r.Header.Get("Access-Control-Request-Headers"))
	if r.Method == "OPTIONS" {
		return true
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	token := r.Header.Get("authorization")
	if token != secret {
		w.WriteHeader(http.StatusUnauthorized)
		err := errors.New("Invalid access token.")
		fmt.Fprintf(w, `{"err":"%v"}`, err)
		log.Println(err)
		return true
	}
	return false
}

func StartAdmin() error {
	secret, _ := gostrgen.RandGen(32, gostrgen.LowerDigit, "", "")
	port := rand.Intn(10000) + 50000
	if dev {
		port = 2022
		secret = "a"
	}
	listen := fmt.Sprintf("[::]:%v", port)

	mux := http.NewServeMux()
	sub, err := fs.Sub(webadmin, "webadmin")
	if err != nil {
		log.Fatal(err)
	}

	mux.Handle("/admin/", http.StripPrefix("/admin/", http.FileServer(http.FS(sub))))

	mux.HandleFunc("/api/servers/", func(w http.ResponseWriter, r *http.Request) {
		if CheckAccessToken(secret, w, r) {
			return
		}

		if r.Method == http.MethodPatch {
			body, err := io.ReadAll(r.Body)
			defer r.Body.Close()
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `{"err":"%v"}`, err)
				log.Println(err)
				return
			}
			var bodyData []*Server
			err = json.Unmarshal(body, &bodyData)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `{"err":"%v"}`, err)
				log.Println(err)
				return
			}
			for _, server := range servers {
				err := server.Shutdown()
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprintf(w, `{"err":"%v"}`, err)
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
							fmt.Fprintf(w, `{"err":"%v"}`, err)
							log.Println(err)
							return
						}
					}
					for _, server := range servers {
						err := server.Shutdown()
						if err != nil {
							w.WriteHeader(http.StatusBadRequest)
							fmt.Fprintf(w, `{"err":"%v"}`, err)
							log.Println(err)
							return
						}
					}
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprintf(w, `{"err":"%v"}`, err)
					log.Println(err)
					return
				}
			}
			servers = bodyData
			fmt.Fprint(w, "{}")
		} else if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			defer r.Body.Close()
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `{"err":"%v"}`, err)
				log.Println(err)
				return
			}
			var formattedServersJSONBuffer bytes.Buffer
			err = json.Indent(&formattedServersJSONBuffer, body, "", "  ")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `{"err":"%v"}`, err)
				log.Println(err)
				return
			}
			err = os.WriteFile(*confPath, formattedServersJSONBuffer.Bytes(), 0644)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `{"err":"%v"}`, err)
				log.Println(err)
				return
			}
			fmt.Fprint(w, "{}")
		} else if r.Method == http.MethodGet {
			b, err := json.Marshal(servers)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprintf(w, `{"err":"%v"}`, err)
				log.Println(err)
				return
			}
			fmt.Fprint(w, string(b))
		}
	})

	mux.HandleFunc("/api/server/", func(w http.ResponseWriter, r *http.Request) {
		if CheckAccessToken(secret, w, r) {
			return
		}

		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			defer r.Body.Close()
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `{"err":"%v"}`, err)
				log.Println(err)
				return
			}
			var bodyData *Server
			err = json.Unmarshal(body, &bodyData)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `{"err":"%v"}`, err)
				log.Println(err)
				return
			}

			if bodyData.Name == "" {
				err := bodyData.Start()
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprintf(w, `{"err":"%v"}`, err)
					log.Println(err)
					return
				}
				servers = append(servers, bodyData)
			} else {
				for serverIndex, server := range servers {
					if server.Name == bodyData.Name {
						err := server.Shutdown()
						if err != nil {
							w.WriteHeader(http.StatusBadRequest)
							fmt.Fprintf(w, `{"err":"%v"}`, err)
							log.Println(err)
							return
						}
						err = bodyData.Start()
						if err != nil {
							w.WriteHeader(http.StatusBadRequest)
							fmt.Fprintf(w, `{"err":"%v"}`, err)
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
	log.Printf("Web admin url: http://%v/admin\n", listen)
	log.Printf("Access token: %v\n", secret)
	return nil
}
