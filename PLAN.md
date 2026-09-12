# pi-mobile-server — plan

A Raspberry Pi Zero 2 W as a pocket server: carry it to a meeting, switch it on,
colleagues join its hotspot and use Etherpad through a login gateway. The
existing camera-drive project becomes one service among several.

Status: Phase 2 deployed (2026-09-12); owner account and a browser check
pending. Next: Phase 3.

## Settled decisions

**Hardware / OS**
- Pi Zero 2 W stays. A Pi 5 may come later, so hardware specifics live only in
  `platform/`, and budgets are read from real RAM, not hardcoded.
- Raspberry Pi OS on Debian 13 trixie, arm64, kernel 6.18 — no re-flash needed.

**Access and security**
- HTTPS. The gateway loads cert/key files and generates a self-signed pair only
  when they are missing (decided 2026-09-12). Let's Encrypt later via DNS-01
  (needs a domain + DNS API; public A records may point at 10.42.0.1 and the
  home IP; renew from a machine with internet and deploy the files).
- Auth model: `owner` (everything + admin) plus per-user service grants at
  `use` or `admin` level. Users for now: owner and their boss.
- Every service binds `127.0.0.1` only; nftables allows 22, 80 (redirect),
  443, plus DHCP/DNS on the hotspot. Deny by default.
- The gateway checks every request, including websocket upgrades. Menus are
  cosmetic; enforcement is at the proxy.
- Argon2id hashes, server-side sessions (HttpOnly, Secure, SameSite=Lax),
  CSRF tokens, login rate limiting, audit log.
- Gateway runs as its own unprivileged user, never with sudo. Privileged
  actions (start/stop units, network changes, power off) go through narrow
  polkit rules or a tiny root helper with a fixed command list.

