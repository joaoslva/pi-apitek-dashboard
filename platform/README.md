# platform

Everything about the Pi itself, as opposed to any one service. Hardware
specifics belong here, so moving to another board means changing this folder.

- `rootfs/` — files mirrored onto the Pi's `/`: firewall, gateway user and its
  polkit rules, cloud-init off, network fallback
- `provision/` — getting a fresh SD card to a reachable Pi, then `base.sh`
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
`0644` otherwise. Two components shipping the same path is an error.

Every directory above a shipped file must be owned by root and not writable
by group or others; `--check` reports `unsafe dir`, an install fixes it. (An
earlier deploy had left `/`, `/etc` and `/usr` owned by `joao`, mode 775.)

After installing, only what changed is reloaded:

| Changed | Action |
|---|---|
| `/etc/systemd/` | `daemon-reload`; units are not restarted |
| `/etc/udev/` | reload rules, replay block `add` events |
| `/etc/sysusers.d/` | `systemd-sysusers` |
| `/etc/nftables.conf` | parsed with `nft -c` before anything is installed; if the firewall is running it is reloaded behind a 2-minute timer that removes table `inet pms`, and the timer is cancelled from a new SSH connection |

## provision/

`base.sh` — setup that is state rather than files. Run it after the first
deploy onto a new card; it is safe to re-run:

```bash
ssh -i ~/.ssh/pi_camera_drive joao@192.168.1.206 sudo bash -s < platform/provision/base.sh
```

It creates the gateway user, deletes cloud-init's netplan WiFi profile, sets
the hostname (`pocketserver`, or the first argument), disables ModemManager
and bluetooth, removes the recovery leftovers (`netreport.service`, which held
boot for ~50 s), keeps one of the three identical NOPASSWD sudo files, locks
the admin's empty password, enables nftables, adds `cgroup_enable=memory` to
`cmdline.txt` and lists anything in system paths not owned by root. Reboot
afterwards if it says so.

From the first bring-up of camera-drive, kept because they still work:

- `fix-pi-card.sh` — writes the user, SSH key, WiFi profile and
  `netreport.service` straight onto the root partition from a laptop,
  bypassing cloud-init. The recovery path for a Pi that will not come up;
  run `base.sh` once the Pi is back.
- `diagnose-pi-card.sh` — read-only version of the above.
- `fill-secrets.sh`, `boot-originals/` — the cloud-init route, which failed
  (see "cloud-init's NoCloud datasource" in `services/camera-drive/README.md`).
- `setup-ap.sh` — creates the `camera-drive-ap` hotspot profile that
  `camera-net-fallback` switches to. Replaced by the Island/Join network modes
  in Phase 6.
