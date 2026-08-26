# Backend changes for the 4 staff/ops web apps — Plan

Status: draft, not yet implemented. Written 2026-08-20 alongside the plans
for four new web apps (`../../q-wash-admin`, `../../q-wash-cabinet`,
`../../q-wash-worker`, `../../q-wash-display`) that replace the throwaway
`pegasus-frontend`/`pegasus-board` test harnesses with real products, built
from a Claude Design mock (`Car Wash Web Apps.dc.html`, project "Car wash
queue app").

The mock assumes a **multi-point network with owners** and a **shift-worker
role**; the API today (per `DATA_MODEL.md`/`PLAN.md`) is deliberately
single-point, with only `customer`/`staff`/`admin` roles and no owner
entity. This doc is the gap-closing plan. Each of the four frontend
`PLAN.md`s links back to the specific section here it depends on.

## New/changed entities

### Owner
New table. A business that owns one or more washing points — separate from
`User` because an owner may not need their own login (staff still log in
per-point), and admin needs a place to hang contact info/network-wide
grouping regardless of whether any login exists for it yet.

| field | type | notes |
|---|---|---|
| id | uuid | PK |
| name | string | business name, e.g. "ООО «Титан»" |
| contact_name | string, nullable | |
| contact_phone | string, nullable | |
| contact_email | string, nullable | |
| created_at / updated_at | timestamp | |

Open question: does an owner ever get their own login (a mini network
dashboard across just their points)? Not in the mock, not planned now —
`owner_id` on `WashingPoint` anticipates it without committing to it.

### ConnectionRequest
New table. Backs the admin sidebar's "Заявки на подключение" (connection
requests) counter — a new washing point's owner applying to join the
network.

| field | type | notes |
|---|---|---|
| id | uuid | PK |
| business_name | string | |
| contact_name | string | |
| contact_phone | string | |
| address | string | |
| boxes_count | int | |
| note | string, nullable | |
| status | enum: `new`, `approved`, `rejected` | |
| reviewed_by | uuid, nullable | FK -> User (admin who acted) |
| reviewed_at | timestamp, nullable | |
| created_at / updated_at | timestamp | |

Approving creates (or reuses, matched by phone) an `Owner` row and a
`WashingPoint` row with `status = pending_review`. **Open question for the
user**: does an approved point need further admin setup (services/boxes/
hours) before it can flip to `active`, or should approval alone activate
it? The mock's "+ Новая мойка" wizard (3 steps: basic info → services →
[unseen step 3]) suggests setup happens either way, but doesn't disambiguate
whether that's a separate flow from connection-request approval. Flag
before building phase 2 below.

### Box
New table. Individual washing bays within a point — today only a
`boxes_count` int exists on `WashingPoint`, with no per-box identity.

