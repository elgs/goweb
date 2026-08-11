package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const adminToken = "test-access-token"

// newAdminServer stands the admin API up on an ephemeral port with a known
// token and an empty config file, and restores every global it touches.
func newAdminServer(t *testing.T) *httptest.Server {
	t.Helper()
	oldSecret, oldHost, oldPort, oldServers, oldConfPath := secret, host, port, servers, confPath
	t.Cleanup(func() {
		secret, host, port, servers, confPath = oldSecret, oldHost, oldPort, oldServers, oldConfPath
	})
	t.Cleanup(func() {
		// whatever the handlers left running belongs to this test
		for _, server := range servers {
			server.Shutdown()
		}
	})

	secret = adminToken
	host, port = "127.0.0.1", "13579"
	servers = nil
	confFile := filepath.Join(t.TempDir(), "goweb.json")
	confPath = &confFile

	mux, err := adminMux()
	if err != nil {
		t.Fatalf("adminMux() = %v, want nil", err)
	}
	admin := httptest.NewServer(mux)
	t.Cleanup(admin.Close)
	return admin
}

func adminDo(t *testing.T, admin *httptest.Server, method, path, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, admin.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("authorization", token)
	}
	resp, err := admin.Client().Do(req)
	if err != nil {
		t.Fatalf("%v %v: %v", method, path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// startedServer returns a running http server answering for one host name.
func startedServer(t *testing.T, name, hostName string) *Server {
	t.Helper()
	server := &Server{Name: name, Type: "http", Listen: "127.0.0.1:0", Hosts: []*Host{redirectHost(hostName)}}
	if err := server.Start(); err != nil {
		t.Fatalf("starting %v: %v", name, err)
	}
	t.Cleanup(func() { server.Shutdown() })
	return server
}

// serving reports whether the server answers for its first host name.
func serving(t *testing.T, server *Server) bool {
	t.Helper()
	if server.listener == nil {
		return false
	}
	resp, err := newTestClient(server.listener.Addr().String(), false).Get("http://" + server.Hosts[0].Name + "/")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusMovedPermanently
}

func accepting(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// ---- authorization ---------------------------------------------------------

func TestAuthorizeRejectsWrongToken(t *testing.T) {
	for _, token := range []string{"", "wrong", "test-access", "test-access-token-and-more"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/servers/", nil)
		if token != "" {
			r.Header.Set("authorization", token)
		}
		if authorize(adminToken, w, r) {
			t.Errorf("token %q was accepted, want it rejected", token)
		}
		if w.Code != http.StatusUnauthorized {
			t.Errorf("token %q: status = %v, want %v", token, w.Code, http.StatusUnauthorized)
		}
		if !strings.Contains(w.Body.String(), "Invalid access token") {
			t.Errorf("token %q: body = %q, want the rejection explained", token, w.Body)
		}
	}
}

func TestAuthorizeAcceptsToken(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/servers/", nil)
	r.Header.Set("authorization", adminToken)
	if !authorize(adminToken, w, r) {
		t.Fatal("authorize() = false, want the request let through")
	}
	if got, want := w.Header().Get("Content-Type"), "application/json; charset=utf-8"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
}

// A preflight carries no token, so it is answered rather than rejected.
func TestAuthorizePreflight(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodOptions, "/api/servers/", nil)
	r.Header.Set("Access-Control-Request-Method", "PATCH")
	r.Header.Set("Access-Control-Request-Headers", "authorization")

	if authorize(adminToken, w, r) {
		t.Fatal("authorize() = true, want the preflight handled and the request stopped")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %v, want %v", w.Code, http.StatusOK)
	}
	if got, want := w.Header().Get("Access-Control-Allow-Methods"), "PATCH"; got != want {
		t.Errorf("Access-Control-Allow-Methods = %q, want %q", got, want)
	}
	if got, want := w.Header().Get("Access-Control-Allow-Headers"), "authorization"; got != want {
		t.Errorf("Access-Control-Allow-Headers = %q, want %q", got, want)
	}
}

// Only the admin's own UI may make credentialed calls from a browser.
func TestAuthorizeAllowsOnlyItsOwnOrigin(t *testing.T) {
	oldHost, oldPort := host, port
	t.Cleanup(func() { host, port = oldHost, oldPort })
	host, port = "admin.example.com", "13579"

	cases := []struct {
		origin, want string
	}{
		{"http://admin.example.com:13579", "http://admin.example.com:13579"},
		{"https://admin.example.com:13579", "https://admin.example.com:13579"},
		{"http://admin.example.com", ""}, // the port is part of the origin
		{"http://admin.example.com:1234", ""},
		{"http://evil.example.com:13579", ""},
		{"", ""},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/servers/", nil)
		r.Header.Set("authorization", adminToken)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		authorize(adminToken, w, r)
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != c.want {
			t.Errorf("origin %q: Access-Control-Allow-Origin = %q, want %q", c.origin, got, c.want)
		}
		wantCredentials := c.want != ""
		if got := w.Header().Get("Access-Control-Allow-Credentials") == "true"; got != wantCredentials {
			t.Errorf("origin %q: credentials allowed = %v, want %v", c.origin, got, wantCredentials)
		}
	}
}

func TestAdminAPIRequiresToken(t *testing.T) {
	admin := newAdminServer(t)
	servers = []*Server{{Name: "secret-server", Type: "http", Listen: "127.0.0.1:0"}}

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/servers/"},
		{http.MethodPost, "/api/servers/"},
		{http.MethodPatch, "/api/servers/"},
		{http.MethodPost, "/api/server/"},
	} {
		resp := adminDo(t, admin, c.method, c.path, "wrong-token", "[]")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%v %v: status = %v, want %v", c.method, c.path, resp.StatusCode, http.StatusUnauthorized)
		}
		if body := bodyString(t, resp); strings.Contains(body, "secret-server") {
			t.Errorf("%v %v: body = %q, want no configuration leaked to an unauthorized caller", c.method, c.path, body)
		}
	}
}

