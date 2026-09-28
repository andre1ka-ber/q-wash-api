# Plan: split the `queue` package so it stays maintainable

Status: **draft, awaiting approval** (docs/rules/general.md §12). No code written for this plan yet.
(The earlier push-notification plan is kept as `plan-push-notifications.md`.)

## Goal

`internal/queue` is where most new features land, and it has grown into the one place that
does everything: booking writes, availability, live boards, the day view, reports and SSE.
`queue/handler.go` alone is ~1,100 lines with ~28 handlers, and `queue.Handler` takes 9
dependencies (four of them other features' repositories). Business logic (building boards,
report aggregation, enriching rows with user/car/service names) lives in the HTTP layer, so it
can only be tested through a full Postgres integration test.

Make it so that adding or changing a queue feature touches one small, focused file; the HTTP
layer only decodes, calls a service and encodes; and the read logic can be unit-tested with
fakes. **Pure refactor: no API, schema, route, JSON-shape or behaviour change.**

## Scope

In:
- Split `handler.go` by resource (mechanical move, no logic change).
- Extract the read side out of `Handler` into small services with their own narrow dependencies:
  `Enricher` (batch-loads users/cars/services for rows), `BoardService`, `DayService`,
  `ReportService`.
- Consumer-side interfaces for what those services need from repositories, so they can be
  unit-tested with in-memory fakes.
- Shrink `NewHandler` to ≤ 5 dependencies.
- Unit tests for the extracted services (board building, live boxes, day items/tickets, report
  aggregation), in addition to the existing integration suite which stays as the safety net.

Out (separate follow-ups, each its own plan if wanted):
- Any API/contract/schema change.
- Moving the pieces into sub-packages (`queue/board`, `queue/reports`) — start in the same
  package; revisit only if it still feels crowded.
- The ownership-check abstraction shared by `service`/`photo`/`admin`/… handlers.
- Injectable `Clock` (replace direct `time.Now()`); worth doing right after, and the new
  services will take `now` as a parameter to make that easy.
- Other features' handlers.

## Target shape

```
queue/
  model.go, repository.go            (unchanged)
  manager.go                         writes only: create, manual create, cancel, pause/resume, status
  availability.go                    (unchanged)
  enricher.go                        Enricher: rows -> names/phones/cars (was rowRefs/loadRefs)
  board_service.go                   BoardService: today's board, live boxes, display board
  day_service.go                     DayService: staff day view + tickets
  report_service.go                  ReportService: aggregation + period maths (was reports.go on Handler)
  handler.go                         constructor + RegisterRoutes only
  handler_booking.go                 create, get, cancel, pause, resume, status, history, availability
  handler_board.go                   list, listByWashingPoint, boxesLive, board
  handler_day.go                     listDay, createManual
  handler_reports.go                 reports
  handler_events.go                  SSE: boardEvents, events
```

`Handler` ends up depending on: `Manager`, `Repository`, `BoardService`, `DayService`,
`ReportService` (+ the event bus for SSE) instead of nine concrete collaborators.

## Steps (each ends green: `go vet`, `gofmt`, unit + full integration suite; one commit each)

1. **Baseline.** Record current test counts; confirm unit + integration green. Note the exported
   surface other packages use (`Queue`, `Repository`, `Status*`, `NewHandler`, `NewManager`,
   `StageNotifier`) — it must not change.
2. **Split `handler.go` by resource** into the files above. Pure cut-and-paste; diff should be
   only moved lines plus imports.
3. **Extract `Enricher`** (today's `rowRefs`/`loadRefs`/`uniqueIDs`): a struct holding the user,
   car and service repositories. `Handler` loses `userRepo`, `carRepo`, `serviceRepo`.
4. **Extract `BoardService`** (`buildBoardResponse`, `toLiveBoxItems`, `toBoardItems`) and
   **`DayService`** (`toDayItems`, ticket numbering). Handlers become decode → service → encode.
   `Handler` loses `scheduleRepo`, `boxRepo`.
5. **Extract `ReportService`** from `reports.go` (the `Handler` methods become service methods;
   the pure helpers stay unexported functions). `Handler` loses the reports-related wiring.
6. **Consumer-side interfaces + unit tests.** Each service declares the small interface it needs
   (e.g. `type bookingReader interface { FindActiveBookingsInRange(...) }`); concrete repos
   satisfy them unchanged. Add fake-based unit tests for the services' branching logic
   (empty day, paused booking, next-in-box, ticket numbering, report deltas/buckets).
7. **Tidy.** Update `NewHandler`/`app.go` wiring, remove now-unused fields/imports, update
   `docs/` (architecture note in `README.md`/`PROGRESS.md`), final full check.

## Files / contracts touched

- `internal/queue/*` (split/moved), `internal/app/app.go` (constructor wiring),
  new unit tests under `internal/queue/`.
- **Contracts (§4): none.** HTTP routes, request/response JSON, DB schema and migrations are not
  touched. Anything that changes an observable response is a bug in this refactor.

## Acceptance criteria

- `go vet ./...` (both tag sets), `gofmt -l` empty, `go test ./...` and the full integration suite
  pass with **no test edited to make it pass** (new tests are additions).
- No file in `internal/queue` over ~400 lines; `queue.NewHandler` has ≤ 5 parameters.
- `git diff` of the response types shows no changes; `docs/openapi.yaml` untouched.
- The new services have fake-based unit tests that don't need Docker.

## Risks

- **Large mechanical diff.** Mitigated by one commit per step and step 2 being a pure move, so
  reviewers can diff moved code.
- **Subtle behaviour drift while extracting** (ordering, filtering of `washing`/`ready` rows,
  the today-only bounds). Mitigated by the existing integration tests around the board, live
  boxes, worker actions, reports and SSE, run after every step; add a characterization test
  first if a step touches something they don't cover.
- **Import cycles** (`queue` is imported by `admin`, `notification`, `qrcode`). Services stay
  inside the package and take their collaborators as constructor arguments/interfaces.
- SSE tests are timing-sensitive; rerun them a few times after step 4.

## Open questions (answers needed before coding)

1. **Same package (recommended) or sub-packages** (`queue/board`, `queue/reports`)? Same package
   is less churn and keeps unexported helpers shared; sub-packages give stricter boundaries but
   need more exported surface.
2. **Injectable clock now?** Recommended as a follow-up plan rather than inside this one, to keep
   this diff behaviour-neutral.
3. **Ownership-check abstraction** (a shared "may this caller act on this washing point" helper
   used by several handlers): include here or a separate plan? Recommended separate.
4. OK to land each step as its own commit on `main` (and push after each green step), rather than
   one big commit at the end?
