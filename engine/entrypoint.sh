#!/usr/bin/env bash
set -Eeuo pipefail

export RCLONE_CONFIG="${RCLONE_CONFIG:-/etc/rclone/rclone.conf}"
cmd="${1:-serve}"

# --- rclone 설정 헬퍼: 운영설정(REMOTE_NAME 등)·시크릿 없이도 동작 ---
# 새 서버에서 rclone.conf가 아직 없을 때, 이 이미지의 rclone으로 바로 설정한다.
case "$cmd" in
  rclone-config)
    mkdir -p "$(dirname "$RCLONE_CONFIG")"
    echo "[entrypoint] rclone config → $RCLONE_CONFIG"
    exec rclone config --config "$RCLONE_CONFIG" ;;
  rclone)
    shift
    exec rclone --config "$RCLONE_CONFIG" "$@" ;;
esac

# --- 운영 명령: 설정 seed + 저장소 URL 구성 ---
[ -f /config/config.env ]   || { mkdir -p /config; cp /opt/backup/config.env.example  /config/config.env; }
[ -f /config/excludes.txt ] || cp /opt/backup/excludes.txt.example /config/excludes.txt

# 단일 컨테이너: restic이 rclone을 stdio로 직접 실행. 자격증명은 rclone.conf(ro)에만.
# 활성 원격·하위 경로: 런타임에 UI로 변경 가능(/config/remote-name, /config/remote-path).
# 없으면 기본값(하위호환). REMOTE_NAME을 재export 해 엔진(os.Getenv)·UI가 오버라이드를 본다.
REMOTE="${REMOTE_NAME:?set REMOTE_NAME}"
if [ -s /config/remote-name ]; then
    REMOTE="$(head -n1 /config/remote-name | tr -d '[:space:]')"
fi
REPO_PATH="backups/${HOST_TAG:?set HOST_TAG}"
if [ -s /config/remote-path ]; then
    REPO_PATH="$(head -n1 /config/remote-path | tr -d '[:space:]')"
fi
export REMOTE_NAME="$REMOTE"
export RESTIC_REPOSITORY="rclone:${REMOTE}:${REPO_PATH}"
export RESTIC_PASSWORD_FILE="/secrets/repo-pass"
echo "[entrypoint] repo=${RESTIC_REPOSITORY}  rclone_config=${RCLONE_CONFIG}  init=${ALLOW_REPO_INIT:-false}"

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
