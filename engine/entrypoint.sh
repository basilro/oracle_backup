#!/usr/bin/env bash
set -Eeuo pipefail
# 운영설정 seed (없으면 예시 복사)
[ -f /config/config.env ]   || cp /opt/backup/config.env.example  /config/config.env
[ -f /config/excludes.txt ] || cp /opt/backup/excludes.txt.example /config/excludes.txt

# rclone REST 자격증명 -> repo URL (비로그)
if [ -f /secrets/rclone-rest.env ]; then set -a; . /secrets/rclone-rest.env; set +a; fi
export RESTIC_REPOSITORY="rest:http://${RCLONE_REST_USER:-}:${RCLONE_REST_PASS:-}@rclone:8080/"
export RESTIC_PASSWORD_FILE="/secrets/repo-pass"

mask() { sed -E 's#(rest:https?://)[^:@/]+:[^@/]+@#\1***:***@#g'; }
echo "[entrypoint] repo=$(echo "$RESTIC_REPOSITORY" | mask)  host=${HOST_TAG:-?}  init=${ALLOW_REPO_INIT:-false}"

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
