package main

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// The firewall opens exactly this range for service ports.
const (
	minServicePort = 8443
	maxServicePort = 8450
)

// Manifest is a service.toml; see services/README.md for the format.
type Manifest struct {
	ID          string `toml:"id"`
	Title       string `toml:"title"`
	Description string `toml:"description"`
	Icon        string `toml:"icon"`
	Route       struct {
		Port     int    `toml:"port"`
		Upstream string `toml:"upstream"`
	} `toml:"route"`
	Run struct {
		Units        []string `toml:"units"`
		ReadyPath    string   `toml:"ready_path"`
		StartTimeout Duration `toml:"start_timeout"`
		Auto         bool     `toml:"auto"`
		IdleStop     Duration `toml:"idle_stop"`
		Exclusive    []string `toml:"exclusive"`
	} `toml:"run"`
	Memory struct {
		Budget string `toml:"budget"`
		Max    string `toml:"max"`
	} `toml:"memory"`
	Access struct {
		Level string       `toml:"level"`
		Rules []AccessRule `toml:"rules"`
	} `toml:"access"`

	upstream *url.URL
	level    Level
}

type AccessRule struct {
	Path    string   `toml:"path"`
	Methods []string `toml:"methods"`
	Level   string   `toml:"level"`

	match string // lower-cased Path
	level Level
}

var (
	idPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	sizePattern   = regexp.MustCompile(`^[0-9]+[KMG]$`)
	methodPattern = regexp.MustCompile(`^[A-Z]+$`)
	unitPattern   = regexp.MustCompile(`^[A-Za-z0-9@._-]+\.service$`)
)

// loadManifests reads every <id>.toml in dir, sorted by title.
func loadManifests(dir string) ([]*Manifest, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return nil, err
	}
	var out []*Manifest
	ports := map[int]string{}
	for _, f := range files {
		m, err := loadManifest(f)
		if err != nil {
			return nil, err
		}
		if want := strings.TrimSuffix(filepath.Base(f), ".toml"); m.ID != want {
			return nil, fmt.Errorf("%s: id %q must match the file name", f, m.ID)
		}
		if other, dup := ports[m.Route.Port]; dup {
			return nil, fmt.Errorf("%s: port %d is already used by %s", f, m.Route.Port, other)
		}
		ports[m.Route.Port] = m.ID
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out, nil
}

func loadManifest(file string) (*Manifest, error) {
	m := &Manifest{}
	md, err := toml.DecodeFile(file, m)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return nil, fmt.Errorf("%s: unknown keys %v", file, und)
	}
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return m, nil
}

func (m *Manifest) validate() error {
	if !idPattern.MatchString(m.ID) {
		return fmt.Errorf("bad id %q", m.ID)
	}
	if m.Title == "" {
		return errors.New("title is required")
	}
	if m.Route.Port < minServicePort || m.Route.Port > maxServicePort {
		return fmt.Errorf("route.port must be %d-%d, the range the firewall opens", minServicePort, maxServicePort)
	}
	u, err := url.Parse(m.Route.Upstream)
	if err != nil || u.Scheme != "http" || u.Port() == "" || (u.Path != "" && u.Path != "/") ||
		u.RawQuery != "" || u.User != nil {
		return fmt.Errorf("route.upstream must look like http://127.0.0.1:PORT, got %q", m.Route.Upstream)
	}
	if h := u.Hostname(); h != "127.0.0.1" && h != "::1" {
		return fmt.Errorf("route.upstream must be a loopback address, got %q", h)
	}
	u.Path = ""
	m.upstream = u

	for _, unit := range m.Run.Units {
		if !unitPattern.MatchString(unit) {
			return fmt.Errorf("run.units: bad unit name %q", unit)
		}
	}
	if m.Run.ReadyPath != "" && !strings.HasPrefix(m.Run.ReadyPath, "/") {
		return errors.New("run.ready_path must start with /")
	}
	for _, s := range []string{m.Memory.Budget, m.Memory.Max} {
		if s != "" && !sizePattern.MatchString(s) {
			return fmt.Errorf("memory: bad size %q (use K, M or G)", s)
		}
	}

	if m.level, err = parseLevel(m.Access.Level); err != nil {
		return fmt.Errorf("access.level: %w", err)
	}
	seen := map[string]bool{}
	for i := range m.Access.Rules {
		r := &m.Access.Rules[i]
		if !strings.HasPrefix(r.Path, "/") || path.Clean(r.Path) != r.Path {
			return fmt.Errorf("access rule path %q must be absolute and clean, without a trailing slash", r.Path)
		}
		for _, meth := range r.Methods {
			if !methodPattern.MatchString(meth) {
				return fmt.Errorf("access rule %s: bad method %q", r.Path, meth)
			}
		}
		if r.level, err = parseLevel(r.Level); err != nil {
			return fmt.Errorf("access rule %s: %w", r.Path, err)
		}
		r.match = strings.ToLower(r.Path)
		methods := slices.Clone(r.Methods)
		slices.Sort(methods)
		key := r.match + " " + strings.Join(methods, ",")
		if seen[key] {
			return fmt.Errorf("access rule %s is listed twice", r.Path)
		}
		seen[key] = true
	}
	return nil
}
