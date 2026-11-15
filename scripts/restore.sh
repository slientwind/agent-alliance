#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -ne 3 ]; then echo 'Usage: restore.sh POSTGRES_CONTAINER NEW_DATABASE INPUT.dump' >&2; exit 2; fi
if [[ ! "$2" =~ ^[a-z][a-z0-9_]{0,50}$ ]]; then echo 'Invalid new database name' >&2; exit 2; fi
if [ ! -s "$3" ]; then echo 'Backup file is empty or absent' >&2; exit 2; fi
# CREATE DATABASE fails if the destination exists. Existing databases are never overwritten.
docker exec "$1" createdb -U alliance "$2"
docker exec -i "$1" pg_restore -U alliance -d "$2" --exit-on-error < "$3"
printf 'Restored into new database: %s\n' "$2"