// ---- reading and writing the configuration ---------------------------------

func TestAdminGetServers(t *testing.T) {
	admin := newAdminServer(t)
	servers = []*Server{
		{Name: "web", Type: "http", Listen: "[::]:80", Hosts: []*Host{redirectHost("a.example.com")}},
		{Name: "db", Type: "tcp", Listen: "[::]:5432", Status: "Invalid upstream"},
	}

	resp := adminDo(t, admin, http.MethodGet, "/api/servers/", adminToken, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %v, want %v", resp.StatusCode, http.StatusOK)
	}
	got, err := NewConfig([]byte(bodyString(t, resp)))
	if err != nil {
		t.Fatalf("the response is not a config: %v", err)
	}
	if len(got) != 2 || got[0].Name != "web" || got[1].Name != "db" {
		t.Fatalf("servers = %+v, want web and db", got)
	}
	if got[0].Hosts[0].Name != "a.example.com" {
		t.Errorf("host = %q, want a.example.com", got[0].Hosts[0].Name)
	}
	// The UI shows why a server or host is not running, so the status has to
	// travel with it.
	if got[1].Status != "Invalid upstream" {
		t.Errorf("Status = %q, want it reported", got[1].Status)
	}
}

func TestAdminSaveWritesConfigFile(t *testing.T) {
	admin := newAdminServer(t)
	config := []*Server{{Name: "web", Type: "http", Listen: "[::]:80", Hosts: []*Host{redirectHost("a.example.com")}}}

	resp := adminDo(t, admin, http.MethodPost, "/api/servers/", adminToken, mustJSON(t, config))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %v, want %v", resp.StatusCode, http.StatusOK)
	}

	written, err := os.ReadFile(*confPath)
	if err != nil {
		t.Fatalf("reading the saved config: %v", err)
	}
	saved, err := NewConfig(written)
	if err != nil {
		t.Fatalf("the saved config does not parse: %v", err)
	}
	if len(saved) != 1 || saved[0].Name != "web" || saved[0].Hosts[0].Name != "a.example.com" {
		t.Errorf("saved config = %+v, want the posted one", saved)
	}
	// It is meant to be readable and diffable on disk.
	if !strings.Contains(string(written), "\n  {") {
		t.Errorf("saved config is not indented:\n%s", written)
	}
	// The write goes through a temp file and a rename, so a crash cannot
	// truncate the config — and the temp file must not survive.
	if _, err := os.Stat(*confPath + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("os.Stat(%v.tmp) = %v, want the temporary file gone", *confPath, err)
	}
	// Saving is not applying: nothing should have started.
	if len(servers) != 0 {
		t.Errorf("running servers = %v, want saving to leave them alone", len(servers))
	}
}

func TestAdminSaveRejectsInvalidConfig(t *testing.T) {
	admin := newAdminServer(t)
	if err := os.WriteFile(*confPath, []byte("the config that was there before"), 0644); err != nil {
		t.Fatal(err)
	}

	for _, body := range []string{"", "not json", `{"name":"web"}`, `[{"name":"web","disabled":"yes"}]`} {
		resp := adminDo(t, admin, http.MethodPost, "/api/servers/", adminToken, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %q: status = %v, want %v", body, resp.StatusCode, http.StatusBadRequest)
		}
		// A config that cannot be parsed must never reach the file.
		if got, err := os.ReadFile(*confPath); err != nil || string(got) != "the config that was there before" {
			t.Fatalf("body %q: config file = %q (err %v), want it untouched", body, got, err)
		}
	}
}

