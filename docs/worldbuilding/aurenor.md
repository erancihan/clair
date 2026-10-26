# Aurenor — worldbuilding

The made-up nation the booking simulations are themed around. Completely fictional; no real-world names. A coastline along its north — the **Gilt Coast**. Reserved as a growing role-play setting: the booking demos today, a map and a game later.

## The nation

| | |
|---|---|
| **Name** | Aurenor |
| **Meaning / vibe** | "golden north" — elegant, faintly mythic |
| **Demonym** | Aurenori |
| **Capital** | Solvarre |
| **North coast** | the Gilt Coast |
| **URL namespace** | `/aurenor` (country home, reserved for a future map/game) |

## Institutions (the booking examples)

Two underlying booking systems, several example institutions on top. Institutions are **collections** — theaters and hospitals list instances, each with its own `{slug}` — so the nation can hold more than one of each. The single-body consulate and civic bureau are flat pages.

| Institution | System | Route |
|---|---|---|
| Theaters (e.g. Aurenor Grand Theater, Harborlight Cinema) | seat reservation | `/aurenor/theaters`, `/aurenor/theaters/{slug}` |
| Hospitals (e.g. Solvarre General Hospital, Gilt Coast Clinic) | appointment scheduling | `/aurenor/hospitals`, `/aurenor/hospitals/{slug}` |
| Aurenor Consulate | appointment scheduling (visa interviews, residence permits) | `/aurenor/consulate` |
| Aurenor Civic Bureau | appointment scheduling (licence / ID / fingerprint) | `/aurenor/bureau` |

## Roadmap

Internal only — the roadmap is deliberately not shown on the site. This section
is the single status source. Evidence, per-finding detail and the reasoning
behind the order live in
`docs/superpowers/specs/2026-09-02-evaluation-and-roadmap-v2.md`. Older
specs under `docs/superpowers/` are history; each carries a status banner.

### Built (v1 steps 1–8)

1. **Mock UI** — landing pages for every institution. *(done)*
2. **Seed Aurenor** — real schedules and showings in the database. *(done)*
3. **Appointments live** — book a doctor, service or visa for real. *(done)*
4. **Theater live** — engine wired: real showings, real seat holds. *(done; checkout is B2)*
5. **Discord out, scheduler in** — drop the unused Discord path, run the hold reaper. *(done)*
6. **Hold truth** — a cap that holds under concurrency, a release route, honest expiry. *(done)*
7. **Seat selection UX** — the picker redesigned around the showing you chose. *(done)*
8. **Appointment UX** — a screen per service, live. *(done)*
   Both booking surfaces follow one rule: a screen opens one connection and
   subscribes to only what it displays.

### Phase A — Stabilise *(next)*

- **A1. Engine hardening** *(done)* — lock the release path, put the capacity predicate on commit, scope the refund lookup, owner-check waitlist claims, cap GA holds, refuse holds on past showings, read every `RowsAffected`.
- **A2. Live layer resilience** *(done)* — resync streams after a LISTEN drop, never cache a failed load, stream the GA showing and the empty board, survive a network blip, keep the buyer's hold across reload, one window for strip and stream.
- **A3. Operator path and hardening** *(done)* — `make build/migrate/seed/run` agree on one binary, Docker entrypoint, compose runs migrate + scheduler, non-zero exit on a bad database, SSE-aware shutdown, real 404s, server timeouts, rate limits, DB-aware health.
- **A4. CI and quality gates** — `go test -race ./...`, `go mod tidy -diff`, golangci-lint, PR-triggered image build.
- **A5. Docs consolidation** — status banners, archive executed plans, commit `docs/`.

### Phase B — Complete the loop

- **B1. Rolling calendar** — date-bearing showing slugs and a job that keeps a 14-day window; catalog as the single source for service copy.
- **B2. Theater checkout and cancel** — mock-payment capture into paid orders and tickets; `CancelOrder`/`RefundOrder` in the engine; a my-tickets panel.
- **B3. Retention** — purge bookings and orders older than a month by business time; never the seeded catalog.
- **B4. Simulator** — `internal/simulator`, demand-curve fill toward a target occupancy with churn, `sim:` namespace, prod-DSN guard, behind `SIM_ENABLED` in `clair scheduler`.

### Phase C — Polish

- **C1. Frontend accessibility and UX pass** — after the owner's own review.
- **C2. Host surface** — pages for the host routes that already exist.
- **C3. Deployment** — SHA-tagged images, a deploy step, migration ordering.
- **C4. Map and game** — Aurenor at `/aurenor`, then a game on top. Out of scope until A and B land.

## Alternative names considered

Kept for future settings, sub-regions, or a rename. Each was drafted with its own north coast.

| Name | Vibe | Demonym | Capital | North coast |
|---|---|---|---|---|
| **Norrevik** | "north bay" — Nordic, grounded, believable | Norrevikan | Halden's Reach | the Norre Sea |
| **Kestrand** | "shore land" — coastal, leans hardest into the north-coast premise | Kestrandi | Marisk | the Kest strand |
| **Sundmark** | "strait borderland" — nautical, maritime nation | Sundmarker | Vantholm | the Cold Sund |
