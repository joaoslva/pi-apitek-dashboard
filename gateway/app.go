package main

import (
	"bytes"
	"context"
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

// Host-only and __Host- prefixed: no Domain, Path=/, Secure. Browsers send it
// to every port of the host, so the proxy strips it before forwarding.
const cookieName = "__Host-pms"

const sessionCacheTTL = 10 * time.Second

type App struct {
	cfg         *Config
	store       *Store
	services    []*Manifest
	gatewayPort string
	dummyHash   string
	tmpl        *template.Template
	static      fs.FS
	ipLimit     *limiter // failed logins per client IP
	pairLimit   *limiter // failed logins per client IP + username
	now         func() time.Time

	mu    sync.Mutex
	cache map[string]*cachedSession
}

type cachedSession struct {
	sess   *Session
	loaded time.Time
	stored time.Time // last seen_at written to the database
}

func newApp(cfg *Config, st *Store, services []*Manifest) (*App, error) {
	port, err := listenPort(cfg.Listen)
	if err != nil {
		return nil, err
	}
	// Unknown usernames are checked against this, so a login takes as long
	// whether or not the user exists.
	dummy, err := hashPassword(newToken())
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"fmtTime": func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") },
	}).ParseFS(webFS, "web/templates/*.html")
	if err != nil {
		return nil, err
	}
	static, err := fs.Sub(webFS, "web")
	if err != nil {
		return nil, err
	}
	return &App{
		cfg:         cfg,
		store:       st,
		services:    services,
		gatewayPort: port,
		dummyHash:   dummy,
		tmpl:        tmpl,
		static:      static,
		ipLimit:     newLimiter(20, 15*time.Minute),
		pairLimit:   newLimiter(5, 15*time.Minute),
		now:         time.Now,
		cache:       map[string]*cachedSession{},
	}, nil
}

// currentSession returns the valid session behind the request's cookie, or nil.
func (a *App) currentSession(r *http.Request) *Session {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" || len(c.Value) > 128 {
		return nil
	}
	hash := tokenHash(c.Value)
	key := string(hash)
	now := a.now()

	a.mu.Lock()
	defer a.mu.Unlock()
	cs := a.cache[key]
	if cs == nil || now.Sub(cs.loaded) > sessionCacheTTL {
		s, err := a.store.Session(hash)
		if err != nil {
			slog.Error("session lookup", "err", err)
			return nil
		}
		if s == nil || s.User.Disabled {
			delete(a.cache, key)
			return nil
		}
		if cs != nil && cs.sess.Seen.After(s.Seen) {
			s.Seen = cs.sess.Seen
		}
		cs = &cachedSession{sess: s, loaded: now, stored: s.Seen}
		a.cache[key] = cs
	}
	s := cs.sess
	if now.Sub(s.Seen) > a.cfg.SessionIdle.Duration || now.Sub(s.Created) > a.cfg.SessionMax.Duration {
		delete(a.cache, key)
		a.store.DeleteSession(hash)
		return nil
	}
	s.Seen = now
	if now.Sub(cs.stored) > time.Minute {
		a.store.TouchSession(hash, now)
		cs.stored = now
	}
	return s
}

// dropCache makes the next request of every session re-read the database,
// after a change to users, grants or sessions.
func (a *App) dropCache() {
	a.mu.Lock()
	defer a.mu.Unlock()
	clear(a.cache)
}

func (s *Session) levelFor(service string) Level {
	if s.User.Owner {
		return LevelOwner
	}
	return s.User.Grants[service]
}

func (a *App) maintenance(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := a.now()
			if err := a.store.PruneSessions(now.Add(-a.cfg.SessionIdle.Duration), now.Add(-a.cfg.SessionMax.Duration)); err != nil {
				slog.Error("prune sessions", "err", err)
			}
			if err := a.store.PruneAudit(10000); err != nil {
				slog.Error("prune audit", "err", err)
			}
			a.ipLimit.prune(now)
			a.pairLimit.prune(now)
		}
	}
}

func (a *App) audit(r *http.Request, actor, action, detail string) {
	actor, detail = truncate(actor, 64), truncate(detail, 500)
	slog.Info("audit", "actor", actor, "ip", clientIP(r), "action", action, "detail", detail)
	if err := a.store.Audit(actor, clientIP(r), action, detail); err != nil {
		slog.Error("audit write", "err", err)
	}
}