// ---- applying the configuration --------------------------------------------

func TestAdminApplyReplacesRunningServers(t *testing.T) {
	admin := newAdminServer(t)
	old := startedServer(t, "old", "old.example.com")
	servers = []*Server{old}
	oldAddr := old.listener.Addr().String()

	replacement := []*Server{{Name: "new", Type: "http", Listen: "127.0.0.1:0", Hosts: []*Host{redirectHost("new.example.com")}}}
	resp := adminDo(t, admin, http.MethodPatch, "/api/servers/", adminToken, mustJSON(t, replacement))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %v, want %v (%v)", resp.StatusCode, http.StatusOK, bodyString(t, resp))
	}

	if len(servers) != 1 || servers[0].Name != "new" {
		t.Fatalf("running servers = %+v, want just the new one", servers)
	}
	if !serving(t, servers[0]) {
		t.Error("the new server is not serving")
	}
	if newAddr := servers[0].listener.Addr().String(); newAddr != oldAddr && accepting(oldAddr) {
		t.Error("the replaced server still accepts connections on its old port")
	}
}

// A config that cannot be applied has to leave the running one exactly as it
// was: a rejected edit in the admin UI must not take sites down.
func TestAdminApplyRollsBackOnFailure(t *testing.T) {
	admin := newAdminServer(t)
	old := startedServer(t, "old", "old.example.com")
	servers = []*Server{old}

	// the first server of the new config binds this address, and has to give
	// it back when the second one fails
	claimed := closedAddr(t)
	broken := []*Server{
		{Name: "fine", Type: "http", Listen: claimed, Hosts: []*Host{redirectHost("fine.example.com")}},
		{Name: "broken", Type: "gopher", Listen: "127.0.0.1:0"},
	}
	resp := adminDo(t, admin, http.MethodPatch, "/api/servers/", adminToken, mustJSON(t, broken))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %v, want %v", resp.StatusCode, http.StatusBadRequest)
	}
	if got := errField(t, resp); !strings.Contains(got, "Unknown server type") {
		t.Errorf("err = %q, want it to name the problem", got)
	}

	if len(servers) != 1 || servers[0] != old {
		t.Fatalf("running servers = %+v, want the previous configuration kept", servers)
	}
	if !serving(t, old) {
		t.Error("the previous server was not brought back up")
	}
	listener, err := net.Listen("tcp", claimed)
	if err != nil {
		t.Errorf("net.Listen(%v) = %v, want the half-applied config to have released the port", claimed, err)
	} else {
		listener.Close()
	}
}

func TestAdminApplyServerAddsNewServer(t *testing.T) {
	admin := newAdminServer(t)
	existing := startedServer(t, "existing", "existing.example.com")
	servers = []*Server{existing}

	added := &Server{Name: "extra", Type: "http", Listen: "127.0.0.1:0", Hosts: []*Host{redirectHost("extra.example.com")}}
	resp := adminDo(t, admin, http.MethodPost, "/api/server/", adminToken, mustJSON(t, added))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %v, want %v (%v)", resp.StatusCode, http.StatusOK, bodyString(t, resp))
	}

	if len(servers) != 2 || servers[1].Name != "extra" {
		t.Fatalf("running servers = %+v, want the new one appended", servers)
	}
	if !serving(t, servers[1]) {
		t.Error("the added server is not serving")
	}
	if !serving(t, existing) {
		t.Error("the untouched server stopped serving")
	}
}

func TestAdminApplyServerReplacesByName(t *testing.T) {
	admin := newAdminServer(t)
	untouched := startedServer(t, "untouched", "untouched.example.com")
	old := startedServer(t, "edge", "old.example.com")
	servers = []*Server{untouched, old}
	oldAddr := old.listener.Addr().String()

	updated := &Server{Name: "edge", Type: "http", Listen: "127.0.0.1:0", Hosts: []*Host{redirectHost("new.example.com")}}
	resp := adminDo(t, admin, http.MethodPost, "/api/server/", adminToken, mustJSON(t, updated))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %v, want %v (%v)", resp.StatusCode, http.StatusOK, bodyString(t, resp))
	}

	if len(servers) != 2 {
		t.Fatalf("running servers = %+v, want the server replaced in place, not added", servers)
	}
	if servers[1].Hosts[0].Name != "new.example.com" {
		t.Errorf("host = %q, want the new configuration in the old slot", servers[1].Hosts[0].Name)
	}
	if !serving(t, servers[1]) {
		t.Error("the replacement is not serving")
	}
	if !serving(t, untouched) {
		t.Error("replacing one server stopped another")
	}
	if newAddr := servers[1].listener.Addr().String(); newAddr != oldAddr && accepting(oldAddr) {
		t.Error("the replaced server still accepts connections on its old port")
	}
}

