package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanPath(t *testing.T) {
	ok := map[string]string{"/": "/", "/a/b": "/a/b", "/a%20b/": "/a b/", "/pad/x.js": "/pad/x.js"}
	for in, want := range ok {
		if got, good := cleanPath(in); !good || got != want {
			t.Errorf("cleanPath(%q) = %q, %v; want %q", in, got, good, want)
		}
	}
	for _, in := range []string{"", "a", "/a//b", "/a/../b", "/a/./b", "/a/%2e%2e/b", "/a%2fb", "/a%5Cb", `/a\b`, "/a%00", "/%zz"} {
		if _, good := cleanPath(in); good {
			t.Errorf("cleanPath(%q) accepted", in)
		}
	}
}

const testManifest = `id = "notes"
title = "Notes"
[route]
port = 8443
upstream = %q
[access]
level = "use"
[[access.rules]]
path = "/admin"
level = "owner"
[[access.rules]]
path = "/api/wipe"
methods = ["POST"]
level = "admin"
[[access.rules]]
path = "/api/power"
level = "deny"
`

func TestRequired(t *testing.T) {
	m := writeManifest(t, t.TempDir(), "http://127.0.0.1:9")
	cases := []struct {
		path, method string
		want         Level
	}{
		{"/", "GET", LevelUse},
		{"/admin", "GET", LevelOwner},
		{"/ADMIN/x", "GET", LevelOwner},
		{"/admins", "GET", LevelUse},
		{"/api/wipe", "POST", LevelAdmin},
		{"/api/wipe", "GET", LevelUse},
		{"/api/power/now", "POST", LevelDeny},
	}
	for _, c := range cases {
		if got := m.required(c.path, c.method); got != c.want {
			t.Errorf("required(%s %s) = %s, want %s", c.method, c.path, got, c.want)
		}
	}
	if allowed(LevelOwner, LevelDeny) || !allowed(LevelAdmin, LevelUse) || allowed(LevelUse, LevelAdmin) {
		t.Error("allowed() is wrong")
	}
}

func TestRepoManifests(t *testing.T) {
	dir := t.TempDir()
	files, _ := filepath.Glob("../services/*/service.toml")
	if len(files) == 0 {
		t.Skip("services/ not visible")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, filepath.Base(filepath.Dir(f))+".toml"), b, 0o600)
	}
	if _, err := loadManifests(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig("rootfs/etc/pms/gateway.toml"); err != nil {
		t.Fatal(err)
	}
}

func TestPasswords(t *testing.T) {
	h, err := hashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !verifyPassword("correct horse", h) || verifyPassword("correct horsE", h) || verifyPassword("x", "garbage") {
		t.Error("verifyPassword is wrong")
	}
	if validPassword("short") == nil || validPassword(strings.Repeat("x", 300)) == nil || validPassword("long enough!") != nil {
		t.Error("validPassword is wrong")
	}
}

func TestOriginOK(t *testing.T) {
	req := func(method, origin, site string) *http.Request {
		r := httptest.NewRequest(method, "https://pocketserver:8443/x", nil)
		r.Host = "pocketserver:8443"
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		return r
	}
	if !originOK(req("GET", "https://evil.example", "")) {
		t.Error("GET should pass")
	}
	if !originOK(req("POST", "https://pocketserver:8443", "")) {
		t.Error("same-origin POST should pass")
	}
	if originOK(req("POST", "https://pocketserver", "")) {
		t.Error("POST from the gateway port should be refused on a service port")
	}
	if originOK(req("POST", "", "same-site")) {
		t.Error("same-site but cross-origin POST should be refused")
	}
	// What a browser sends for a form on a no-referrer page: Origin null.
	if !originOK(req("POST", "null", "same-origin")) {
		t.Error("same-origin POST with Origin: null should pass on Sec-Fetch-Site")
	}
	if originOK(req("POST", "https://pocketserver:8443", "cross-site")) {
		t.Error("Sec-Fetch-Site cross-site should win over a matching Origin")
	}
	if originOK(req("POST", "null", "")) {
		t.Error("Origin: null without Sec-Fetch-Site should be refused")
	}
	ws := req("GET", "https://pocketserver:8444", "")
	ws.Header.Set("Connection", "keep-alive, Upgrade")
	ws.Header.Set("Upgrade", "websocket")
	if originOK(ws) {
		t.Error("cross-origin websocket should be refused")
	}
}

