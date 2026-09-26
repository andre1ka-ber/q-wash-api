# Data Model

Status: draft v1 (2026-08-05). This is the source of truth for schema decisions.
Update this file whenever a model or relationship changes.

## Conventions

- All primary keys are `uuid`, generated as **UUIDv7** in app code via a GORM `BeforeCreate` hook (`google/uuid`'s `NewV7()`). UUIDv7 is time-ordered, so it indexes and sorts far better than v4/`gen_random_uuid()` for PK/FK columns at insert-heavy tables like `queue`. Column type stays plain `uuid` in Postgres.
- All tables have `created_at` / `updated_at`. Soft-delete (`deleted_at`) only where noted.
- Money stored as integer minor units (cents) to avoid float rounding issues.
- Times stored in UTC (Postgres `timestamptz`). Washing point `open_time`/`close_time` ("HH:MM" strings) have no per-point timezone field yet — they're interpreted in a fixed `Asia/Dushanbe` (UTC+5, no DST) constant (`businessLocation` in `internal/queue/handler.go`), matching the app's single current market (Tajikistan, `+992` numbers). Revisit (per-point timezone field) when scaling to multiple points/cities/timezones.

## Entities

### User
Customers authenticate via phone + OTP, no password. Staff/admin *additionally* have an optional username + password (see below) for the queue board and staff panel, which shouldn't run a phone-OTP flow.

| field | type | notes |
|---|---|---|
| id | uuid | PK |
| phone_number | string | unique, E.164 format, indexed |
| name | string, nullable | optional display name |
| role | enum: `customer`, `staff`, `admin`, `worker` | default `customer`. `staff`/`admin` can manage washing points/services and advance queue status. `worker` (added for the shift-technician web app, see `PLAN_WEB_APPS.md`) logs in the same way staff/admin do; gated into the per-point live surface (queue board, status, pause/resume, live-boxes) via `requireQueueOps`, but not `requireStaff` (washing-point/service/photo/schedule/box management, network-wide `GET /queue`). |
| washing_point_id | uuid, nullable | FK -> WashingPoint. Scopes a `staff`/`worker` account to the one point they work at; always NULL for `admin` (network-wide) and `customer`. Added for the multi-point web apps, see `PLAN_WEB_APPS.md`. |
| last_login_at | timestamp, nullable | updated on successful OTP verify or password login |
| username | string, nullable, unique | only set for staff/admin; NULL for customers. Postgres unique indexes permit multiple NULLs, so no partial index is needed. |
| password_hash | string, nullable | bcrypt hash; NULL for customers |
| created_at / updated_at | timestamp | |

> Deliberately kept as nullable columns on `users` rather than a separate `staff_credentials` table: staff/admin already need a full `User` row regardless (they can book their own washes, appear in `queue.user_id`, receive notifications, etc.), so a separate table would only relocate two columns while adding a join everywhere a staff identity is resolved — no real normalization benefit at this scale. There is no API endpoint to set username/password (same reasoning as role promotion below): `cmd/seed` sets dev-only credentials directly via the DB for the seeded admin/staff accounts.

### OtpCode
Short-lived one-time codes for login. Not exposed via API beyond request/verify.

| field | type | notes |
|---|---|---|
| id | uuid | PK |
| phone_number | string | indexed |
| code_hash | string | bcrypt hash of the 6-digit code, never store plaintext |
| expires_at | timestamp | now + `OTP_TTL` (default 5 min) |
| attempts | int | failed-verify counter; locked out at `OTP_MAX_ATTEMPTS` (default 5) |
| consumed_at | timestamp, nullable | set once used |
| created_at | timestamp | |

### RefreshToken
Supports JWT access + rotating refresh token pattern. The token itself is an
opaque random string (never a JWT), so an individual session can be revoked
by deleting/flagging its row — a JWT refresh token could not be revoked
without a separate blocklist anyway.

| field | type | notes |
|---|---|---|
| id | uuid | PK |
| user_id | uuid | FK -> User |
| token_hash | string | HMAC-SHA256(`REFRESH_TOKEN_PEPPER`, token), hex-encoded, indexed |
| expires_at | timestamp | now + `JWT_REFRESH_TTL` (default 30 days) |
| revoked_at | timestamp, nullable | set on logout/rotation |
| created_at | timestamp | |

### WashingPoint
Modeled for a multi-point network with owners (see `PLAN_WEB_APPS.md`), though
`internal/queue`'s availability algorithm still treats each point in
isolation.

| field | type | notes |
|---|---|---|
| id | uuid | PK |
| owner_id | uuid, nullable | FK -> Owner. Nullable through migration; set via `POST`/`PATCH /washing-points` (admin, or staff on their own point). |
| name | string | |
| address | string | |
| latitude | float | |
| longitude | float | |
| boxes_count | int | number of parallel washing boxes/bays, default 2, drives availability capacity. Individual `Box` rows (below) layer metadata/open-closed state on top of this count; the count itself stays the source of truth for availability math. |
| open_time | varchar(5) "HH:MM" | **Legacy as of phase 5**: no longer read by the availability algorithm or `Manager.CreateBooking` — `WashingPointSchedule` (below) is now the sole source of truth for booking. Stored as zero-padded text (not Postgres `time`) to avoid driver type-mapping friction; validated by a CHECK constraint. Kept only as the initial values a new point's schedule is seeded from on creation (see `WashingPointSchedule`'s `ScheduleSeeder` note below) and as informational display metadata; editing it via `PATCH /washing-points/{id}` does not change the schedule. |
| close_time | varchar(5) "HH:MM" | daily closing time, same status as `open_time` |
| status | enum: `active`, `paused`, `pending_review` | replaces the old `is_active bool`. `pending_review` is reachable via `ConnectionRequest` approval (`connectionrequest.Manager.Approve`) or directly via `PATCH /washing-points/{id}`. |
| description | text, nullable | surfaced by `washingpoint.Handler` (`GET`/`POST`/`PATCH /washing-points`) |
| amenities | text[], nullable | free-text tags (e.g. "Wi-Fi", "Coffee"), not a lookup table; surfaced by `washingpoint.Handler` |
| created_at / updated_at | timestamp | |