**Gateway**
- Written in Go, cross-compiled on the laptop (Go is not installed there: build
  in Docker's `golang` image), one arm64 binary with HTML/CSS/JS embedded.
- Replaces nginx: TLS, auth, reverse proxy (websockets), portal, owner admin UI,
  service manager, network manager. Pure-Go SQLite.
- **Port per service** (decided 2026-09-12, replaces path prefixes): login,
  portal and admin on `https://<host>/` (443); each service on its own HTTPS
  port in 8443–8450 (Etherpad 8443, camera 8444). Browsers isolate origins by
  port, so an XSS in a proxied app cannot read or drive the gateway's pages;
  and apps run at their own root, so no prefix rewriting. The session cookie
  (`__Host-`, host-only) is still sent to every port: the proxy strips it
  before forwarding, and non-GET requests and websocket upgrades must carry
  an `Origin` equal to the port's own origin (cross-port CSRF).
- Each service folder has a `service.toml` (name, route prefix, upstream port,
  systemd unit, permissions, memory budget, icon, exclusive group). The menu
  and the access rules are generated from it.

**Services and memory**
- Service states: `off`, `on`, `auto` (start on first authorised visit, stop
  after idle).
- **Default at boot: all services off. On shutdown: all active services are
  stopped cleanly.**
- **Camera and Etherpad never run together by default** (same exclusive group).
- Admission check: refuse to start something that does not fit the budget;
  never kill a service in active use; manual choices override automation.
  Core (gateway, sshd, NetworkManager) is never stoppable.
- Profiles set network mode + service states in one tap:
  - Meeting: Island hotspot, Etherpad on (start it before people arrive —
    cold start is ~51 s)
  - Camera: Island hotspot, camera on
  - Maintenance: Join TP-Link, everything off except core
- `MemoryMax=` only works after adding `cgroup_enable=memory` to
  `/boot/firmware/cmdline.txt` (firmware appends `cgroup_disable=memory`).

**Network**
- Modes:
  - **Island** (default at every boot): hotspot on `wlan0`, NM shared mode.
  - **Join**: client on a room/home network. WPA2-Personal and WPA2-Enterprise
    (username + password, PEAP/TTLS). Enterprise form must validate the
    server certificate (CA cert or domain match) so credentials cannot be
    harvested by an evil twin.
  - **Relay: DEFERRED** until a USB WiFi dongle + micro-USB OTG hub are bought
    (candidates: MT7612U dual-band, RT5370 2.4 GHz — confirm model first).
    The onboard firmware rejects WPA on a second virtual interface in both
    directions (tested, see below).
- Captive "click to accept" portals are out of scope.
- Switching to Join uses confirm-or-revert: join, wait ~3 min for the owner to
  open a confirm page from the new network, otherwise return to the hotspot.
- Watchdog in Join mode: network gone for a few minutes -> back to hotspot.
- The settings page uses saved profiles plus a scan taken at boot before the
  hotspot rises (scanning while hosting is unreliable).
- Hotspot SSID/password editable; friendly hostname on the hotspot via NM's
  dnsmasq (`/etc/NetworkManager/dnsmasq-shared.d/`); rename host from
  `cameradrive`.

**Etherpad**
- Ship the official `etherpad_<ver>_arm64.deb` *contents* plus the official
  Node 24 arm64 build as one unpacked bundle under `/opt`, built and deployed
  from the laptop. Do not `apt install` the .deb: it needs `nodejs>=24`
  (Debian has 20) and auto-enables a service that would fight the manager.
- The deploy must write `var/installed_plugins.json`
  (`{"plugins":[{"name":"ep_etherpad-lite","version":"<ver>"}]}`), or startup
  fails with `spawn pnpm ENOENT`.
- dbType `sqlite` (backed by rusty-store-kv, prebuilt for arm64); listen on
  127.0.0.1; Etherpad's own admin UI disabled or owner-only; budget ~200 MB.
- Verify checksums: GitHub release asset `digest`, and Node's `SHASUMS256.txt`.

**camera-drive**
- Stays Python for now. Move to `services/camera-drive/`.
- Binds `127.0.0.1:8081` without `CAP_NET_BIND_SERVICE` (done in Phase 2);
  reached only through the gateway on :8444. With a port per service the page
  keeps its absolute URLs; no prefix support needed.
- Power off moves to the platform (owner only); wipe becomes owner/admin only.
- The udev-triggered offload keeps working independently of the web app state.

**Later extras**
- Guest accounts with expiry, and a screen with two QR codes (WiFi join + login).
- Clock sync from the owner's browser at login when drift is large (no RTC).

## Repo layout (target)

```
pi-mobile-server/
├── PLAN.md
├── .gitignore               # backups/, build output
├── backups/                 # local data backups, never committed
├── platform/
│   ├── README.md
│   ├── provision/           # base.sh; fill-secrets, fix-pi-card, diagnose, setup-ap, cloud-init templates
│   ├── rootfs/              # files mirrored onto the Pi (units, nft, polkit, NM, zram…)
│   └── deploy.sh            # --list / --check / install; platform + services/*/rootfs
├── gateway/                 # Go: auth, proxy, portal, admin, service + network manager
│                            #   build.sh, user.sh, rootfs/ (unit, gateway.toml, built binary)
└── services/
    ├── README.md            # service.toml format and access rules
    ├── camera-drive/        # README lessons, service.toml, rootfs/ (app, units, udev)
    └── etherpad/            # service.toml; bundle build script, settings, unit (Phase 5)
```

## Phases

0. **Repo** — DONE 2026-09-11. First commit `6c7aff8` on `main` (later
   phases go through branches and PRs); camera-drive split into
   `services/camera-drive/` and `platform/`;
   `__pycache__` removed; `service.toml` format in `services/README.md` with
   manifests for camera-drive and etherpad; `platform/deploy.sh` generalised
   (installs root:root 0644/0755, refuses duplicate paths and symlinks,
   `--check` is read-only). `--check` showed the Pi's file contents match the
   repo exactly.
1. **Platform base** — DONE 2026-09-12. Files in `platform/rootfs/`, state in
   `platform/provision/base.sh` (idempotent, re-run shows no changes).
   - cloud-init off (`/etc/cloud/cloud-init.disabled`); its netplan WiFi
     profile and `/etc/netplan/*.yaml` (plain-text password) deleted;
     `/etc/hosts` no longer cloud-init managed.
   - Hostname `pocketserver`. ModemManager and bluetooth disabled.
     `netreport.service`, `ap-diag.sh` and a stale `__pycache__` removed.
     Boot 1 min 24 s -> 21.5 s.
   - `cgroup_enable=memory` added; `memory` controller active after reboot.
   - nftables: table `inet pms`, input policy drop; 22/80/443, mDNS,
     DHCPv6 replies, ICMP, DHCP+DNS on `wlan0`. A drop-in replaces the stock
     `nft flush ruleset` on stop. Closed ports time out from the LAN.
   - `pms-gateway` system user (sysusers.d, uid 985). Polkit lets it
     start/stop/restart `camera-drive-web` and `etherpad` only, power off and
     reboot, and NM network-control, modify.system, wifi.scan; everything
     else is refused (tested: restart cron and stop ssh denied).
   - Found and fixed: `/`, `/etc`, `/etc/systemd`, `/etc/udev`, `/usr`,
     `/usr/local`, `/usr/local/bin` were `joao:joao 775` (a root escalation
     for anything running as joao, e.g. camera-drive-web); camera binaries
     `joao`-owned, units 664. `deploy.sh` now fixes and reports unsafe parent
     directories.
   - Found and fixed: `joao` had an empty password (console login and `su`
     with none); now locked. Three identical NOPASSWD sudo files -> one,
     `/etc/sudoers.d/010-joao`.
   - `deploy.sh` also runs `systemd-sysusers`, and reloads a running firewall
     behind a 2-minute revert timer that a fresh SSH connection cancels.
2. **Gateway MVP** — TLS (self-signed), login, sessions, CSRF, rate limit,
   owner UI for users and grants, portal menu from `service.toml`, reverse
   proxy with websockets, audit log. Design (branch `phase-2-gateway`):
   - `gateway/`: one Go `main` package, built by `gateway/build.sh` in
     `golang:1.26-trixie` (CGO off, arm64) into
     `gateway/rootfs/usr/local/bin/pms-gateway` (gitignored); `gateway` is a
     deploy.sh component with its unit and `/etc/pms/gateway.toml`.
     deploy.sh also ships `services/<id>/service.toml` to
     `/etc/pms/services/<id>.toml`.
   - Unit runs as `pms-gateway`, `StateDirectory=pms-gateway` (SQLite DB,
     generated TLS), ambient `CAP_NET_BIND_SERVICE` only, full sandboxing.
   - SQLite (modernc, WAL): users (argon2id m=19 MiB t=2 p=1, at most 2
     hashes at once), grants (use/admin per service), sessions (only the
     SHA-256 of the token stored; idle 12 h, max 7 days), audit log.
   - Owner created from the CLI only (`pms-gateway user add NAME --owner`,
     password on stdin, via a laptop script) — no first-visit setup page
     that anyone on the hotspot could claim. The UI cannot create owners.
   - Login: rate limit per IP and per IP+user, dummy hash for unknown users,
     `next` must be https on the same host and a known port.
   - Gateway pages: no JavaScript, strict CSP, per-session CSRF token plus
     Origin check on every POST.
   - Proxy: session check, access rules from service.toml (404 without a
     grant), strips the gateway cookie both ways, `X-Forwarded-*` and
     `X-Pms-User`/`X-Pms-Level` set, immediate flush for streams, friendly
     502 when the service is down (starting it is Phase 3).
   - Port 80: the gateway only redirects to https. camera-drive-web moved to
     `127.0.0.1:8081` in this phase (pulled forward from Phase 4) after the
     owner found `http://<ip>/` served the camera app past the login.
   - Deployed 2026-09-12: 8 tests pass (paths, rules, origin, next URL,
     passwords, proxy incl. cookie stripping and websockets, login + rate
     limit + CSRF); 12.9 MB binary; 25 MB RAM on the Pi; `systemd-analyze
     security` 1.5; enabled at boot. Signed out, 8443/8444 redirect to login.
     Details in `gateway/README.md`.
   - Bug found by the owner's first sign-in: pages sent
     `Referrer-Policy: no-referrer`, so browsers posted forms with
     `Origin: null` and the cross-origin check refused the login. Now
     `Sec-Fetch-Site` is checked first (Origin only as fallback) and the
     policy is `same-origin`; regression test added.
3. **Service manager** — start/stop via systemd, off/on/auto, budgets and
   exclusive groups, "starting…" page, all-off at boot, stop-all on shutdown,
   profiles.
4. **camera-drive behind the gateway** — owner-only destructive actions; remove
   the camera app's own power-off (already `deny` in its manifest). (Localhost
   bind and an owner Shut down / Restart on the portal done in Phase 2; no
   prefix work needed with a port per service.)
