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
	RuntimeId         string  `json:"runtime_id"`
	Name              string  `json:"name"`
	Path              string  `json:"path"`
	CertPath          string  `json:"cert_path"`
	KeyPath           string  `json:"key_path"`
	Disabled          bool    `json:"disabled"`
	HttpRedirectPort  float64 `json:"https_redirect_port"`
	DisableDirListing bool    `json:"disable_dir_listing"`
}

func NewConfig(confBytes []byte) ([]*Server, error) {
	var servers []*Server
	err := json.Unmarshal(confBytes, &servers)
	return servers, err
}
