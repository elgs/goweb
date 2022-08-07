package main

import (
	"encoding/json"
	"net/http"
)

type Server struct {
	RuntimeId string  `json:"runtime_id"`
	Name      string  `json:"name"`
	Type      string  `json:"type"`
	Listen    string  `json:"listen"`
	Disabled  bool    `json:"disabled"`
	Hosts     []*Host `json:"hosts"`
	hostMap   map[string]*Host
	server    *http.Server
}

type Host struct {
	RuntimeId         string `json:"runtime_id"`
	Name              string `json:"name"`
	Type              string `json:"type"` // serve_static, 301_redirect and reverse_proxy
	Path              string `json:"path"` // for type serve_static
	CertPath          string `json:"cert_path"`
	KeyPath           string `json:"key_path"`
	ForwardURLs       string `json:"forward_urls"` // for type reverse_proxy space separated
	RedirectURL       string `json:"redirect_url"` // for type 301_redirect
	Disabled          bool   `json:"disabled"`
	DisableDirListing bool   `json:"disable_dir_listing"`
}

func NewConfig(confBytes []byte) ([]*Server, error) {
	var servers []*Server
	err := json.Unmarshal(confBytes, &servers)
	return servers, err
}
