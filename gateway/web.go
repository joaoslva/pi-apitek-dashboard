package main

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// gatewayHandler serves login, the portal and owner administration on the
// gateway's own port.
func (a *App) gatewayHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.withSession(a.handlePortal))
	mux.HandleFunc("GET /login", a.handleLoginPage)
	mux.HandleFunc("POST /login", a.handleLogin)
	mux.HandleFunc("POST /logout", a.withSession(a.handleLogout))
	mux.HandleFunc("GET /account", a.withSession(a.handleAccount))
	mux.HandleFunc("POST /account/password", a.withSession(a.handleOwnPassword))
	mux.HandleFunc("GET /admin/{$}", a.withOwner(a.handleAdmin))
	mux.HandleFunc("GET /admin/audit", a.withOwner(a.handleAudit))
	mux.HandleFunc("POST /admin/users", a.withOwner(a.handleCreateUser))
	mux.HandleFunc("POST /admin/users/{name}/{action}", a.withOwner(a.handleUserAction))
	mux.HandleFunc("POST /admin/power/{action}", a.withOwner(a.handlePower))
	mux.Handle("GET /static/", http.FileServerFS(a.static))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		a.renderError(w, r, http.StatusNotFound, "There is nothing here.")
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostPattern.MatchString(r.Host) {
			http.Error(w, "bad host", http.StatusBadRequest)
			return
		}
		if !originOK(r) {
			a.audit(r, "", "cross-origin-refused", r.Method+" "+truncate(r.URL.Path, 200))
			a.renderError(w, r, http.StatusForbidden, "This request came from another site and was refused.")
			return
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		}
		mux.ServeHTTP(w, r)
	})
}

type sessionHandler func(http.ResponseWriter, *http.Request, *Session)

// withSession requires a signed-in user, and a matching CSRF token on POST.
func (a *App) withSession(h sessionHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := a.currentSession(r)
		if s == nil {
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			} else {
				a.renderError(w, r, http.StatusUnauthorized, "Your session has ended. Sign in again.")
			}
			return
		}
		if r.Method == http.MethodPost &&
			subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(s.CSRF)) != 1 {
			a.renderError(w, r, http.StatusForbidden, "This form has expired. Go back, reload the page and try again.")
			return
		}
		h(w, r, s)
	}
}

// withOwner hides owner pages from everyone else behind a 404.
func (a *App) withOwner(h sessionHandler) http.HandlerFunc {
	return a.withSession(func(w http.ResponseWriter, r *http.Request, s *Session) {
		if !s.User.Owner {
			a.renderError(w, r, http.StatusNotFound, "There is nothing here.")
			return
		}
		h(w, r, s)
	})
}

type portalItem struct {
	Title, Description, Initial, URL, Level string
}

func (a *App) handlePortal(w http.ResponseWriter, r *http.Request, s *Session) {
	var items []portalItem
	for _, m := range a.services {
		lvl := s.levelFor(m.ID)
		if lvl == LevelNone {
			continue
		}
		items = append(items, portalItem{
			Title:       m.Title,
			Description: m.Description,
			Initial:     strings.ToUpper(m.Title[:1]),
			URL:         a.serviceOrigin(r, m) + "/",
			Level:       lvl.String(),
		})
	}
	a.render(w, r, http.StatusOK, "portal", view{Title: "Services", Session: s, Data: items})
}

type loginData struct{ Next, Username string }

