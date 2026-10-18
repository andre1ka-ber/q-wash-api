FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/api ./cmd/api

FROM alpine:3.20
ARG MIGRATE_VERSION=v4.18.1
ARG MIGRATE_SHA256=2a08137b4720aa457bc760540723e313783f1fab27473463bdcc5fc2e9252959
RUN apk add --no-cache ca-certificates curl su-exec \
    && curl -fsSL -o /tmp/migrate.tar.gz https://github.com/golang-migrate/migrate/releases/download/${MIGRATE_VERSION}/migrate.linux-amd64.tar.gz \
    && echo "${MIGRATE_SHA256}  /tmp/migrate.tar.gz" | sha256sum -c - \
    && tar xz -C /usr/local/bin migrate -f /tmp/migrate.tar.gz \
    && rm -f /tmp/migrate.tar.gz \
    && apk del curl \
    && addgroup -S app && adduser -S -G app app

WORKDIR /app
COPY --from=builder /out/api ./api
COPY migrations ./migrations
COPY docker-entrypoint.sh ./
RUN chmod +x docker-entrypoint.sh && mkdir -p /app/uploads && chown app:app /app/uploads

EXPOSE 8080
# Starts as root only so the entrypoint can fix ownership of the uploads volume;
# it then drops to the unprivileged "app" user before running migrate and the API.
ENTRYPOINT ["./docker-entrypoint.sh"]
