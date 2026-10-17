# Plan: security hardening from the OWASP audit (2026-10-17)

Status: **approved 2026-10-17; phase 1 done, phases 2-6 pending** (docs/rules/general.md §12). Progress in
`progress-security-hardening.md`. Open decisions below (token_version vs TTL, proxy flag, `/docs`, limits,
phase 6 go-ahead) were not answered at approval; they are asked again before each affected phase.
Primary subproject: `q-wash-api`. Smaller items touch `q-wash-web` and deploy/infra config; those are
marked and need an explicit go-ahead because `CLAUDE.md` forbids touching `.github/workflows/`,
deploy config or infra config unless the task names it.

Sources: OWASP Top 10:2025 (https://top10.owasp.org/2025/) and OWASP API Security Top 10:2023
(https://api-security.owasp.org/), both listed in `docs/links.md`.

Audit method: static read of `q-wash-api` (auth, router, config, photo/storage, queue, washingpoint,
qrcode, connectionrequest, user, device, Docker/compose/CI), `q-wash-shared` (token storage, API client,
SSE client), the four web apps (pattern search), `q-wash-web`, and `q-wash` (secure storage, manifests).
`npm audit --omit=dev` reports 0 vulnerabilities in all five TS packages. `govulncheck` was not available,
so Go dependency CVEs are **unchecked**. Nothing was exploited or run against a live environment.

## Verified clean (no work needed)

- SQL injection: GORM parameterized queries throughout; no `Raw`/`Sprintf` SQL.
- XSS: no `dangerouslySetInnerHTML`/`innerHTML` in web apps; QR scan page uses `html/template`.
- Photo upload: sniffed content type, SVG refused, UUID filenames, 10 MiB cap, ownership checked first.
- Object-level authorization: all staff/worker handlers read apply `OwnsWashingPoint`; customer cars,
  devices and bookings are owner-scoped with 404 on mismatch.
- Mobile token storage: `flutter_secure_storage`.
- Production startup rejects the dev JWT secret and refresh pepper, and secrets shorter than 32 chars.
- Passwords bcrypt; refresh tokens opaque + HMAC-hashed + rotated; OTP from `crypto/rand`.

## Issue list

IDs are stable; the first audit message used the same numbers.

| # | Sev | Issue | Where | OWASP 2025 | API 2023 |
|---|---|---|---|---|---|
| 1 | High | No rate limit or lockout on `POST /auth/login`, `/auth/otp/request`, `/auth/otp/verify`. OTP request is throttled per phone only, so SMS can be pumped to many numbers (cost) and logins brute-forced; seeded accounts use weak passwords | `internal/auth/handler.go`, `internal/app/app.go` | A06, A07 | API2, API4, API6 |
| 2 | High | `PATCH /washing-points/{id}` is open to staff and accepts `status` and `owner_id`: staff can move their own point from `pending_review` to `active` (bypassing admin approval) and reassign or clear its owner | `internal/washingpoint/handler.go:290-362` | A01 | API3, API5 |
| 3 | High | chi `RealIP` trusts `X-Forwarded-For`/`X-Real-IP` from any client; the per-IP limiter is bypassable unless the proxy overwrites them (already noted in `docs/edge-cases.md`) | `internal/platform/httpserver/router.go` | A02 | API8 |
| 4 | Med | Access-token `role`/`wpid` are trusted without a DB check, so a demoted or deleted user keeps access for up to the 15 min TTL; password change does not invalidate access tokens | `internal/auth/middleware.go`, `internal/platform/jwt` | A01, A07 | API2 |
| 5 | Med | `DecodeJSON` has no `http.MaxBytesReader`; large bodies are read until the 10 s read timeout | `internal/httputil/httputil.go` | A06 | API4 |
| 6 | Med | Username enumeration by timing: unknown user returns before any bcrypt compare | `internal/auth/service.go` `LoginWithPassword` | A07 | API2 |
| 7 | Med | No security headers (`nosniff`, HSTS, frame/referrer policy) on the API; `/uploads/*` served without `nosniff`/restrictive CSP; no CSP shipped for the web apps or `q-wash-web` | router, `static.go`, web deploy | A02 | API8 |
| 8 | Med | `/docs` loads `swagger-ui-dist@5` (floating major) from unpkg with no SRI; compromised CDN means script on the API origin. `/docs` and `/openapi.yaml` are public in production | `internal/platform/httpserver/swagger.go` | A03 | API8, API9 |
| 9 | Med | Supply chain / image hardening: `migrate` binary fetched with `curl` and no checksum; container runs as root; third-party GitHub Actions pinned by tag, not SHA; production image ships `/app/seed` (creates `admin/admin12345`) | `Dockerfile`, `.github/workflows/deploy.yml` | A03, A08 | API8 |
| 10 | Low | Insecure defaults: DB user/password `pegasus/pegasus`, `sslmode=disable`; production validation checks only the JWT secret and pepper | `internal/config/config.go` | A02, A04 | API8 |
| 11 | Low | Refresh rotation is find-then-revoke, not atomic: two concurrent refreshes with the same token can both succeed; no reuse detection that revokes the token family | `internal/auth/service.go` `Refresh` | A07 | API2 |
| 12 | Low | No audit logging of failed logins, OTP lockouts, credential resets, password changes | auth, washingpoint | A09 | — |
| 13 | Low | Public `GET /q/{token}` and `/qr-codes/scan/{token}` write a scan row per hit, unauthenticated and unthrottled (stats inflation, DB growth) | `internal/qrcode/handler.go:377` | A06 | API4, API6 |
| 14 | Low | `q-wash-web/config.js` hardcodes `http://localhost:8080/api/v1`; production must be HTTPS | `q-wash-web/config.js` | A02, A04 | API8 |
| 15 | Low | `google-services.json` and `GoogleService-Info.plist` are committed in `q-wash`; normal for client config, but the Firebase API keys must be restricted to the app's package and bundle id | `q-wash/android`, `q-wash/ios` | A02 | — |

Not yet reviewed (open gaps, tracked as phase 5):

- A10 Mishandling of Exceptional Conditions: fail-open or partial-write paths in booking creation, the
  notification manager and scheduler, SSE error handling, and photo `Upload` (file written before the DB
  row, so a failed insert leaves an orphan file).
- API7 SSRF: no server-side fetch of user-supplied URLs was seen, but not every outbound call was traced.
- API10 Unsafe Consumption of APIs: Payom and FCM clients have timeouts; response validation not checked.
- Go dependency CVEs (`govulncheck`).

## Decisions needing approval

Per `CLAUDE.md`, the following need explicit approval before work starts:

- **Schema change (#4):** a `token_version` column on `users` (new migration, never edit an applied one).
  Alternative with no schema change: shorten access TTL to ~5 min for staff/worker/admin. Recommendation:
  start with the TTL change and skip the column; revisit if 5 min refresh load is a problem.
- **API contract (#2):** `PATCH /washing-points/{id}` would return 403 when a non-admin sends `status` or
  `owner_id`. Update `docs/API.md` and `docs/openapi.yaml`. cabinet must not send those fields; the plan
  includes checking `q-wash-cabinet`'s call sites.
- **Infra/deploy files (#9, #7 web CSP, #14):** `Dockerfile`, `.github/workflows/deploy.yml`, the web
  server config. Need an explicit go-ahead to edit.
- **New dependency:** none planned. The limiter extends the existing in-house `internal/platform/ratelimit`.
- **Rate-limit numbers (defaults, say if wrong):** login 10/min per IP and 5 failures per username per 15 min
  then 15 min lockout; `otp/request` 5/hour per IP in addition to the existing per-phone cooldown;
  `otp/verify` 20/hour per IP; QR scan 60/min per IP.

## Phases

Each phase ships with tests and doc updates, and runs `go vet ./... && go test ./...` plus
`go vet -tags=integration ./...` (and the integration suite where Docker is available; see
`docs/edge-cases.md`).

### Phase 1: access control and request limits (#2, #5)

- washingpoint `update`: reject `status` and `owner_id` from non-admin with 403 `forbidden`; staff may still
  edit name, address, coordinates, hours, description, amenities, `boxes_count`.
- `DecodeJSON`: wrap the body in `http.MaxBytesReader` (1 MiB), map `*http.MaxBytesError` to 413.
- Tests: unit and integration (staff PATCH with `status`, with `owner_id`, without either; oversized body).
- Docs: `docs/API.md`, `docs/openapi.yaml`.

### Phase 2: authentication abuse controls (#1, #3, #6, #13)

- Extend `ratelimit` with a keyed limiter usable for username and phone keys, plus a failure-count lockout
  for username login. Wire per-IP limiters on `login`, `otp/request`, `otp/verify`, QR scan.
- #3: trusted-proxy handling. Replace unconditional `middleware.RealIP` with a config flag
  (`TRUST_PROXY_HEADERS`, default off in production, on in development) so a direct client cannot spoof its
  IP. Default behaviour change needs a deploy note in `README.md` and `docs/edge-cases.md`.
- #6: run a dummy bcrypt compare when the username is unknown.
- Tests: limiter windows and lockout, spoofed header ignored when the flag is off, timing path exercised.

### Phase 3: sessions and tokens (#4, #11, #12)

- #4 per the decision above (TTL change, or `token_version`). Revoke refresh tokens on password change
  (already done) and make access-token lifetime match the chosen policy.
- #11: make rotation atomic (single `UPDATE ... WHERE revoked_at IS NULL RETURNING`); on reuse of a revoked
  token, revoke all tokens for that user.
- #12: structured `slog` audit events (login failure/success, OTP lockout, credential reset, password
  change) with user id or username and IP, never the secret or the OTP.
- Tests: concurrent refresh, reuse detection, audit events emitted.

### Phase 4: headers, config, docs endpoint (#7 API part, #8, #10)

- Middleware adding `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`,
  `X-Frame-Options: DENY`, and HSTS when `IsProduction()`. `/uploads/*` additionally gets
  `Content-Security-Policy: default-src 'none'`.
- `/docs`: pin `swagger-ui-dist` to an exact version with SRI hashes, or serve it only outside production.
  Choice needs your call (see questions).
- Config: in production, refuse the default DB user/password and warn on `sslmode=disable`.
- Tests: header presence, config validation cases in `config_test.go`.

### Phase 5: remaining review and dependency scan

- A10 pass over booking creation, notification manager/scheduler, SSE, photo upload ordering. Fix or file
  each finding.
- API7 and API10: trace outbound calls (Payom, FCM) and response handling.
- Run `govulncheck ./...` (needs it installed and allowed in the sandbox); record results.

### Phase 6: infra and other subprojects (needs go-ahead, #7 web part, #9, #14, #15)

- `Dockerfile`: verify the `migrate` tarball checksum, add a non-root `USER`, drop `/app/seed` from the
  production image.
- `deploy.yml`: pin third-party actions by commit SHA.
- Web apps and `q-wash-web`: CSP and security headers in whatever serves them (config not in this repo).
- `q-wash-web/config.js`: HTTPS API URL for the deployed build; confirm how the deploy overrides it.
- `q-wash`: confirm Firebase key restrictions in the Firebase console (manual step, not code).

## Order and risk

1 → 2 → 3 → 4 first (all in `q-wash-api`, independently committable). Phase 2's proxy-header flag is the
only change that can break a working deploy if the proxy setup differs from assumptions, so it ships with
an explicit deploy note. Phase 6 waits for the go-ahead above.

## Docs to update when done

`docs/API.md`, `docs/openapi.yaml`, `README.md` (new env vars), `docs/edge-cases.md` (proxy trust, rate
limits), `PROGRESS.md`, and this platform's `docs/` if any integration contract changes.

## Progress

Track in `progress-security-hardening.md` once approved (same convention as `progress-push-notifications.md`).
