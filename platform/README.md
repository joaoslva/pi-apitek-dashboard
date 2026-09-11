# platform

Everything about the Pi itself, as opposed to any one service. Hardware
specifics belong here, so moving to another board means changing this folder.

- `rootfs/` — files mirrored onto the Pi's `/` (network, and from Phase 1
  firewall, polkit, boot settings)
- `provision/` — getting a fresh SD card to a reachable Pi
- `deploy.sh` — pushes `platform/rootfs/` and every `services/*/rootfs/`

## Deploying

```bash
platform/deploy.sh --list                # what each component ships
platform/deploy.sh --check               # what differs on the Pi, changes nothing
platform/deploy.sh                       # install everything
platform/deploy.sh camera-drive          # install one component
PI=joao@10.42.0.1 platform/deploy.sh     # another address
```

Files are installed as `root:root`, mode `0755` if executable in the repo,
`0644` otherwise. Existing directories are never touched. Two components
shipping the same path is an error. Units are reloaded, not restarted.

## provision/

From the first bring-up of camera-drive, kept because they still work:

- `fix-pi-card.sh` — writes the user, SSH key, WiFi profile and
  `netreport.service` straight onto the root partition from a laptop,
  bypassing cloud-init. The recovery path for a Pi that will not come up.
- `diagnose-pi-card.sh` — read-only version of the above.
- `fill-secrets.sh`, `boot-originals/` — the cloud-init route, which failed
  (see "cloud-init's NoCloud datasource" in `services/camera-drive/README.md`).
- `setup-ap.sh` — creates the `camera-drive-ap` hotspot profile that
  `camera-net-fallback` switches to. Replaced by the Island/Join network modes
  in Phase 6.