func (a *App) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	next := r.URL.Query().Get("next")
	if a.currentSession(r) != nil {
		http.Redirect(w, r, a.validNext(r, next), http.StatusSeeOther)
		return
	}
	a.render(w, r, http.StatusOK, "login", view{
		Title: "Sign in",
		Flash: flashes[r.URL.Query().Get("ok")],
		Data:  loginData{Next: next},
	})
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	name := strings.ToLower(strings.TrimSpace(r.PostFormValue("username")))
	pw := r.PostFormValue("password")
	next := r.PostFormValue("next")
	page := func(status int, msg string) {
		a.render(w, r, status, "login", view{Title: "Sign in", Error: msg, Data: loginData{Next: next, Username: truncate(name, 32)}})
	}

	now := a.now()
	pair := ip + "\x00" + name
	if a.ipLimit.blocked(ip, now) || a.pairLimit.blocked(pair, now) {
		a.audit(r, name, "login-limited", "")
		page(http.StatusTooManyRequests, "Too many attempts. Wait a few minutes and try again.")
		return
	}

	var u *User
	if validUsername(name) == nil && len(pw) <= maxPasswordBytes {
		var err error
		if u, err = a.store.UserByName(name); err != nil {
			slog.Error("login lookup", "err", err)
			a.renderError(w, r, http.StatusInternalServerError, "Something went wrong.")
			return
		}
	}
	hash := a.dummyHash
	if u != nil {
		hash = u.Password
	}
	if !verifyPassword(pw, hash) || u == nil || u.Disabled {
		a.ipLimit.fail(ip, now)
		a.pairLimit.fail(pair, now)
		a.audit(r, name, "login-failed", "")
		page(http.StatusUnauthorized, "Wrong username or password.")
		return
	}
	a.pairLimit.reset(pair)

	// A fresh token on every login; whatever session this browser had ends.
	if old, err := r.Cookie(cookieName); err == nil {
		a.store.DeleteSession(tokenHash(old.Value))
	}
	token := newToken()
	if err := a.store.CreateSession(tokenHash(token), u.ID, newToken(), ip, truncate(r.UserAgent(), 200), now); err != nil {
		slog.Error("create session", "err", err)
		a.renderError(w, r, http.StatusInternalServerError, "Something went wrong.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/", MaxAge: int(a.cfg.SessionMax.Seconds()),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	a.audit(r, u.Name, "login", "")
	http.Redirect(w, r, a.validNext(r, next), http.StatusSeeOther)
}

func clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request, s *Session) {
	a.store.DeleteSession(s.Hash)
	a.dropCache()
	clearCookie(w)
	a.audit(r, s.User.Name, "logout", "")
	http.Redirect(w, r, "/login?ok=signedout", http.StatusSeeOther)
}

func (a *App) handleAccount(w http.ResponseWriter, r *http.Request, s *Session) {
	a.render(w, r, http.StatusOK, "account", view{Title: "Account", Session: s})
}

func (a *App) handleOwnPassword(w http.ResponseWriter, r *http.Request, s *Session) {
	key := clientIP(r) + "\x00" + s.User.Name
	now := a.now()
	if a.pairLimit.blocked(key, now) {
		a.renderError(w, r, http.StatusTooManyRequests, "Too many attempts. Wait a few minutes and try again.")
		return
	}
	u, err := a.store.UserByName(s.User.Name)
	if err != nil || u == nil {
		a.renderError(w, r, http.StatusInternalServerError, "Something went wrong.")
		return
	}
	if !verifyPassword(r.PostFormValue("current"), u.Password) {
		a.pairLimit.fail(key, now)
		a.audit(r, u.Name, "password-change-failed", "")
		a.renderError(w, r, http.StatusForbidden, "The current password is wrong.")
		return
	}
	pw := r.PostFormValue("new")
	if pw != r.PostFormValue("confirm") {
		a.renderError(w, r, http.StatusBadRequest, "The two new passwords are different.")
		return
	}
	if err := validPassword(pw); err != nil {
		a.renderError(w, r, http.StatusBadRequest, "The new "+err.Error()+".")
		return
	}
	hash, err := hashPassword(pw)
	if err == nil {
		err = a.store.SetPassword(u.ID, hash)
	}
	if err != nil {
		slog.Error("set password", "err", err)
		a.renderError(w, r, http.StatusInternalServerError, "Something went wrong.")
		return
	}
	a.dropCache()
	clearCookie(w)
	a.audit(r, u.Name, "password-changed", "")
	http.Redirect(w, r, "/login?ok=changed", http.StatusSeeOther)
}

type powerData struct{ Heading, Message string }

// handlePower shuts the Pi down or restarts it, for the owner only.
func (a *App) handlePower(w http.ResponseWriter, r *http.Request, s *Session) {
	action := r.PathValue("action")
	var page powerData
	switch action {
	case "poweroff":
		page = powerData{"Shutting down", "Wait until the green light on the Pi has stopped flashing, about 20 seconds, before unplugging it."}
	case "reboot":
		page = powerData{"Restarting", "The Pi is back in about half a minute. This page does not reload by itself."}
	default:
		a.renderError(w, r, http.StatusNotFound, "There is nothing here.")
		return
	}
	if r.PostFormValue("confirm") != "yes" {
		a.renderError(w, r, http.StatusBadRequest, "Tick “I'm sure” to confirm.")
		return
	}
	a.audit(r, s.User.Name, "power-"+action, "")
	if err := a.power(action); err != nil {
		slog.Error("power", "action", action, "err", err)
		a.renderError(w, r, http.StatusInternalServerError, "The Pi refused: "+err.Error())
		return
	}
	a.render(w, r, http.StatusOK, "power", view{Title: page.Heading, Data: page})
}

type grantRow struct{ ID, Title, Level string }

type adminUser struct {
	*User
	Rows []grantRow
}