5. **Etherpad bundle** — laptop build script, hardened unit, behind `/pad/`,
   Meeting profile.
6. **Network modes** — Island default, Join with confirm-or-revert, watchdog,
   enterprise form, settings UI.
7. **Extras** — guest expiry + QR, clock sync, Relay once a dongle exists.

## Measured on the Pi (2026-09-11)

| | |
|---|---|
| RAM visible | 416 MB (gpu_mem 64 MB); 256 MB CMA pool from `vc4-kms-v3d` |
| Idle baseline | ~270 MB available with nothing extra running |
| camera-drive-web | ~37 MB RSS |
| Etherpad 3.3.3 | ready after 51 s; ~200 MB peak during start; ~128 MB steady with one client; +~18 MB esbuild helper; start pushes ~200 MB into zram (~50 MB real) |
| Swap | 416 MB zstd zram (rpi-swap) |
| WiFi | BCM43430/1 family, firmware 7.45.96.s1, 2.4 GHz only; TP-Link on channel 1 |
| Relay test | failed: `brcmf_configure_wpaie: wpa_auth error -52` (AP on uap0), `wl_set_wpa_version failed (-52)` (client on uap0), also with hotspot started first |
| Boot | 21.5 s to multi-user after Phase 1 (was 1 min 24 s, 49 s of it netreport); ~250 MB available a minute after boot |
| Storage | SanDisk 64 GB, ~4 GB used |
| Software | NM 1.52, systemd 257, polkit 126, nftables (no rules), dnsmasq-base, Python 3.13, ffmpeg 7.1 |

