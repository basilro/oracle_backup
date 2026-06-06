# --- Go 빌더 (BUILDPLATFORM에서 네이티브 실행 → TARGET arch로 크로스컴파일) ---
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS gobuild
WORKDIR /src
ARG TARGETOS
ARG TARGETARCH
COPY web/go.mod ./
RUN go mod download 2>/dev/null || true
COPY web/ ./
RUN go mod tidy && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-arm64} go build -trimpath -o /backup-engine .

# --- rclone 바이너리 (핀: 1.66) ---
FROM rclone/rclone:1.66 AS rclonebin

# --- restic 핀 (0.16.5) ---
# BUILDPLATFORM(네이티브)에서 받기만 한다: TARGETARCH 바이너리를 다운로드/해제만 하고
# 실행 검증은 하지 않음(크로스아치 바이너리를 QEMU로 실행하면 크래시). 무결성은 공식
# 릴리스 + 버전 핀 URL로 보장하고, 실제 실행은 런타임(타깃 아치, 네이티브)에서 이뤄짐.
FROM --platform=$BUILDPLATFORM alpine:3.19 AS resticstage
ARG RESTIC_VER=0.16.5
ARG TARGETARCH
RUN apk add --no-cache curl bzip2 && \
    arch="${TARGETARCH:-arm64}" && \
    curl -fsSL "https://github.com/restic/restic/releases/download/v${RESTIC_VER}/restic_${RESTIC_VER}_linux_${arch}.bz2" -o /tmp/r.bz2 && \
    bunzip2 /tmp/r.bz2 && install -m755 /tmp/r /usr/local/bin/restic && \
    test -s /usr/local/bin/restic

# --- 런타임 ---
FROM alpine:3.19
# tini는 compose `init: true`가 PID1로 주입하므로 이미지에서는 불필요(중복 경고 방지).
RUN apk add --no-cache bash docker-cli gzip curl wget tzdata coreutils util-linux findutils ca-certificates fuse3
COPY --from=resticstage /usr/local/bin/restic /usr/local/bin/restic
COPY --from=rclonebin /usr/local/bin/rclone /usr/local/bin/rclone
COPY --from=gobuild /backup-engine /opt/backup/backup-engine
COPY scripts/ /opt/backup/scripts/
COPY entrypoint.sh /opt/backup/entrypoint.sh
COPY config.env.example /opt/backup/config.env.example
COPY excludes.txt.example /opt/backup/excludes.txt.example
RUN chmod +x /opt/backup/entrypoint.sh /opt/backup/scripts/*.sh
ENV TZ=Asia/Seoul
ENTRYPOINT ["/opt/backup/entrypoint.sh"]
CMD ["serve"]
