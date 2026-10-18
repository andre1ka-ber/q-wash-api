#!/bin/sh
set -e

: "${DB_HOST:?DB_HOST not set}"
: "${DB_PORT:?DB_PORT not set}"
: "${DB_USER:?DB_USER not set}"
: "${DB_PASSWORD:?DB_PASSWORD not set}"
: "${DB_NAME:?DB_NAME not set}"

DB_URL="postgres://${DB_USER}:${DB_PASSWORD}@${DB_HOST}:${DB_PORT}/${DB_NAME}?sslmode=${DB_SSLMODE:-disable}"

# Volumes created by an earlier root-running image are root-owned; fix once, then drop privileges.
if [ "$(id -u)" = "0" ]; then
    chown -R app:app /app/uploads
    exec su-exec app "$0" "$@"
fi

echo "running migrations..."
migrate -path migrations -database "$DB_URL" up

echo "starting api..."
exec ./api
