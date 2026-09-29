# Plan: auto-create staff + worker credentials on washing point creation

## Goal

When a washing point is created — either by admin via `POST /washing-points`,
or via `connectionrequest.Manager.Approve` (self-service owner onboarding,
which also creates a `WashingPoint`) — automatically provision:

- one `staff`-role login (cabinet access), scoped to that point
- one `worker`-role login (worker app access), scoped to that point

Credentials (username + a one-time plaintext password) are returned in the
creation response only. Passwords are never stored or logged in plaintext,
never retrievable again — only re-issuable via an explicit admin-triggered
reset, which itself returns a new plaintext password once.

## Decisions made (confirmed with user)

- **Both roles**: one staff + one worker account per point, not staff-only.
- **Username**: derived from the point's name — slugified, ASCII, with a
  numeric suffix (`-2`, `-3`, ...) on collision. Worker username is the same
  slug with a `-worker` suffix (also collision-checked). No admin input
  needed at creation time.
- **Password**: auto-generated, shown once in the response. A separate
  admin-only reset endpoint regenerates and re-reveals a password for an
  existing staff/worker account (e.g. if the first reveal was lost).
- **Connection-request approval**: gets the same treatment — same
  provisioning call, same one-time reveal in its response.
- **Phone number**: `user.User.PhoneNumber` is currently `NOT NULL UNIQUE`,
  but the platform's own auth model is customers-by-phone /
  staff-worker-admin-by-username — phone is meaningless for these accounts.
  Confirmed: migrate `phone_number` to nullable (DB-level), enforce
  "customers must have a phone" at the application layer instead (the OTP
  registration path already always supplies one, so no behavior change
  there — this only removes a constraint that never should have applied to
  staff/worker/admin rows).

## Scope

In scope:
- New migration `000025_...`: `users.phone_number` → nullable.
- `internal/user`: unique-username generation helper; the two-account
  provisioning function (staff + worker) reusable from both call sites.
- A small password-generation helper (`crypto/rand`-based) and a shared
  `HashPassword` helper (bcrypt) to stop the hash-inlined-at-every-call-site
  pattern (`cmd/seed`, test helper) — new code uses it, existing call sites
  left alone unless trivial to switch.
- `internal/washingpoint`: wrap `create()` in a transaction (currently
  unguarded — a mid-sequence failure today leaves a half-provisioned point;
  fixing this is required to safely add a 3rd/4th write to the same
  sequence, not optional cleanup).
- `internal/connectionrequest`: same transaction-wrapping for `Approve`
  (same existing atomicity gap), same provisioning call.
- New admin endpoints (`internal/admin`, admin-only):
  - `GET /admin/washing-points/{id}/credentials` → `{staff: {username},
    worker: {username}}` (usernames only, for display — no passwords).
  - `POST /admin/washing-points/{id}/credentials/{role}/reset`
    (`role` = `staff`|`worker`) → generates + sets a new password, revokes
    that user's existing refresh tokens (reusing
    `auth.Repository.RevokeAllRefreshTokensForUser`, already used by
    logout — forces re-login with the new password), returns
    `{username, password}` once.
- `POST /washing-points` response gains a `credentials` field (staff +
  worker, one-time).
- `connectionrequest.Approve`'s response gains the same `credentials` field.
- Docs: `API.md`, `openapi.yaml`, `DATA_MODEL.md`, this repo's
  `PROGRESS.md`.
- `q-wash-shared`: types for the new response fields + a `resetCredentials`
  API wrapper + a credentials-fetch wrapper.
- `q-wash-admin`:
  - `NewPointDrawer.tsx`: one-time "save these now" modal showing both
    accounts after a successful create.
  - `EditPointDrawer.tsx`: new "Учётные данные" section — both usernames,
    a "Сбросить пароль" button per role, opening the same one-time reveal
    modal.
  - `ConnectionRequestDrawer.tsx`: same one-time reveal after approving a
    request.

Out of scope (explicitly not doing):
- Any UI/endpoint to change a *username* after creation (only password
  reset).
- PIN-per-box kiosk login (already noted elsewhere as deferred).
- Multiple staff/worker accounts per point (still exactly one of each,
  matches current `WashingPointID` scoping-to-one-point model — adding
  more accounts per point is a separate future feature).
- Touching `q-wash-cabinet`/`q-wash-worker` login flows themselves — they
  already do username+password login; nothing changes for them.

## Steps

1. **Migration** `migrations/000025_users_phone_optional.{up,down}.sql`:
   `ALTER TABLE users ALTER COLUMN phone_number DROP NOT NULL` (up),
   `SET NOT NULL` (down — down migration will fail if any staff/worker rows
   with a null phone exist by then, which is expected/acceptable for a
   down migration). Update `user.User.PhoneNumber` struct tag
   (`not null` → dropped) and its doc comment.