func TestValidNext(t *testing.T) {
	app, _ := testApp(t, "http://127.0.0.1:9")
	r := httptest.NewRequest("GET", "https://pocketserver/login", nil)
	r.Host = "pocketserver"
	good := []string{"https://pocketserver/", "https://pocketserver:8443/p/x?y=1", "https://POCKETSERVER:443/admin/"}
	bad := []string{"", "/", "//evil.example/", "https://evil.example:8443/", "http://pocketserver:8443/",
		"https://pocketserver:22/", "https://user@pocketserver:8443/", "javascript:alert(1)"}
	for _, n := range good {
		if app.validNext(r, n) == "/" {
			t.Errorf("validNext(%q) refused", n)
		}
	}
	for _, n := range bad {
		if got := app.validNext(r, n); got != "/" {
			t.Errorf("validNext(%q) = %q", n, got)
		}
	}
}

func writeManifest(t *testing.T, dir, upstream string) *Manifest {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "notes.toml"), []byte(fmt.Sprintf(testManifest, upstream)), 0o600); err != nil {
		t.Fatal(err)
	}
	ms, err := loadManifests(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ms[0]
}

func testApp(t *testing.T, upstream string) (*App, *Manifest) {
	t.Helper()
	dir := t.TempDir()
	m := writeManifest(t, dir, upstream)
	st, err := openStore(filepath.Join(dir, "gw.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &Config{Listen: ":443", SessionIdle: Duration{time.Hour}, SessionMax: Duration{24 * time.Hour}}
	app, err := newApp(cfg, st, []*Manifest{m})
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := hashPassword("alice password")
	for _, name := range []string{"alice", "bob"} {
		if err := st.CreateUser(name, hash, false); err != nil {
			t.Fatal(err)
		}
	}
	alice, _ := st.UserByName("alice")
	st.SetGrant(alice.ID, "notes", LevelUse)
	return app, m
}

func sessionFor(t *testing.T, app *App, name string) string {
	t.Helper()
	u, _ := app.store.UserByName(name)
	token := newToken()
	if err := app.store.CreateSession(tokenHash(token), u.ID, "csrf-token", "127.0.0.1", "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	return token
}

func TestProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			conn, brw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			brw.Flush()
			line, _ := brw.ReadString('\n')
			brw.WriteString("echo:" + line)
			brw.Flush()
			return
		}
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "evil"})
		http.SetCookie(w, &http.Cookie{Name: "keep", Value: "1"})
		json.NewEncoder(w).Encode(map[string]string{
			"cookie": r.Header.Get("Cookie"), "user": r.Header.Get("X-Pms-User"),
			"level": r.Header.Get("X-Pms-Level"), "path": r.URL.Path, "proto": r.Header.Get("X-Forwarded-Proto"),
		})
	}))
	defer upstream.Close()

	app, m := testApp(t, upstream.URL)
	srv := httptest.NewServer(app.serviceHandler(m))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	alice, bob := sessionFor(t, app, "alice"), sessionFor(t, app, "bob")

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path, token, origin string, hdr map[string]string) (*http.Response, map[string]string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		if token != "" {
			req.Header.Set("Cookie", cookieName+"="+token+"; other=1")
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body := map[string]string{}
		b, _ := io.ReadAll(resp.Body)
		json.Unmarshal(b, &body)
		return resp, body
	}

	resp, _ := do("GET", "/x", "", "", map[string]string{"Accept": "text/html"})
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "/login?next=") {
		t.Errorf("anonymous navigation: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := do("GET", "/x", "", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous fetch: %d", resp.StatusCode)
	}

	resp, body := do("GET", "/x", alice, "", map[string]string{"X-Pms-User": "owner-spoof"})
	if resp.StatusCode != 200 || body["user"] != "alice" || body["level"] != "use" || body["cookie"] != "other=1" {
		t.Errorf("alice GET /x: %d %v", resp.StatusCode, body)
	}
	for _, c := range resp.Header.Values("Set-Cookie") {
		if strings.HasPrefix(c, cookieName) {
			t.Errorf("upstream Set-Cookie for the session leaked: %s", c)
		}
	}

	checks := []struct {
		method, path, token, origin string
		want                        int
	}{
		{"GET", "/admin/", alice, "", 403},
		{"GET", "/Admin", alice, "", 403},
		{"POST", "/api/wipe", alice, "https://" + host, 403},
		{"GET", "/api/wipe", alice, "", 200},
		{"GET", "/x", bob, "", 404},
		{"POST", "/x", alice, "https://evil.example", 403},
		{"POST", "/x", alice, "https://" + host, 200},
		{"GET", "/a/%2e%2e/admin", alice, "", 400},
	}
	for _, c := range checks {
		if resp, _ := do(c.method, c.path, c.token, c.origin, nil); resp.StatusCode != c.want {
			t.Errorf("%s %s as %s: %d, want %d", c.method, c.path, c.token[:4], resp.StatusCode, c.want)
		}
	}

	websocket := func(origin string) string {
		conn, err := net.Dial("tcp", host)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nOrigin: %s\r\nCookie: %s=%s\r\n\r\n",
			host, origin, cookieName, alice)
		br := bufio.NewReader(conn)
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusSwitchingProtocols {
			return resp.Status
		}
		fmt.Fprint(conn, "hello\n")
		line, _ := br.ReadString('\n')
		return line
	}
	if got := websocket("https://" + host); got != "echo:hello\n" {
		t.Errorf("websocket through the proxy: %q", got)
	}
	if got := websocket("https://evil.example"); !strings.HasPrefix(got, "403") {
		t.Errorf("cross-origin websocket: %q", got)
	}
}

