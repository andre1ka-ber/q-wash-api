# Progress: security hardening (plan-security-hardening.md)

Plan approved 2026-10-17. Phases 1-6 done (phase 6 has manual follow-ups, see below).

## Phase 1: access control and request limits (#2, #5): done 2026-10-17

- #2: `PATCH /washing-points/{id}` returns 403 `forbidden` when a non-admin body includes `status` or
  `owner_id` (`internal/washingpoint/handler.go`). Checked before any field is applied, so a rejected
  request changes nothing. `q-wash-cabinet` only sends `description` and `amenities` through this
  endpoint (`PhotosPage.tsx`), so no client change is needed.
- #5: `httputil.DecodeJSON` wraps the body in `http.MaxBytesReader` (1 MiB, `MaxJSONBodyBytes`) and maps
  an over-limit body to 413 `request_too_large`. Applies to every JSON endpoint. Photo upload keeps its
  own 10 MiB multipart limit.
- Tests added: `internal/httputil/decode_test.go` (normal, unknown field, oversized);
  `TestRBAC_StaffCannotChangeStatusOrOwner` in `internal/integration/scenarios_test.go` (staff status,
  owner set/clear, ordinary edit still 200, status unchanged after rejections, admin can still change it).
- Docs updated: `docs/API.md` (PATCH row, conventions), `docs/openapi.yaml` (PATCH description).
- Verified: `gofmt`, `go vet ./...`, `go vet -tags=integration ./...`, `go test ./...` pass; integration
  run `-run 'TestRBAC|Photo|ConnectionRequest|Credentials|PasswordChange'` passes against real Postgres
  (Docker available). The full integration suite was not run.
- Not covered: no integration test sends a >1 MiB body through the router; the limit is covered at the
  `DecodeJSON` unit level only.

## Phase 2: authentication abuse controls (#1, #3, #6, #13): done 2026-10-17

Decisions (user, 2026-10-17): `TRUST_PROXY_HEADERS` flag; default rate-limit numbers from the plan.

- #1: per-IP limiters on `POST /auth/login` (10/min), `/auth/otp/request` (5/h), `/auth/otp/verify` (20/h),
  QR scan routes (60/min, one budget shared by `/q/{token}` and `/qr-codes/scan/{token}`); per-username
  lockout (5 wrong passwords → 15 min, 429 `login_locked`, applies to unknown usernames too). New
  `internal/platform/ratelimit/lockout.go` (`Lockout`, `PerIP`). All numbers are `RATE_LIMIT_*` env vars,
  `0` turns one off; negative values fail startup. Zero `RateLimitConfig` (existing tests) limits nothing.
- #3: `middleware.RealIP` removed. `TRUST_PROXY_HEADERS` (default on outside production, off in
  production) installs `proxyClientIP` (`internal/platform/httpserver/clientip.go`), which uses the
  **rightmost** `X-Forwarded-For` entry, else `X-Real-IP`. Deviation from the plan text: chi's RealIP takes
  the leftmost, client-controlled entry (and is flagged deprecated for it), so merely gating it behind a flag
  would still have been spoofable behind an appending proxy. Single proxy hop only. Startup warning when
  production runs with the flag off.
- #6: `burnPasswordCompare` runs a dummy bcrypt compare for an unknown username or an account without a
  password.
- #13: covered by the QR scan limiter above.
- Tests: `ratelimit/lockout_test.go`, `httpserver/clientip_test.go`, `config_test.go` additions,
  `integration/rate_limit_test.go` (lockout incl. reset/case/unknown user, per-IP limits on login and both OTP
  endpoints with distinct phones, QR shared budget, forwarded-header trust matrix).
- Docs: `docs/API.md` ("Abuse limits"), `docs/openapi.yaml` (429s), `README.md` (config), platform
  `docs/edge-cases.md` (rewritten proxy-trust entry).
- Not done: `.env.example` is not updated (this session cannot read or edit `.env*` files). Add these lines
  by hand:
  `TRUST_PROXY_HEADERS=true` (dev default; set true in production behind a proxy),
  `RATE_LIMIT_LOGIN_PER_MINUTE=10`, `RATE_LIMIT_LOGIN_MAX_FAILURES=5`, `RATE_LIMIT_LOGIN_LOCKOUT=15m`,
  `RATE_LIMIT_OTP_REQUEST_PER_HOUR=5`, `RATE_LIMIT_OTP_VERIFY_PER_HOUR=20`, `RATE_LIMIT_QR_SCAN_PER_MINUTE=60`.
- Not tested: the dummy-compare timing equalizer is not asserted (wall-clock timing is flaky); only its
  unknown-user code path runs in the lockout test.
- Deploy note: a production deploy behind a reverse proxy must set `TRUST_PROXY_HEADERS=true`, else all
  users share one rate-limit bucket. Customers behind one carrier NAT share a per-IP budget; raise
  `RATE_LIMIT_OTP_REQUEST_PER_HOUR` if real users hit it.

