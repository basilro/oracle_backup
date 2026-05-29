#!/usr/bin/env bash
# home-restore.sh — 조회/복원 CLI (Go actions가 호출)
set -Eeuo pipefail
source /config/config.env
: "${STAGING_ROOT:=/home/docker/_backup_staging}"
RESTIC="${RESTIC_BIN:-restic}"
cmd="${1:-}"; [ -n "$cmd" ] || { echo "usage: $0 {snapshots|ls|restore|dbs}" >&2; exit 1; }
shift
case "$cmd" in
  snapshots) "$RESTIC" snapshots --json ;;
  ls)        "$RESTIC" ls "${1:?snap}" "${2:-/}" ;;
  restore)
    snap="${1:?snap}"; target="${2:?target}"; shift 2; mkdir -p "$target"
    args=(); for p in "$@"; do args+=(--include "$p"); done
    "$RESTIC" restore "$snap" --target "$target" "${args[@]}" ;;
  dbs)
    snap="${1:?snap}"; target="${2:?target}"; mkdir -p "$target"
    "$RESTIC" restore "$snap" --target "$target" --include "$STAGING_ROOT" ;;
  *) echo "unknown: $cmd" >&2; exit 1 ;;
esac