func TestLoginFlow(t *testing.T) {
	app, _ := testApp(t, "http://127.0.0.1:9")
	srv := httptest.NewServer(app.gatewayHandler())
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	login := func(pw string) *http.Response {
		resp, err := client.PostForm(srv.URL+"/login", url.Values{"username": {"Alice"}, "password": {pw}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	resp := login("alice password")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Set-Cookie"), cookieName+"=") {
		t.Fatalf("good login: %d %q", resp.StatusCode, resp.Header.Get("Set-Cookie"))
	}
	cookie := strings.SplitN(resp.Header.Get("Set-Cookie"), ";", 2)[0]

	// Logging out needs the CSRF token.
	req, _ := http.NewRequest("POST", srv.URL+"/logout", strings.NewReader("csrf=wrong"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", cookie)
	if resp, _ := client.Do(req); resp.StatusCode != http.StatusForbidden {
		t.Errorf("logout without CSRF token: %d", resp.StatusCode)
	}

	// A non-owner does not see admin pages.
	req, _ = http.NewRequest("GET", srv.URL+"/admin/", nil)
	req.Header.Set("Cookie", cookie)
	if resp, _ := client.Do(req); resp.StatusCode != http.StatusNotFound {
		t.Errorf("admin as non-owner: %d", resp.StatusCode)
	}

	for i := 0; i < 5; i++ {
		if resp := login("wrong password"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("bad login %d: %d", i, resp.StatusCode)
		}
	}
	if resp := login("alice password"); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("login after 5 failures: %d, want 429", resp.StatusCode)
	}
}
