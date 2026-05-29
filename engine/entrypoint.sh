#!/usr/bin/env bash
set -Eeuo pipefail
# 운영설정 seed (없으면 예시 복사)
[ -f /config/config.env ]   || cp /opt/backup/config.env.example  /config/config.env
[ -f /config/excludes.txt ] || cp /opt/backup/excludes.txt.example /config/excludes.txt

# 단일 컨테이너: restic이 rclone을 stdio로 직접 실행(별도 rclone 서비스 불필요).
# 자격증명은 rclone.conf(ro 마운트)에만 존재 — repo URL에는 시크릿 없음.
export RCLONE_CONFIG="${RCLONE_CONFIG:-/etc/rclone/rclone.conf}"
export RESTIC_REPOSITORY="rclone:${REMOTE_NAME:?set REMOTE_NAME}:backups/${HOST_TAG:?set HOST_TAG}"
export RESTIC_PASSWORD_FILE="/secrets/repo-pass"

echo "[entrypoint] repo=${RESTIC_REPOSITORY}  rclone_config=${RCLONE_CONFIG}  init=${ALLOW_REPO_INIT:-false}"

cmd="${1:-serve}"
case "$cmd" in
  serve)
    exec /opt/backup/backup-engine ;;
  init)
    if restic cat config >/dev/null 2>&1; then echo "repo already exists"; exit 0; fi
    [ "${ALLOW_REPO_INIT:-false}" = "true" ] || { echo "set ALLOW_REPO_INIT=true to init"; exit 1; }
    exec restic init ;;
  preflight)
    echo "checking restic repo reachability..."
    if restic cat config >/dev/null 2>&1; then echo "PREFLIGHT: PASS"; else echo "PREFLIGHT: FAIL"; exit 1; fi ;;
  backup)
    exec /opt/backup/scripts/home-backup.sh ;;
  *)
    exec "$@" ;;
esac
