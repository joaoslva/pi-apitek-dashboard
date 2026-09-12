# gateway

`pms-gateway` is the only way into the Pi's services: TLS, sign-in, the
portal, owner administration and an authenticating reverse proxy. One Go
binary with its pages embedded; the pages use no JavaScript.

- `https://<pi>/` — sign in, the portal (only the services you were granted),
  your account; for the owner also Users and Audit log
- `https://<pi>:<route.port>/` — each service, behind the same session,
  proxied to its loopback upstream. Etherpad is 8443, the camera 8444.

## Build and deploy

```bash
gateway/build.sh               # go vet + tests in Docker, then the arm64 binary
platform/deploy.sh gateway     # binary, unit, /etc/pms/gateway.toml, every service.toml
```

Go is not needed on the laptop: `build.sh` runs `golang:1.26-trixie`, with the
module cache in `~/.cache/pms-gateway-go`. `build.sh tidy` refreshes `go.mod`.
`deploy.sh` refuses to ship a binary older than its sources, and restarts the
gateway when the binary, its unit or anything under `/etc/pms/` changed.

`platform/provision/base.sh` enables the unit at boot.

## Users

Run from the laptop; the password is typed with echo off and sent over SSH
stdin, never as an argument:

```bash
gateway/user.sh add joao --owner   # the first owner
gateway/user.sh passwd joao        # lost password; ends that user's sessions
gateway/user.sh list
```

Owners can only be created this way. There is no first-visit setup page,
because anyone on the hotspot could get to it first. Other users are created
and granted access in the Users page.

Passwords need at least 10 characters.

## Certificate

On first start, with no files at `tls_cert` and `tls_key`, the gateway generates
a self-signed ECDSA certificate for the hostname, `<hostname>.local`,
`10.42.0.1` and the addresses it has at that moment. It logs the SHA-256
fingerprint:

```bash
journalctl -u pms-gateway | grep cert_sha256
```

Compare it with the certificate the browser shows before accepting the
warning. Accept it for each port you use; some browsers remember the choice
per host, others per host and port.

To use a real certificate (e.g. Let's Encrypt via DNS-01 later), put both files
at the configured paths, readable by `pms-gateway`, and restart the service.

## Security model

- **Origins.** Every service has its own port, which browsers treat as a
  separate origin: script in one service cannot read or submit the gateway's
  pages. The session cookie (`__Host-pms`, HttpOnly, Secure, SameSite=Lax) is
  still sent to every port, so the proxy removes it from requests, drops any
  `Set-Cookie` for it in responses, and requires non-GET requests and websocket
  upgrades to carry an `Origin` equal to their own port's origin.
- **Access.** No grant means 404 on the whole port and no menu entry. Rules from
  `service.toml` are checked on every request, including websocket upgrades;
  see `services/README.md`. Ambiguous paths (`..`, `//`, encoded `/`) are
  rejected before matching.
- **Passwords.** argon2id, m=19 MiB, t=2, p=1; at most two hashes at a time so
  a login burst cannot exhaust the Pi's RAM. Unknown usernames are checked
  against a dummy hash, so timing does not reveal which users exist.
- **Login limits.** 5 failures per client IP and username, 20 per IP, per 15
  minutes. In memory; a restart clears them.
- **Sessions.** Random 256-bit tokens; the database keeps only their SHA-256.
  Idle 12 h, at most 7 days. Changing or resetting a password, disabling or
  deleting a user ends their sessions. Changes reach running requests within
  10 s (the session cache).
- **Forms.** Per-session CSRF token and the `Origin` check on every POST.
  Pages send a CSP with `default-src 'none'`, `frame-ancestors 'none'` and
  `form-action` limited to the gateway and service origins.
- **Audit log.** Logins (good, failed, rate-limited), sign-outs, user and grant
  changes, denied requests and refused cross-origin requests; the newest
  10 000 entries are kept.
- **Process.** Runs as `pms-gateway` with only `CAP_NET_BIND_SERVICE`, under
  systemd sandboxing (`systemd-analyze security`: 1.5). Starting and stopping
  services (Phase 3) goes through the polkit rules in `platform/`.

## Files on the Pi

| Path | |
|---|---|
| `/usr/local/bin/pms-gateway` | the binary (`serve`, `check`, `user`) |
| `/etc/systemd/system/pms-gateway.service` | unit |
| `/etc/pms/gateway.toml` | listen address, paths, session lengths |
| `/etc/pms/services/<id>.toml` | copies of `services/<id>/service.toml` |
| `/var/lib/pms-gateway/gateway.db` | users, grants, sessions, audit (SQLite, WAL) |
| `/var/lib/pms-gateway/tls/` | generated certificate and key |

A manifest removed from the repo is not removed from `/etc/pms/services/` by
deploy; delete it on the Pi and restart the gateway.

## Measured (2026-09-12, Pi Zero 2 W)

25 MB memory after start; about 1 s from start to listening, most of it the
certificate and the dummy hash.
