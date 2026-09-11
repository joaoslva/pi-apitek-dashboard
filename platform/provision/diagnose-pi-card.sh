#!/usr/bin/env bash
#
# Read-only diagnosis of why cloud-init did not provision the Pi.
# Mounts the root partition ro, dumps everything relevant, unmounts.
# Run with: sudo platform/provision/diagnose-pi-card.sh

set -uo pipefail

ROOT_DEV="${ROOT_DEV:-/dev/sda2}"
BOOT="${BOOT:-/run/media/joao/bootfs}"
MP=/mnt/pi-diag

hdr() { printf '\n########## %s\n' "$1"; }

[ "$(id -u)" -eq 0 ] || { echo "run me with sudo"; exit 1; }

hdr "boot partition: were the secrets actually filled in?"
if [ -d "$BOOT" ]; then
  for f in user-data network-config; do
    if grep -qE '__WIFI_PASSWORD__|__PASSWORD_HASH__' "$BOOT/$f" 2>/dev/null; then
      echo "  !! $f STILL HAS A PLACEHOLDER (fill-secrets.sh did not run)"
    else
      echo "  ok $f has no placeholders"
    fi
  done
  echo "  --- instance_id we set ---"
  grep -E '^instance_id' "$BOOT/meta-data" 2>/dev/null
  echo "  --- ssid line (password not printed) ---"
  grep -E '^\s+"' "$BOOT/network-config" 2>/dev/null | grep -v password
else
  echo "  boot partition not mounted at $BOOT (that is fine, card may be in the Pi)"
fi

mkdir -p "$MP"
mountpoint -q "$MP" && umount "$MP"
if ! mount -o ro "$ROOT_DEV" "$MP" 2>&1; then
  echo "could not mount $ROOT_DEV -- is the card in the laptop?"
  exit 1
fi

hdr "is cloud-init explicitly disabled?"
if [ -e "$MP/etc/cloud/cloud-init.disabled" ]; then
  echo "  >>> FOUND /etc/cloud/cloud-init.disabled  <-- THIS ALONE STOPS EVERYTHING"
else
  echo "  no cloud-init.disabled flag"
fi

hdr "/etc/cloud/cloud.cfg.d/ (where the seed path would be configured)"
ls -la "$MP/etc/cloud/cloud.cfg.d/" 2>&1
for f in "$MP"/etc/cloud/cloud.cfg.d/*; do
  [ -f "$f" ] || continue
  echo "--- $(basename "$f") ---"
  grep -vE '^\s*#|^\s*$' "$f" | head -25
done

hdr "datasource_list / seedfrom anywhere in /etc/cloud"
grep -rniE 'datasource_list|seedfrom|nocloud' "$MP/etc/cloud/" 2>/dev/null | grep -v '^\s*#' | head -20 || echo "  nothing"

hdr "cloud-init systemd units: enabled, disabled or masked?"
for u in cloud-init-local cloud-init cloud-config cloud-final cloud-init-main; do
  found=""
  for d in multi-user.target.wants cloud-init.target.wants sysinit.target.wants; do
    [ -e "$MP/etc/systemd/system/$d/$u.service" ] && found="$found enabled:$d"
  done
  [ -L "$MP/etc/systemd/system/$u.service" ] && found="$found MASKED->$(readlink "$MP/etc/systemd/system/$u.service")"
  printf '  %-18s %s\n' "$u" "${found:-not enabled}"
done
echo "  --- cloud-init.target wants ---"
ls -la "$MP/etc/systemd/system/cloud-init.target.wants/" 2>&1 | head

hdr "did cloud-init run THIS boot? (dates should be today if so)"
ls -la "$MP/var/lib/cloud/" 2>&1
echo "--- instances ---"; ls -la "$MP/var/lib/cloud/instances/" 2>&1
echo "--- cached instance-id ---"; cat "$MP/var/lib/cloud/data/instance-id" 2>&1
echo "--- result.json ---"; cat "$MP/var/lib/cloud/data/result.json" 2>&1
echo "--- seed dir ---"; ls -la "$MP/var/lib/cloud/seed/" 2>&1

hdr "cloud-init logs"
if [ -f "$MP/var/log/cloud-init.log" ]; then
  echo "  size: $(stat -c%s "$MP/var/log/cloud-init.log") bytes, mtime: $(stat -c%y "$MP/var/log/cloud-init.log")"
  echo "--- datasource / ds-identify lines ---"
  grep -iE 'ds-identify|datasource|no instance data|seed' "$MP/var/log/cloud-init.log" 2>/dev/null | tail -25
  echo "--- errors/warnings ---"
  grep -iE 'error|warn|traceback|fail' "$MP/var/log/cloud-init.log" 2>/dev/null | tail -25
  echo "--- last 20 lines ---"
  tail -20 "$MP/var/log/cloud-init.log"
else
  echo "  >>> NO /var/log/cloud-init.log AT ALL -- cloud-init never executed"
fi

hdr "did our user get created?"
grep -E '^joao:' "$MP/etc/passwd" 2>&1 || echo "  no joao in /etc/passwd"
ls -la "$MP/home/" 2>&1
echo "--- provisioning breadcrumb ---"
cat "$MP/srv/camera-drive/.provisioned" 2>&1 || echo "  breadcrumb absent"

hdr "did any wifi config get rendered?"
ls -la "$MP/etc/NetworkManager/system-connections/" 2>&1
ls -la "$MP/etc/netplan/" 2>&1

hdr "hostname"
cat "$MP/etc/hostname" 2>&1

umount "$MP" && echo && echo "unmounted cleanly."