> `WashingPointSchedule` (below) is the per-weekday replacement for the single-window assumption `open_time`/`close_time` used to encode — as of `PLAN_WEB_APPS.md` phase 5, the availability algorithm reads it exclusively.

### Owner
A business that owns one or more washing points. Kept separate from `User`
— an owner doesn't necessarily have their own login. `owner.Handler`
(`GET/POST /owners`, `GET/PATCH /owners/{id}`), admin-only — unlike washing
points, owner contact info isn't public. See `PLAN_WEB_APPS.md`.

| field | type | notes |
|---|---|---|
| id | uuid | PK |
| name | string | |
| contact_name / contact_phone / contact_email | string, nullable | |
| created_at / updated_at | timestamp | |

### ConnectionRequest
Backs the admin app's onboarding queue — a prospective owner applying to
join the network. `connectionrequest.Handler` (`GET/POST
/connection-requests`, `GET /connection-requests/{id}`, `PATCH
/connection-requests/{id}` with `{status: approved|rejected}`), admin-only.
Approving one (`connectionrequest.Manager.Approve`) creates an `Owner`
(reusing one whose `contact_phone` matches, if any) and a `WashingPoint`
with `status = pending_review` and placeholder `latitude`/`longitude` of
`0,0` — the request doesn't collect coordinates (that's the separate admin
"+ New point" wizard flow (`q-wash-admin`), which sets them directly); an
admin must correct them via `PATCH /washing-points/{id}` before the point
can go live. Rejecting or re-reviewing an already-reviewed request 409s
`connection_request_already_reviewed`. See `PLAN_WEB_APPS.md`.

| field | type | notes |
|---|---|---|
| id | uuid | PK |
| business_name / contact_name / contact_phone / address | string | |
| boxes_count | int | |
| note | string, nullable | |
| status | enum: `new`, `approved`, `rejected` | |
| reviewed_by | uuid, nullable | FK -> User |
| reviewed_at | timestamp, nullable | |
| created_at / updated_at | timestamp | |

### Box
An individual washing bay within a point — metadata + open/closed state
layered on top of `WashingPoint.boxes_count`, which stays the number
availability math uses. `internal/box` (`GET/POST /washing-points/{id}/boxes`,
`PATCH`/`DELETE /washing-points/{id}/boxes/{boxId}`) — reads public, writes
staff/admin scoped to their own point, same ownership pattern as
`WashingPointPhoto` above. Every point (existing, backfilled by the phase-1
migration; new, seeded by `box.Manager.SeedDefault` from
`washingpoint.Handler.create`/`connectionrequest.Manager.Approve`, mirroring
`schedule.Manager.SeedDefault`) starts with one open row per box number.

