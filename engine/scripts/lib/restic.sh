# shellcheck shell=bash
# restic 래퍼. RESTIC_REPOSITORY/RESTIC_PASSWORD_FILE 는 entrypoint가 env로 제공.
RESTIC_BIN="${RESTIC_BIN:-restic}"

# 저장소 존재 프로브: 0=존재, 2=없음(명시), 1=기타오류
repo_probe() {
    local err; err=$("$RESTIC_BIN" cat config 2>&1 >/dev/null); local rc=$?
    [ $rc -eq 0 ] && return 0
    if printf '%s' "$err" | grep -qiE 'repository does not exist|no key found|unable to open config|does not exist'; then
        return 2
    fi
    log "restic repo probe error: $(printf '%s' "$err" | redact | head -1)"
    return 1
}

restic_ensure_repo() {
    repo_probe; local p=$?
    if [ $p -eq 0 ]; then log "restic repo exists"; return 0; fi
    if [ $p -eq 2 ]; then
        if [ "${ALLOW_REPO_INIT:-false}" = "true" ]; then
            log "repo absent + ALLOW_REPO_INIT=true -> init"; "$RESTIC_BIN" init; return 0
        fi
        die "repo absent and ALLOW_REPO_INIT=false (refusing to init)"
    fi
    die "repo unreachable (network/auth) - refusing to back up"
}

restic_backup() {  # $1=summary-out-file
    local sumfile="$1"; local extra=()
    if [ "${#DYNAMIC_EXCLUDES[@]}" -gt 0 ]; then
        for ex in "${DYNAMIC_EXCLUDES[@]}"; do extra+=(--exclude "$ex"); done
    fi
    log "restic backup (limit=${UPLOAD_LIMIT_KBPS:-0}KiB/s, dyn-excludes=${#DYNAMIC_EXCLUDES[@]})"
    # stdout(JSON summary)만 sumfile로, 진행/에러는 stderr->로그. PIPESTATUS로 실제 종료코드.
    "$RESTIC_BIN" backup --json \
        --exclude-file=/config/excludes.txt "${extra[@]}" \
        --limit-upload "${UPLOAD_LIMIT_KBPS:-0}" \
        --tag daily --tag auto --host "$HOST_TAG" \
        "${BACKUP_PATHS[@]}" \
        > >(tee "$sumfile") 2> >(redact >&2)
    return "${PIPESTATUS[0]}"
}

restic_forget() { log "forget keep-daily=${KEEP_DAILY:-7}"; "$RESTIC_BIN" forget --keep-daily "${KEEP_DAILY:-7}" --tag auto --prune; }
restic_check()  { log "check subset=${1:-5%}"; "$RESTIC_BIN" check --read-data-subset="${1:-5%}"; }
