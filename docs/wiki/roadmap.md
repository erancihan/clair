# Roadmap

The single status source for the booking work. The roadmap is internal and is not shown on the site. Finding ids (K, S, W, O) come from the 2026-09-02 audit of the tree. Closed findings are not listed. Git history holds the audit and the executed plans.

Three phases. A makes what exists hold and makes it runnable by someone else. B completes the loop the simulator needs. C is polish that waits for the owner's own frontend review.

## Phase A. Stabilise

- A1. Booking service hardening. *(done 2026-09-02)* Lock the release path, put the capacity predicate on commit, scope the refund lookup, owner-check waitlist claims, cap GA holds, refuse holds on past showings, read every `RowsAffected`.
- A2. Live layer resilience. *(done 2026-09-02)* Resync streams after a LISTEN drop, never cache a failed load, stream the GA showing and the empty board, survive a network blip, keep the buyer's hold across reload, one window for strip and stream.
- A3. Operator path and hardening. *(done 2026-10-21)* `make build/migrate/seed/run` agree on one binary, Docker entrypoint, compose runs migrate and scheduler, non-zero exit on a bad database, SSE-aware shutdown, real 404s, server timeouts, rate limits, DB-aware health. Verified against Postgres 16 with the full `go test -race` suite.
- A4. CI and quality gates. *(done 2026-10-26)* The pre-merge checks that stop a broken `master` from reaching the image registry. CI runs `go mod tidy -diff`, generate, vet, a CGO-free build and `go test -race -count=1 ./...` against Postgres, and `make test` runs the same steps. golangci-lint runs with the `.golangci.yml` baseline (staticcheck, errcheck, govet, unused). Every pull request runs `docker build`, and a push to `master` tags `:latest` and `:sha-<short sha>`.
- A5. Docs consolidation. *(done 2026-10-26)* Plans and specs deleted, `docs/wiki/` is the only documentation folder, `docs/` is committed.

## Phase B. Complete the loop

- B1. Rolling calendar. Findings O11, O12, O13. Date-bearing event slugs, a scheduler job that keeps a 14-day window of showings, catalog as the single source for service names and blurbs.
- B2. Theater checkout and cancel. Findings K6, K7. A mock-payment checkout that captures a hold into a paid order with issued tickets, `CancelOrder` and `RefundOrder` in the booking service, a my-tickets panel. Prerequisite for simulator churn.
- B3. Retention. `PurgeOldBookingData(db, now)` deleting by business time (`event.start_at`, `slot.end_at`), status-guarded on money rows, batched, with supporting indexes. A gated daily scheduler job. Never touches seeded schedules, rules, tiers or seats.
- B4. Simulator. Findings K20, K22. Package `internal/simulator`. Demand-curve fill through in-process domain calls that converges toward a target occupancy per showing or slot, with churn from B2's cancel path and abandoned holds. `sim:` owner-ref namespace, production DSN guard, advisory-lock single runner, wired into `clair scheduler` behind `SIM_ENABLED`.

## Phase C. Polish

- C1. Frontend accessibility and UX pass. Findings W7 to W18, W20. Waits for the owner's own review of the current screens.
- C2. Host surface. The host routes (create event or schedule, rules, exceptions) exist but no page drives them.
- C3. Deployment. SHA-tagged images, a real deploy step, migration ordering, per-service healthchecks.
- C4. Map and game. Aurenor at `/aurenor`, then a game on top. Out of scope until A and B land.

## Open findings

