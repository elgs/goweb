package main

import (
	"encoding/json"
)

type Server struct {
	Type   string  `json:"type"`
	Listen string  `json:"listen"`
	Hosts  *[]Host `json:"hosts"`
}

type Host struct {
	Name             string  `json:"name"`
	Path             string  `json:"path"`
	HttpRedirectPort float64 `json:"https_redirect_port"`
	CertPath         string  `json:"cert_path"`
	KeyPath          string  `json:"key_path"`
}

func NewConfig(confBytes []byte) (*[]Server, error) {
	var servers []Server
	err := json.Unmarshal(confBytes, &servers)
	return &servers, err
}
