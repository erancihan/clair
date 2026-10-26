# Clair

A personal site and the services behind it, written in Go: server-rendered pages
(templ + Tailwind + Alpine), a couple of games, and a booking service that runs
the Aurenor simulations.

## Installation

### Prerequisites
- Go
- Node (for the CSS build)
- PostgreSQL
- Valkey (optional; the app runs without it)

### Steps
1. Clone the repository.
2. `go mod download`, then `go mod vendor`, then `npm ci`.
3. Copy `.env.example` to `.env` and fill it in.
4. `make db-up` to start PostgreSQL and Valkey with Docker Compose.

## Usage

### Running

After `make build`, the everyday sequence is `make migrate`, `make seed` (once),
`make run-server`, and `make run-scheduler` in a second terminal. The same
subcommands run without a build:

- The web server:
  ```bash
  go run cmd/clair/main.go server
  ```

- The scheduler — background jobs that depend on time passing rather than on a
  request arriving. Today that is the hold reaper, which releases reservations
  nobody paid for:
  ```bash
  go run cmd/clair/main.go scheduler
  ```

- The migrator — applies the schema. Nothing else changes structure, so a
  rebuild or a restart never rewrites tables on its own. Idempotent:
  ```bash
  go run cmd/clair/main.go migrate
  ```

- The seeder — populates the Aurenor booking simulations. Run it after the
  migrator. Idempotent:
  ```bash
  go run cmd/clair/main.go seed
  ```

- Live reload while developing:
  ```bash
  make dev
  ```

### Building

```bash
make build
```

Builds the CSS, runs `go generate`, and writes the binary to `./builds/clair`.
The same binary serves every subcommand above.

### With Docker Compose

```bash
make docker                                   # build the image as clair:latest
docker compose -f docker-compose.dev.yaml up  # migrate, then server + scheduler
docker compose -f docker-compose.dev.yaml --profile seed up seed   # once, to fill Aurenor
```

`docker-compose.yaml` runs the published image from ghcr.io instead of building.

## Configuration

`.env` values, all read at startup:

| Key | Optional | Description |
|:-|:-:|:-|
| `APP_ENV` | yes | `production` makes `SESSION_KEY` mandatory and cookies `Secure`. |
| `SESSION_KEY` | in dev | Signing key for session cookies. Required in production. |
| `DATABASE_URL` | | PostgreSQL connection string. |
| `DATABASE_AUTO_MIGRATE` | yes | `true` applies the schema on every startup. Off by default: use `clair migrate` instead. |
| `DATABASE_LOG_LEVEL` | yes | `silent`, `error`, `warn` (default) or `info` (every statement). |
| `BOOKING_PAYMENT_WEBHOOK_SECRET` | | Shared secret the payment provider signs its webhooks with. The webhook refuses every delivery while this is unset. |
| `SEED_HOST_EMAIL` | yes | Owner of the seeded Aurenor schedules and events. |
| `VALKEY_HOST`, `VALKEY_PORT` | yes | Valkey connection. Unset means no Valkey. |
| `SERVER_PORT` | yes | Defaults to 4000. |
| `RATE_LIMIT_GUEST_RPS` | yes | Guest writes per second per client address. Defaults to 5. |
| `RATE_LIMIT_GUEST_BURST` | yes | Guest writes a client address may make at once before the per-second rate applies. Defaults to 20. |
| `RATE_LIMIT_STREAMS_PER_IP` | yes | Open event streams per client address. Defaults to 12. |
| `TRUST_PROXY_HEADERS` | yes | `true` keys the limits on the first `X-Forwarded-For` hop. Set it only behind a proxy you control. Defaults to `false`. |
| `PUBLIC_PATH` | yes | Directory served at `/public/`. Defaults to `internal/web/public`. |
| `TEST_DATABASE_URL` | yes | Database the test suite uses when set; otherwise `DATABASE_URL`. |

## Testing

Tests need a running PostgreSQL and skip when `DATABASE_URL` is unset:

```bash
make db-up && make test
```

## Road map

The roadmap lives in `docs/wiki/roadmap.md`. How the booking service works,
with sequence diagrams, is in `docs/wiki/booking-service.md`. Auth, sessions
and the user model are done. The booking systems are built and the next work
is CI gates, then a new deploy.
