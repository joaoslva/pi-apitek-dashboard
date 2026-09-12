package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

type sessionKey struct{}

// serviceHandler guards one service port: session, access rules, origin,
// then a reverse proxy to the loopback upstream.
func (a *App) serviceHandler(m *Manifest) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(m.upstream)
			pr.SetXForwarded()
			pr.Out.Host = pr.In.Host
			removeCookie(pr.Out.Header, cookieName)
			for k := range pr.Out.Header {
				if strings.HasPrefix(k, "X-Pms-") {
					pr.Out.Header.Del(k)
				}
			}
			if s, ok := pr.In.Context().Value(sessionKey{}).(*Session); ok {
				pr.Out.Header.Set("X-Pms-User", s.User.Name)
				pr.Out.Header.Set("X-Pms-Level", s.levelFor(m.ID).String())
			}
		},
		// Flush every write: the camera's live view is an endless multipart stream.
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			removeSetCookie(resp.Header, cookieName)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Warn("upstream unavailable", "service", m.ID, "err", err)
			a.renderError(w, r, http.StatusBadGateway, m.Title+" is not running right now.")
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostPattern.MatchString(r.Host) {
			http.Error(w, "bad host", http.StatusBadRequest)
			return
		}
		p, ok := cleanPath(r.URL.EscapedPath())
		if !ok {
			a.renderError(w, r, http.StatusBadRequest, "That address is not allowed.")
			return
		}
		if !originOK(r) {
			a.audit(r, "", "cross-origin-refused", fmt.Sprintf("%s %s %s", m.ID, r.Method, truncate(p, 200)))
			a.renderError(w, r, http.StatusForbidden, "This request came from another site and was refused.")
			return
		}
		s := a.currentSession(r)
		if s == nil {
			if isNavigation(r) {
				next := "https://" + r.Host + r.URL.RequestURI()
				http.Redirect(w, r, a.gatewayOrigin(r)+"/login?next="+url.QueryEscape(next), http.StatusSeeOther)
				return
			}
			a.renderError(w, r, http.StatusUnauthorized, "Sign in first.")
			return
		}
		have := s.levelFor(m.ID)
		if have == LevelNone {
			a.renderError(w, r, http.StatusNotFound, "There is nothing here.")
			return
		}
		if !allowed(have, m.required(p, r.Method)) {
			a.audit(r, s.User.Name, "denied", fmt.Sprintf("%s %s %s", m.ID, r.Method, truncate(p, 200)))
			a.renderError(w, r, http.StatusForbidden, "Your access to "+m.Title+" does not include this.")
			return
		}
		rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, s)))
	})
}

// removeCookie drops one cookie from a request's Cookie headers.
func removeCookie(h http.Header, name string) {
	var kept []string
	for _, line := range h.Values("Cookie") {
		for _, part := range strings.Split(line, ";") {
			part = strings.TrimSpace(part)
			n, _, _ := strings.Cut(part, "=")
			if part == "" || strings.TrimSpace(n) == name {
				continue
			}
			kept = append(kept, part)
		}
	}
	h.Del("Cookie")
	if len(kept) > 0 {
		h.Set("Cookie", strings.Join(kept, "; "))
	}
}

// removeSetCookie stops an upstream from overwriting the gateway's session.
func removeSetCookie(h http.Header, name string) {
	vals := h.Values("Set-Cookie")
	if len(vals) == 0 {
		return
	}
	h.Del("Set-Cookie")
	for _, v := range vals {
		n, _, _ := strings.Cut(v, "=")
		if !strings.EqualFold(strings.TrimSpace(n), name) {
			h.Add("Set-Cookie", v)
		}
	}
}