| Id | Where | Kind | Finding | Fix | Step |
|---|---|---|---|---|---|
| K6 | `booking/hold.go` `CaptureOrder` | gap | A hold that lapses between checkout and the webhook makes `CaptureOrder` fail and roll back the payment row too. No void or refund path exists outside `CancelByToken`. | Record the payment in its own transaction. Add `RefundOrder` to the service. | B2 |
| K7 | `booking/inventory.go` `Cancel` | gap | `booking.Cancel` has one caller, in appointments. Nothing decrements `booked` for a tier or seat inventory, so a captured sale is irreversible. | Add `CancelOrder(db, orderID, now)` that releases each item's units and drives the refund. | B2 |
| K20 | `appointments/book.go` `CancelByToken` | smell | Calls `time.Now()` internally. Every other entry point takes `now`. | Add the parameter. | B4 |
| K22 | `server/booking/api_availability.go` | smell | The "load rules, exceptions and booked counts" preamble is duplicated between `availability()` and `availabilityViews()`. | Move it into `appointments.OpenSlotsFor`. | B4 |
| W7 | `pages/aurenor_showing_script.templ` | bug | `seatLimit` returns `Math.max(1, ...)`, so a sold-out GA showing renders an enabled Hold button that always answers 409. | Branch on `remaining === 0`. Add a sold-out panel. | C1 |
| W8 | `pages/aurenor_service_script.templ`, `aurenor_theater_live.templ` | ux | When the chosen day empties, `apply()` silently jumps to `days[0]`. | Set the live-region notice. | C1 |
| W9 | `pages/aurenor_showing.templ` | a11y | `role="gridcell"` on the `<button>` discards the button role and makes `aria-pressed` meaningless. | Wrap: `<div role="gridcell"><button aria-pressed>`. | C1 |
| W10 | `pages/aurenor_datestrip.templ` | a11y | `role="tablist"` and `role="tab"` with no `aria-controls`, no tabpanel, no roving tabindex, no arrow keys. 45 tab stops on the consulate strip. | Roving tabindex plus arrow keys, or plain buttons with `aria-pressed`. | C1 |
| W11 | `pages/aurenor_showing_script.templ`, `aurenor_showing.templ` | a11y | "taken" is about 3.3:1 and "not for sale" about 1.4:1 contrast at 11 px. | Darken both. | C1 |
| W12 | `pages/aurenor_service.templ`, `aurenor_showing.templ` | a11y | Nothing moves focus or announces after a booking or hold succeeds. Focus falls to `<body>`. | `tabindex="-1"` plus `role="status"` on the panels. Focus them. | C1 |
| W13 | `pages/aurenor_service.templ` | ux | The service screen never names the institution. Two hospitals' cardiology pages are identical above the fold. | Carry the venue name into `ServiceCard`. Render it as subtitle and in the back link. | C1 |
| W14 | `pages/aurenor_script.templ` | bug | My-holds pruning uses the raw client clock, and the countdown ticks every 30 s. | Global clock offset next to `aurenorCSRF`. Tick per second. | C1 |
| W15 | `pages/aurenor_showing.templ` | ux | The sticky buyer bar stacks chips, notice, two inputs, error, totals, button and fine print. On a phone it covers a third of the viewport. | Keep count, total and button sticky. Inputs inline above. | C1 |
| W16 | `pages/aurenor_showing_script.templ` | bug, a11y | After a failed auto-pick, `autoPicked` stays true and the stepper drifts from the selection. Auto-pick moves the tab stop but not focus. | Clear the flag on the no-run branch. Reuse the focus block. | C1 |
| W17 | `pages/aurenor_theater_live.templ` | dead, ux | The "No showings on <day>" panel and `nextDayWithShowings` are unreachable. An empty board renders as a red `role="alert"`. | Delete the dead branch. Add a neutral empty state. | C1 |
| W18 | `components/header.templ` | dead, bug | `NavItem`'s active branch is unreachable. `max-w-9xl` and `lg:max-w-12xl` do not exist in Tailwind. | Delete. Compute active from path. Use `max-w-7xl`. | C1 |
| W20 | `routes/booking.go`, `components/nav.go` | ux | `/aurenor` answers 404 and no nav entry exists. A deep-linked visitor has no route back. | Mount an `/aurenor` index that lists the four systems. | C1 |
| O11 | `internal/aurenor/seed.go`, `catalog.go` | bug | Event slugs are `theater-film-HHMM` with no date, and `startAt` is `now + DayOffset` with `DayOffset` in 1 to 7. Re-runs skip all 17 by slug. Eight days after a seed, 0 of 17 showings are in the future. | Date-bearing slugs plus a scheduled job that keeps an N-day window. | B1 |
| O12 | `internal/aurenor/catalog.go`, `pages/aurenor_helpers.go` | smell | The twelve service names are typed twice: catalog bare, page with blurbs. | Move blurbs into the catalog. Assert both directions. | B1 |
| O13 | `internal/aurenor/catalog_test.go` | gap | Only "16 schedules" is asserted. The 84 rules and 17 events are checked by `len != 0`. | Assert all three. | B1 |

## Decisions that stand

- The booking service is called "the booking service" in prose. Never "kernel" or "engine". The package is `booking`.
- No real place names, no "government", no public sign-in link in anything a user sees.
- The accessor layer in `internal/server/booking` stays. It is what lets the route table live outside the domain packages.
- `versions[topic]` in `live.Hub` is never pruned. It is one integer per topic ever seen, bounded by the catalog, and the snapshot cache compares stamps against it.
- The JSON error envelope (old finding S16) waits for the frontend pass, because it changes what the pages parse.
- The simulator comes fourth in B. It needs a 14-day horizon, a cancel path and a rolling calendar, none of which exist yet.