| field | type | notes |
|---|---|---|
| id | uuid | PK |
| washing_point_id | uuid | FK -> WashingPoint |
| number | int | 1..boxes_count, unique per point; server-assigned on create (one past the current max), never client-chosen |
| label | string, nullable | e.g. "Detailing lift" |
| is_open | bool | default true; `queue.Manager.CreateBooking` rejects a `box_number` matching a closed row (409 `box_closed`), and `GET .../availability` excludes a closed box from every slot's `available_boxes` for the whole day — implemented as a synthetic all-day busy interval layered onto the existing sweep-line algorithm (`queue.Handler.availability`), not a change to `ComputeAvailableSlotsForDay` itself. A `box_number`/row pairing that doesn't exist (e.g. an older point never backfilled) fails open. |
| created_at / updated_at | timestamp | |

Deleting a box (`DELETE .../boxes/{boxId}`) does not change
`WashingPoint.boxes_count` — the two are updated independently, so a delete
can't silently shrink capacity and a `boxes_count` change doesn't
auto-create/remove rows.

### WashingPointSchedule
Per-weekday operating hours — as of `PLAN_WEB_APPS.md` phase 5, this is the
**sole source of truth** the availability algorithm
(`queue.ComputeAvailableSlotsForDay`) and `queue.Manager.CreateBooking`'s
operating-hours check read; `WashingPoint.open_time`/`close_time` are no
longer consulted by any booking logic (see the note on that table above).
`internal/schedule` (`GET/PUT /washing-points/{id}/schedule`) — reads
public, writes staff/admin scoped to their own point; `PUT` atomically
replaces all 7 rows (`schedule.Repository.ReplaceAll`, delete-then-insert
in one transaction), not a per-weekday upsert. Every washing point is
auto-seeded with a default 7-row schedule on creation (same hours every
day, no break, from the create request's `open_time`/`close_time`) via a
small `ScheduleSeeder` interface `washingpoint.Handler` and
`connectionrequest.Manager` each declare locally and `*schedule.Manager`
satisfies structurally — avoids those packages importing `internal/schedule`
directly, which already imports `internal/washingpoint` the other way for
its own ownership checks. A washing point with zero schedule rows (should
never happen through normal use) is treated as closed every day, never as
falling back to some assumed default — see `queue.resolveDaySchedule`.

Existing points from before this table existed were backfilled with 7 rows
each (same hours every day, no break), matching their prior
`open_time`/`close_time` behavior exactly on cutover (migration `000016`).

| field | type | notes |
|---|---|---|
| id | uuid | PK |
| washing_point_id | uuid | FK -> WashingPoint |
| weekday | smallint | 0=Monday..6=Sunday, unique per point |
| is_open | bool | |
| open_time / close_time | varchar(5) "HH:MM", nullable | NULL when `is_open = false` |
| break_start / break_end | varchar(5) "HH:MM", nullable | optional single lunch-break window, splitting the day into two disjoint bookable windows (`queue.DaySchedule.Windows`) when both are set |
| created_at / updated_at | timestamp | |

### WashingPointPhoto
Backs the cabinet app's "photos & description" tab. `internal/photo`
(`GET/POST /washing-points/{id}/photos`, `PATCH`/`DELETE
/washing-points/{id}/photos/{photoId}`) — reads public, writes staff/admin
scoped to their own point. Files are written via
`internal/platform/storage.Storage`; the only implementation today is a
dev-only `LocalDisk` (writes to `UPLOADS_DIR`, served back at
`UPLOADS_BASE_URL`, config in `config.StorageConfig`) — not multi-instance
safe, swappable for S3/GCS later with no change to `internal/photo`.

| field | type | notes |
|---|---|---|
| id | uuid | PK |
| washing_point_id | uuid | FK -> WashingPoint |
| url | string | |
| is_cover | bool | default false; exactly one cover per point, enforced in `photo.Manager` — the first photo uploaded is always the cover; deleting the cover auto-promotes the oldest remaining one, same "auto-promote on delete" pattern as `ServicePriceOption.is_default` |
| sort_order | int | |
| created_at | timestamp | |

Uploaded bytes are content-sniffed server-side (`http.DetectContentType`,
never the client-declared `Content-Type` or filename extension); only
JPEG/PNG/GIF/WebP are accepted. `image/svg+xml` is deliberately excluded —
an SVG can embed `<script>`, which would be a stored-XSS vector if served
back as a static file exactly as uploaded.

### Service
Belongs to one washing point.

| field | type | notes |
|---|---|---|
| id | uuid | PK |
| washing_point_id | uuid | FK -> WashingPoint |
| name | string | |
| description | string, nullable | |
| duration_minutes | int | used for scheduling/end-time calc |
| picture_url | string, nullable | |
| is_active | bool | default true |
| created_at / updated_at | timestamp | |

### ServicePriceOption
Sub-type pricing for a service (e.g. car size, or scope like "full body" vs "parts only").

| field | type | notes |
|---|---|---|
| id | uuid | PK |
| service_id | uuid | FK -> Service |
| name | string | e.g. "Sedan", "SUV", "Full body", "Parts only" |
| price_cents | int | |
| is_default | bool | exactly one default per service; used when a service has effectively one flat price |
| created_at / updated_at | timestamp | |

> Every service must have at least one price option. `POST /washing-points/{id}/services` requires a non-empty `price_options` array (client-supplied, not an auto-created placeholder) — an empty array is rejected with 400. If none of the supplied options is marked `is_default`, the first is auto-promoted. Deleting the only remaining option, or one still referenced by a `queue` row, is rejected (409); deleting the current default auto-promotes another remaining option so a service is never left without one. See `internal/service/manager.go`.

### Car
| field | type | notes |
|---|---|---|
| id | uuid | PK |
| user_id | uuid | FK -> User |
| name | string | e.g. "My Tesla" / plate — kept as a single free-text field per spec |
| created_at / updated_at | timestamp | |

> Cars are hard-deleted (unlike washing points/services, there's no `is_active` flag) since a customer's own car list is low-stakes to remove outright. `DELETE /cars/{id}` is refused with 409 if any `queue` row still references the car — the FK (`ON DELETE RESTRICT`) would reject it at the DB level regardless, but the API checks first to return a clean error instead of a raw constraint violation.

### Queue
The booking / queue entry. Central entity tying everything together.

| field | type | notes |
|---|---|---|
| id | uuid | PK |
| status | enum: `queue`, `waiting`, `washing`, `ready`, `canceled`, `no_show` | mostly forward-moving state machine, see below. Cabinet labels: Записан=`queue`, Приехал=`waiting`, Моется=`washing`, Готово=`ready`, Отменён=`canceled`, Не приехал=`no_show` |
| source | enum: `app`, `qr`, `manual`, default `app` | where the booking came from (migration `000022`). `manual` = staff walk-in via `POST /washing-points/{id}/queue/manual`; `qr` is reserved (nothing sets it yet) |
| user_id | uuid | FK -> User |
| car_id | uuid | FK -> Car |
| service_id | uuid | FK -> Service |
| price_option_id | uuid | FK -> ServicePriceOption, resolved price/duration snapshot at booking time |
| washing_point_id | uuid | FK -> WashingPoint, denormalized for query convenience |
| box_number | int, not null | 1..boxes_count, client-chosen at booking time (validated against `boxes_count` and actual availability, never auto-assigned) |
| scheduled_start_at | timestamp | requested slot start |
| scheduled_end_at | timestamp | `scheduled_start_at + service.duration_minutes`, stored for fast overlap queries |
| notes | string, nullable | free-text, optional |
| canceled_at | timestamp, nullable | |
| paused_at | timestamp, nullable | only meaningful while `status = washing`. Toggled by `PATCH /queue/{id}/pause`/`/resume` (staff/worker/admin, `docs/PLAN_WEB_APPS.md` phase 7) via `queue.Manager.Pause`/`Resume` — 409 `cannot_pause`/`cannot_resume` outside `status = washing` or on a redundant call. Deliberately not a new `status` value: the forward-only state machine below stays untouched. |
| created_at / updated_at | timestamp | |

State machine: `queue -> waiting -> washing -> ready`; staff may also mark `no_show` from `queue`/`waiting`, and restore a `no_show` or `canceled` booking back to `queue` (`staffStatusTransitions` in `queue/manager.go`; the restore is re-checked by the box-overlap `EXCLUDE`, which ignores both `canceled` and `no_show` rows, and by the one-active-booking index). `canceled` reachable only from `queue` or `waiting` (spec: "cancel queue before changing status to washing"). No transition skips a stage; enforced in `queue.Manager` (app layer, not a DB constraint) — `PATCH /queue/{id}/status` (staff/worker/admin as of phase 7) drives the forward path one step at a time, `PATCH /queue/{id}/cancel` is the only way to reach `canceled` — the booking's own owner, or staff/worker/admin at its washing point (broadened from owner-only in phase 7, for the worker app's "Снять" no-show action; `queue.Manager.CancelBooking` itself no longer checks ownership — `queue.Handler.cancel` does, before calling it, same layering `updateStatus`/`pause`/`resume` already used).

> Note on original spec: the `queue` model line ended with a trailing "optional" whose referent was ambiguous. Interpreted here as "there may be additional optional fields" (`notes`), not that `service_id` itself is optional — a service is required to resolve duration/price for scheduling. Flag if that's wrong.

`cars_ahead` (on the `GET /queue/{id}`/`GET /me/queue`/create/SSE response) and `available_boxes` (on the availability response) are both computed at request time, not stored columns — see "Availability algorithm" below and `queue.Repository.CountActiveAhead`.

A user may have at most one active (`queue`/`waiting`/`washing`) booking at a time — the customer-facing "Моя очередь" model assumes exactly one. Enforced both app-side (`queue.Manager.CreateBooking` checks `Repository.HasActiveForUser` before creating) and DB-side (partial unique index `queue_one_active_booking_per_user` on `user_id` where `status IN ('queue','waiting','washing')`, migration `000011`) — same defense-in-depth pattern as the box-overlap `EXCLUDE` constraint below, since the app-level check alone can't catch two of the same user's requests racing across two different washing points.

### Notification
| field | type | notes |
|---|---|---|
| id | uuid | PK |
| user_id | uuid | FK -> User |
| queue_id | uuid, nullable | FK -> Queue, when the notification relates to a booking |
| status | enum: `pending`, `sent`, `failed` | |
| channel | enum: `sms` | only channel for now; extensible |
| text | string | |
| send_at | timestamp | when it should be sent |
| sent_at | timestamp, nullable | |
| created_at | timestamp | |

> Added `user_id`, `queue_id`, `channel` beyond the original spec's four fields — needed to know who/what a notification is for and how to deliver it. Implemented in Phase 10 (`internal/notification`): `POST /notifications` (staff/admin) sends immediately through the same `sms.Sender` stub OTP uses, *if* `send_at` is due (now or past) — `status` becomes `sent`/`failed` and `sent_at` is set. A `send_at` in the future just creates the row as `pending` and leaves it there: there is still no background worker in this MVP to sweep due notifications and send them later, so a scheduled reminder never actually fires on its own yet (flagged as future work, see PLAN.md Phase 12). `GET /me/notifications` (any authenticated user, own records, paginated) is the read side.

### QrCode
A physical sticker in the admin's QR-code pool, generated ahead of
printing and assigned one-per-`WashingPoint`. `qrcode.Handler` mounts
`/qr-codes*` (`internal/qrcode`): `POST /qr-codes/generate`, `GET
/qr-codes`, `GET /qr-codes/{id}`, `POST /qr-codes/{id}/assign|unassign|
disable` (admin-only); `GET /qr-codes/mine`, `POST
/qr-codes/mine/request-replacement` (staff, scoped to their own
`WashingPointID`, same pattern as `internal/photo`); `GET
/qr-codes/scan/{token}` (public, no auth — records a `QrScan` and serves a
static HTML page). The human-facing sticker number (`QW-0031`) is derived
from `seq` at read time (`QRCode.Code()`), never stored — one source of
truth for what's printed. `status` plus the nullable `washing_point_id` is
the whole assignment state machine: `free` -> `assigned` (-> `free` again
on unassign) -> `disabled` (terminal, releases the point). One code per
point is enforced by a unique constraint on `washing_point_id` (NULLs
don't conflict) plus `Manager.Assign` proactively freeing whatever was
already on the target point. `replacement_requested_at` is staff's
one-button "my sticker is damaged" signal — no separate notification
entity, the admin pool page filters/badges on this column instead, same
"status field is the whole workflow" simplicity as `ConnectionRequest`.

| field | type | notes |
|---|---|---|
| id | uuid | PK |
| seq | bigserial | source of the QW-#### display code |
| token | string | opaque, unguessable — the public scan URL's path segment |
| batch_label | string | e.g. "Партия #2 · 12 авг" |
| status | enum: `free`, `assigned`, `disabled` | |
| washing_point_id | uuid, nullable | FK -> WashingPoint, unique |
| assigned_at / disabled_at / replacement_requested_at | timestamp, nullable | |
| created_at / updated_at | timestamp | |

### QrScan
One recorded hit of a code's public scan URL — the raw event log behind
scan-count stats and the "bookings via QR" heuristic
(`queue.Repository.CountCreatedNearTimes`: any `Queue` row for the code's
washing point created within 30 minutes of a scan counts as attributed —
a rough proxy, not real causal tracking, since no scan-to-booking session
exists yet).

| field | type | notes |
|---|---|---|
| id | uuid | PK |
| qr_code_id | uuid | FK -> QrCode |
| scanned_at | timestamp | defaults to now() |

## Entity relationships

```
User 1---N Car
User 1---N Queue
User 1---N Notification
User 1---N RefreshToken
WashingPoint 1---N User (staff/worker, via washing_point_id)

WashingPoint 1---N Service
WashingPoint 1---N Queue
WashingPoint 1---N Box
WashingPoint 1---N WashingPointSchedule
WashingPoint 1---N WashingPointPhoto
Owner 1---N WashingPoint

Service 1---N ServicePriceOption
Service 1---N Queue

Car 1---N Queue
ServicePriceOption 1---N Queue
Queue 1---N Notification (optional)
```

`ConnectionRequest` has no FK relationships yet — approving one is meant to
*create* an `Owner`/`WashingPoint` pair, not reference existing rows.

## Availability algorithm (booking windows)

Implemented in Phase 7 as `GET /washing-points/{id}/availability`; the pure sweep-line function is `queue.ComputeAvailableSlotsWithBoxes` (`internal/queue/availability.go`, unit tested), fed by `queue.Repository.FindActiveBookingsInRange` for step 2 below. (An older aggregate-only sibling, `ComputeAvailableSlots`, is kept and still unit tested but no longer used by the handler.) As of `PLAN_WEB_APPS.md` phase 5, `queue.ComputeAvailableSlotsForDay` sits on top of it: it resolves the requested date's `WashingPointSchedule` row into a `queue.DaySchedule` (real `businessLocation` instants, `queue.resolveDaySchedule`), splits it into one or two open sub-`Windows()` when a break is set, and runs the sweep-line function over each window separately, concatenating the results — this is what actually replaced the flat `open_time`/`close_time` bounds everywhere in this section below.

Input: `washing_point_id`, `service_id` (+ optional `price_option_id`, duration doesn't vary by price option), `date`.

1. Load washing point (`boxes_count`) and service (`duration_minutes`); resolve the requested date's weekday (`queue.weekdayIndex`, 0=Monday..6=Sunday) and its `WashingPointSchedule` row. A closed day, or a washing point with no schedule row for that weekday, immediately returns an empty result — never falls back to any assumed hours.
2. Load all non-canceled queue rows for that washing point whose interval overlaps `[schedule_open, schedule_close)` for the requested date, tagged with which `box_number` each occupies. No cross-midnight look-back is needed: since `close_time > open_time` is enforced at write time (both for the flat legacy columns and for schedule rows via `schedule.Manager.Replace`'s validation) and a booking can't be created past close, every booking is fully contained within a single calendar day by construction.
3. Generate candidate start times at a fixed step (15 minutes) from each open sub-window's start to its `close - duration_minutes` — one pass per window when a break splits the day into two.
4. For each candidate `[start, start+duration)`, compute which specific box numbers are free — occupied by no busy interval overlapping that window — and keep the candidate only if at least one is.
5. Return the list of `{start, end, available_boxes}`, letting the client offer the user a choice of box rather than the server picking one.

Booking creation (`POST /queue`, `queue.Manager.CreateBooking`) re-checks the client-supplied `box_number` inside a DB transaction, before insert, to avoid race conditions between two users booking the same box concurrently. Rather than locking the individual overlapping `queue` rows (which wouldn't help — there may be no existing row yet to lock for a currently-empty box), the transaction takes a `SELECT ... FOR UPDATE` lock on the *washing point* row itself, fully serializing concurrent booking attempts for that washing point: the second transaction only proceeds — and re-reads the now-current set of busy bookings — after the first commits.

As a second line of defense against that same race, the `queue` table also has a Postgres `EXCLUDE` constraint (via `btree_gist`) that makes it physically impossible for two non-canceled rows to share the same `(washing_point_id, box_number)` over overlapping `[scheduled_start_at, scheduled_end_at)` ranges — so even if the application-level check ever raced, the DB rejects the conflicting insert outright rather than silently double-booking a box. `cmd/seed` includes a self-check that verifies this constraint fires as expected.
