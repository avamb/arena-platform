#!/usr/bin/env bash
# Nightly Arena backup: database dump + media archive, kept 14 days.
set -euo pipefail
DEST=/var/backups/arena
STAMP=$(date -u +%Y%m%d-%H%M%S)
DB=arena-backend-prod-xy5zqo-db-1
MEDIA_VOL=arena-backend-prod-xy5zqo_arena-media
docker exec "$DB" sh -c "pg_dump -U \$POSTGRES_USER -d \$POSTGRES_DB -Fc" > "$DEST/arena_$STAMP.dump"
docker run --rm -v "$MEDIA_VOL":/src:ro -v "$DEST":/dst alpine tar czf "/dst/media_$STAMP.tgz" -C /src .
find "$DEST" -name "arena_*.dump" -mtime +14 -delete
find "$DEST" -name "media_*.tgz" -mtime +14 -delete
