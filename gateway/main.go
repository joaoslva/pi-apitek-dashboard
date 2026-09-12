// pms-gateway is the only way into pi-mobile-server: TLS, login, the portal,
// owner administration, and one authenticating reverse proxy per service port.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const defaultConfig = "/etc/pms/gateway.toml"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe(os.Args[2:])
	case "check":
		err = cmdCheck(os.Args[2:])
	case "user":
		err = cmdUser(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  pms-gateway serve  [-config FILE]
  pms-gateway check  [-config FILE]
  pms-gateway user list   [-config FILE]
  pms-gateway user add    [-config FILE] [-owner] NAME   (password on stdin)
  pms-gateway user passwd [-config FILE] NAME            (password on stdin)
`)
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfig, "config file")
	fs.Parse(args)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	services, err := loadManifests(cfg.ServicesDir)
	if err != nil {
		return err
	}
	st, err := openStore(cfg.Database)
	if err != nil {
		return err
	}
	defer st.Close()
	cert, err := loadOrCreateCert(cfg.TLSCert, cfg.TLSKey, cfg.Hostnames)
	if err != nil {
		return err
	}
	app, err := newApp(cfg, st, services)
	if err != nil {
		return err
	}

	tlsConf := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	errLog := log.New(handshakeFilter{}, "", 0)

	servers := []*http.Server{{
		Addr:              cfg.Listen,
		Handler:           app.gatewayHandler(),
		TLSConfig:         tlsConf,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          errLog,
	}}
	for _, m := range services {
		// HTTP/1.1 only: websocket upgrades through the proxy need it, and
		// no Read/WriteTimeout, which would cut websockets, streams and uploads.
		var protos http.Protocols
		protos.SetHTTP1(true)
		servers = append(servers, &http.Server{
			Addr:              fmt.Sprintf(":%d", m.Route.Port),
			Handler:           app.serviceHandler(m),
			TLSConfig:         tlsConf,
			Protocols:         &protos,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    64 << 10,
			ErrorLog:          errLog,
		})
	}

	// Bind everything before serving anything, so a taken port fails the start.
	listeners := make([]net.Listener, len(servers))
	for i, s := range servers {
		if listeners[i], err = net.Listen("tcp", s.Addr); err != nil {
			return fmt.Errorf("listen %s: %w", s.Addr, err)
		}
	}
	errc := make(chan error, len(servers)+1)
	for i, s := range servers {
		go func() { errc <- s.ServeTLS(listeners[i], "", "") }()
	}
	if cfg.RedirectHTTP != "" {
		rs := &http.Server{
			Addr:              cfg.RedirectHTTP,
			Handler:           http.HandlerFunc(app.redirectToHTTPS),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      10 * time.Second,
			ErrorLog:          errLog,
		}
		ln, err := net.Listen("tcp", rs.Addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", rs.Addr, err)
		}
		servers = append(servers, rs)
		go func() { errc <- rs.Serve(ln) }()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	go app.maintenance(ctx)

	slog.Info("gateway listening", "addr", cfg.Listen, "services", len(services),
		"cert_sha256", fmt.Sprintf("%X", sha256.Sum256(cert.Certificate[0])))
	st.Audit("system", "", "gateway-start", fmt.Sprintf("%d services", len(services)))

	select {
	case <-ctx.Done():
		err = nil
	case err = <-errc:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		s.Shutdown(shutdownCtx)
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// Browsers that have not accepted the self-signed certificate abort every
// handshake; logging each one would drown the journal.
type handshakeFilter struct{}

func (handshakeFilter) Write(p []byte) (int, error) {
	if !bytes.Contains(p, []byte("TLS handshake error")) {
		os.Stderr.Write(p)
	}
	return len(p), nil
}

func cmdCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfig, "config file")
	fs.Parse(args)
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	services, err := loadManifests(cfg.ServicesDir)
	if err != nil {
		return err
	}
	fmt.Printf("config ok: %s, %d services\n", *cfgPath, len(services))
	for _, m := range services {
		fmt.Printf("  %-14s :%d -> %s  units=%v\n", m.ID, m.Route.Port, m.Route.Upstream, m.Run.Units)
	}
	return nil
}

func cmdUser(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("missing user subcommand")
	}
	sub := args[0]
	fs := flag.NewFlagSet("user "+sub, flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfig, "config file")
	owner := fs.Bool("owner", false, "make the new user an owner")
	fs.Parse(args[1:])

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	st, err := openStore(cfg.Database)
	if err != nil {
		return err
	}
	defer st.Close()

	switch sub {
	case "list":
		users, err := st.ListUsers()
		if err != nil {
			return err
		}
		for _, u := range users {
			var grants []string
			for svc, lvl := range u.Grants {
				grants = append(grants, svc+"="+lvl.String())
			}
			fmt.Printf("%-20s owner=%-5v disabled=%-5v %s\n", u.Name, u.Owner, u.Disabled, strings.Join(grants, " "))
		}
		return nil

	case "add", "passwd":
		if fs.NArg() != 1 {
			return errors.New("give exactly one NAME, after the flags")
		}
		name := fs.Arg(0)
		if err := validUsername(name); err != nil {
			return err
		}
		pw, err := readPassword(os.Stdin)
		if err != nil {
			return err
		}
		if err := validPassword(pw); err != nil {
			return err
		}
		hash, err := hashPassword(pw)
		if err != nil {
			return err
		}
		if sub == "add" {
			if err := st.CreateUser(name, hash, *owner); err != nil {
				return err
			}
			st.Audit("cli", "", "user-created", fmt.Sprintf("%s owner=%v", name, *owner))
		} else {
			u, err := st.UserByName(name)
			if err != nil {
				return err
			}
			if u == nil {
				return fmt.Errorf("no user %q", name)
			}
			if err := st.SetPassword(u.ID, hash); err != nil {
				return err
			}
			st.Audit("cli", "", "password-reset", name)
		}
		fmt.Println("ok")
		return nil
	}
	return fmt.Errorf("unknown user subcommand %q", sub)
}

// readPassword takes stdin up to 1 KiB, minus one trailing newline.
func readPassword(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, 1024))
	if err != nil {
		return "", err
	}
	s := strings.TrimSuffix(string(b), "\n")
	return strings.TrimSuffix(s, "\r"), nil
}
