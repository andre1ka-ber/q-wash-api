# Progress: security hardening (plan-security-hardening.md)

Plan approved 2026-10-17. Phases 1-4 done; phases 5-6 not started.

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
  Not done (phase 6, needs go-ahead): CSP/headers for the web apps and `q-wash-web`, which are served by
  something outside this repo.
- #8: `GET /docs` is not registered when `APP_ENV=production` (404); `GET /openapi.yaml` stays. Plan
  alternative (pin + SRI) was not chosen.
- #10: production now refuses to start when `DB_PASSWORD` is the dev default `pegasus` (same check as the JWT
  secrets), and logs a startup warning for `DB_SSLMODE=disable`. **Deploy note:** if the production `.env`
  still has `DB_PASSWORD=pegasus`, the next deploy will fail to start until it is changed (and the Postgres
  user's password changed to match).
- Tests: `httpserver/headers_test.go` (headers on/off per environment, `/docs` dev vs production, upload CSP),
  `config_test.go` (DB password refusal, sslmode warning, Load paths now set `DB_PASSWORD`).
- Docs: `docs/API.md` (conventions, `/docs` note), `README.md`, platform `docs/links.md` (`/docs` is dev-only).
