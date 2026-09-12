package main

import (
	"fmt"
	"net/url"
	"strings"
)

type Level int

const (
	LevelNone Level = iota
	LevelUse
	LevelAdmin
	LevelOwner
	LevelDeny // matches nobody, the owner included
)

func parseLevel(s string) (Level, error) {
	switch s {
	case "use":
		return LevelUse, nil
	case "admin":
		return LevelAdmin, nil
	case "owner":
		return LevelOwner, nil
	case "deny":
		return LevelDeny, nil
	}
	return LevelNone, fmt.Errorf("unknown level %q (use, admin, owner or deny)", s)
}

func (l Level) String() string {
	switch l {
	case LevelUse:
		return "use"
	case LevelAdmin:
		return "admin"
	case LevelOwner:
		return "owner"
	case LevelDeny:
		return "deny"
	}
	return "none"
}

// cleanPath decodes an escaped request path, refusing anything an upstream
// could interpret differently from the rule matcher.
func cleanPath(escaped string) (string, bool) {
	if !strings.HasPrefix(escaped, "/") || strings.Contains(escaped, `\`) {
		return "", false
	}
	lower := strings.ToLower(escaped)
	for _, bad := range []string{"%2f", "%5c", "%00"} {
		if strings.Contains(lower, bad) {
			return "", false
		}
	}
	p, err := url.PathUnescape(escaped)
	if err != nil || strings.Contains(p, "//") {
		return "", false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return "", false
		}
	}
	return p, true
}

// required is the level a request needs. The longest matching rule wins;
// at the same path a rule naming methods beats one that does not.
func (m *Manifest) required(p, method string) Level {
	p = strings.ToLower(p)
	need, best := m.level, -1
	for _, r := range m.Access.Rules {
		if !pathCovers(r.match, p) || !methodMatches(r.Methods, method) {
			continue
		}
		score := 2 * len(r.match)
		if len(r.Methods) > 0 {
			score++
		}
		if score > best {
			need, best = r.level, score
		}
	}
	return need
}

func pathCovers(rule, p string) bool {
	return rule == "/" || p == rule || strings.HasPrefix(p, rule+"/")
}

func methodMatches(methods []string, method string) bool {
	if len(methods) == 0 {
		return true
	}
	for _, m := range methods {
		if m == method || (method == "HEAD" && m == "GET") {
			return true
		}
	}
	return false
}

func allowed(have, need Level) bool {
	return need != LevelDeny && have != LevelDeny && have >= need
}
