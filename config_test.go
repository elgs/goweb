package main

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestNewConfig(t *testing.T) {
	conf := `[
	  {
	    "name": "web",
	    "type": "https",
	    "listen": "[::]:443",
	    "access_log": true,
	    "hosts": [
	      {
	        "name": "example.com",
	        "type": "serve_static",
	        "path": "/var/www",
	        "cert_path": "/certs/example.com.pem",
	        "key_path": "/certs/example.com-key.pem",
	        "disable_dir_listing": true,
	        "allowed_origins": "https://app.example.com"
	      },
	      {
	        "name": "api.example.com",
	        "type": "reverse_proxy",
	        "forward_urls": "http://127.0.0.1:8080 http://127.0.0.1:8081",
	        "disabled": true
	      }
	    ]
	  },
	  {
	    "name": "db",
	    "type": "tcp",
	    "listen": "[::]:5432",
	    "disabled": true,
	    "hosts": [{"name": "primary", "upstream": "10.0.0.5:5432"}]
	  }
	]`

	servers, err := NewConfig([]byte(conf))
	if err != nil {
		t.Fatalf("NewConfig() = %v, want nil", err)
	}
	if got, want := len(servers), 2; got != want {
		t.Fatalf("parsed %v servers, want %v", got, want)
	}

	web := servers[0]
	if web.Name != "web" || web.Type != "https" || web.Listen != "[::]:443" {
		t.Errorf("server = %+v, want name web, type https, listen [::]:443", web)
	}
	if !web.AccessLog || web.Disabled {
		t.Errorf("AccessLog = %v, Disabled = %v, want true and false", web.AccessLog, web.Disabled)
	}
	if got, want := len(web.Hosts), 2; got != want {
		t.Fatalf("parsed %v hosts, want %v", got, want)
	}
	static := web.Hosts[0]
	if static.Path != "/var/www" || static.CertPath != "/certs/example.com.pem" || static.KeyPath != "/certs/example.com-key.pem" {
		t.Errorf("static host = %+v, want its path and certificate paths read", static)
	}
	if !static.DisableDirListing || static.AllowedOrigins != "https://app.example.com" {
		t.Errorf("DisableDirListing = %v, AllowedOrigins = %q, want true and the configured origin", static.DisableDirListing, static.AllowedOrigins)
	}
	if got, want := web.Hosts[1].ForwardURLs, "http://127.0.0.1:8080 http://127.0.0.1:8081"; got != want {
		t.Errorf("ForwardURLs = %q, want %q", got, want)
	}
	if !web.Hosts[1].Disabled {
		t.Error("Disabled = false, want true")
	}
	if got, want := servers[1].Hosts[0].Upstream, "10.0.0.5:5432"; got != want {
		t.Errorf("Upstream = %q, want %q", got, want)
	}
}

func TestNewConfigRejectsInvalidJSON(t *testing.T) {
	cases := []struct {
		name, conf string
	}{
		{"truncated", `[{"name": "web"`},
		{"not a list", `{"name": "web"}`},
		{"wrong field type", `[{"name": "web", "disabled": "yes"}]`},
		{"empty", ``},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewConfig([]byte(c.conf)); err == nil {
				t.Error("NewConfig() = nil, want an error")
			}
		})
	}
}

func TestNewConfigAcceptsEmptyList(t *testing.T) {
	servers, err := NewConfig([]byte(`[]`))
	if err != nil {
		t.Fatalf("NewConfig() = %v, want nil", err)
	}
	if len(servers) != 0 {
		t.Errorf("parsed %v servers, want none", len(servers))
	}
}

// jsonFields returns the json field names of a struct, in declaration order.
func jsonFields(v any) []string {
	structType := reflect.TypeOf(v)
	fields := make([]string, 0, structType.NumField())
	for i := 0; i < structType.NumField(); i++ {
		tag, _, _ := strings.Cut(structType.Field(i).Tag.Get("json"), ",")
		if tag != "" && tag != "-" {
			fields = append(fields, tag)
		}
	}
	return fields
}

// The field names are the file format: renaming one silently drops that
// setting from every existing goweb.json.
func TestConfigFieldNames(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  []string
	}{
		{
			name:  "Server",
			value: Server{},
			want:  []string{"name", "type", "listen", "disabled", "access_log", "hosts", "status"},
		},
		{
			name:  "Host",
			value: Host{},
			want: []string{"name", "type", "path", "cert_path", "key_path", "forward_urls",
				"redirect_url", "upstream", "disabled", "disable_dir_listing", "status", "allowed_origins"},
		},
	}
	for _, c := range cases {
		if got := jsonFields(c.value); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v fields = %v, want %v", c.name, got, c.want)
		}
	}
}

// jsFunctionBody returns the source of a top-level `function name(...) {...}`,
// by counting braces from the opening one.
func jsFunctionBody(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "function "+name+"(")
	if start < 0 {
		t.Fatalf("function %v() not found in the admin UI source", name)
	}
	depth := 0
	for i := start + strings.Index(src[start:], "{"); i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : i+1]
			}
		}
	}
	t.Fatalf("function %v() has no closing brace", name)
	return ""
}

// The admin UI rebuilds each server from scratch before saving it, so a field
// cleanServer() and cleanHost() forget is dropped from goweb.json the first
// time anyone presses save — silently, and for every server at once.
func TestAdminUIKeepsEveryConfigField(t *testing.T) {
	src, err := gowebadmin.ReadFile("gowebadmin/app.js")
	if err != nil {
		t.Fatalf("reading the embedded admin UI: %v", err)
	}
	cleaners := jsFunctionBody(t, string(src), "cleanServer") + jsFunctionBody(t, string(src), "cleanHost")

	for _, field := range append(jsonFields(Server{}), jsonFields(Host{})...) {
		if field == "status" {
			continue // reported by the server, never sent back to it
		}
		// \b keeps cert_path from standing in for path
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(field) + `\b`).MatchString(cleaners) {
			t.Errorf("the admin UI never mentions %q in cleanServer()/cleanHost(): saving from the UI would drop it", field)
		}
	}
}
