# Plan: push notifications for the customer app (FCM)

Status: **approved and implemented (2026-09-26)**, except real-FCM verification, which needs the user's Firebase project — see progress.md (docs/rules/general.md §12). No code written yet.
Spans `q-wash-api` (this plan) and `q-wash` (Flutter app).

## Goal

The customer's phone gets a push notification at each stage of their booking:

1. **Reminder** — about 1 hour before `scheduled_start_at`, while the booking is still active.
2. **Late start** — at `scheduled_start_at + 5 min`, if the booking has not started washing yet
   (status still `queue` or `waiting`).
3. **Started** — the moment staff move the booking to `washing`.
4. **Finished** — the moment staff move the booking to `ready` ("car is ready, you can pick it up").

These four are the whole scope for now (per the user's answer). Delivery is real push via
Firebase Cloud Messaging, so it works with the app closed.

## Scope

In:
- Device-token registration (app → API) and cleanup of dead tokens.
- Sending via FCM HTTP v1.
- Event notifications (3 & 4) sent from the status-change path.
- Time-based notifications (1 & 2) via a small in-process background scheduler in the API.
- Notification records kept in the existing `notifications` table so `GET /me/notifications`
  keeps working; a `kind` column dedupes so a stage is never sent twice for a booking.
- Flutter: Firebase setup, ask notification permission, register/refresh/unregister token,
  show foreground notifications, tap opens the queue screen.

Out (not now):
- Notifying on cancel / no-show / arrival, marketing pushes, per-user notification settings.
- In-app notification inbox UI (records exist in the API; no screen).
- Staff/worker web apps getting notifications.
- Real SMS provider (the SMS stub stays as is).
- Notifying clients created by staff as walk-ins who never installed the app (no token → skipped).

## Decisions (defaults — say if any is wrong)

- **Stage 2 wording/condition:** "your slot started 5 minutes ago — please come" only when status
  is `queue` or `waiting`; never for `washing`/`ready`/`canceled`/`no_show`.
- **Reminder skipped** when the booking is created less than 1 hour before its start (nothing to
  remind about; the user just made it). Stage 2 still applies.
- **Dedupe:** at most one notification per (booking, kind); kinds: `reminder_1h`, `late_5m`,
  `started`, `finished`.
- **Cancel/no-show/restore** before a scheduled stage fires suppresses it (the sweep re-checks
  the booking status at send time).
- **Text:** Russian, short, e.g. «Через час ваша запись в Pegasus Detailing · 14:00», «Ваша запись
  началась 5 минут назад — подъезжайте к боксу», «Мойка началась», «Авто готово — можно забирать».
- **Timezone:** times shown in Asia/Dushanbe, as everywhere else.
- **Delivery failures** never fail the status change (best-effort, logged), same as today's SMS
  path.

## Contracts / schema touched (need explicit approval per CLAUDE.md)

Migration `000024_push_notifications`:
- New table `device_tokens` (`id`, `user_id` → users, `token` unique, `platform` android|ios,
  `created_at`, `updated_at`).
- `notifications.channel` CHECK widened to `('sms','push')`; new nullable `kind varchar(32)` +
  unique index on `(queue_id, kind)` where `kind IS NOT NULL` (dedupe).

New endpoints (documented in `docs/API.md` + `openapi.yaml`):
- `PUT /me/devices` — body `{token, platform}`; upsert for the caller.
- `DELETE /me/devices/{token}` — on logout.
No change to existing endpoints' shapes.

New config (env): `FCM_PROJECT_ID`, `FCM_SERVICE_ACCOUNT_JSON` (path or inline). If unset the
API runs with push disabled (logs once) — local dev and tests need no Firebase.

## Steps

API (`q-wash-api`):
1. Migration 000024 + models/repos for device tokens; notification `kind`/channel.
2. `internal/platform/push` — `Sender` interface (like `sms.Sender`) with an FCM HTTP v1
   implementation (OAuth via `golang.org/x/oauth2/google` + `net/http`, no full Firebase Admin
   SDK) and a no-op/fake for tests; prunes tokens FCM reports as unregistered.
3. `PUT/DELETE /me/devices` handlers + tests.
4. Hook stages 3 & 4 into the booking status-change path in `queue.Manager` (via a small
   notifier interface so `queue` doesn't import `notification`), tests.
5. Scheduler: goroutine started from `app.go`, ticks every 30–60 s, finds active bookings that are
   due for `reminder_1h` / `late_5m` and not yet notified (uses the unique index so several API
   instances can't double-send), re-checks status, sends. Stopped on shutdown. Tests with a fake
   clock/sender.
6. Docs: API.md, openapi.yaml, DATA_MODEL.md, PROGRESS.md; `.env.example`.

App (`q-wash`):
7. Add `firebase_core`, `firebase_messaging`, `flutter_local_notifications` (foreground display);
   docs/techstack.md + docs/links.md updated.
8. Platform config: `google-services.json` (Android), `GoogleService-Info.plist` + push
   capability/APNs (iOS).
9. Token service: request permission, register after login, refresh on rotation, unregister on
   logout; tap handler opens the Queue tab.
10. Tests (token registration flow with a faked API/messaging boundary), `flutter analyze`.

## Risks

- **Needs things only you can provide** (below) — code is written against them but real delivery
  can't be verified without them.
- iOS push needs a paid Apple Developer account + APNs key uploaded to Firebase.
- The scheduler is in-process: fine for one API instance; the unique index keeps multiple
  instances safe from duplicates, at the cost of a little wasted polling.
- Adding dependencies (Flutter: 3; Go: `golang.org/x/oauth2`) — needs approval.

## Open questions (must be answered before coding)

1. **Approve** the schema/API changes and new dependencies above?
2. **Firebase project:** will you create it and give me (a) `google-services.json` /
   `GoogleService-Info.plist` for the app, and (b) a service-account JSON for the server (kept
   out of git, provided via env in deploy)? Until then I can build everything against a fake
   sender and leave FCM disabled.
3. **Deploy:** the server env vars go into your `.env`/compose on the server — OK that I only
   document them (I will not touch deploy config or CI)?
4. Are the defaults in "Decisions" right (especially: no reminder if booked <1 h ahead; late
   notice only while still `queue`/`waiting`)?
