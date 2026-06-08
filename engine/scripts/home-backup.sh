#!/usr/bin/env bash
# home-backup.sh — 컨테이너 백업 오케스트레이터
set -Eeuo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source /config/config.env
[ -f /secrets/db-creds.env ] && { set -a; source /secrets/db-creds.env; set +a; }
source "$HERE/lib/common.sh"; source "$HERE/lib/db-dump.sh"; source "$HERE/lib/restic.sh"

: "${STAGING_ROOT:=/home/docker/_backup_staging}"
BACKUP_PATHS=(/home /var/lib/docker/volumes)
SUMMARY_OUT="${SUMMARY_OUT:-/state/last-summary.json}"
DB_SUMMARY_FILE="${DB_SUMMARY_FILE:-/state/last-db-summary.json}"

setup_logging; install_error_trap; acquire_lock
log "=== backup start host=$HOST_TAG ==="
START=$(date +%s)
STAGE="$STAGING_ROOT/$(date -u +%Y%m%dT%H%M%SZ)"

# Guard against catastrophic wipe before any rm -rf touches STAGING_ROOT.
case "$STAGING_ROOT" in
  /|/home|/var|/etc|/root|""|*..*) die "unsafe STAGING_ROOT: $STAGING_ROOT" ;;
esac
cleanup_staging() { rm -rf "${STAGING_ROOT:?}"/* 2>/dev/null || true; }
# Capture the real exit code in the ERR trap BEFORE cleanup_staging clobbers $?.
trap 'ec=$?; db_summary_finalize; cleanup_staging; on_error "$LINENO" "$ec"' ERR
trap 'db_summary_finalize; cleanup_staging' EXIT
cleanup_staging                       # Phase0: 시작 시 잔여 정리(중단된 이전 런 대비)
mkdir -p "$STAGE"

# 사전 연결 확인 (init 안 함)
restic_ensure_repo

# 이전 실행이 강제종료되어 남긴 stale 잠금 정리(없으면 무동작). forget의 배타 잠금 실패 예방.
restic_unlock_stale

# 여유 공간 (staging)
FREE_MB=$(df -Pm "$STAGING_ROOT" | awk 'NR==2{print $4}')
[ "${FREE_MB:-0}" -ge "${DB_DUMP_MIN_FREE_MB:-5000}" ] || { notify fail "디스크 공간 부족 ${FREE_MB}MB"; die "low free space ${FREE_MB}MB"; }

# Phase1: DB 덤프
if [ "${DB_BACKUP_ENABLED:-true}" = "true" ]; then
    log "--- Phase1 DB dumps ---"; db_summary_open
    dump_postgres; dump_mongodb; dump_redis
    db_summary_close
else log "DB backup disabled"; echo 'null' > "$DB_SUMMARY_FILE"; fi

# Phase2: restic backup
log "--- Phase2 restic backup ---"
restic_backup "$SUMMARY_OUT"

# Phase3: forget+prune
log "--- Phase3 forget+prune ---"; restic_forget

ELAPSED=$(( $(date +%s) - START ))
log "=== backup OK in ${ELAPSED}s ==="
record_success; notify ok "백업 완료 (${ELAPSED}초)"