2. **`internal/user`**: add
   - `GenerateUniquePassword() (string, error)` (or similarly named) —
     `crypto/rand`, fixed length, avoids visually-ambiguous characters.
   - `(r *Repository) uniqueUsername(ctx, base string) (string, error)` —
     tries `base`, then `base-2`, `base-3`, ... via `FindByUsername` until
     one 404s.
   - `ProvisionPointAccounts(ctx context.Context, tx *gorm.DB,
     washingPointID uuid.UUID, pointName string) (StaffWorkerCreds, error)`
     — creates both `User` rows (role staff/worker, `WashingPointID` set,
     `PhoneNumber` nil, generated username, bcrypt-hashed generated
     password) against the given transaction handle, returns the plaintext
     pair for the caller to surface once.
3. **`internal/auth`**: add `HashPassword(password string) (string,
   error)` wrapping the existing inline `bcrypt.GenerateFromPassword`
   pattern, used by the new provisioning code and the new reset endpoint.
4. **`internal/washingpoint`**: wrap `create()`'s body in
   `db.Transaction(func(tx *gorm.DB) error {...})`; inside, after the
   existing repo-create + schedule-seed + box-seed calls, call
   `user.ProvisionPointAccounts`; include the returned creds in the
   create-response DTO (new, response-only fields — not persisted as
   plaintext anywhere).
5. **`internal/connectionrequest`**: same transaction-wrapping for
   `Approve`'s existing 5-step sequence; call the same provisioning
   function after the `WashingPoint` create step; add `credentials` to
   `Approve`'s response DTO.
6. **`internal/admin`**: new routes + handlers for the credentials
   GET/reset endpoints described above; reset regenerates via the same
   password-generation + hash helpers, `user.Repository.SetCredentials`
   for the write, `auth...RevokeAllRefreshTokensForUser` for forced
   re-login.
7. **Docs**: update `API.md`/`openapi.yaml` (new fields + 2 new admin
   endpoints), `DATA_MODEL.md` (`User.PhoneNumber` nullability note, new
   provisioning behavior on `WashingPoint` creation), `PROGRESS.md`.
8. **Tests** (integration, `internal/integration`): a new scenario
   covering: point creation returns usable staff+worker logins (actually
   `POST /auth/login` with the returned creds and assert 200); username
   collision produces a `-2` suffix; reset endpoint changes the password,
   old password stops working, old refresh token is revoked; connection
   request approval also returns working creds; RBAC (staff/non-admin
   forbidden from the new admin endpoints).
9. **`q-wash-shared`**: extend `WashingPoint`-creation and
   connection-request-approval response types with `credentials`; add
   `getWashingPointCredentials` / `resetWashingPointCredentials` API
   wrappers; add their types.
10. **`q-wash-admin`**: the three UI changes listed in Scope. One shared
    "reveal credentials" modal component (used by all three call sites),
    copy-to-clipboard, explicit "I've saved this" acknowledgement before
    closing (since the password can't be shown again without a reset).

## Files / contracts touched

- **Migration** (new file, additive — never edits an applied one).
- **API contract** (needs your sign-off before implementation, on top of
  the earlier Q&A): `POST /washing-points` response shape changes (new
  field, backward compatible), `connectionrequest.Approve`'s response
  shape changes (new field), 2 new admin endpoints.
- Cross-repo: `q-wash-api` (backend), `q-wash-shared` (types/API
  wrappers), `q-wash-admin` (UI). `q-wash-cabinet`/`q-wash-worker` are
  unaffected (they already log in with username+password; nothing about
  *how* they log in changes).

## Risks / open questions

- **Password charset/length**: plan defaults to 16 chars,
  letters+digits, excluding `0/O/1/l/I` for readability when copied by
  hand. Flag if a different policy is wanted.
- **Down-migration ordering**: `000025`'s down migration
  (`SET NOT NULL`) will fail if it's ever run after staff/worker rows with
  a null phone already exist — acceptable (down migrations are a
  point-in-time rollback tool, not meant to run after the feature has
  real data), but noting it explicitly since it's a one-way door in
  practice.
- **Existing washing points**: this only provisions accounts for *newly
  created* points from here on. Points that already exist (and
  seed-script/manual accounts) are untouched — no backfill migration is
  in scope unless you want one.
- **Transaction-wrapping `create()`/`Approve`**: this fixes a real
  pre-existing atomicity gap (not previously wrapped in a transaction) as
  a side effect of safely adding more writes to the same sequence — flagging
  since it's a behavior change beyond "just add credentials," even though
  it's strictly a correctness improvement.

## Status (2026-09-29)

Implementation complete except one external blocker: `migrations/` is
agent-write-blocked by this repo's `.claude/hooks/protect-paths.sh` —
`migrations/000025_users_phone_optional.{up,down}.sql` (content above)
still needs to be created by the user, then `make migrate-up` run, before
`POST /washing-points`'s account-provisioning step will actually succeed
(verified live: point-row insert works, staff-account insert currently
fails on the still-NOT-NULL `phone_number` column, exactly as expected).
Everything else — code, docs, tests, `q-wash-shared`, `q-wash-admin` UI —
is done, and it visibly works end-to-end (login, credential reveal,
password reset) against a live local backend. Delete this file once the
migration lands and the full flow (including point creation) is verified.
