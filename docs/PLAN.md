# Q-Wash API — Plan

Car-washing queue API. One washing point today, modeled so more points can be
added later without a rewrite.

## Confirmed decisions

| topic | decision |
|---|---|
| Language/runtime | Go 1.26 |
| HTTP router | [chi](https://github.com/go-chi/chi) |
| DB | PostgreSQL |
| DB access | GORM (models/queries); [golang-migrate](https://github.com/golang-migrate/migrate) with plain `.sql` files for schema versioning (no `AutoMigrate` in prod) |
| Primary keys | UUIDv7 (time-ordered), generated in app code via `google/uuid` — better index locality than UUIDv4/`gen_random_uuid()` |
| Auth | Phone number + SMS OTP, no password. JWT access token + rotating refresh token. |
| Roles | `customer`, `staff`, `admin` on `User.role`. Staff/admin manage washing points/services and advance queue status; customers manage their own cars/bookings. |
| SMS delivery | `SmsSender` interface; dev implementation logs the code to stdout. Real provider swapped in later behind the same interface. |
| Washing point capacity | `boxes_count` field (default 2), configurable per point. Availability = sweep-line overlap check against this capacity. |
| Operating hours | Single daily `open_time`/`close_time` per washing point (same every day) for MVP. |
| Service pricing | `ServicePriceOption` sub-table per service (e.g. car size, or scope like "full body"/"parts only"); every service has at least one (default) option. |

See [DATA_MODEL.md](DATA_MODEL.md) for full schema and the availability algorithm, [API.md](API.md) for endpoint contracts.

## Project layout

```
cmd/api/main.go              entrypoint: config, DB, router, graceful shutdown
internal/
  config/                    env-based config struct
  platform/
    db/                      GORM setup, connection
    httpserver/               chi router, middleware (auth, RBAC, logging, recover)
    jwt/                      token issue/verify
    sms/                      SmsSender interface + stub impl
  user/                       User model, repo, handlers
  auth/                       OTP request/verify, refresh, login flow (uses user, jwt, sms)
  washingpoint/                WashingPoint model, repo, handlers
  service/                    Service + ServicePriceOption, repo, handlers
  car/                        Car model, repo, handlers
  queue/                      Queue model, repo, handlers, availability + booking logic
  notification/               Notification model, repo, handlers
  apperror/                   shared error types -> HTTP status mapping
  httputil/                   response helpers, pagination, request validation
migrations/                   golang-migrate .sql files
docs/                         this plan, data model, API contract
docker-compose.yml            local Postgres
Makefile                      common dev commands
.env.example
```

## Phases

Each phase should leave the project in a compiling, runnable state. Tracked in [PROGRESS.md](../PROGRESS.md).

1. **Scaffolding** — go.mod, folder structure, config loader, docker-compose (Postgres), GORM connection, chi router with `/health`, Makefile, `.env.example`.
2. **Schema & migrations** — migrate files for all tables in DATA_MODEL.md, GORM model structs matching them, DB connects and migrates cleanly.
3. **Auth** — OTP request/verify endpoints, JWT issue/verify, refresh endpoint, auth middleware, RBAC middleware (role checks).
4. **Washing point CRUD** — admin/staff write, public read.
5. **Service + price option CRUD** — admin/staff write, public read.
6. **Car CRUD** — customer manages own cars.
7. **Availability endpoint** — sweep-line algorithm per DATA_MODEL.md.
8. **Queue booking** — create (with box assignment + race-safe overlap check), cancel, staff status-transition endpoint, list/get.
9. **History** — `GET /me/queue` (all statuses, paginated).
10. **Notifications** — CRUD/list records; OTP send goes through the same `SmsSender` stub from phase 1.
11. **Hardening** — consistent error responses, request validation, pagination helpers, seed script, README, unit + integration tests (testcontainers-go against real Postgres).
12. **Future/optional** — Dockerfile for the API itself, multi-washing-point considerations doc, per-weekday schedules, push notifications, a background worker to actually send `pending` notifications whose `send_at` has arrived (currently only sent synchronously at creation time if already due — see DATA_MODEL.md).

## Open assumptions to revisit

- `queue.notes` — original spec had a trailing "optional" after `service_id` whose meaning was ambiguous; treated as "there may be additional optional fields" rather than making `service_id` nullable. See DATA_MODEL.md note.
- `Notification` gained `user_id`, `queue_id`, `channel` beyond the original 4 fields — needed to route/target the notification.
- `Car` kept to exactly `id, name, user_id` per spec (no plate/type field) — revisit if that's actually needed.
- No timezone field on `WashingPoint` yet — fine for single-point deployment in one city; add when scaling to multiple points/regions.
