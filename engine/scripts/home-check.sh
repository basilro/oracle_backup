#!/usr/bin/env bash
# home-check.sh — restic 저장소 무결성 검증 (주간 스케줄)
set -Eeuo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source /config/config.env
source "$HERE/lib/common.sh"; source "$HERE/lib/restic.sh"

setup_logging; install_error_trap; acquire_lock
log "=== integrity check start host=$HOST_TAG ==="
START=$(date +%s)
restic_ensure_repo
restic_check "${CHECK_SUBSET:-5%}"
ELAPSED=$(( $(date +%s) - START ))
log "=== check OK in ${ELAPSED}s ==="
notify ok "integrity check passed in ${ELAPSED}s"