// --- addresses ---------------------------------------------------------------

var hostPattern = regexp.MustCompile(`^([A-Za-z0-9.-]+|\[[0-9A-Fa-f:.]+\])(:[0-9]{1,5})?$`)

func clientIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

func splitHost(hostport string) (host, port string) {
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		return h, p
	}
	return strings.Trim(hostport, "[]"), ""
}

// origin builds https://host[:port], leaving out the default port.
func origin(host, port string) string {
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port == "" || port == "443" {
		return "https://" + host
	}
	return "https://" + host + ":" + port
}

func (a *App) gatewayOrigin(r *http.Request) string {
	host, _ := splitHost(r.Host)
	return origin(host, a.gatewayPort)
}

func (a *App) serviceOrigin(r *http.Request, m *Manifest) string {
	host, _ := splitHost(r.Host)
	return origin(host, strconv.Itoa(m.Route.Port))
}

// validNext accepts only https URLs on this host and on a port the gateway
// serves, so the login page cannot be used as an open redirect.
func (a *App) validNext(r *http.Request, next string) string {
	if next == "" {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" {
		return "/"
	}
	host, _ := splitHost(r.Host)
	if !strings.EqualFold(u.Hostname(), host) {
		return "/"
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	if port == a.gatewayPort {
		return u.String()
	}
	for _, m := range a.services {
		if port == strconv.Itoa(m.Route.Port) {
			return u.String()
		}
	}
	return "/"
}

// originOK refuses state-changing requests and websocket upgrades sent from
// another origin, which includes another service port on the same host.
// Clients that send neither Origin nor Sec-Fetch-Site are not browsers, and
// cannot be made to carry someone else's cookie.
func originOK(r *http.Request) bool {
	safe := r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions
	if safe && !isUpgrade(r) {
		return true
	}
	if o := r.Header.Get("Origin"); o != "" {
		return strings.EqualFold(o, "https://"+r.Host)
	}
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" {
		return s == "same-origin" || s == "none"
	}
	return true
}

func isUpgrade(r *http.Request) bool {
	if r.Header.Get("Upgrade") == "" {
		return false
	}
	for _, v := range r.Header.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
				return true
			}
		}
	}
	return false
}

func isNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func (a *App) redirectToHTTPS(w http.ResponseWriter, r *http.Request) {
	if !hostPattern.MatchString(r.Host) {
		http.Error(w, "bad host", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, a.gatewayOrigin(r)+r.URL.RequestURI(), http.StatusFound)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// --- pages -------------------------------------------------------------------

type view struct {
	Title   string
	Home    string // gateway origin + "/", so links work from service ports too
	CSS     string
	Session *Session
	Flash   string
	Error   string
	Data    any
}

type errorData struct {
	Status        int
	Text, Message string
}

var flashes = map[string]string{
	"created":   "User created. They have no access until you grant it.",
	"grants":    "Access saved.",
	"password":  "Password set. That user's sessions were ended.",
	"disable":   "User disabled and signed out.",
	"enable":    "User enabled.",
	"logout":    "User signed out everywhere.",
	"delete":    "User deleted.",
	"signedout": "Signed out.",
	"changed":   "Password changed. Sign in again.",
}

func (a *App) pageHeaders(w http.ResponseWriter, r *http.Request) {
	gw := a.gatewayOrigin(r)
	actions := []string{gw}
	for _, m := range a.services {
		actions = append(actions, a.serviceOrigin(r, m))
	}
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; style-src "+gw+"; form-action "+
		strings.Join(actions, " ")+"; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cache-Control", "no-store")
}

func (a *App) render(w http.ResponseWriter, r *http.Request, status int, name string, v view) {
	v.Home = a.gatewayOrigin(r) + "/"
	v.CSS = a.gatewayOrigin(r) + "/static/style.css"
	var buf bytes.Buffer
	if err := a.tmpl.ExecuteTemplate(&buf, name, v); err != nil {
		slog.Error("template", "name", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	a.pageHeaders(w, r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func (a *App) renderError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	a.render(w, r, status, "error", view{
		Title: http.StatusText(status),
		Data:  errorData{Status: status, Text: http.StatusText(status), Message: msg},
	})
}
