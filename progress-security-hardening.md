# Progress: security hardening (plan-security-hardening.md)

Plan approved 2026-10-17. Phases 2-6 not started.

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