| field | type | notes |
|---|---|---|
| id | uuid | PK |
| washing_point_id | uuid | FK -> WashingPoint |
| number | int | 1..boxes_count, unique per point |
| label | string, nullable | e.g. "Детейлинг · подъёмник" (mock's box "kind" text) |
| is_open | bool | default true — a closed box drops out of availability |
| created_at / updated_at | timestamp | |

`boxes_count` on `WashingPoint` stays as the capacity number driving
availability math; `Box` rows are metadata + open/closed state layered on
top. Availability (`ComputeAvailableSlotsWithBoxes`) gains one more filter:
exclude box numbers with `is_open = false`.

Deferred, not in v1: which services a box can perform (mock's "Услуги: 6 из
6" column implies a per-box service subset). All open boxes support all of
the point's active services for now — revisit if that turns out wrong.

### WashingPointSchedule
New table, replacing the single `open_time`/`close_time` columns with a
per-weekday schedule — `DATA_MODEL.md` already flagged this as the planned
extension point ("A per-weekday schedule table can be introduced later...
without breaking the availability API contract").

| field | type | notes |
|---|---|---|
| id | uuid | PK |
| washing_point_id | uuid | FK -> WashingPoint |
| weekday | int | 0=Monday..6=Sunday |
| is_open | bool | matches the mock's per-day toggle |
| open_time | varchar(5) "HH:MM", nullable | null when `is_open = false` |
| close_time | varchar(5) "HH:MM", nullable | |
| break_start | varchar(5) "HH:MM", nullable | mock shows one lunch-break window per day, optional |
| break_end | varchar(5) "HH:MM", nullable | |

Unique `(washing_point_id, weekday)`. Migration backfills 7 rows per
existing point from its current `open_time`/`close_time` (same every day,
`is_open = true`), matching current behavior exactly on cutover.

**This is the one real algorithmic change, not just additive CRUD**: the
sweep-line function `queue.ComputeAvailableSlotsWithBoxes` currently reads
one open/close pair per point; it needs to resolve the correct
`WashingPointSchedule` row for the requested date's weekday (in
`businessLocation`, per the existing per-point-timezone note) and use its
`open_time`/`close_time`/break window instead. Existing unit tests for the
sweep-line function need a same-every-day-schedule fixture added alongside
new per-weekday-variation fixtures.

**Done, with one deviation flagged**: the old `open_time`/`close_time`
columns on `WashingPoint` were meant to be dropped once the schedule table
became the source of truth ("no dual-write period, this is pre-launch"),
but were kept instead — dropping them would have meant reworking the
`POST`/`PATCH /washing-points` request/response DTOs (already documented
extensively across `API.md`/`openapi.yaml` by this point) in the same pass
as the genuinely correctness-critical algorithm change, widening this
already-highest-risk phase's blast radius for a reversible cleanup that
carries no functional cost left as-is. The columns are legacy/informational
only now — no booking logic reads them, and nothing dual-writes to them
from schedule changes — see `PROGRESS.md`'s phase 5 entry for the full
reasoning. Revisit dropping them as a small, low-risk follow-up once the
frontend apps built against this API are confirmed to not depend on them.

### WashingPointPhoto
New table, backs the cabinet app's "Фото и описание" tab.

| field | type | notes |
|---|---|---|
| id | uuid | PK |
| washing_point_id | uuid | FK -> WashingPoint |
| url | string | |
| is_cover | bool | default false, exactly one cover per point (same "auto-promote on delete" pattern as `ServicePriceOption.is_default`) |
| sort_order | int | |
| created_at | timestamp | |

Needs a storage backend behind an interface — mirrors the existing
`sms.Sender` stub pattern (`platform/sms`): a `platform/storage` package
with a `Storage` interface (`Put(io.Reader) (url string, err error)`), one
dev implementation that writes to local disk and serves it back via a
static route, swappable for S3/GCS later. **Open question for the user**:
is local-disk storage acceptable for this environment, or is there already
an object-storage target to use instead? Affects phase 4 below, nothing
else.

### WashingPoint — new columns
| field | type | notes |
|---|---|---|
| owner_id | uuid, nullable | FK -> Owner. Nullable through migration (existing seeded point has no owner yet); required going forward once an owner-assignment UX exists |
| status | enum: `active`, `paused`, `pending_review` | replaces `is_active bool` — migration maps `is_active = true` -> `active`, `false` -> `paused`. `pending_review` only reachable via connection-request approval |
| description | text, nullable | |
| amenities | text[], nullable | free-text tags (mock: "Зона ожидания", "Кофе", "Wi-Fi", ...) — a Postgres array, not a lookup table; revisit if amenities ever need to be filterable/coded rather than just displayed |

`is_active` is removed, not kept alongside `status` — every current caller
of it becomes a `status = 'active'` check.

### User — new role + point scoping
| field | type | notes |
|---|---|---|
| role | enum: `customer`, `staff`, `admin`, **`worker`** | adds a fourth role for shift technicians (mock: "Приложение мастера") |
| washing_point_id | uuid, nullable | FK -> WashingPoint. Set for `staff`/`worker` (which point they're scoped to); always null for `admin` (network-wide) and `customer` |

Migration backfills the existing seeded `staff` user's `washing_point_id`
to the one existing washing point.

Auth stays exactly as-is: `worker` logs in via the same
`POST /auth/login` (username+password) `staff`/`admin` already use — no new
auth mechanism. A lighter PIN-per-box kiosk login was considered (workers
may not want to type a full password on a shared shop tablet) but deferred;
flag if the user wants it revisited before phase 7.

### Queue — pause support, broadened cancel
| field | type | notes |
|---|---|---|
| paused_at | timestamp, nullable | only meaningful while `status = washing` |

Not a new enum value — the state machine's forward-only invariant
(`queue -> waiting -> washing -> ready`, `canceled` only from `queue`/
`waiting`) is a deliberate design constraint per `DATA_MODEL.md` and stays
untouched. Pause/resume toggles `paused_at` without moving `status`.

`PATCH /queue/{id}/cancel` is currently owner-only ("the only way to reach
`canceled`" per `DATA_MODEL.md`). The worker app's "Снять" (remove no-show)
action needs staff/worker/admin to also be able to cancel a booking at
their own point — broadens the RBAC check on that one endpoint, no schema
change.

## New/changed endpoints, by app

### `q-wash-admin`
- `GET/POST /owners`, `GET/PATCH /owners/{id}` — owner CRUD.
- `GET /admin/washing-points` — network-wide list with owner, status,
  box/service counts (richer than the existing public `GET
  /washing-points`, which stays public/customer-facing and unchanged).
- `POST /washing-points` / `PATCH /washing-points/{id}` — extended body:
  `owner_id`, `status`, `description`, `amenities`.
- `GET/POST /connection-requests`, `PATCH /connection-requests/{id}`
  (approve/reject).
- `GET /admin/stats` — points count/active count, bookings today
  network-wide, average utilization, cancellation count — new aggregation
  queries across `queue`/`washing_point`.
- `GET /queue` (existing) gains a `washing_point_id` filter param usable by
  `admin` role to scope or omit-for-all-points; `staff`/`worker` remain
  forced to their own `washing_point_id` regardless of the param.
- Deferred, not planned yet: a network-wide services catalog/template
  library (mock's "Услуги-справочник" nav item) — per-point `Service` CRUD
  already covers real functionality; a shared template library is a
  convenience layer on top, not blocking.

### `q-wash-cabinet`
- `PATCH /washing-points/{id}` — `description`, `amenities` (staff/admin,
  own point only for staff).
- `POST /washing-points/{id}/photos`, `DELETE .../photos/{photoId}`,
  `PATCH .../photos/{photoId}` (set cover / reorder).
- `GET/PUT /washing-points/{id}/schedule` — bulk-replace the 7
  `WashingPointSchedule` rows.
- `GET/POST/PATCH/DELETE /washing-points/{id}/boxes` — `Box` CRUD.
- Services/price options: existing endpoints already cover this tab, no
  change needed.

### `q-wash-worker`
- `GET /washing-points/{id}/boxes/live` — new: each `Box` joined with its
  current active `Queue` row (if any) and `paused_at`.
- `PATCH /queue/{id}/status` (existing) — advance queue -> washing ->
  ready.
- `PATCH /queue/{id}/pause`, `PATCH /queue/{id}/resume` — new, toggle
  `paused_at`, 409 if `status != washing`.
- `PATCH /queue/{id}/cancel` (existing, RBAC broadened per above) — the
  "Снять" no-show action.
- `GET /washing-points/{id}/queue` (existing) gains a `date` filter for
  "today's queue" (defaults to today in `businessLocation` if omitted).

### `q-wash-display`
Auth decided with the user: **no separate mechanism** — same
username+password `POST /auth/login` every other app uses, following
`pegasus-board`'s already-proven pattern exactly (log in once on the
kiosk device, the rotating refresh token keeps the unattended screen's
session alive indefinitely with no human ever re-entering credentials).
Simpler than the token-in-URL design this section originally proposed, and
already validated in production by `pegasus-board` — no new auth code path
needed in `q-wash-shared`, no token-rotation endpoint, no separate public
router group.
- `GET /washing-points/{id}/board` — new, staff/admin-gated (same
  `requireStaff` middleware as every other management route): a
  purpose-built summary shaped for the lobby screen — currently-serving
  boxes + waiting list + average wait. Even less data than the existing
  `GET /washing-points/{id}/queue` board endpoint already exposes (which
  shows last-4-phone-digits; the mock's display screen shows no customer
  identity at all, just car + service) — matches the existing privacy
  posture of never leaking one customer's data to another.
- `GET /washing-points/{id}/board/events` — optional SSE variant reusing
  the existing `internal/platform/eventbus` pub/sub from the customer
  queue-screen SSE work, so the lobby screen updates live instead of
  polling. Nice-to-have, not required for v1 — polling is a fine
  fallback, same as `pegasus-board` does today.

## Phased build order

- [x] **1 — Migrations**: `Owner`, `ConnectionRequest`, `Box`,
      `WashingPointSchedule`, `WashingPointPhoto`, `WashingPoint` new
      columns (`owner_id`, `status` replacing `is_active`, `description`,
      `amenities`), `User` new columns (`worker` role,
      `washing_point_id`), `Queue.paused_at`. Backfill existing rows
      (single point → 7 schedule rows, `is_active` → `status`, existing
      staff user → `washing_point_id`). (A `display_token` column was
      added and then removed in this same phase once phase 8's auth model
      changed — see the `q-wash-display` section above; never used by any
      endpoint.)
- [x] **2 — Owners + connection requests**: CRUD + approval flow. Decided
      with the user: approval leaves the point `pending_review`, not
      `active` — an admin must finish setup (services/boxes/hours, real
      lat/lon) before flipping it active via `PATCH /washing-points/{id}`.
- [x] **3 — Admin washing-point extensions**: `owner_id`/`status`/
      `description`/`amenities` on create/update, `GET
      /admin/washing-points`, `GET /admin/stats`, `GET /queue`
      network-wide filter.
- [x] **4 — Photos**: `platform/storage` interface + dev stub, photo CRUD
      endpoints. Went with local-disk storage for now (see the resolved
      open assumption below).
- [x] **5 — Per-weekday schedule**: `WashingPointSchedule` CRUD +
      availability rework. Went with a new `queue.ComputeAvailableSlotsForDay`
      layer on top of the existing (unchanged) sweep-line function rather
      than modifying it directly — resolves the schedule row into a
      `DaySchedule`, splits around a break into up to two windows, runs the
      original algorithm per window. `WashingPoint.open_time`/`close_time`
      were **kept, not dropped** (a deliberate, flagged deviation from this
      section's "no dual-write period" — see `PROGRESS.md`'s phase 5 entry
      for the reasoning) but are legacy/informational only as of this
      phase; every new point is auto-seeded with a default schedule from
      them on creation so nothing is ever left unbookable.
- [x] **6 — Boxes**: `Box` CRUD (`internal/box`, same repo/manager/handler
      shape as `photo`) + availability filter for `is_open = false`. The
      filter is layered onto the existing sweep-line algorithm rather than
      changing it: a closed box is added to `GET .../availability`'s `busy`
      list as a synthetic all-day interval, same "layered on top" framing
      the migration's own comment used — `ComputeAvailableSlotsWithBoxes`/
      `ComputeAvailableSlotsForDay` are untouched, so their existing unit
      tests needed no changes. `POST /queue` gained the matching check
      directly (409 `box_closed`) since a client picks `box_number`
      explicitly there, bypassing the availability endpoint's own
      suggestions. New points (`POST /washing-points`, connection-request
      approval) auto-seed `boxes_count` open boxes via a `BoxSeeder`
      interface mirroring `ScheduleSeeder` exactly (same
      can't-import-`internal/box`-from-`washingpoint`-or-
      `connectionrequest` constraint). `boxes_count` and `Box` rows stay
      independently updatable — deleting a box doesn't shrink `boxes_count`
      and vice versa; not attempted as a synced pair, flagged as a known,
      accepted gap rather than solved, since neither direction is required
      by anything in this doc. See `PROGRESS.md` for the full write-up.
- [x] **7 — Worker role**: `User.washing_point_id` + `worker` role were
      already in place since phase 1 (login already accepted `worker`) —
      what this phase actually added was a new, deliberately narrower RBAC
      group. Not a blanket widening of `requireStaff`: a new
      `requireQueueOps` (staff/worker/admin) middleware set gates only the
      per-point live surface a shift technician needs (queue board, status,
      pause/resume, live-boxes), while `requireStaff` (staff/admin only,
      unchanged) still gates washingpoint/service/photo/schedule/box
      management and the network-wide `GET /queue` — a worker was never
      meant to edit the services catalog. `PATCH /queue/{id}/pause`/
      `/resume` toggle the already-existing (since phase 1, unused until
      now) `Queue.PausedAt` column, 409 outside `status = washing` or on a
      redundant call. `PATCH /queue/{id}/cancel`'s ownership check moved
      from `queue.Manager` (owner-only) into `queue.Handler` (owner OR
      staff/worker/admin at the booking's own point), matching the layering
      every other staff-gated queue action already used. `GET
      /washing-points/{id}/queue` gained a `?date=` filter defaulting to
      today; new `GET /washing-points/{id}/boxes/live` (implemented on
      `queue.Handler`, not `box.Handler`, to reuse queue's booking-enrichment
      helpers without an import cycle) joins each box with its current or
      next booking. See `PROGRESS.md` for the full write-up.
- [ ] **8 — Display board**: `GET /washing-points/{id}/board` (staff/admin
      RBAC, same login every other app uses — no new auth mechanism, see
      the `q-wash-display` section above), optional SSE variant.
- [ ] **9 — Docs + tests**: `docs/API.md`, `docs/openapi.yaml`,
      `docs/DATA_MODEL.md` updated per phase (not batched at the end);
      unit tests throughout; integration tests for the new RBAC surface
      (worker role) alongside the existing auth/RBAC integration suite.

Each phase should leave the project compiling and `make test` clean, same
convention as `PLAN.md`. Log actual progress in `PROGRESS.md` as work
happens, not here.

## Open assumptions to revisit (collected from above)

- ~~Does connection-request approval alone activate a point, or does it
  stay `pending_review` pending further setup?~~ Resolved: stays
  `pending_review` (phase 2, done).
- ~~Local-disk photo storage acceptable for now, or is there an existing
  object-storage target?~~ Resolved by proceeding with local-disk storage
  (`internal/platform/storage.LocalDisk`) as originally proposed — the plan
  section above always described it as a swappable dev stub behind a
  `Storage` interface (same pattern as `sms.Sender`), so this was a low-risk
  default rather than a real fork in the road; revisit before a real
  multi-instance deployment, since `LocalDisk` has no such story.
- ~~Is a bare `display_token` in a URL acceptable for the display board?~~
  Resolved: dropped entirely — the display app authenticates the same way
  as every other app (username+password, `pegasus-board`'s proven
  unattended-screen pattern), no token model needed.
- Per-box service restriction (mock's "Услуги: N из M") — deferred
  entirely, not scheduled in any phase above.
- Owner self-service login — not planned, `owner_id` only anticipates it.
- PIN-per-box kiosk login for workers instead of username+password —
  deferred, flag if wanted before phase 7.