func (a *App) handleAdmin(w http.ResponseWriter, r *http.Request, s *Session) {
	users, err := a.store.ListUsers()
	if err != nil {
		slog.Error("list users", "err", err)
		a.renderError(w, r, http.StatusInternalServerError, "Something went wrong.")
		return
	}
	var list []adminUser
	for _, u := range users {
		au := adminUser{User: u}
		for _, m := range a.services {
			au.Rows = append(au.Rows, grantRow{ID: m.ID, Title: m.Title, Level: u.Grants[m.ID].String()})
		}
		list = append(list, au)
	}
	a.render(w, r, http.StatusOK, "admin", view{
		Title: "Users", Session: s, Flash: flashes[r.URL.Query().Get("ok")], Data: list,
	})
}

func (a *App) handleAudit(w http.ResponseWriter, r *http.Request, s *Session) {
	entries, err := a.store.RecentAudit(300)
	if err != nil {
		slog.Error("audit list", "err", err)
		a.renderError(w, r, http.StatusInternalServerError, "Something went wrong.")
		return
	}
	a.render(w, r, http.StatusOK, "audit", view{Title: "Audit log", Session: s, Data: entries})
}

func (a *App) handleCreateUser(w http.ResponseWriter, r *http.Request, s *Session) {
	name := strings.ToLower(strings.TrimSpace(r.PostFormValue("username")))
	pw := r.PostFormValue("password")
	if err := validUsername(name); err != nil {
		a.renderError(w, r, http.StatusBadRequest, "The "+err.Error()+".")
		return
	}
	if err := validPassword(pw); err != nil {
		a.renderError(w, r, http.StatusBadRequest, "The "+err.Error()+".")
		return
	}
	hash, err := hashPassword(pw)
	if err == nil {
		err = a.store.CreateUser(name, hash, false)
	}
	if errors.Is(err, ErrExists) {
		a.renderError(w, r, http.StatusConflict, "A user called "+name+" already exists.")
		return
	}
	if err != nil {
		slog.Error("create user", "err", err)
		a.renderError(w, r, http.StatusInternalServerError, "Something went wrong.")
		return
	}
	a.audit(r, s.User.Name, "user-created", name)
	http.Redirect(w, r, "/admin/?ok=created", http.StatusSeeOther)
}

func (a *App) handleUserAction(w http.ResponseWriter, r *http.Request, s *Session) {
	name, action := r.PathValue("name"), r.PathValue("action")
	u, err := a.store.UserByName(name)
	if err != nil {
		a.renderError(w, r, http.StatusInternalServerError, "Something went wrong.")
		return
	}
	if u == nil {
		a.renderError(w, r, http.StatusNotFound, "No such user.")
		return
	}
	if u.Owner {
		a.renderError(w, r, http.StatusForbidden, "Owners are managed on the Pi with pms-gateway user.")
		return
	}

	switch action {
	case "grants":
		for _, m := range a.services {
			var lvl Level
			switch r.PostFormValue("grant-" + m.ID) {
			case "none", "":
				lvl = LevelNone
			case "use":
				lvl = LevelUse
			case "admin":
				lvl = LevelAdmin
			default:
				a.renderError(w, r, http.StatusBadRequest, "Unknown access level.")
				return
			}
			if lvl == u.Grants[m.ID] {
				continue
			}
			if err = a.store.SetGrant(u.ID, m.ID, lvl); err != nil {
				break
			}
			a.audit(r, s.User.Name, "grant", fmt.Sprintf("%s %s=%s", u.Name, m.ID, lvl))
		}
	case "password":
		pw := r.PostFormValue("password")
		if verr := validPassword(pw); verr != nil {
			a.renderError(w, r, http.StatusBadRequest, "The "+verr.Error()+".")
			return
		}
		var hash string
		if hash, err = hashPassword(pw); err == nil {
			err = a.store.SetPassword(u.ID, hash)
		}
		a.audit(r, s.User.Name, "password-reset", u.Name)
	case "disable", "enable":
		err = a.store.SetDisabled(u.ID, action == "disable")
		a.audit(r, s.User.Name, "user-"+action+"d", u.Name)
	case "logout":
		err = a.store.DeleteUserSessions(u.ID)
		a.audit(r, s.User.Name, "user-signed-out", u.Name)
	case "delete":
		if r.PostFormValue("confirm") != u.Name {
			a.renderError(w, r, http.StatusBadRequest, "Type the username exactly to confirm deleting it.")
			return
		}
		err = a.store.DeleteUser(u.ID)
		a.audit(r, s.User.Name, "user-deleted", u.Name)
	default:
		a.renderError(w, r, http.StatusNotFound, "There is nothing here.")
		return
	}
	a.dropCache()
	if err != nil {
		slog.Error("user action", "action", action, "err", err)
		a.renderError(w, r, http.StatusInternalServerError, "Something went wrong.")
		return
	}
	http.Redirect(w, r, "/admin/?ok="+action, http.StatusSeeOther)
}
