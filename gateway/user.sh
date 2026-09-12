#!/usr/bin/env bash
#
# Manage gateway users from the laptop. The password is typed here with echo
# off and travels over SSH stdin, never as a command argument.
#
#   gateway/user.sh add NAME [--owner]   create a user; owners only from here
#   gateway/user.sh passwd NAME          set a password, ending their sessions
#   gateway/user.sh list
#
# The gateway service must have started once, which creates its database directory.

set -euo pipefail

PI="${PI:-joao@192.168.1.206}"
KEY="${KEY:-$HOME/.ssh/pi_camera_drive}"
SSH_OPTS=(-i "$KEY" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10)
REMOTE=(sudo -n -u pms-gateway /usr/local/bin/pms-gateway user)

die() { printf 'error: %s\n' "$1" >&2; exit 1; }

cmd="${1:-}"
case "$cmd" in
  list)       exec ssh "${SSH_OPTS[@]}" "$PI" "${REMOTE[@]}" list ;;
  add|passwd) ;;
  *)          sed -n '6,8p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2; exit 2 ;;
esac

# ssh joins its arguments into one remote shell command, so only let through
# names that mean nothing to a shell.
name="${2:-}"
[[ "$name" =~ ^[a-z0-9][a-z0-9._-]{0,31}$ ]] || die "username must be 1-32 of a-z 0-9 . _ -"
flags=()
case "${3:-}" in
  "")      ;;
  --owner) [ "$cmd" = add ] || die "--owner only applies to add"; flags=(-owner) ;;
  *)       die "unexpected argument: $3" ;;
esac

read -rsp "password for $name (10+ characters): " pw; echo
read -rsp "again: " pw2; echo
[ "$pw" = "$pw2" ] || die "passwords did not match"
unset pw2

printf '%s\n' "$pw" | ssh "${SSH_OPTS[@]}" "$PI" "${REMOTE[@]}" "$cmd" "${flags[@]}" "$name"
unset pw