## Working on the Pi

- `ssh -i ~/.ssh/pi_camera_drive joao@192.168.1.206` — hostname
  `pocketserver` (was `cameradrive`), but the laptop cannot resolve `.local`
  names. The Pi's WiFi can miss the first ARP; retry.
- `platform/deploy.sh --check` shows drift between the repo and the Pi without
  changing anything; run it before and after deploying.
- Non-login SSH PATH lacks `/usr/sbin` (`iw`, `NetworkManager`, `swapon`).
- `joao` has passwordless sudo from one file, `/etc/sudoers.d/010-joao`, and a
  locked password (SSH key only). `deploy.sh` needs `sudo -n`. Give joao a
  password before ever removing NOPASSWD, or sudo is gone.
- New card or recovered Pi: `platform/deploy.sh`, then `base.sh`, then reboot
  (see `platform/README.md`).
- Before anything that can break networking, arm a revert with
  `systemd-run --on-active=…` first.
- Long jobs on the Pi run as `systemd-run` units so a dropped SSH cannot
  interrupt them.

## Pitfalls already paid for

From `camera-drive/README.md` (keep that file with the service):
`RuntimeDirectory=` instead of listing `/run/...` in `ReadWritePaths`;
`ProtectSystem=strict` makes `/run` read-only; do not restrict
`CapabilityBoundingSet` on anything calling sudo; `KillMode=mixed` for ffmpeg
supervisors; `[hidden]{display:none !important}` before display rules;
cloud-init `instance-id` needs a hyphen; no RTC, so early boot logs carry old
dates.

From Phase 1: Debian's `nftables.conf` starts with `flush ruleset` and the
unit's `ExecStop` flushes too — both would wipe NetworkManager's shared-mode
tables, so only `table inet pms` is ever replaced; a deploy that copies a
rootfs tree onto `/` with ownership preserved hands the system directories to
the laptop user; `passwd -S` showing `NP` means an empty password; a bash
`until` loop returns its body's last status, not the condition's.

From Phase 2: `Referrer-Policy: no-referrer` makes a page's own form posts
send `Origin: null`, so an Origin-equality CSRF check refuses them — check
`Sec-Fetch-Site` first; unit tests that set headers by hand do not catch
browser behaviour like this.

From 2026-09-11: NM keyfiles must be mode 600; the duplicate netplan profile will
autoconnect on any free WiFi interface; memory cgroup off by default; the
Etherpad plugin marker; the camera's hardware H.264 encoder draws from the CMA
pool, so test `/api/mp4` before shrinking it.
