# Q-Wash API

A Go API for a car-washing queue: phone + SMS-OTP auth, washing point / service
CRUD, booking with box-capacity-aware availability, a live queue board, and
booking history. Built for a single washing point, modeled so more can be
added later without a rewrite.

Full design docs live in [`docs/`](docs/PLAN.md); progress and decisions are
logged in [`PROGRESS.md`](PROGRESS.md).

## Stack

- Go 1.26, [chi](https://github.com/go-chi/chi) router
- PostgreSQL, [GORM](https://gorm.io), [golang-migrate](https://github.com/golang-migrate/migrate) for schema versioning
- JWT access tokens + opaque rotating refresh tokens (no passwords — phone + SMS OTP)
- UUIDv7 primary keys

## Prerequisites

- Go 1.26+
- Docker (for local Postgres, and for integration tests)

## Quick start

```bash
cp .env.example .env        # defaults work as-is for local dev
make up                     # start Postgres (docker compose)
make migrate-up             # apply schema migrations
make seed                   # baseline dev data (1 washing point, services, users, a booking)
make run                    # start the API on :8080
```

Verify it's up:

```bash
curl localhost:8080/health
curl localhost:8080/api/v1/washing-points
```

The seeded users (phone → role): `+15550000001` → admin, `+15550000002` →
staff, `+15550000006` → worker, `+15550000003` → customer (plus two more
demo customers used only to seed a double-booking scenario). Log in as any
of them via `POST /api/v1/auth/otp/request` — since there's no real SMS
provider wired up, the OTP code is printed to the API's stdout log (`sms
(stub, not actually sent)`), not actually sent anywhere.

The admin/staff/worker accounts also get a username + password (seeded by
`cmd/seed`, dev-only) for `POST /api/v1/auth/login` — used by the queue
board, staff panel, and `q-wash-worker` instead of phone+OTP:
`admin`/`admin12345`, `staff`/`staff12345`, and `worker`/`worker12345`. The
staff and worker accounts are both scoped to the seeded washing point via
`washing_point_id`.

## API docs

Interactive Swagger UI: **http://localhost:8080/docs** (once `make run` is
up). Raw spec at `GET /openapi.yaml`, source at
[`docs/openapi.yaml`](docs/openapi.yaml). "Try it out" hits this same
server — click Authorize and paste an access token from `/auth/otp/verify`
to exercise the protected routes.

## Makefile targets

| target | what it does |
|---|---|
| `make up` / `make down` | start/stop the local Postgres container |
| `make migrate-new name=x` | scaffold a new migration pair in `migrations/` |
| `make migrate-up` / `make migrate-down` | apply/roll back migrations |
| `make seed` | idempotent baseline dev data (safe to re-run) |
| `make run` | run the API locally |
| `make build` | build a binary to `bin/api` |
| `make test` | unit tests (fast, no Docker needed) |
| `make test-integration` | full-stack tests against a real, ephemeral Postgres (needs Docker — see below) |
| `make tidy` | `go mod tidy` |

## Testing

Two tiers:

- **Unit tests** (`make test`) — pure logic, no DB, no mocks: the
  availability sweep-line algorithm and box-assignment/status-transition
  rules in `internal/queue`, phone/OTP/refresh-token generation in
  `internal/auth`, schedule validation in `internal/schedule`,
  time-parsing helpers in `internal/admin`, and local-disk photo storage
  (put/delete round-trip, path-traversal rejection) in
  `internal/platform/storage`. Fast, run on every `go test ./...`.
- **Integration tests** (`make test-integration`) — full HTTP stack against
  a real, ephemeral Postgres container (via
  [testcontainers-go](https://golang.testcontainers.org/)), driven only
  through the same HTTP API a real client would use. Cover the auth flow
  (OTP → tokens → refresh rotation → logout), RBAC/ownership across
  washing-point management, booking double-booking prevention,
  cancel-then-rebook, the forward-only status state machine, photo
  upload/ownership/cover-promotion, schedule seeding/validation, box
  CRUD/availability filtering, worker-role RBAC and pause/resume, admin
  network-wide views, connection-request approve/reject, and the display
  board (including its SSE pushes). Gated behind the `integration` build
  tag so a plain `go test ./...` never needs Docker; each test function
  spins up and tears down its own container.

## Project layout

```
cmd/api/        entrypoint: config, DB connection, graceful shutdown
cmd/seed/       idempotent dev-data seeder + a self-check of the DB overlap constraint
internal/app/   wires every feature's handlers onto one router (imported by
                cmd/api and by the integration tests, so they exercise the
                exact same server)
internal/
  auth/         phone+OTP login, staff/admin username+password login, JWT issue/verify, refresh rotation, RBAC middleware
  user/         GET/PATCH /me
  washingpoint/ washing point CRUD (public read, staff/admin write)
  service/      service + price-option CRUD, default-price invariants
  car/          customer's own cars (ownership-, not role-, scoped)
  queue/        availability algorithm, booking create/cancel/status/history, live queue/board (polling + SSE)
  schedule/     per-weekday operating-hours CRUD + availability resolution
  box/          box (bay) CRUD, is_open toggling
  photo/        washing-point photo upload/CRUD, cover-photo promotion
  owner/        owner (network operator) CRUD
  connectionrequest/ prospective owner's onboarding request + admin approve/reject
  admin/        network-wide admin views (washing-point list, stats)
  notification/ staff-sent notifications, delivered via the sms.Sender stub
  apperror/     typed error -> HTTP status/code mapping
  httputil/     JSON response helpers, pagination
  integration/  full-HTTP-stack tests, gated behind the `integration` build tag
  platform/     db, jwt, sms, reqctx, httpserver (also serves /docs + /openapi.yaml), storage (photo uploads) — infra with no business logic
migrations/     golang-migrate .sql files, one pair per table
docs/           PLAN.md (phases/decisions), DATA_MODEL.md (schema + algorithms),
                API.md (endpoint contract, prose), openapi.yaml (endpoint contract, machine-readable,
                embedded via docs.go and served at /openapi.yaml + /docs)
```

## API

See [`docs/API.md`](docs/API.md) for the full endpoint contract (request/response
shapes, validation error codes, role requirements). See
[`docs/DATA_MODEL.md`](docs/DATA_MODEL.md) for the schema and the availability/
booking algorithms.

## Configuration

All config is env vars (see [`.env.example`](.env.example) for the full list
with defaults) — app env/port, CORS origins, DB connection, JWT
secrets/TTLs, OTP TTL/cooldown/max-attempts, and local photo-upload storage
(`UPLOADS_DIR`/`UPLOADS_BASE_URL`). `.env` is loaded automatically in dev
(and ignored by git); real environment variables always take precedence
over it.

## Known limitations

- No real SMS provider — `sms.Sender` has one stub implementation that logs
  instead of sending. Swapping in a real provider means implementing the
  same interface, no other code changes.
- No background worker: a notification scheduled for the future (`send_at`
  after now) is created as `pending` and stays that way — there's nothing
  yet that sweeps due notifications and sends them later.
- Both SSE endpoints (`GET /queue/{id}/events`, `GET
  /washing-points/{id}/board/events`) are backed by the same in-memory
  pub/sub with no persistence — a process restart drops all connected
  streams (clients are expected to reconnect and/or refetch on
  foreground) and it doesn't scale past one API instance without a shared
  broker.
- Single washing point, single fixed timezone (`Asia/Dushanbe`, hardcoded —
  no per-point timezone field yet) — see the "open assumptions" section of
  `docs/PLAN.md` for what's designed to extend cleanly later versus what
  would need rework.
