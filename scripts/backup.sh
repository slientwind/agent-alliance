#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -ne 2 ]; then echo 'Usage: backup.sh POSTGRES_CONTAINER OUTPUT.dump' >&2; exit 2; fi
umask 077
if [ -e "$2" ]; then echo 'Refusing to overwrite an existing backup' >&2; exit 2; fi
docker exec "$1" pg_dump -U alliance -d alliance -Fc > "$2"
chmod 600 "$2"
printf 'Backup written: %s\n' "$2"
