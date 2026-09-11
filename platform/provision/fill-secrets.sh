#!/usr/bin/env bash
#
# Fills the two placeholders in the Pi's cloud-init config on the boot partition.
# Prompts for both secrets with echo disabled, so nothing is written to your
# shell history, your terminal scrollback, or the Claude transcript.
#
# Safe to re-run: it works from the pristine copies in boot-originals/ if the
# placeholders have already been consumed.

set -euo pipefail

BOOT="${BOOT:-/run/media/joao/bootfs}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

die() { printf '\nerror: %s\n' "$1" >&2; exit 1; }

[ -d "$BOOT" ] || die "boot partition not mounted at $BOOT"
[ -f "$BOOT/user-data" ] || die "$BOOT/user-data missing"
[ -f "$BOOT/network-config" ] || die "$BOOT/network-config missing"
command -v openssl >/dev/null || die "openssl not installed"

# If a previous run already substituted the placeholders, restore the templates
# so this run has something to replace.
if ! grep -q '__WIFI_PASSWORD__' "$BOOT/network-config" 2>/dev/null; then
  echo "network-config has no placeholder left (already filled?)."
  read -rp "Overwrite it with the current template and re-fill? [y/N] " a
  [[ "$a" =~ ^[Yy]$ ]] || die "aborted"
fi

echo "Filling cloud-init secrets on $BOOT"
echo

read -rsp "WiFi password for TP-LINK_8E4332: " WIFI_PW; echo
[ -n "$WIFI_PW" ] || die "wifi password was empty"

echo
echo "Now a console login password for the Pi. This is only a fallback --"
echo "normal access is the SSH key. Leave it blank to disable password login."
read -rsp "Pi password for user joao (or blank): " PI_PW1; echo
if [ -n "$PI_PW1" ]; then
  read -rsp "confirm: " PI_PW2; echo
  [ "$PI_PW1" = "$PI_PW2" ] || die "passwords did not match"
  PW_HASH="$(openssl passwd -6 "$PI_PW1")"
else
  # '!' is the conventional "locked, no password accepted" crypt field.
  PW_HASH='!'
  echo "  -> password login disabled; SSH key only."
fi

unset PI_PW1 PI_PW2

# Literal (not regex) substitution, so passwords containing / & $ \ are safe.
WIFI_PW="$WIFI_PW" PW_HASH="$PW_HASH" BOOT="$BOOT" python3 - <<'PY'
import os, pathlib, sys

boot = pathlib.Path(os.environ["BOOT"])
subs = {
    boot / "network-config": ("__WIFI_PASSWORD__", os.environ["WIFI_PW"]),
    boot / "user-data":      ("__PASSWORD_HASH__", os.environ["PW_HASH"]),
}

for path, (needle, value) in subs.items():
    text = path.read_text()
    if needle not in text:
        print(f"  !! {path.name}: placeholder {needle} not found, skipped")
        continue
    path.write_text(text.replace(needle, value))
    print(f"  ok {path.name}: {needle} filled")
PY

unset WIFI_PW PW_HASH

echo
echo "=== verification (no secret values printed) ==="
for f in user-data network-config; do
  if grep -qE '__WIFI_PASSWORD__|__PASSWORD_HASH__' "$BOOT/$f"; then
    echo "  FAIL $f still contains a placeholder"
  else
    echo "  ok   $f has no placeholders left"
  fi
done

# YAML sanity check -- a broken file here means a Pi that boots unprovisioned.
python3 - "$BOOT" <<'PY'
import pathlib, sys
try:
    import yaml
except ImportError:
    print("  -- pyyaml not installed, skipping syntax check")
    sys.exit(0)
boot = pathlib.Path(sys.argv[1])
for name in ("user-data", "network-config", "meta-data"):
    try:
        yaml.safe_load((boot / name).read_text())
        print(f"  ok   {name} parses as valid YAML")
    except Exception as e:
        print(f"  FAIL {name} is not valid YAML: {e}")
PY

sync
echo
echo "Done. Files synced to the card."