func TestAdminApplyServerRequiresName(t *testing.T) {
	admin := newAdminServer(t)
	resp := adminDo(t, admin, http.MethodPost, "/api/server/", adminToken, mustJSON(t, &Server{Type: "http", Listen: "127.0.0.1:0"}))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %v, want %v", resp.StatusCode, http.StatusBadRequest)
	}
	if got, want := errField(t, resp), "Name is required"; got != want {
		t.Errorf("err = %q, want %q", got, want)
	}
	if len(servers) != 0 {
		t.Errorf("running servers = %v, want none started", len(servers))
	}
}

func TestAdminApplyServerRejectsInvalidBody(t *testing.T) {
	admin := newAdminServer(t)
	resp := adminDo(t, admin, http.MethodPost, "/api/server/", adminToken, "not json")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %v, want %v", resp.StatusCode, http.StatusBadRequest)
	}
}

// Replacing a server stops the old one first, so a replacement that will not
// start has to put it back.
func TestAdminApplyServerRollsBackOnFailure(t *testing.T) {
	admin := newAdminServer(t)
	old := startedServer(t, "edge", "old.example.com")
	servers = []*Server{old}

	resp := adminDo(t, admin, http.MethodPost, "/api/server/", adminToken,
		mustJSON(t, &Server{Name: "edge", Type: "gopher", Listen: "127.0.0.1:0"}))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %v, want %v", resp.StatusCode, http.StatusBadRequest)
	}
	if got := errField(t, resp); !strings.Contains(got, "Unknown server type") {
		t.Errorf("err = %q, want it to name the problem", got)
	}
	if len(servers) != 1 || servers[0] != old {
		t.Fatalf("running servers = %+v, want the previous server kept", servers)
	}
	if !serving(t, old) {
		t.Error("the previous server was not brought back up")
	}
}

// ---- request bodies --------------------------------------------------------

func TestLoadServersFromRequestBody(t *testing.T) {
	body := `[{"name":"web","type":"http","hosts":[{"name":"a.example.com"}]}]`
	loaded, err := LoadServersFromRequestBody(httptest.NewRequest(http.MethodPost, "/api/servers/", strings.NewReader(body)))
	if err != nil {
		t.Fatalf("LoadServersFromRequestBody() = %v, want nil", err)
	}
	if len(loaded) != 1 || loaded[0].Name != "web" || loaded[0].Hosts[0].Name != "a.example.com" {
		t.Errorf("loaded = %+v, want the posted config", loaded)
	}
	if _, err := LoadServersFromRequestBody(httptest.NewRequest(http.MethodPost, "/api/servers/", strings.NewReader("nope"))); err == nil {
		t.Error("LoadServersFromRequestBody() = nil, want an error for a body that is not a config")
	}
}

func TestLoadServerFromRequestBody(t *testing.T) {
	body := `{"name":"web","type":"https","listen":"[::]:443"}`
	loaded, err := LoadServerFromRequestBody(httptest.NewRequest(http.MethodPost, "/api/server/", strings.NewReader(body)))
	if err != nil {
		t.Fatalf("LoadServerFromRequestBody() = %v, want nil", err)
	}
	if loaded.Name != "web" || loaded.Type != "https" || loaded.Listen != "[::]:443" {
		t.Errorf("loaded = %+v, want the posted server", loaded)
	}
	if _, err := LoadServerFromRequestBody(httptest.NewRequest(http.MethodPost, "/api/server/", strings.NewReader("[]"))); err == nil {
		t.Error("LoadServerFromRequestBody() = nil, want an error for a body that is not a server")
	}
}

// ---- the embedded UI -------------------------------------------------------

func TestAdminServesUI(t *testing.T) {
	admin := newAdminServer(t)
	for path, want := range map[string]string{"/": "goweb admin", "/app.js": "cleanServer", "/resources/ui.css": ""} {
		resp := adminDo(t, admin, http.MethodGet, path, "", "")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %v: status = %v, want %v", path, resp.StatusCode, http.StatusOK)
			continue
		}
		if body := bodyString(t, resp); !strings.Contains(body, want) {
			t.Errorf("GET %v: body does not contain %q", path, want)
		}
	}
}