## Phase 3: sessions and tokens (#4, #11, #12): done 2026-10-17

Decision (user, 2026-10-17) for #4: short TTL for staff roles, no `token_version` column (no schema change).

- #4: `jwt.Manager.WithPrivilegedTTL`; staff/worker/admin access tokens live `JWT_STAFF_ACCESS_TTL`
  (default 5m), customers keep `JWT_ACCESS_TTL` (15m). Worst case a demoted or removed staff member keeps
  access 5 minutes. Not changed: password change still does not invalidate access tokens already issued
  (they expire within the TTL); revoking refresh tokens already happens there.
  Client check: `q-wash-shared`'s `apiRequest` refreshes on 401 and retries once, and the display board's
  polling fallback keeps tokens fresh for the SSE reconnect, so no web-app change was made. The SSE client
  itself does not refresh on 401 (unchanged).
- #11: refresh rotation is one atomic `UPDATE ... WHERE revoked_at IS NULL` (`RevokeRefreshTokenIfActive`);
  of N concurrent rotations of one token exactly one succeeds. Deviation from the plan: reuse of a revoked
  token is **logged** (`auth.refresh_reuse`) but does **not** revoke the user's other sessions. `revoked_at`
  cannot tell a rotated token from one revoked by logout or password change (no column for it; adding one
  is a schema change), so family-revoking would let one stale device, or a device left over after a
  password change, log out the user's current session. Revisit with a `rotated_at`/`replaced_by` column if
  wanted.
- #12: new `internal/platform/audit` (event + client IP + request id; phones masked, usernames truncated).
  Events: login ok/failed, OTP requested/failed/verified/failed, refresh failed/reuse, password changed/
  failed, credential reset (actor, target, point). No secrets are logged (tested).
- Tests: `jwt/jwt_test.go`, `audit/audit_test.go`, `config_test.go` (TTL), integration
  `token_security_test.go` (staff vs customer lifetime, 8 concurrent refreshes → exactly one 200, audit
  events present and free of passwords/tokens).
- Docs: `docs/API.md` ("Session lifetime and audit log"), `README.md`.
- Not done: `.env.example` (cannot be edited from this session): add `JWT_STAFF_ACCESS_TTL=5m`.

## Phase 4: headers, config, docs endpoint (#7 API part, #8, #10): done 2026-10-17

Decisions (user, 2026-10-17): #8 disable `/docs` in production; #10 refuse to start with the default DB password.

- #7 (API part): new `securityHeaders` middleware: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`,
  `Referrer-Policy: no-referrer` on every response, `Strict-Transport-Security: max-age=31536000` in
  production. `/uploads/*` additionally gets `Content-Security-Policy: default-src 'none'; sandbox`. No global
  CSP: the QR scan page is HTML with an inline style. `NewRouter` gained a `production` parameter.
  Web-app CSP/headers: see phase 6.
- #8: `GET /docs` is not registered when `APP_ENV=production` (404); `GET /openapi.yaml` stays. Plan
  alternative (pin + SRI) was not chosen.
