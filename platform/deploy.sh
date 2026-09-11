#!/usr/bin/env bash
#
# Push files onto the Raspberry Pi. Each component owns a rootfs/ tree that
# mirrors the Pi's /:
#
#   platform/rootfs/usr/local/bin/foo        ->  /usr/local/bin/foo
#   services/<name>/rootfs/etc/udev/...      ->  /etc/udev/...
#
# Usage:
#   platform/deploy.sh [--list|--check] [--no-reload] [component ...]
#
#   component    "platform" or a services/ folder name; default: all of them
#   --list       print what each component ships, touch nothing
#   --check      show what differs on the Pi, change nothing
#   --no-reload  install without reloading systemd and udev
#
#   PI=joao@10.42.0.1 platform/deploy.sh

set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PI="${PI:-joao@192.168.1.206}"
KEY="${KEY:-$HOME/.ssh/pi_camera_drive}"
SSH_OPTS=(-i "$KEY" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10)

die() { printf 'error: %b\n' "$1" >&2; exit 1; }

MODE=install
RELOAD=1
components=()
for arg in "$@"; do
  case "$arg" in
    --list)      MODE=list ;;
    --check)     MODE=check ;;
    --no-reload) RELOAD=0 ;;
    -h|--help)   sed -n '2,17p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*)          die "unknown option $arg" ;;
    *)           components+=("$arg") ;;
  esac
done

rootfs_of() {
  if [ "$1" = platform ]; then echo "$REPO/platform/rootfs"; else echo "$REPO/services/$1/rootfs"; fi
}

# Paths relative to a rootfs, as they will appear on the Pi.
files_in() {
  (cd "$1" && find . -name __pycache__ -prune -o -type f -print | sed 's|^\.||' | sort)
}

if [ ${#components[@]} -eq 0 ]; then
  components=(platform)
  for d in "$REPO"/services/*/rootfs; do
    [ -d "$d" ] && components+=("$(basename "$(dirname "$d")")")
  done
fi

roots=()
for c in "${components[@]}"; do
  r="$(rootfs_of "$c")"
  [ -d "$r" ] || die "component '$c' has no rootfs ($r)"
  roots+=("$r")
done

# Only regular files: the install step below does not handle symlinks,
# sockets or anything else, so refuse them rather than half-deploy.
odd="$(find "${roots[@]}" -name __pycache__ -prune -o ! -type f ! -type d -print)"
[ -z "$odd" ] || die "only regular files can be deployed:\n$odd"

# A path shipped by two components would be silently won by whichever copied last.
dups="$(for r in "${roots[@]}"; do files_in "$r"; done | sort | uniq -d)"
[ -z "$dups" ] || die "paths shipped by more than one component:\n$dups"

if [ "$MODE" = list ]; then
  for i in "${!components[@]}"; do
    echo "== ${components[$i]}"
    files_in "${roots[$i]}" | sed 's/^/  /'
  done
  exit 0
fi

echo "==> target: $PI ($MODE: ${components[*]})"
STAGE="$(ssh "${SSH_OPTS[@]}" "$PI" mktemp -d /tmp/pms-deploy.XXXXXX)" || die "cannot reach $PI"
trap 'ssh "${SSH_OPTS[@]}" "$PI" rm -rf "$STAGE" || true' EXIT

srcs=()
for r in "${roots[@]}"; do srcs+=("$r/"); done
rsync -rlpt --exclude=__pycache__ -e "ssh $(printf '%q ' "${SSH_OPTS[@]}")" "${srcs[@]}" "$PI:$STAGE/"

ssh "${SSH_OPTS[@]}" "$PI" sudo -n bash -s -- "$STAGE" "$MODE" "$RELOAD" <<'REMOTE'
set -euo pipefail
S=$1 MODE=$2 RELOAD=$3

changed=()
while IFS= read -r -d '' f; do
  dest="${f#"$S"}"
  if [ -x "$f" ]; then want=755; else want=644; fi

  if [ ! -e "$dest" ]; then
    why=new
  elif ! cmp -s "$f" "$dest"; then
    why=content
  elif [ "$(stat -c '%a %U:%G' "$dest")" != "$want root:root" ]; then
    why="was $(stat -c '%a %U:%G' "$dest")"
  else
    continue
  fi

  if [ "$MODE" = check ]; then
    printf '  differs    %-52s %s\n' "$dest" "$why"
  else
    # install -D creates missing parents (root, 0755) and leaves existing
    # directories alone; it replaces the file rather than writing into it,
    # so a running binary is safe to update.
    install -D -o root -g root -m "$want" "$f" "$dest"
    printf '  installed  %-52s %s\n' "$dest" "$why"
  fi
  changed+=("$dest")
done < <(find "$S" -type f -print0 | sort -z)

[ ${#changed[@]} -gt 0 ] || { echo "  everything up to date"; exit 0; }
[ "$MODE" = install ] && [ "$RELOAD" = 1 ] || exit 0

if printf '%s\n' "${changed[@]}" | grep -q '^/etc/systemd/'; then
  systemctl daemon-reload
  echo "==> systemd reloaded (changed units are not restarted)"
fi
if printf '%s\n' "${changed[@]}" | grep -q '^/etc/udev/'; then
  udevadm control --reload
  # Re-run add rules for devices already plugged in, e.g. a camera in storage mode.
  udevadm trigger --subsystem-match=block --action=add >/dev/null 2>&1 || true
  echo "==> udev rules reloaded"
fi
REMOTE

echo "==> done"
