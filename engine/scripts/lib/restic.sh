# shellcheck shell=bash
# restic 래퍼. RESTIC_REPOSITORY/RESTIC_PASSWORD_FILE 는 entrypoint가 env로 제공.
RESTIC_BIN="${RESTIC_BIN:-restic}"

# 저장소 존재 프로브: 0=존재, 2=명시적 없음, 1=기타오류(네트워크/인증 등)
# 안전: 네트워크/인증 실패를 먼저 1로 걸러내고, 좁은 "없음" 신호만 2로 본다.
# (그래야 일시적 장애를 "저장소 없음"으로 오인해 빈 저장소를 만들지 않는다.)
repo_probe() {
    local err; err=$("$RESTIC_BIN" cat config 2>&1 >/dev/null); local rc=$?
    [ $rc -eq 0 ] && return 0
    if printf '%s' "$err" | grep -qiE 'connection refused|dial tcp|no route to host|i/o timeout|timeout|wrong password|no key found|decrypt|certificate|tls|429|500|502|503|denied|forbidden|401|403'; then
        log "restic repo probe: unreachable/auth error: $(printf '%s' "$err" | redact | head -1)"
        return 1
    fi
    if printf '%s' "$err" | grep -qiE 'no such file or directory|404|repository does not exist|unable to open config file'; then
        return 2
    fi
    log "restic repo probe error (treated as unreachable): $(printf '%s' "$err" | redact | head -1)"
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
    # stdout(JSON summary)는 sumfile로, 진행/에러는 stderr->로그(마스킹).
    # rc를 명시적으로 캡처(이건 파이프가 아니라 단순 명령이므로 PIPESTATUS는 부적절).
    local rc=0
    "$RESTIC_BIN" backup --json \
        --exclude-file=/config/excludes.txt "${extra[@]}" \
        --limit-upload "${UPLOAD_LIMIT_KBPS:-0}" \
        --tag daily --tag auto --host "$HOST_TAG" \
        "${BACKUP_PATHS[@]}" \
        > >(tee "$sumfile") 2> >(redact >&2) || rc=$?
    # restic exit 3 = 일부 파일 읽기 실패(라이브 볼륨에서 흔함) → 스냅샷은 생성됨. 경고로 처리.
    if [ "$rc" -eq 3 ]; then warn "restic completed with partial read errors (rc=3)"; return 0; fi
    return "$rc"
}

# 강제종료(컨테이너 재시작/크래시)로 남은 stale 잠금 정리. plain `unlock`은 죽은
# 프로세스의 잠금만 제거하고 살아있는 잠금은 건드리지 않으므로, 정상 restic 작업이
# 동시에 돌아도 안전하다. 누수된 배타 잠금 때문에 forget+prune이 실패하는 것을 방지.
restic_unlock_stale() { log "clearing stale locks (if any)"; "$RESTIC_BIN" unlock || warn "unlock returned non-zero (continuing)"; }

restic_forget() { log "forget keep-daily=${KEEP_DAILY:-7}"; "$RESTIC_BIN" forget --keep-daily "${KEEP_DAILY:-7}" --tag auto --prune; }
restic_check()  { log "check subset=${1:-5%}"; "$RESTIC_BIN" check --read-data-subset="${1:-5%}"; }
