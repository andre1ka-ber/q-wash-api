FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/api ./cmd/api
RUN CGO_ENABLED=0 go build -o /out/seed ./cmd/seed

FROM alpine:3.20
RUN apk add --no-cache ca-certificates curl \
    && curl -L https://github.com/golang-migrate/migrate/releases/download/v4.18.1/migrate.linux-amd64.tar.gz | tar xz \
    && mv migrate /usr/local/bin/migrate \
    && rm -f LICENSE README.md

WORKDIR /app
COPY --from=builder /out/api ./api
COPY --from=builder /out/seed ./seed
COPY migrations ./migrations
COPY docker-entrypoint.sh ./
RUN chmod +x docker-entrypoint.sh

EXPOSE 8080
ENTRYPOINT ["./docker-entrypoint.sh"]
