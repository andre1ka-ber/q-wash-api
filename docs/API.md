# API Contract

Base path: `/api/v1`. JSON in/out. Auth via `Authorization: Bearer <access_token>` unless marked public.

Status: skeleton — filled in as each phase lands. Request/response bodies get exact field lists once the corresponding phase is implemented.

This is the prose companion to the machine-readable spec: [`openapi.yaml`](openapi.yaml) (OpenAPI 3.0.3), served at runtime as `GET /openapi.yaml` with an interactive Swagger UI at `GET /docs`. The two are meant to agree; if they ever drift, this file's "why" (rationale, edge cases, non-obvious defaults) still belongs here even after everything is implemented — the spec captures shapes and status codes, not reasoning.

## Auth — implemented (Phase 3)

| method | path | notes |
|---|---|---|
| POST | `/auth/otp/request` | body: `{phone_number}` (E.164, e.g. `+15551234567`). 202 `{status:"otp_sent"}`. Rate-limited: 429 `otp_cooldown` if an unconsumed code for that phone was issued within the last `OTP_COOLDOWN` (default 60s). |
| POST | `/auth/otp/verify` | body: `{phone_number, code}` (code: 6 digits). Finds-or-creates the user (default role `customer`), returns a token pair. Errors: 400 `otp_invalid`/`otp_expired`, 429 `otp_locked` after `OTP_MAX_ATTEMPTS` (default 5) wrong tries on the same code. |
| POST | `/auth/login` | body: `{username, password}`. Username/password path for **staff/admin only** — the queue board and staff panel, not the customer flow, which stays phone+OTP. 400 `invalid_credentials` if either field is missing; 401 `invalid_credentials` for a wrong password *or* an unknown username (deliberately the same code/message for both, to avoid confirming which usernames exist); 403 `forbidden` if the credentials are correct but the account's role isn't `staff`/`admin`. Returns the same token-pair shape as OTP verify/refresh. |
| POST | `/auth/refresh` | body: `{refresh_token}`. Rotates: old token is revoked, a new pair is issued. 400 `refresh_token_required` if missing; 401 `invalid_refresh_token` if unknown/expired/already-revoked — distinct codes so a client can tell "you forgot to send it" from "the token you sent isn't valid." |
| POST | `/auth/logout` | auth required. body (optional): `{refresh_token}` — revokes that one, or every active refresh token for the user if omitted. 204 No Content. Idempotent. |

Token pair response shape (returned by verify/login/refresh):
```json
{
  "access_token": "...", "access_token_expires_at": "...",
  "refresh_token": "...", "refresh_token_expires_at": "...",
  "user": { "id": "...", "phone_number": "...", "name": null, "role": "customer", "washing_point_id": null, "last_login_at": "..." }
}
```

