# pi-mobile-server — plan

A Raspberry Pi Zero 2 W as a pocket server: carry it to a meeting, switch it on,
colleagues join its hotspot and use Etherpad through a login gateway. The
existing camera-drive project becomes one service among several.

Status: Phase 0 done (2026-09-11). Next: Phase 1.

## Settled decisions

**Hardware / OS**
- Pi Zero 2 W stays. A Pi 5 may come later, so hardware specifics live only in
  `platform/`, and budgets are read from real RAM, not hardcoded.
- Raspberry Pi OS on Debian 13 trixie, arm64, kernel 6.18 — no re-flash needed.

**Access and security**
- HTTPS with a self-signed certificate.
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
- Path-based routing: `/pad/`, `/camera/`, `/admin/`. Works the same on the
  hotspot and on a joined network.
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
- Make the page prefix-aware (it uses absolute URLs: `/api/state`, `/thumb/…`,
  `/media/…`), bind `127.0.0.1:8081`, drop `CAP_NET_BIND_SERVICE`.
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
│   ├── provision/           # fill-secrets, fix-pi-card, diagnose, setup-ap, cloud-init templates
│   ├── rootfs/              # files mirrored onto the Pi (units, nft, polkit, NM, zram…)
│   └── deploy.sh            # --list / --check / install; platform + services/*/rootfs
├── gateway/                 # Go: auth, proxy, portal, admin, service + network manager
└── services/
    ├── README.md            # service.toml format and access rules
    ├── camera-drive/        # README lessons, service.toml, rootfs/ (app, units, udev)
    └── etherpad/            # service.toml; bundle build script, settings, unit (Phase 5)
```

## Phases

0. **Repo** — DONE 2026-09-11. `git init` (branch `main`, nothing committed
   yet); camera-drive split into `services/camera-drive/` and `platform/`;
   `__pycache__` removed; `service.toml` format in `services/README.md` with
   manifests for camera-drive and etherpad; `platform/deploy.sh` generalised
   (installs root:root 0644/0755, refuses duplicate paths and symlinks,
   `--check` is read-only). `--check` showed the Pi's file contents match the
   repo exactly.
1. **Platform base** — disable cloud-init; delete duplicate NM profile
   `netplan-wlan0-TP-LINK_8E4332`; disable ModemManager and bluetooth; stop
   `netreport` holding boot open; `cgroup_enable=memory`; nftables ruleset;
   gateway system user; polkit rules; hostname. First real deploy fixes
   ownership: `/usr/local/bin/camera-*` are owned by `joao` on the Pi
   (`camera-wipe` and `camera-offload` run as root) and units are 664 — must
   happen before removing joao's NOPASSWD sudo.
2. **Gateway MVP** — TLS (self-signed), login, sessions, CSRF, rate limit,
   owner UI for users and grants, portal menu from `service.toml`, reverse
   proxy with websockets, audit log.
3. **Service manager** — start/stop via systemd, off/on/auto, budgets and
   exclusive groups, "starting…" page, all-off at boot, stop-all on shutdown,
   profiles.
4. **camera-drive behind the gateway** — prefix-aware page, localhost bind,
   owner-only destructive actions.
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
| Storage | SanDisk 64 GB, ~4 GB used |
| Software | NM 1.52, systemd 257, polkit 126, nftables (no rules), dnsmasq-base, Python 3.13, ffmpeg 7.1 |

## Working on the Pi

- `ssh -i ~/.ssh/pi_camera_drive joao@192.168.1.206` — the laptop cannot
  resolve `cameradrive.local`. The Pi's WiFi can miss the first ARP; retry.
- `platform/deploy.sh --check` shows drift between the repo and the Pi without
  changing anything; run it before and after deploying.
- Non-login SSH PATH lacks `/usr/sbin` (`iw`, `NetworkManager`, `swapon`).
- `joao` currently has passwordless sudo.
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

From today: NM keyfiles must be mode 600; the duplicate netplan profile will
autoconnect on any free WiFi interface; memory cgroup off by default; the
Etherpad plugin marker; the camera's hardware H.264 encoder draws from the CMA
pool, so test `/api/mp4` before shrinking it.
