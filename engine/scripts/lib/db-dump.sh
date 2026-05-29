# shellcheck shell=bash
# DB 덤프. 컨테이너 상태(docker inspect)로 판정. 요약은 $DB_SUMMARY_FILE 에 JSON 누적.
DYNAMIC_EXCLUDES=()
_db_first=1
_db_opened=0
_db_closed=0
db_summary_add() {  # $1=name $2=state $3=detail
    [ "$_db_first" = 1 ] && _db_first=0 || printf ',' >> "$DB_SUMMARY_FILE"
    printf '"%s":{"state":"%s","detail":"%s"}' "$1" "$2" "$3" >> "$DB_SUMMARY_FILE"
}
db_summary_open()  { mkdir -p "$(dirname "$DB_SUMMARY_FILE")"; _db_first=1; _db_opened=1; _db_closed=0; printf '{' > "$DB_SUMMARY_FILE"; }
db_summary_close() { [ "$_db_opened" = 1 ] && [ "$_db_closed" = 0 ] || return 0; printf '}' >> "$DB_SUMMARY_FILE"; _db_closed=1; }
# finalize: close the JSON object even if a dump die()d mid-way (idempotent).
db_summary_finalize() { db_summary_close 2>/dev/null || true; }

container_running() { [ "$(docker inspect --format '{{.State.Running}}' "$1" 2>/dev/null)" = "true" ]; }
container_exists()  { docker inspect "$1" >/dev/null 2>&1; }

dump_postgres() {
    local c="${PG_CONTAINER:-postgres}"
    [ -n "$c" ] || { return 0; }
    container_exists "$c" || { db_summary_add postgres ABSENT "no container"; return 0; }
    if ! container_running "$c"; then db_summary_add postgres SKIPPED_STOPPED "raw data backed up"; return 0; fi
    local user="${PG_USER:-postgres}" out="$STAGE/postgres"; mkdir -p "$out"
    log "dump postgres c=$c user=$user"
    if docker exec -u postgres "$c" pg_dumpall -U "$user" | gzip -1 > "$out/all.sql.gz"; then
        local sz; sz=$(stat -c%s "$out/all.sql.gz")
        [ "$sz" -gt 1000 ] || { db_summary_add postgres DUMP_FAILED "too small ($sz)"; die "postgres dump too small"; }
        db_summary_add postgres DUMPED_OK "${sz}B"; DYNAMIC_EXCLUDES+=("/home/docker/postgres/data")
    else db_summary_add postgres DUMP_FAILED "pg_dumpall error"; die "postgres dump failed"; fi
}

dump_mongodb() {
    local c="${MONGO_CONTAINER:-mongodb}"
    [ -n "$c" ] || { return 0; }
    container_exists "$c" || { db_summary_add mongodb ABSENT "no container"; return 0; }
    if ! container_running "$c"; then db_summary_add mongodb SKIPPED_STOPPED "raw data backed up"; return 0; fi
    local out="$STAGE/mongodb"; mkdir -p "$out"; local a=()
    [ -n "${MONGO_USER:-}" ] && [ -n "${MONGO_PASS:-}" ] && a=(-u "$MONGO_USER" -p "$MONGO_PASS" --authenticationDatabase admin)
    log "dump mongodb c=$c"
    if docker exec "$c" sh -c "rm -f /tmp/mongo.archive" \
       && docker exec "$c" mongodump --quiet "${a[@]}" --gzip --archive=/tmp/mongo.archive \
       && docker cp "$c:/tmp/mongo.archive" "$out/mongo.archive"; then
        docker exec "$c" rm -f /tmp/mongo.archive || true
        local sz; sz=$(stat -c%s "$out/mongo.archive")
        [ "$sz" -gt 100 ] || { db_summary_add mongodb DUMP_FAILED "too small ($sz)"; die "mongo dump too small"; }
        db_summary_add mongodb DUMPED_OK "${sz}B"; DYNAMIC_EXCLUDES+=("/home/docker/mongodb")
    else db_summary_add mongodb DUMP_FAILED "mongodump error"; die "mongodb dump failed"; fi
}

dump_redis() {
    local c="${REDIS_CONTAINER:-redis}"
    [ -n "$c" ] || { return 0; }
    container_exists "$c" || { db_summary_add redis ABSENT "no container"; return 0; }
    if ! container_running "$c"; then db_summary_add redis SKIPPED_STOPPED "raw data backed up"; return 0; fi
    local out="$STAGE/redis"; mkdir -p "$out"; local a=()
    [ -n "${REDIS_PASS:-}" ] && a=(-a "$REDIS_PASS" --no-auth-warning)
    local dp="${REDIS_DUMP_PATH:-/data/dump.rdb}"
    log "dump redis c=$c"
    local before; before=$(docker exec "$c" redis-cli "${a[@]}" LASTSAVE) || { db_summary_add redis DUMP_FAILED "auth/conn"; die "redis conn"; }
    docker exec "$c" redis-cli "${a[@]}" BGSAVE >/dev/null
    local w=0; while [ "$w" -lt 60 ]; do sleep 2; w=$((w+2)); [ "$(docker exec "$c" redis-cli "${a[@]}" LASTSAVE)" != "$before" ] && break; done
    [ "$w" -lt 60 ] || { db_summary_add redis DUMP_FAILED "BGSAVE timeout"; die "redis BGSAVE timeout"; }
    docker cp "$c:$dp" "$out/dump.rdb"
    db_summary_add redis DUMPED_OK "$(stat -c%s "$out/dump.rdb")B"; DYNAMIC_EXCLUDES+=("/home/docker/redis")
}