`washing_point_id` is set only for `staff`/`worker` accounts (which point they're scoped to); omitted (`null`) for `customer`/`admin`. Added so staff-facing apps (e.g. `q-wash-cabinet`) can discover their own point without a point-picker UI.

There is no API endpoint to *set* a username/password for a user — same as role promotion, it's a direct-DB operation by design (see `docs/DATA_MODEL.md`). `cmd/seed` sets dev-only credentials for the seeded admin/staff accounts (see its output).
Access tokens are JWTs (HS256, 15m default TTL) carrying `uid`/`role` claims, verified statelessly (no DB hit) by `auth.RequireAuth` middleware. Refresh tokens are opaque random strings, stored HMAC-hashed, never JWTs — so individual sessions can be revoked. Role-gated routes (Phase 4+) use `auth.RequireRole("staff", "admin")` chained after `RequireAuth`.

## Users — implemented (Phase 3)

| method | path | role | notes |
|---|---|---|---|
| GET | `/me` | any authenticated | current user profile |
| PATCH | `/me` | any authenticated | body: `{name}`, required, 1-255 chars |

## Washing points — implemented (Phases 4 & 7)

| method | path | role | notes |
|---|---|---|---|
| GET | `/washing-points` | public | `{items: [...]}`, all points (any status), ordered by name |
| GET | `/washing-points/{id}` | public | detail; 404 `washing_point_not_found` |
| POST | `/washing-points` | admin | body: `{name, address, latitude, longitude, boxes_count?, open_time?, close_time?, owner_id?, status?, description?, amenities?}`. Defaults: `boxes_count=2`, `open_time="08:00"`, `close_time="20:00"`, `status="active"` (accepts any valid status if given, e.g. to create directly into `pending_review`). Admin-only (403 for staff) — creating a point directly bypasses the connection-request onboarding flow, so it's reserved for admin; staff go through `POST /connection-requests` + approval instead. |
| PATCH | `/washing-points/{id}` | staff, admin | any subset of the create fields (`owner_id`/`description`/`amenities` included) plus `status` (`active`/`paused`/`pending_review`, 400 `invalid_status` otherwise); re-validates the resulting record (e.g. `close_time > open_time`). `owner_id: ""` clears it. Staff may only act on their own point (`User.washing_point_id`) — 404 `washing_point_not_found` for any other point, same as a nonexistent id. Admin has no such restriction. |
| DELETE | `/washing-points/{id}` | staff, admin | sets `status=paused` (no row deletion); 204. Same own-point-only restriction on staff as PATCH. |
| GET | `/washing-points/{id}/availability` | public | query: `service_id` (uuid), `date` (`YYYY-MM-DD`, interpreted as a calendar day in the business timezone, see below). Returns `{items: [{start, end, available_boxes}, ...]}`, RFC3339 timestamps (absolute instants — clients should convert to local time for display, not string-match), stepped every 15 minutes across that date's resolved schedule window(s) (see "Per-weekday schedule" below) — two disjoint windows on a day with a break, none on a closed day. A slot appears only if the service's `duration_minutes` fits before the window's close and at least one box is free (and open — see "Boxes" below) for the whole `[start, end)`; `available_boxes` lists which specific box numbers those are, so the client can offer a box choice rather than the server picking one. A box closed via `PATCH .../boxes/{boxId}` never appears in `available_boxes`, treated as booked for the whole day rather than changing the sweep-line algorithm itself. 400 `invalid_service_id`/`invalid_date`; 400 `service_not_at_washing_point` if the service belongs to a different washing point; 404 if the washing point or service doesn't exist. Algorithm: `internal/queue/availability.go` (`ComputeAvailableSlotsWithBoxes`/`ComputeAvailableSlotsForDay`, unit tested in `availability_test.go`). |

Scheduling has no per-point timezone field yet (single washing point, single market) — schedule rows' `open_time`/`close_time`/`break_start`/`break_end` are `HH:MM` strings interpreted in a fixed `Asia/Dushanbe` (UTC+5, no DST) constant (`businessLocation` in `internal/queue/handler.go`), matching the app's current market (Tajikistan). All `queue`/`availability` timestamps on the wire are still real RFC3339 instants (Postgres `timestamptz`), just anchored to that timezone rather than UTC when derived from a schedule row. Revisit (per-point timezone field) when scaling to multiple points/regions.

### Per-weekday schedule — implemented (`docs/PLAN_WEB_APPS.md` phase 5)

As of phase 5, `GET .../availability` and `POST /queue`'s operating-hours
check read **only** `internal/schedule`'s per-weekday rows — the legacy
`open_time`/`close_time` fields on `WashingPoint` itself (still present on
the `POST`/`PATCH /washing-points` bodies above, unchanged) are no longer
consulted by any booking logic; they're kept only as the initial values a
newly created point's schedule is seeded from (see below), and are
otherwise informational. Editing them via `PATCH /washing-points/{id}`
does **not** change the schedule — use `PUT .../schedule` for that. Every
washing point created via `POST /washing-points` or connection-request
approval is automatically seeded with a bookable default (every day,
`open_time`–`close_time` from the create body, no break) so it's never
left unbookable; a washing point with genuinely zero schedule rows (should
never happen through normal use) is treated as closed every day, not as
falling back to any default hours.

| method | path | role | notes |
|---|---|---|---|
| GET | `/washing-points/{id}/schedule` | public | `{items: [...]}`, exactly 7 rows ordered by `weekday` (`0`=Monday..`6`=Sunday) |
| PUT | `/washing-points/{id}/schedule` | staff, admin | body: `{items: [{weekday, is_open, open_time?, close_time?, break_start?, break_end?}, ...]}`, exactly 7 rows, one per weekday, atomically replacing the full week (not a per-row upsert). `open_time`/`close_time` required when `is_open` is `true`; `break_start`/`break_end` are optional but must both be present or both absent, and must fall within `open_time`–`close_time`. Staff may only replace their own point's schedule — 404 `washing_point_not_found` otherwise, same as every other staff-gated resource. |

Schedule row shape:
```json
{"weekday": 0, "is_open": true, "open_time": "08:00", "close_time": "20:00", "break_start": null, "break_end": null}
```
`break_start`/`break_end` (and `open_time`/`close_time` on a closed day) are
omitted from the JSON body when unset, rather than sent as `null`.

Validation errors: 400 `invalid_schedule` (not exactly 7 rows),
`invalid_weekday` (outside 0–6), `duplicate_weekday`, `invalid_hours`
(missing/malformed/inverted `open_time`/`close_time` on an open day),
`invalid_break` (only one of `break_start`/`break_end` set, malformed,
inverted, or outside `open_time`–`close_time`).

Validation errors: `invalid_name`, `invalid_address`, `invalid_latitude` (±90), `invalid_longitude` (±180), `invalid_boxes_count` (≥1), `invalid_open_time`/`invalid_close_time` (`HH:MM` 24h), `invalid_hours` (`close_time` must be after `open_time`), `invalid_owner_id` (uuid parse), `invalid_status` — all 400.

Response (`GET`/`POST`/`PATCH /washing-points...`): `owner_id`, `description` and `amenities` are omitted from the JSON body when unset (`null`/empty), rather than sent as `null`/`[]`.

### Boxes — implemented (`docs/PLAN_WEB_APPS.md` phase 6)

Backs the cabinet app's "Боксы" tab. `WashingPoint.boxes_count` stays the
capacity number `GET .../availability`/`POST /queue` validate `box_number`
against; a `Box` row is metadata (a display `label`) plus an `is_open` flag
layered on top of one of those numbered slots — closing one drops it out of
`available_boxes` (see above) without lowering `boxes_count` itself. Every
washing point created via `POST /washing-points` or connection-request
approval is automatically seeded with `boxes_count` open boxes, numbered
`1..boxes_count`, same "never left with nothing to list" reasoning as the
schedule auto-seed above.

| method | path | role | notes |
|---|---|---|---|
| GET | `/washing-points/{id}/boxes` | public | `{items: [...]}`, ordered by `number` |
| POST | `/washing-points/{id}/boxes` | staff, admin | body: `{label?}`. `number` is always server-assigned (one past the current highest for that point, `1` if none) — never client-chosen, so there's no way to open a gap or collide. Starts `is_open: true`. |
| PATCH | `/washing-points/{id}/boxes/{boxId}` | staff, admin | body: `{label?, is_open?}`. `number` can't be changed after creation. |
| DELETE | `/washing-points/{id}/boxes/{boxId}` | staff, admin | 204, hard delete. Does **not** change `boxes_count` — that's a separate field on the washing point itself (`PATCH /washing-points/{id}`), so deleting a box doesn't silently shrink capacity or vice versa. |

Staff may only act on their own washing point — 404
`washing_point_not_found`/`box_not_found` for another point's boxes or a
nonexistent id, same ownership-hiding pattern as photos/schedule; a box id
addressed through a different point's URL prefix than the one it actually
belongs to also 404s. Validation errors: 400 `invalid_label` (>255 chars).

Box response:
```json
{"id": "...", "number": 1, "label": "Detailing lift", "is_open": true}
```
`label` is omitted from the JSON body when unset, rather than sent as `null`.

`POST /queue`'s `box_number` is additionally checked against the matching
`Box` row (if one exists) as of this phase: 409 `box_closed` if it's closed.
A `box_number` with no matching row at all (e.g. an older point never
backfilled) fails open — not rejected on that basis.

## Photos — implemented (`docs/PLAN_WEB_APPS.md` phase 4)

Backs the cabinet app's "photos & description" tab. Files are stored via
`internal/platform/storage.Storage` — the only implementation today is a
dev-only `LocalDisk` (writes to `UPLOADS_DIR`, served back at
`UPLOADS_BASE_URL`, default `/uploads`; not multi-instance safe), swappable
for S3/GCS later with no change to any caller.

| method | path | role | notes |
|---|---|---|---|
| GET | `/washing-points/{id}/photos` | public | `{items: [...]}`, ordered by `sort_order` then `created_at` |
| POST | `/washing-points/{id}/photos` | staff, admin | `multipart/form-data`: a `file` field (required, ≤10MB) and an optional `is_cover` field (`"true"` to request cover status). The uploaded bytes are content-sniffed server-side (never trusting the client's declared content type or filename extension) and only JPEG/PNG/GIF/WebP are accepted — `image/svg+xml` is deliberately rejected, since an SVG can embed `<script>` and would be a stored-XSS vector if served back as-is. The first photo for a point is always the cover regardless of `is_cover`; a later `is_cover: true` unsets any existing cover first, so exactly one stays true. |
| PATCH | `/washing-points/{id}/photos/{photoId}` | staff, admin | body: `{is_cover?, sort_order?}`. `is_cover: true` promotes it (unsetting the previous cover); there's no way to unset the sole cover directly — promote another one instead. |
| DELETE | `/washing-points/{id}/photos/{photoId}` | staff, admin | 204. The underlying file is best-effort deleted (a storage error there doesn't fail the request — the DB row is the source of truth). If the deleted photo was the cover and others remain, the oldest remaining one is auto-promoted. |

Staff may only act on their own washing point (`User.washing_point_id`) —
404 `washing_point_not_found`/`photo_not_found` for another point's photos
or a nonexistent id, same ownership-hiding pattern as every other
staff-gated resource; a photo id addressed through a *different* point's
URL prefix than the one it actually belongs to also 404s. Validation
errors: 400 `invalid_file` (missing/unparseable `file` field),
`invalid_content_type` (not one of the four accepted image types),
`invalid_sort_order` (negative); 413 `file_too_large` (>10MB).

Photo response:
```json
{"id": "...", "url": "/uploads/...", "is_cover": true, "sort_order": 0}
```

## Services — implemented (Phase 5)

| method | path | role | notes |
|---|---|---|---|
| GET | `/washing-points/{id}/services` | public | `{items: [...]}`, each with nested `price_options` |
| GET | `/services/{id}` | public | detail; 404 `service_not_found` |
| POST | `/washing-points/{id}/services` | staff, admin | body: `{name, description?, duration_minutes, picture_url?, price_options: [{name, price_cents, is_default?}, ...]}`. `price_options` non-empty is required; if none is marked `is_default`, the first is auto-promoted; more than one marked default is 400 `multiple_default_price_options`. |
| PATCH | `/services/{id}` | staff, admin | partial update of the service's own fields (not price options) |
| DELETE | `/services/{id}` | staff, admin | sets `is_active=false`; 204 |
| POST | `/services/{id}/price-options` | staff, admin | body: `{name, price_cents, is_default?}`. If `is_default: true`, any existing default for that service is unset first. |
| PATCH | `/price-options/{id}` | staff, admin | partial update. Explicitly setting `is_default: false` on the current sole default is rejected — 400 `cannot_unset_default`; mark another one default instead. |
| DELETE | `/price-options/{id}` | staff, admin | 409 `last_price_option` if it's the service's only option; 409 `price_option_in_use` if any `queue` row references it. If the deleted option was the default and others remain, the oldest remaining one is auto-promoted to default. |

Every row above is also scoped to the caller's own washing point for staff
(resolved from the service, or the price option's parent service, back to
its `washing_point_id`) — 404 `service_not_found`/`price_option_not_found`
for another point's resource, same as a nonexistent id. Admin has no such
restriction.

Validation errors mirror washing points' style: `invalid_name`, `invalid_duration`, `invalid_description` (>2000 chars), `invalid_picture_url` (>500 chars), `price_options_required`, `invalid_price_option_name`, `invalid_price_cents` (≥0) — all 400.

## Cars — implemented (Phase 6)

| method | path | role | notes |
|---|---|---|---|
| GET | `/me/cars` | any authenticated | `{items: [...]}`, only the caller's own cars |
| POST | `/me/cars` | any authenticated | body: `{name}` |
| PATCH | `/cars/{id}` | owner | body: `{name}`; ownership enforced — a non-owner (or nonexistent id) both 404 `car_not_found`, never 403, so existence isn't leaked |
| DELETE | `/cars/{id}` | owner | 409 `car_in_use` if any `queue` row references it; otherwise a real row delete (cars have no `is_active` field) |

## Queue — implemented (Phases 8 & 9; pause/resume, broadened cancel, live-boxes, date filter added `docs/PLAN_WEB_APPS.md` phase 7)

Two role groups gate this section, not one: `requireStaff` (staff, admin
only — the network-wide board and, implicitly, everything management-side
elsewhere in this doc) and `requireQueueOps` (staff, **worker**, admin —
the per-point live surface a shift technician actually needs: the board,
status, pause/resume, and live-boxes). A `worker` account can do
everything marked `staff, worker, admin` below and nothing marked plainly
`staff, admin`.

| method | path | role | notes |
|---|---|---|---|
| GET | `/queue` | staff, admin | network-wide counterpart to `GET /washing-points/{id}/queue` — same live-board shape (`queue`/`waiting`/`washing` bookings), but not scoped to a path-level `{id}`, and **not** date-filtered (unlike the per-point endpoint below). `admin` may pass `?washing_point_id=` to scope it or omit it for every point at once; `staff` are always forced to their own `washing_point_id` regardless of the query param — `worker` cannot reach this endpoint at all (not in `requireStaff`). 400 `invalid_washing_point_id` for a malformed uuid. |
| POST | `/queue` | any authenticated | body: `{car_id, service_id, price_option_id, box_number, scheduled_start_at, notes?}`. `box_number` is client-chosen (see the washing point's `boxes_count` and the availability endpoint's `available_boxes`) — the server validates it, never auto-assigns. `scheduled_start_at` is RFC3339 and must be in the future. `washing_point_id` is *not* in the body — derived from `service_id`. Not role-restricted to "customer": any authenticated user (including staff) can book for themselves, matching how cars are ownership- not role-scoped. A user may have at most one active (`queue`/`waiting`/`washing`) booking at a time — 409 `active_booking_exists` otherwise; cancel or wait for it to complete first. |
| GET | `/queue/{id}` | owner, staff, admin | detail; anyone else 404s `queue_not_found` (never 403 — doesn't confirm the id exists). For staff, "anyone else" includes another washing point's staff — scoped to the booking's own point, same as every other staff-gated queue endpoint below. |
| GET | `/queue/{id}/events` | owner | `text/event-stream`. Pushes the booking (same shape as `GET /queue/{id}`) immediately, then again whenever anything changes for its washing point (a booking created/canceled/advanced there), until the booking reaches `ready`/`canceled` or the client disconnects. A `: ping` comment is sent every 25s to keep the connection alive through proxies. Same ownership rule as `GET /queue/{id}` (404, not 403, for a non-owner). |
| PATCH | `/queue/{id}/cancel` | owner, **staff, worker, admin** | only while status is `queue`/`waiting`; 409 `cannot_cancel` otherwise, including re-canceling. As of phase 7, not owner-only any more — staff/worker/admin at the booking's own washing point may also cancel it (the worker app's "Снять" no-show action), scoped the same way every other staff-gated queue endpoint is (404 `queue_not_found`, not 403, for a different point's staff). |
| PATCH | `/queue/{id}/status` | staff, worker, admin | body: `{status}`, one of `waiting`/`washing`/`ready` (never `queue` or `canceled` — 400 `invalid_status` for anything else). Forward-only, one step at a time: 409 `invalid_status_transition` on a skip, a backward move, or any move from `ready`/`canceled`. Scoped to the caller's own washing point — 404 `queue_not_found` otherwise. |
| PATCH | `/queue/{id}/pause` | staff, worker, admin | toggles `paused_at` to now, without moving `status`. Only while `status = washing` — 409 `cannot_pause` if not washing or already paused. Same own-point scoping as `/status`. |
| PATCH | `/queue/{id}/resume` | staff, worker, admin | clears `paused_at`. Only while `status = washing` **and** currently paused — 409 `cannot_resume` otherwise. Same own-point scoping as `/status`. |
| GET | `/washing-points/{id}/queue` | staff, worker, admin | live board: bookings with status `queue`/`waiting`/`washing`, ordered by `scheduled_start_at`, **further scoped to one calendar day** as of phase 7 (`?date=YYYY-MM-DD`, defaulting to today in the business timezone when omitted — this narrowed the default from "every live booking regardless of date" to "today's"; no shipped app called this endpoint before phase 7, so nothing regressed). 400 `invalid_date` for a malformed value. A distinct, display-oriented shape (not the full `Booking` object) — see below. Scoped to the caller's own washing point — 404 `washing_point_not_found` for another point. |
| GET | `/washing-points/{id}/boxes/live` | staff, worker, admin | the worker app's box-cards screen: each of the point's boxes (see "Boxes" above) joined with whichever booking currently occupies it (`current`, `status=washing`) or, if free, the earliest still-upcoming one assigned to that box number (`next`) — unlike the board above, **not** date-filtered, since "what's happening right now" can span midnight. Scoped to the caller's own washing point — 404 `washing_point_not_found` for another point. |
| GET | `/me/queue` | any authenticated | paginated (`?page=&page_size=`, see Conventions below), all statuses, most-recently-scheduled first. Only the caller's own bookings — no role can see another user's history through this endpoint. |

Create validation errors (400 unless noted): `invalid_car_id`/`invalid_service_id`/`invalid_price_option_id` (uuid parse), `invalid_box_number` (outside `1..boxes_count`), `invalid_scheduled_start_at` (not RFC3339, or not in the future), `invalid_notes` (>2000 chars), `service_inactive`/`washing_point_inactive`, `invalid_price_option` (doesn't belong to the service), `outside_operating_hours` (as of phase 5, checked against the requested date's resolved per-weekday schedule window — see "Per-weekday schedule" above — not the legacy flat columns; a closed day or a request straddling a break both hit this code), 404 `car_not_found` (not owned or doesn't exist) / `service_not_found` / `washing_point_not_found`, 409 `slot_unavailable` (the requested box isn't free for some instant in the requested interval) / `box_closed` (the requested box exists and is closed, see "Boxes" above) / `active_booking_exists` (the caller already has a `queue`/`waiting`/`washing` booking, at this or any other washing point).

Booking response:
```json
{
  "id": "...", "status": "queue", "user_id": "...", "car_id": "...",
  "service_id": "...", "price_option_id": "...", "washing_point_id": "...",
  "box_number": 1, "scheduled_start_at": "...", "scheduled_end_at": "...",
  "notes": null, "canceled_at": null, "paused_at": null, "created_at": "...", "cars_ahead": 0
}
```
`cars_ahead` is the count of other bookings at the same washing point with
status `queue`/`waiting`/`washing` and an earlier `scheduled_start_at` —
enough for a "N cars ahead of you" UI without exposing whose bookings they
are. It's `0` once the booking itself reaches `ready`/`canceled`.
`paused_at` is omitted from the JSON body when unset, rather than sent as
`null`; only ever set while `status = washing`.

Race safety for `POST /queue`: the create transaction takes a row lock (`SELECT ... FOR UPDATE`) on the washing point for its duration, so two concurrent booking attempts for the same washing point are fully serialized — the second transaction only proceeds (and re-reads busy bookings for the requested box) after the first commits. The DB's `EXCLUDE` constraint on `queue` (see DATA_MODEL.md) is a last-resort backstop translated into the same `slot_unavailable` 409 if it ever fires despite the lock. The one-active-booking-per-user rule has its own DB-level backstop the same way: a partial `UNIQUE` index (`queue_one_active_booking_per_user`, see DATA_MODEL.md) catches the race the row lock doesn't cover — two of the *same* user's requests landing concurrently for *different* washing points — translated into the same `active_booking_exists` 409.

Queue board response (`GET /washing-points/{id}/queue`) — a separate, display-oriented shape, not the `Booking` object above. Built for a shared/public-facing screen (e.g. `pegasus-board`), so it deliberately omits `user_id`/`car_id`/full booking detail and never exposes a full phone number:
```json
{
  "id": "...", "status": "washing", "box_number": 1,
  "scheduled_start_at": "...", "scheduled_end_at": "...", "paused_at": null,
  "customer_phone_last4": "0003", "car_name": "Demo Car"
}
```
`customer_phone_last4` and `car_name` are batch-fetched (not per-row queries) from the booking's `user_id`/`car_id`. `paused_at` is omitted when unset, same as the booking response above.

Live-boxes response (`GET /washing-points/{id}/boxes/live`):
```json
{
  "items": [
    {"number": 1, "label": null, "is_open": true, "current": {
      "id": "...", "status": "washing", "service_name": "Full wash",
      "scheduled_start_at": "...", "scheduled_end_at": "...", "paused_at": null,
      "customer_phone_last4": "0003", "car_name": "Demo Car"
    }},
    {"number": 2, "label": null, "is_open": true}
  ]
}
```
A box with neither an occupying booking nor an upcoming one (like box 2
above) has neither `current` nor `next` in its item. `service_name`/
`customer_phone_last4`/`car_name` are batch-fetched the same way the queue
board's are.

## Admin — implemented (`docs/PLAN_WEB_APPS.md` phase 3)

Admin-only, same reasoning as the owners/connection-requests section below —
this is the network-wide admin app's own surface, not something staff at a
single point have any use for.

| method | path | role | notes |
|---|---|---|---|
| GET | `/admin/washing-points` | admin | `{items: [...]}`, ordered by name (same order as the public list). Richer than the public `GET /washing-points`: adds `owner_id`/`owner_name` and `services_count` (active services only, batch-counted). `services_count` and `boxes_count` are two different numbers — `boxes_count` is the point's box *capacity*, not a count of `Box` rows (the `Box` entity doesn't exist yet, `docs/PLAN_WEB_APPS.md` phase 6). |
| GET | `/admin/stats` | admin | network-wide dashboard numbers: `{points_total, points_active, bookings_today, canceled_today, average_utilization}`. "Today" is a calendar day in the `Asia/Dushanbe` business timezone (same constant `GET /washing-points/{id}/availability` uses). `average_utilization` is booked box-minutes today ÷ available box-minutes today (`boxes_count × open-hours`, summed over active points only) — a coarse ratio (0–1, can slightly exceed 1 if a booking straddles the day boundary and both halves land in the same day's window) until per-weekday schedules and per-box open/closed state (phases 5/6) make it exact. |

`GET /queue` (see the Queue section above) also gained a `?washing_point_id=`
filter usable only by `admin` — the network-wide live-queue view this
dashboard's per-point drill-down uses; staff/worker calling the same route
are always forced to their own point regardless of the query param.

## Notifications — implemented (Phase 10)

| method | path | role | notes |
|---|---|---|---|
| GET | `/me/notifications` | any authenticated | paginated, own records only, most recently created first |
| POST | `/notifications` | staff, admin | body: `{user_id, queue_id?, text, send_at?}`. `send_at` defaults to now (RFC3339 if given). If `queue_id` is given, staff may only reference a booking at their own washing point — 404 `queue_not_found` otherwise (checked after `queue_user_mismatch`, before sending). |

`POST /notifications` sends immediately through the same `sms.Sender` stub used for OTP (`status` becomes `sent`/`failed`, `sent_at` set) when `send_at` is now or in the past; a future `send_at` just creates the row as `status: "pending"` and leaves it there — **there is no background worker in this MVP** to sweep due notifications, so a scheduled-for-later notification will never actually send until a future phase adds one. The HTTP request always succeeds once the record is created, regardless of delivery outcome — delivery success/failure is a property of the record (`status`), not of the API call.

Validation: 400 `invalid_user_id`/`invalid_queue_id` (uuid parse), `invalid_text` (empty or >2000 chars), `invalid_send_at` (not RFC3339), `queue_user_mismatch` (`queue_id` doesn't belong to `user_id`); 404 `user_not_found`/`queue_not_found`.

## Owners & connection requests — implemented (`docs/PLAN_WEB_APPS.md` phase 2)

Both admin-only (not staff) — this is the network-wide admin app's surface;
staff are scoped to one point (`User.washing_point_id`) and have no
business managing another point's owner or the onboarding queue.

| method | path | role | notes |
|---|---|---|---|
| GET | `/owners` | admin | `{items: [...]}`, ordered by name |
| GET | `/owners/{id}` | admin | detail; 404 `owner_not_found` |
| POST | `/owners` | admin | body: `{name, contact_name?, contact_phone?, contact_email?}` |
| PATCH | `/owners/{id}` | admin | any subset of the create fields |
| GET | `/connection-requests` | admin | `{items: [...]}`, newest first. Optional `?status=new\|approved\|rejected` filter (400 `invalid_status` otherwise) |
| GET | `/connection-requests/{id}` | admin | detail; 404 `connection_request_not_found` |
| POST | `/connection-requests` | admin | body: `{business_name, contact_name, contact_phone, address, boxes_count, note?}`. Admin-created for now — no public self-service "apply" form yet. |
| PATCH | `/connection-requests/{id}` | admin | body: `{status: "approved"\|"rejected"}` — the only write on an existing request, no editing the submitted fields. 409 `connection_request_already_reviewed` if not still `new`. |

Approving (`status: "approved"`) creates an `Owner` (reusing one whose
`contact_phone` matches the request's, if any) and a `WashingPoint` with
`status = pending_review` and placeholder `latitude`/`longitude` of `0,0` —
the request doesn't collect coordinates, so an admin must set them via
`PATCH /washing-points/{id}` before the point can go live. Rejecting just
sets `status = rejected`; neither action is reversible through this
endpoint.

Validation: 400 `invalid_name` (owners); `invalid_business_name`/
`invalid_contact_name`/`invalid_contact_phone`/`invalid_address`/
`invalid_boxes_count`/`invalid_note` (connection requests).

## Conventions

- Errors: `{"error": {"code": "string", "message": "string"}}`, HTTP status matches the error class (400 validation, 401/403 auth, 404 not found, 409 conflict e.g. slot no longer available, 422 business-rule violation).
- Pagination: `?page=&page_size=` (both optional, 1-indexed; implemented in `httputil.ParsePagination`/`WritePaginated`, currently used by `GET /me/queue`). Defaults: `page=1`, `page_size=20`; `page_size` is clamped to 100. Invalid values (non-numeric, zero, negative) fall back to the default rather than erroring — pagination params are a convenience, not worth rejecting a request over. Response wraps the list as `{"items": [...], "page": N, "page_size": N, "total": N}`.
- Timestamps: RFC3339, real absolute instants. Most are UTC (Postgres `timestamptz` defaults); `queue.scheduled_start_at`/`scheduled_end_at` and `availability` slot `start`/`end` are anchored to the `Asia/Dushanbe` business timezone instead when derived from a washing point's `open_time`/`close_time` (see the Washing Points section) — clients should parse and convert to local time for display rather than assume a `Z` suffix.
