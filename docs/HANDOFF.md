# Hand-off — shop-front rebase

Booking (`feat/booking-systems`) finished roadmap phases A1–A3 and the Postgres-backed test run on 2026-10-21. This file keeps only what is still open: the rebase of the shop-front branch after booking merges to `master`. After that rebase is done, delete this file.

## Branches

| | Booking | Shop front |
|---|---|---|
| Branch | `feat/booking-systems` | `docs/shop-front-plan` |
| Was at | main checkout | worktree `../temp/clair-shopfront` |
| Base | `master` @ `a1b3215` | `master` @ `a1b3215` |
| Status source | `docs/worldbuilding/aurenor.md` (roadmap) and the plan banners in `docs/superpowers/plans/` | `docs/shop-plan.md` |

Both branches were committed as checkpoints at hand-off. Clean the history up when you open the PRs.

## Shop front: where it stands

- `docs/shop-plan.md` is revised against `master` (the uncommitted revision is now a commit).
- Phase 0 ("seams") is written but has not been verified with a database: `internal/server/shop/{api.go,api_http.go,models.go}`, `internal/server/routes/shop.go`, `internal/web/pages/shop_storefront.templ`, `test/shop_mount_test.go`, and one-line additions to `server.go`, `database.go`, `nav.go`.
- Locked decision: the shop has its own `shop_*` tables for orders, stock and payment. It does not use the booking engine.

## Merge order

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
