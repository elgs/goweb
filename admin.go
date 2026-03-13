package main

import (
	"bytes"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
)

var dev = os.Getenv("env") == "dev"

//go:embed gowebadmin/dist
var gowebadmin embed.FS

func CheckAccessToken(secret string, w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == fmt.Sprintf("http://%v:%v", host, port) || origin == fmt.Sprintf("https://%v:%v", host, port) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}
	w.Header().Set("Access-Control-Allow-Methods", r.Header.Get("Access-Control-Request-Method"))
	w.Header().Set("Access-Control-Allow-Headers", r.Header.Get("Access-Control-Request-Headers"))
	if r.Method == "OPTIONS" {
		return true
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	token := r.Header.Get("authorization")
	if subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"err": "Invalid access token."})
		log.Println("Invalid access token.")
		return true
	}
	return false
}

func LoadServersFromRequestBody(r *http.Request) ([]*Server, error) {
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var _servers []*Server
	err = json.Unmarshal(body, &_servers)
	if err != nil {
		return nil, err
	}
	return _servers, nil
}

func LoadServerFromRequestBody(r *http.Request) (*Server, error) {
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var _server Server
	err = json.Unmarshal(body, &_server)
	if err != nil {
		return nil, err
	}
	return &_server, nil
}

func StartAdmin() error {
	listen := fmt.Sprintf("%v:%v", host, port)

	mux := http.NewServeMux()
	sub, err := fs.Sub(gowebadmin, "gowebadmin/dist")
	if err != nil {
		log.Fatal(err)
	}

	// mux.Handle("/admin/", http.StripPrefix("/admin/", http.FileServer(http.FS(sub))))
	mux.Handle("/", http.FileServer(http.FS(sub)))

	writeErr := func(w http.ResponseWriter, status int, err error) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"err": err.Error()})
		log.Println(err)
	}

	mux.HandleFunc("/api/servers/", func(w http.ResponseWriter, r *http.Request) {
		if CheckAccessToken(secret, w, r) {
			return
		}

		switch r.Method {
		case http.MethodPatch:
			// apply servers
			_servers, err := LoadServersFromRequestBody(r)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			oldServers := servers
			for _, server := range oldServers {
				err := server.Shutdown()
				if err != nil {
					writeErr(w, http.StatusBadRequest, err)
					return
				}
			}
			for _, server := range _servers {
				err := server.Start()
				if err != nil {
					// rollback: shut down new servers that started
					for _, s := range _servers {
						s.Shutdown()
					}
					// restore old servers
					for _, s := range oldServers {
						s.Start()
					}
					writeErr(w, http.StatusBadRequest, err)
					return
				}
			}
			servers = _servers
			fmt.Fprint(w, "{}")
		case http.MethodPost:
			// save servers
			defer r.Body.Close()
			body, err := io.ReadAll(r.Body)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			var formattedServersJSONBuffer bytes.Buffer
			err = json.Indent(&formattedServersJSONBuffer, body, "", "  ")
			if err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			err = os.WriteFile(*confPath, formattedServersJSONBuffer.Bytes(), 0644)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			fmt.Fprint(w, "{}")
		case http.MethodGet:
			// get servers
			mu.Lock()
			b, err := json.Marshal(servers)
			mu.Unlock()
			if err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			w.Write(b)
		}
	})

	mux.HandleFunc("/api/server/", func(w http.ResponseWriter, r *http.Request) {
		if CheckAccessToken(secret, w, r) {
			return
		}

		if r.Method == http.MethodPost {
			// apply server
			_server, err := LoadServerFromRequestBody(r)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}

			if _server.Name == "" {
				writeErr(w, http.StatusBadRequest, fmt.Errorf("Name is required"))
				return
			}

			mu.Lock()
			defer mu.Unlock()
			newServer := true
			for serverIndex, server := range servers {
				if server.Name == _server.Name {
					newServer = false
					err := server.Shutdown()
					if err != nil {
						writeErr(w, http.StatusBadRequest, err)
						return
					}
					err = _server.Start()
					if err != nil {
						writeErr(w, http.StatusBadRequest, err)
						return
					}
					servers[serverIndex] = _server
					break
				}
			}
			if newServer {
				err := _server.Start()
				if err != nil {
					writeErr(w, http.StatusBadRequest, err)
					return
				}
				servers = append(servers, _server)
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
	log.Printf("Web admin url: http://%v/\n", listen)
	return nil
}
