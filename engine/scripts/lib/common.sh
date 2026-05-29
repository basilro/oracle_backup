# shellcheck shell=bash
# 공통: 로깅(마스킹), 트랩, 락, 상태, 알림
LOG_DIR="${LOG_DIR:-/var/log/backup}"
STATE_DIR="${STATE_DIR:-/state}"
LOCK_FILE="${LOCK_FILE:-/state/backup.lock}"

# user:pass@ 형태 자격증명 마스킹
redact() { sed -E 's#(rest:https?://)[^:@/]+:[^@/]+@#\1***:***@#g'; }
log()  { printf '[%s] %s\n' "$(date -u +%FT%TZ)" "$*" | redact; }
warn() { log "WARN: $*" >&2; }
die()  { log "FATAL: $*" >&2; exit 1; }

setup_logging() {
    mkdir -p "$LOG_DIR" "$STATE_DIR"
    local ts; ts=$(date -u +%Y%m%dT%H%M%SZ)
    LOG_FILE="${LOG_FILE_OVERRIDE:-$LOG_DIR/backup-$ts.log}"
    exec > >(tee -a "$LOG_FILE" | redact) 2>&1
}

acquire_lock() {
    exec 9>"$LOCK_FILE"
    flock -n 9 || die "Another backup run is in progress (lock: $LOCK_FILE)"
}

record_success() { date -u +%FT%TZ > "$STATE_DIR/last-success"; rm -f "$STATE_DIR/last-failure"; }
record_failure() { date -u +%FT%TZ > "$STATE_DIR/last-failure"; echo "$1" >> "$STATE_DIR/last-failure"; }

webhook_url() { if [ -f /secrets/discord-webhook ]; then cat /secrets/discord-webhook; else echo "${BACKUP_ALERT_WEBHOOK:-}"; fi; }

notify() {  # $1=category $2=message  (자격증명/경로 미포함, 일반 카테고리만)
    local url; url=$(webhook_url); [ -n "$url" ] || return 0
    curl -fsS -X POST -H 'Content-Type: application/json' \
        -d "$(printf '{"text":"[backup:%s] %s on %s"}' "$1" "$2" "${HOST_TAG:-$(hostname)}")" \
        "$url" >/dev/null 2>&1 || warn "webhook notify failed"
}

on_error() {
    local ec=$? ln=${1:-?}
    record_failure "exit=$ec line=$ln"
    notify fail "run failed (exit=$ec)"
    exit "$ec"
}
install_error_trap() { trap 'on_error $LINENO' ERR; set -Eeuo pipefail; }
