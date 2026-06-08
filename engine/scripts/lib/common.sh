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
    # redact BEFORE tee so the persisted file never contains raw credentials
    exec > >(redact | tee -a "$LOG_FILE") 2>&1
}

acquire_lock() {
    exec 9>"$LOCK_FILE"
    flock -n 9 || die "Another backup run is in progress (lock: $LOCK_FILE)"
}

record_success() { date -u +%FT%TZ > "$STATE_DIR/last-success"; rm -f "$STATE_DIR/last-failure"; }
record_failure() { date -u +%FT%TZ > "$STATE_DIR/last-failure"; echo "$1" >> "$STATE_DIR/last-failure"; }

webhook_url() { if [ -f /secrets/discord-webhook ]; then cat /secrets/discord-webhook; else echo "${BACKUP_ALERT_WEBHOOK:-}"; fi; }

notify() {  # $1=category(ok|fail) $2=message  (자격증명/경로 미포함, 일반 카테고리만)
    local url; url=$(webhook_url); [ -n "$url" ] || return 0
    local label; case "$1" in ok) label="성공";; fail) label="실패";; *) label="$1";; esac
    # Discord webhooks require "content"; Slack-compat ignores extra keys.
    curl -fsS -X POST -H 'Content-Type: application/json' \
        -d "$(printf '{"content":"[백업:%s] %s (호스트: %s)","text":"[백업:%s] %s"}' "$label" "$2" "${HOST_TAG:-$(hostname)}" "$label" "$2")" \
        "$url" >/dev/null 2>&1 || warn "webhook 알림 전송 실패"
}

# on_error: $1=line, $2=exit code (passed explicitly so a cleanup step in the
# trap cannot clobber $? before we read it).
on_error() {
    local ln="${1:-?}" ec="${2:-$?}"
    record_failure "exit=$ec line=$ln"
    notify fail "실행 실패 (종료코드=$ec, 줄=$ln)"
    exit "$ec"
}
install_error_trap() { trap 'on_error "$LINENO" "$?"' ERR; set -Eeuo pipefail; }
