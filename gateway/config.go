package main

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/BurntSushi/toml"
)

// Duration reads Go duration strings ("90s", "12h") from TOML.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	if v <= 0 {
		return fmt.Errorf("duration must be positive: %s", b)
	}
	d.Duration = v
	return nil
}

type Config struct {
	Listen       string   `toml:"listen"`
	RedirectHTTP string   `toml:"redirect_http"`
	ServicesDir  string   `toml:"services_dir"`
	Database     string   `toml:"database"`
	TLSCert      string   `toml:"tls_cert"`
	TLSKey       string   `toml:"tls_key"`
	Hostnames    []string `toml:"hostnames"`
	SessionIdle  Duration `toml:"session_idle"`
	SessionMax   Duration `toml:"session_max"`
}

func loadConfig(path string) (*Config, error) {
	cfg := &Config{
		Listen:      ":443",
		ServicesDir: "/etc/pms/services",
		Database:    "/var/lib/pms-gateway/gateway.db",
		TLSCert:     "/var/lib/pms-gateway/tls/cert.pem",
		TLSKey:      "/var/lib/pms-gateway/tls/key.pem",
		SessionIdle: Duration{12 * time.Hour},
		SessionMax:  Duration{7 * 24 * time.Hour},
	}
	md, err := toml.DecodeFile(path, cfg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return nil, fmt.Errorf("%s: unknown keys %v", path, und)
	}
	if _, err := listenPort(cfg.Listen); err != nil {
		return nil, fmt.Errorf("%s: listen: %w", path, err)
	}
	if cfg.RedirectHTTP != "" {
		if _, err := listenPort(cfg.RedirectHTTP); err != nil {
			return nil, fmt.Errorf("%s: redirect_http: %w", path, err)
		}
	}
	if cfg.SessionIdle.Duration > cfg.SessionMax.Duration {
		return nil, fmt.Errorf("%s: session_idle is longer than session_max", path)
	}
	return cfg, nil
}

func listenPort(addr string) (string, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", errors.New("bad port in " + addr)
	}
	return port, nil
}
