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

dump_postgres() {  # $1=name $2=container $3=data
    local name="${1:-postgres}" c="${2:-postgres}" data="${3:-}"
    [ -n "$c" ] || { return 0; }
    container_exists "$c" || { db_summary_add "$name" ABSENT "no container"; return 0; }
    if ! container_running "$c"; then db_summary_add "$name" SKIPPED_STOPPED "raw data backed up"; return 0; fi
    local user="${PG_USER:-postgres}" out="$STAGE/$name"; mkdir -p "$out"
    log "dump postgres name=$name c=$c user=$user"
    if docker exec -u postgres "$c" pg_dumpall -U "$user" | gzip -1 > "$out/all.sql.gz"; then
        local sz; sz=$(stat -c%s "$out/all.sql.gz")
        [ "$sz" -gt 1000 ] || { db_summary_add "$name" DUMP_FAILED "too small ($sz)"; die "postgres dump too small"; }
        db_summary_add "$name" DUMPED_OK "${sz}B"; [ -n "$data" ] && DYNAMIC_EXCLUDES+=("$data")
    else db_summary_add "$name" DUMP_FAILED "pg_dumpall error"; die "postgres dump failed"; fi
}

dump_mongodb() {  # $1=name $2=container $3=data
    local name="${1:-mongodb}" c="${2:-mongodb}" data="${3:-}"
    [ -n "$c" ] || { return 0; }
    container_exists "$c" || { db_summary_add "$name" ABSENT "no container"; return 0; }
    if ! container_running "$c"; then db_summary_add "$name" SKIPPED_STOPPED "raw data backed up"; return 0; fi
    local out="$STAGE/$name"; mkdir -p "$out"; local a=()
    [ -n "${MONGO_USER:-}" ] && [ -n "${MONGO_PASS:-}" ] && a=(-u "$MONGO_USER" -p "$MONGO_PASS" --authenticationDatabase admin)
    log "dump mongodb name=$name c=$c"
    if docker exec "$c" sh -c "rm -f /tmp/mongo.archive" \
       && docker exec "$c" mongodump --quiet "${a[@]}" --gzip --archive=/tmp/mongo.archive \
       && docker cp "$c:/tmp/mongo.archive" "$out/mongo.archive"; then
        docker exec "$c" rm -f /tmp/mongo.archive || true
        local sz; sz=$(stat -c%s "$out/mongo.archive")
        [ "$sz" -gt 100 ] || { db_summary_add "$name" DUMP_FAILED "too small ($sz)"; die "mongo dump too small"; }
        db_summary_add "$name" DUMPED_OK "${sz}B"; [ -n "$data" ] && DYNAMIC_EXCLUDES+=("$data")
    else db_summary_add "$name" DUMP_FAILED "mongodump error"; die "mongodb dump failed"; fi
}

dump_redis() {  # $1=name $2=container $3=data
    local name="${1:-redis}" c="${2:-redis}" data="${3:-}"
    [ -n "$c" ] || { return 0; }
    container_exists "$c" || { db_summary_add "$name" ABSENT "no container"; return 0; }
    if ! container_running "$c"; then db_summary_add "$name" SKIPPED_STOPPED "raw data backed up"; return 0; fi
    local out="$STAGE/$name"; mkdir -p "$out"; local a=()
    [ -n "${REDIS_PASS:-}" ] && a=(-a "$REDIS_PASS" --no-auth-warning)
    local dp="${REDIS_DUMP_PATH:-/data/dump.rdb}"
    log "dump redis name=$name c=$c"
    local before; before=$(docker exec "$c" redis-cli "${a[@]}" LASTSAVE) || { db_summary_add "$name" DUMP_FAILED "auth/conn"; die "redis conn"; }
    docker exec "$c" redis-cli "${a[@]}" BGSAVE >/dev/null
    local w=0; while [ "$w" -lt 60 ]; do sleep 2; w=$((w+2)); [ "$(docker exec "$c" redis-cli "${a[@]}" LASTSAVE)" != "$before" ] && break; done
    [ "$w" -lt 60 ] || { db_summary_add "$name" DUMP_FAILED "BGSAVE timeout"; die "redis BGSAVE timeout"; }
    docker cp "$c:$dp" "$out/dump.rdb"
    db_summary_add "$name" DUMPED_OK "$(stat -c%s "$out/dump.rdb")B"; [ -n "$data" ] && DYNAMIC_EXCLUDES+=("$data")
}

