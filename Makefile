.PHONY: run build seed test test-integration tidy up down migrate-up migrate-down migrate-new

run:
	go run ./cmd/api

build:
	go build -o bin/api ./cmd/api

seed:
	go run ./cmd/seed

test:
	go test ./...

# Needs a working Docker daemon (spins up an ephemeral Postgres per test
# via testcontainers-go) — not run by `make test`.
test-integration:
	go test -tags=integration ./internal/integration/... -v

tidy:
	go mod tidy

up:
	docker compose up -d

down:
	docker compose down

# usage: make migrate-new name=create_users
migrate-new:
	migrate create -ext sql -dir migrations -seq $(name)

migrate-up:
	migrate -path migrations -database "postgres://pegasus:pegasus@localhost:5432/pegasus?sslmode=disable" up

migrate-down:
	migrate -path migrations -database "postgres://pegasus:pegasus@localhost:5432/pegasus?sslmode=disable" down 1