- #10: production now refuses to start when `DB_PASSWORD` is the dev default `pegasus` (same check as the JWT
  secrets), and logs a startup warning for `DB_SSLMODE=disable`. **Deploy note:** if the production `.env`
  still has `DB_PASSWORD=pegasus`, the next deploy will fail to start until it is changed (and the Postgres
  user's password changed to match).
- Tests: `httpserver/headers_test.go` (headers on/off per environment, `/docs` dev vs production, upload CSP),
  `config_test.go` (DB password refusal, sslmode warning, Load paths now set `DB_PASSWORD`).
- Docs: `docs/API.md` (conventions, `/docs` note), `README.md`, platform `docs/links.md` (`/docs` is dev-only).

## Phase 5: remaining review and dependency scan: done 2026-10-17

Reviewed: booking creation/cancel/status (`queue/manager.go`), notification manager and scheduler, SSE
handlers, photo upload ordering, Payom/FCM clients (outbound), `govulncheck`.

Fixed:
- **Staff could send free-text notifications to any user id.** `POST /notifications` without `queue_id` skipped
  the washing-point ownership check entirely. Now non-admin callers must give `queue_id` (400
  `queue_id_required`); admin may omit it. No client in this repo calls the endpoint. API contract change.
- **Lost-update race on booking status.** `UpdateStatus` wrote the new status with no check of the old one,
  so a customer's cancel racing a worker's "start washing" could resurrect a canceled booking, and two
  identical transitions both reported success. The write is now compare-and-set on the status that was read
  (409 `status_changed`); `UpdatePausedAt` only applies while the booking is still `washing`. Applies to
  `/status`, `/cancel`, `/pause`, `/resume`. API contract: new 409 code.
- **Orphan files on failed uploads.** `photo.Manager.Upload` now removes the stored file if the DB step fails;
  `LocalDisk.Put` removes a partial file when the copy fails.
- **Dependency CVE:** `golang.org/x/text` v0.40.0 → v0.41.0 (GO-2026-6629, reachable). Indirect dependency,
  version bump only (`go.mod`/`go.sum`, 3 lines).

`govulncheck` (v1.1.4, run via `go run`) result after the bump: **19 vulnerabilities, all in the Go standard
library of the local toolchain go1.26.5** (net/http, crypto/tls, html/template, net/url, encoding/xml/asn1,
net/textproto), fixed in go1.26.6 through go1.26.9. Not fixable in this repo's code. The `Dockerfile` builds
with `golang:1.26-alpine`, which resolves to the newest 1.26.x at build time, so a fresh image build should
already contain the fixes (confirm by rebuilding and re-running `govulncheck` on the image's toolchain).
**Action for you:** upgrade the local Go toolchain (`~/go-sdk`) to ≥ 1.26.9 and consider raising the `go`
directive in `go.mod` from `go 1.26.5` to `go 1.26.9` so any build uses a fixed toolchain. Not done here: it
changes the dev environment and the pinned stack in `docs/techstack.md`.

Reviewed, no change needed:
- Booking creation: re-checks availability inside a row lock on the washing point, DB EXCLUDE constraint as a
  backstop, one-active-booking unique index; ownership of the car checked; notes capped at 2000 chars.
- Stage notifications never fail the status change (background goroutine, 20 s timeout, errors logged);
  `(booking, kind)` unique index prevents double sends across instances.
- API7 (SSRF): no outbound request uses user-supplied URLs. Payom base URL comes from config; FCM endpoint is a
  constant plus the configured project id; device tokens are only sent as JSON body values.
- API10 (unsafe consumption): FCM/Payom responses are read through bounded readers and only status codes /
  known JSON error fields are used; both clients have 10 s timeouts.

Residual risks, not changed (listed so they are not lost):
- **SSE:** `http.Server.WriteTimeout` is 30 s and also applies to streaming responses, so `/events` streams
  are probably cut every 30 s and the clients reconnect (a fresh snapshot each time). Not verified; the fix is
  `http.NewResponseController(w).SetWriteDeadline(time.Time{})` in the two SSE handlers. There is also no
  per-user cap on open SSE connections (an authenticated user can hold many).
- A stage notification row is created before sending; if the process dies between the two it stays `pending`
  and is never retried.
- FCM error bodies (≤ 4 KB) are included in the logged error text.
- `NotifyStage` goroutines are unbounded in number (each status change spawns one).
- `PAYOM_BASE_URL` may be configured as `http://...`; the bearer token would then travel unencrypted. Not
  enforced because the Payom unit tests use plain-HTTP `httptest` servers; a production-only https check in
  config validation would close it.

## Phase 6 (infra and other subprojects), 2026-10-18

Go-ahead given by the user for `Dockerfile`, `deploy.yml` and `q-wash-web/config.js`.

- #9 `Dockerfile`: `migrate` tarball downloaded with `curl -f` and verified against a pinned SHA-256 (build args
  `MIGRATE_VERSION`/`MIGRATE_SHA256`; checksum taken from the upstream release's `sha256sum.txt`); `curl` removed
  after use; `/app/seed` no longer built or shipped (`cmd/seed` creates `admin/admin12345`). The container now
  runs as an unprivileged `app` user: the entrypoint starts as root only to `chown` the `uploads` volume (volumes
  created by the old root image stay root-owned), then drops privileges with `su-exec` before `migrate` and the API.
  Verified: image builds, `id` is `app`, `/app` has no `seed`, entrypoint reaches `migrate` as `app`.
  Not verified: a full container run against a real Postgres and an existing root-owned uploads volume.
- #9 `deploy.yml`: all five third-party actions pinned to commit SHAs (tag kept as a trailing comment).
  Dependabot/Renovate would be needed to keep them current.
- #14 `q-wash-web`: `config.js` default left as the local dev URL; the form now refuses to submit (and logs)
  unless `API_BASE_URL` is `https://`, or http on localhost for a localhost page (`apiUrlAllowed`, tested).
  Committed in the `q-wash-web` repo (`ba92432`).
- #7 web part: recommended CSP/nosniff/referrer/HSTS headers documented in `q-wash-web/README.md`. The serving
  config is outside these repos, so nothing could be applied. The four Vite apps are not covered: they need their
  own header set (and a check for inline scripts/styles) once their host is known.
- #15 Firebase key restrictions: manual console step, not done.

Manual follow-ups for the user: set `API_BASE_URL` to the real HTTPS URL on deploy; apply the headers on the web
host; restrict the Firebase API keys to the app package/bundle id; watch the first deploy (non-root + chown).
