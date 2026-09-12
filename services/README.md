# services

One folder per service. Each has:

- `service.toml` — what the gateway needs to route to, guard, start and stop it
- `rootfs/` — files mirrored onto the Pi's `/` by `platform/deploy.sh`
  (units, binaries, udev rules). Optional: a service built on the laptop may
  ship its files another way.
- `README.md` — how the service works and what it cost to learn

## Rules that are not per service

- Every service is **off at boot**, whatever state it was in before.
- Shutting down stops every active service cleanly first.
- A service binds `127.0.0.1` only. The gateway is the only way in.

## `service.toml`

```toml
id          = "camera-drive"      # must equal the folder name
title       = "Camera"            # shown in the portal menu
description = "One line under the title"
icon        = "camera"            # name from the gateway's embedded icon set

[route]
port     = 8444                     # public HTTPS port, 8443-8450 (the firewall opens that range)
upstream = "http://127.0.0.1:8081"  # loopback only, no path

[run]
units         = ["camera-drive-web.service"]  # started in order, stopped in reverse
ready_path    = "/api/state"   # upstream path; 2xx/3xx means ready
start_timeout = "30s"          # give up and stop the units after this
auto          = true           # may start on the first authorised visit
idle_stop     = "20m"          # with auto: stop after no requests and no open websockets for this long
exclusive     = ["foreground"] # never runs while another service in a shared group runs

[memory]
budget = "60M"    # expected peak; admission refuses a start that does not fit
max    = "150M"   # systemd MemoryMax= (needs cgroup_enable=memory)

[access]
level = "use"     # minimum level for anything on this service

[[access.rules]]
path    = "/api/wipe"   # this path and everything below it
methods = ["POST"]      # optional; omitted means every method
level   = "admin"
```

Sizes use systemd suffixes (`K`, `M`, `G`, binary). Durations use Go syntax
(`90s`, `20m`, `2h`).

### Access levels

`use` < `admin` < `owner`, plus `deny`.

- A user's grant for a service is `use` or `admin`. The owner holds every level
  on every service. `deny` matches nobody, including the owner.
- A user without a grant does not see the service in the menu, and gets a 404
  for every path on its port.
- The gateway rejects ambiguous request paths before matching (encoded `/`,
  `\` or NUL, `.` or `..` segments, repeated slashes).
- Rules match whole segments: `/api/wipe` covers `/api/wipe/x` but not
  `/api/wipes`. Matching ignores case, because some upstreams route `/ADMIN`
  like `/admin` (Express does). The longest matching rule wins, a rule with
  `methods` beats one without at the same path, and `[access] level` applies
  when none match. `HEAD` counts as `GET`.
- Websocket upgrades are checked like any other request, and must come from
  the service's own origin, as must every non-GET request.

### What the upstream sees

- The request as the browser sent it, at the root of the service: no prefix.
- `Host` as the browser sent it; `X-Forwarded-For`, `-Host`, `-Proto`.
- `X-Pms-User` (username) and `X-Pms-Level` (`use`, `admin` or `owner`), set by
  the gateway; anything the client sent under `X-Pms-` is removed.
- Never the gateway's session cookie, and any `Set-Cookie` for it in the
  response is dropped. Other cookies pass through — and browsers share cookies
  between ports of one host, so services see each other's cookies.

### Exclusive groups

The service manager refuses to start a service while another service that
shares one of its `exclusive` groups is running. Stop the other one first.
Camera and Etherpad share `foreground`, so they never run together unless a
manifest is changed.
