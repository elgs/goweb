package main

import (
	"bytes"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
)

//go:embed gowebadmin
var gowebadmin embed.FS

// authorize handles CORS headers and validates the admin access token. It
// returns true when the caller should continue handling the request, false
// when the request has been fully handled (CORS preflight or rejected).
func authorize(secret string, w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == fmt.Sprintf("http://%v:%v", host, port) || origin == fmt.Sprintf("https://%v:%v", host, port) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}
	w.Header().Set("Access-Control-Allow-Methods", r.Header.Get("Access-Control-Request-Method"))
	w.Header().Set("Access-Control-Allow-Headers", r.Header.Get("Access-Control-Request-Headers"))
	if r.Method == http.MethodOptions {
		return false
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	token := r.Header.Get("authorization")
	if subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"err": "Invalid access token."})
		slog.Warn("Invalid admin access token", "client", clientIP(r.RemoteAddr))
		return false
	}
	return true
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

// adminMux builds the admin handler: the embedded web UI on / and the config
// API under /api.
func adminMux() (*http.ServeMux, error) {
	mux := http.NewServeMux()
	sub, err := fs.Sub(gowebadmin, "gowebadmin")
	if err != nil {
		return nil, err
	}

	mux.Handle("/", http.FileServer(http.FS(sub)))

	writeErr := func(w http.ResponseWriter, r *http.Request, status int, err error) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"err": err.Error()})
		slog.Warn("Admin request failed",
			"method", r.Method,
			"path", r.URL.Path,
			"client", clientIP(r.RemoteAddr),
			"status", status,
			"err", err)
	}

	mux.HandleFunc("/api/servers/", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(secret, w, r) {
			return
		}

		switch r.Method {
		case http.MethodPatch:
			// apply servers
			_servers, err := LoadServersFromRequestBody(r)
			if err != nil {
				writeErr(w, r, http.StatusBadRequest, err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			oldServers := servers
			for _, server := range oldServers {
				if err := server.Shutdown(); err != nil {
					slog.Error("Failed to shut down server", "server", server.Name, "err", err)
				}
			}
			for _, server := range _servers {
				err := server.Start()
				if err != nil {
					// rollback: shut down new servers that started
					for _, s := range _servers {
						if err := s.Shutdown(); err != nil {
							slog.Error("Failed to shut down server", "server", s.Name, "err", err)
						}
					}
					// restore old servers
					for _, s := range oldServers {
						if err := s.Start(); err != nil {
							slog.Error("Failed to restore server", "server", s.Name, "err", err)
						}
					}
					slog.Warn("Admin config apply rolled back", "client", clientIP(r.RemoteAddr), "err", err)
					writeErr(w, r, http.StatusBadRequest, err)
					return
				}
			}
			servers = _servers
			slog.Info("Admin applied config", "client", clientIP(r.RemoteAddr), "servers", len(_servers))
			fmt.Fprint(w, "{}")
		case http.MethodPost:
			// save servers
			defer r.Body.Close()
			body, err := io.ReadAll(r.Body)
			if err != nil {
				writeErr(w, r, http.StatusBadRequest, err)
				return
			}
			var _servers []*Server
			if err := json.Unmarshal(body, &_servers); err != nil {
				writeErr(w, r, http.StatusBadRequest, err)
				return
			}
			var formattedServersJSONBuffer bytes.Buffer
			if err := json.Indent(&formattedServersJSONBuffer, body, "", "  "); err != nil {
				writeErr(w, r, http.StatusBadRequest, err)
				return
			}
			// write to a temp file and rename so a failure mid-write cannot
			// truncate the existing config
			tmpPath := *confPath + ".tmp"
			if err := os.WriteFile(tmpPath, formattedServersJSONBuffer.Bytes(), 0644); err != nil {
				writeErr(w, r, http.StatusInternalServerError, err)
				return
			}
			if err := os.Rename(tmpPath, *confPath); err != nil {
				writeErr(w, r, http.StatusInternalServerError, err)
				return
			}
			slog.Info("Admin saved config", "client", clientIP(r.RemoteAddr), "path", *confPath, "servers", len(_servers))
			fmt.Fprint(w, "{}")
		case http.MethodGet:
			// get servers
			mu.Lock()
			b, err := json.Marshal(servers)
			mu.Unlock()
			if err != nil {
				writeErr(w, r, http.StatusInternalServerError, err)
				return
			}
			w.Write(b)
		}
	})

	mux.HandleFunc("/api/server/", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(secret, w, r) {
			return
		}

		if r.Method == http.MethodPost {
			// apply server
			_server, err := LoadServerFromRequestBody(r)
			if err != nil {
				writeErr(w, r, http.StatusBadRequest, err)
				return
			}

			if _server.Name == "" {
				writeErr(w, r, http.StatusBadRequest, fmt.Errorf("Name is required"))
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
						writeErr(w, r, http.StatusBadRequest, err)
						return
					}
					err = _server.Start()
					if err != nil {
						// rollback: bring the old server back up
						if rbErr := server.Start(); rbErr != nil {
							slog.Error("Failed to restore server", "server", server.Name, "err", rbErr)
						}
						writeErr(w, r, http.StatusBadRequest, err)
						return
					}
					servers[serverIndex] = _server
					break
				}
			}
			if newServer {
				err := _server.Start()
				if err != nil {
					writeErr(w, r, http.StatusBadRequest, err)
					return
				}
				servers = append(servers, _server)
			}
			slog.Info("Admin applied server config", "client", clientIP(r.RemoteAddr), "server", _server.Name)
			fmt.Fprint(w, "{}")
		}
	})

	return mux, nil
}

func StartAdmin() error {
	listen := fmt.Sprintf("%v:%v", host, port)

	mux, err := adminMux()
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          httpErrorLog(),
	}
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			slog.Error("Admin server failed", "err", err)
		}
	}()
	slog.Info("Web admin listening", "url", fmt.Sprintf("http://%v/", listen))
	return nil
}