dump_mysql()   { _dump_mysql_family "$1" "$2" "$3" mysqldump; }
dump_mariadb() { _dump_mysql_family "$1" "$2" "$3" mariadb-dump; }

# _dump_mysql_family: $1=name $2=container $3=data $4=preferred dump binary.
# mysqldump/mariadb-dump 는 대상 컨테이너 안에서 실행한다(엔진 이미지에 불필요).
_dump_mysql_family() {
    local name="${1:-mysql}" c="${2:-}" data="${3:-}" prefer="${4:-mysqldump}"
    [ -n "$c" ] || { return 0; }
    container_exists "$c" || { db_summary_add "$name" ABSENT "no container"; return 0; }
    if ! container_running "$c"; then db_summary_add "$name" SKIPPED_STOPPED "raw data backed up"; return 0; fi
    local out="$STAGE/$name"; mkdir -p "$out"
    # 컨테이너 안에서 쓸 덤프 바이너리: 선호 → mysqldump 폴백.
    local bin
    bin=$(docker exec "$c" sh -c "command -v $prefer || command -v mysqldump" 2>/dev/null | head -n1)
    [ -n "$bin" ] || { db_summary_add "$name" DUMP_FAILED "no mysqldump/mariadb-dump in container"; die "mysql dump tool missing in $c"; }
    local user="${MYSQL_USER:-root}" pass="${MYSQL_PASS:-}"
    log "dump $name c=$c bin=$bin"
    # 비밀번호는 MYSQL_PWD env로 전달해 argv 노출을 피한다(mysql/mariadb 클라이언트 표준 지원).
    local rc=0
    if [ -n "$pass" ]; then
        docker exec -e MYSQL_PWD="$pass" "$c" "$bin" -u"$user" --single-transaction --all-databases 2>/dev/null | gzip -1 > "$out/all.sql.gz" || rc=$?
    else
        docker exec "$c" "$bin" -u"$user" --single-transaction --all-databases 2>/dev/null | gzip -1 > "$out/all.sql.gz" || rc=$?
    fi
    [ "$rc" -eq 0 ] || { db_summary_add "$name" DUMP_FAILED "dump error rc=$rc"; die "$name dump failed"; }
    local sz; sz=$(stat -c%s "$out/all.sql.gz")
    [ "$sz" -gt 1000 ] || { db_summary_add "$name" DUMP_FAILED "too small ($sz)"; die "$name dump too small"; }
    db_summary_add "$name" DUMPED_OK "${sz}B"; [ -n "$data" ] && DYNAMIC_EXCLUDES+=("$data")
}

# run_db_jobs: iterate /config/db-jobs.json if present, else legacy 3-type defaults.
run_db_jobs() {
    if [ -s /config/db-jobs.json ]; then
        local name type container data enabled
        while IFS=$'\t' read -r name type container data enabled; do
            [ -n "$name" ] || continue
            if [ "$enabled" != "true" ]; then db_summary_add "$name" DISABLED "off"; continue; fi
            case "$type" in
                postgres) dump_postgres "$name" "$container" "$data" ;;
                mongodb)  dump_mongodb  "$name" "$container" "$data" ;;
                redis)    dump_redis    "$name" "$container" "$data" ;;
                mysql)    dump_mysql    "$name" "$container" "$data" ;;
                mariadb)  dump_mariadb  "$name" "$container" "$data" ;;
                *) db_summary_add "$name" BAD_TYPE "$type" ;;
            esac
        done < <(jq -r '.[] | [.name,.type,.container,(.data//""),(.enabled|tostring)] | @tsv' /config/db-jobs.json)
    else
        dump_postgres postgres "${PG_CONTAINER:-postgres}" /home/docker/postgres/data
        dump_mongodb  mongodb  "${MONGO_CONTAINER:-mongodb}" /home/docker/mongodb
        dump_redis    redis    "${REDIS_CONTAINER:-redis}" /home/docker/redis
    fi
}
