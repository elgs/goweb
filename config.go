package main

import (
	"encoding/json"
	"net/http"
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
