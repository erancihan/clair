# Hand-off — 2026-10-20

Work moved to another machine. This file is the state at the hand-off and the first things to do there. Delete it once the steps below are done.

## Two lines of work

| | Booking (this branch) | Shop front |
|---|---|---|
| Branch | `feat/booking-systems` | `docs/shop-front-plan` |
| Was at | main checkout | worktree `../temp/clair-shopfront` |
| Base | `master` @ `a1b3215` | `master` @ `a1b3215` |
| Status source | `docs/worldbuilding/aurenor.md` (roadmap) and the plan banners in `docs/superpowers/plans/` | `docs/shop-plan.md` |

Both branches were committed as checkpoints at hand-off. Clean the history up when you open the PRs.

## Booking: where it stands

- Roadmap v2 phases A1 and A2 are done (`docs/superpowers/specs/2026-09-02-evaluation-and-roadmap-v2.md`).
- A3 (`docs/superpowers/plans/2026-09-02-a3-operator-path-and-hardening.md`) is code complete. Its banner lists what was verified and how the work differs from the plan.
- **No test that needs Postgres has run against Tasks 4–8.** The machine that finished them had no Docker. Build, vet, `go test -race` on every package that does not need a database, and a route-table smoke test all passed.

### First steps on the new machine

```sh
cp .env.example .env            # then fill SESSION_KEY, BOOKING_PAYMENT_WEBHOOK_SECRET
go mod vendor                   # vendor/ is gitignored; the Makefile builds with -mod vendor
docker compose -f docker-compose.dev.yaml up -d postgres
export $(grep -E '^DATABASE_URL=' .env | xargs)
go generate ./... && go build ./... && go vet ./... && go test -race -count=1 ./...
```

Then finish A3 Task 9 (Steps 1, 3, 4, 5) and fix anything red. These are the tests most likely to need work, because they were written without a database to run them:

- `test/server_lifecycle_test.go` (`TestShutdownEndsOpenStreams`)
- `test/ratelimit_http_test.go`
- `TestHealthReportsTheDatabase`, `TestPaymentWebhookRefusesUnknownProvider`, `TestCreateScheduleDuplicateSlugIs409AndOtherErrorsAreNot`
- the changed `TestMountedRoutes` (404 row), `TestAuthentication` (`/appointments/me`), `TestUsersEndpoint…` (exact path) and the three cancel-by-token call sites

If an existing HTTP test trips the guest rate limit (burst 20), raise the default in `internal/server/routes/booking.go`. Do not change the test.

After A3: A4 (CI gates), then A5 (docs consolidation). Both are in the roadmap.

## Shop front: where it stands

- `docs/shop-plan.md` is revised against `master` (the uncommitted revision is now a commit).
- Phase 0 ("seams") is written but has not been verified with a database: `internal/server/shop/{api.go,api_http.go,models.go}`, `internal/server/routes/shop.go`, `internal/web/pages/shop_storefront.templ`, `test/shop_mount_test.go`, and one-line additions to `server.go`, `database.go`, `nav.go`.
- Locked decision: the shop has its own `shop_*` tables for orders, stock and payment. It does not use the booking engine.

### Merge order

Land booking first. `master` does not build at `a1b3215` (`internal/cmd/scheduler.go` calls the deleted `clair.NewEventLoop`), and `clair migrate` is not registered. The booking branch fixes both.

The shop branch fixed the same two defects with a copy of an older booking `scheduler.go`. A3 Task 4 then changed that file again (the scheduler has no HTTP listener now). When you rebase the shop branch onto the merged booking work:

- `internal/cmd/scheduler.go`, `internal/cmd/root.go`: take master's version and drop the shop copy.
- `internal/server/server.go`: booking's `Routes()` now calls `routes.MountSite`, `routes.MountGames` and `routes.MountBooking`. Add `routes.MountShop(app, s.context)` after them. The shop's `/shop/` 404 fallback is still needed for unknown paths under `/shop/`, but the site catch-all now answers 404 too (A3 Task 5).
- `internal/database/database.go`, `internal/web/components/nav.go`: re-apply the one-line additions.
- `internal/server/routes/`: both branches add files to this package. They do not conflict, but check that no two files declare the same helper. `routes/booking.go` already declares `page`, `envInt` and `envFloat`.
- `internal/server/routes/shop.go` repeats the package doc comment from `routes/booking.go`. Keep one.

## Rules that carry over

- The owner signs commits with GPG. An agent proposes commit and rebase commands; the owner runs them.
- Prose says "the booking engine", never "kernel".
- No real place names, no "government", no public sign-in link in anything a user sees.
