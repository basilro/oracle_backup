# --- Go 빌더 ---
FROM golang:1.22-alpine AS gobuild
WORKDIR /src
COPY web/go.mod ./
RUN go mod download 2>/dev/null || true
COPY web/ ./
RUN go mod tidy && CGO_ENABLED=0 go build -trimpath -o /backup-engine .

# --- restic 핀 (0.16.5) ---
FROM alpine:3.19 AS resticstage
ARG RESTIC_VER=0.16.5
ARG TARGETARCH=arm64
RUN apk add --no-cache curl bzip2 && \
    curl -fsSL "https://github.com/restic/restic/releases/download/v${RESTIC_VER}/restic_${RESTIC_VER}_linux_${TARGETARCH}.bz2" -o /tmp/r.bz2 && \
    bunzip2 /tmp/r.bz2 && install -m755 /tmp/r /usr/local/bin/restic && \
    restic version | grep -q "${RESTIC_VER}"

# --- 런타임 ---
FROM alpine:3.19
# tini는 compose `init: true`가 PID1로 주입하므로 이미지에서는 불필요(중복 경고 방지).
RUN apk add --no-cache bash docker-cli gzip curl wget tzdata coreutils util-linux findutils
COPY --from=resticstage /usr/local/bin/restic /usr/local/bin/restic
COPY --from=gobuild /backup-engine /opt/backup/backup-engine
COPY scripts/ /opt/backup/scripts/
COPY entrypoint.sh /opt/backup/entrypoint.sh
COPY config.env.example /opt/backup/config.env.example
COPY excludes.txt.example /opt/backup/excludes.txt.example
RUN chmod +x /opt/backup/entrypoint.sh /opt/backup/scripts/*.sh
ENV TZ=Asia/Seoul
ENTRYPOINT ["/opt/backup/entrypoint.sh"]
CMD ["serve"]
