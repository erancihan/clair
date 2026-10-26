# A3 Operator Path and Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Status (2026-10-21): done; verified against Postgres.** Tasks 1–10 complete. On 2026-10-21, with Postgres 16 from `docker-compose.dev.yaml`, Task 9 ran in full: `go generate ./... && go build ./... && go vet ./... && go test -race -count=1 ./...` is green in every package with no skipped test (the eleven tests the hand-off flagged all ran and passed without changes, and the guest rate-limit default was not raised); both compose files pass `config -q`; the image builds and `docker run --rm clair:a3-check --help` prints the subcommands; the live server answers `/api/health` with `"database":"ok"` and the build version, `/no-such-page` with 404, and SIGINT with an open seat stream returns in under 0.1 s. The definition-of-done walk in Step 5 maps each item to a passing test.
>
> Deviations. Task 3: the owner decided Sentry was stale, so instead of gating it on `APP_ENV` it was removed outright (`internal/clair/sentry.go`, the `SetupSentry` call, `SENTRY_DSN`, and the `getsentry/sentry-go` module via `go mod tidy && go mod vendor`; `internal/clair` now holds only `version.go`). `DATABASE_LOG_LEVEL`, `PUBLIC_PATH` and `TEST_DATABASE_URL` are documented and guarded by `test/env_docs_test.go`. Task 4: `seedOneShowing` seeds before the server is built. Task 5: `TestPaymentWebhookRefusesUnknownProvider` sends an unsigned delivery, so its 404 can only come from the provider check (a signed delivery about order 1 would also be 404, from the order lookup). Task 5's health handler moved into `routes/site.go` in Task 7 as planned. Task 7: `internal/server/games/mount.go` is deleted (it held only `Mount`); its doc comment lives on `routes.MountGames`. Rate limits: the guest budget is one bucket shared by every guest write in a mount (`bookingLimits`), not one per route. Nothing is committed.

**Goal:** `git clone && make build && make migrate && make seed && make run-server` works on a fresh machine; `docker compose up` migrates, then runs server and scheduler; a bad `DATABASE_URL` exits non-zero; shutdown drains SSE; unknown URLs return 404; health pings Postgres; guest writes and streams are rate-limited; every env var the code reads is documented.

**Architecture:** Nothing here changes what the booking engine does. It changes how the process starts, stops, reports and refuses. The route table finishes moving into `internal/server/routes` (site + games join booking there). A closing signal travels through `BackEndContext` so long-lived handlers end when the server is told to stop. Rate limiting is an in-house token bucket keyed by client IP in `internal/utils/middleware`, because the repo vendors its dependencies and the need is small.

**Tech Stack:** Go 1.26, cobra, GORM/pgx, templ, Docker + Compose, GNU make, npm (Tailwind v4 via gulp/postcss).

**Spec:** `docs/superpowers/specs/2026-09-02-evaluation-and-roadmap-v2.md` §2.2 S7, S10–S15, S19–S24, S26, S27; §2.3 W19, W21; §2.4 O1, O3–O7, O10, O16; §3 "A3" definition of done. S16 (JSON error envelope) and S25 (accessor layer) are retagged by Task 10 — S16 to C1 because it changes what the pages parse, S25 as a deliberate non-change.

## Global Constraints

- **Do not commit.** The owner signs commits with GPG. "Checkpoint" means `git status` shows only the listed files.
- **Run tests with the database:** `export $(grep -E '^DATABASE_URL=' .env | xargs)` first. Postgres: `docker start clair-postgres-1`.
- **Vendored modules:** `Makefile` builds with `-mod vendor`. Do not add a module dependency in this plan; write the small pieces (rate limiter) in-house.
- **After editing a `.templ` file:** `go generate ./...`.
- **Never "kernel".** Prose says "the booking engine".
- **No real place names, no "government", no public sign-in link** in anything user-facing.
- Cobra: `RunE` returning an error exits 1 and prints `Error: …`; `SilenceUsage` is already on.
- Test helpers: `newRoutesServer(t)` (`test/mount_seam_test.go`), `setupTestServer(t, port)` (`test/utils.go`, drives the real `cmd.ServerCmd`), `newTheaterServer`, `doJSON`, `mustStatus`, `readBody`.

---

### Task 1: One binary, working `make` targets (O1, O5, O16)

**Files:**
- Modify: `Makefile`
- Modify: `README.md` (Installation step 2, Building, Running)

**Interfaces:**
- `OUTFILE := ./builds/clair`; `build` and every runner agree on it. `build-linux-amd64` writes `./builds/clair-linux-amd64`.
- New targets `run-server`, `run-scheduler`; `run` is an alias for `run-server`. `--verbose` is gone (it was never a flag).
- `assets` runs `npm ci` before `npm run css`.

- [x] **Step 1: Write the failing check**

There is no Go test for a Makefile; the check is the commands themselves. Run:

```bash
make -n run-server 2>&1 | head -2; make -n migrate 2>&1 | head -2
```

Expected today: `make: *** No rule to make target 'run-server'` and `./builds/clair.bin migrate` (a file `build` never produces).

- [x] **Step 2: Rewrite the Makefile**

```make
#!make
-include .env
export $(shell sed -e '/^\#/d' -e 's/=.*//' .env 2>/dev/null)
export GOWORKDIR=./

OUT_DIR := ./builds
OUTFILE := ${OUT_DIR}/clair
GO_ARGS := -mod vendor
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/erancihan/clair/internal/clair.Version=${VERSION}
GO_BUILD_CMD := go build ${GO_ARGS} -ldflags "${LDFLAGS}"

GNUMAKEFLAGS=-j3

.PHONY: all deps assets build build-linux-amd64 dev run run-server run-scheduler migrate seed \
        docker docker-build docker-run docker-dev tidy db-up db-down test

all: build

deps:
	go mod download

# CSS is built from the templ sources, then templ is generated. npm ci is what
# makes this work on a fresh clone: nothing else installs the Node toolchain.
assets: PATH:=$(PWD)/node_modules/.bin:$(PATH)
assets: deps
	npm ci
	npm run css
	go generate ./...

build: assets
	${GO_BUILD_CMD} -o "${OUTFILE}" ./cmd/clair

build-linux-amd64: assets
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
		${GO_BUILD_CMD} -o "${OUTFILE}-linux-amd64" ./cmd/clair

# dev with air. air already calls build
dev:
	go tool air

# ----------------------
# One binary, one subcommand per process. The order on a fresh database is
# migrate → seed → run-server (+ run-scheduler).
run: run-server

run-server:
	"${OUTFILE}" server

run-scheduler:
	"${OUTFILE}" scheduler

# apply the database schema (idempotent; requires DATABASE_URL)
migrate:
	"${OUTFILE}" migrate

# seed the Aurenor booking-simulation data (idempotent; requires DATABASE_URL)
seed:
	"${OUTFILE}" seed

# docker
docker: docker-build
docker-build:
	docker build --build-arg VERSION=${VERSION} -t clair .

docker-run:
	docker run -it --rm --env-file=.env clair server

docker-dev: docker-build docker-run

# tidy and vendor
tidy:
	go mod tidy
	go mod vendor

# local infra for development / tests (PostgreSQL + Valkey)
db-up:
	docker compose -f docker-compose.dev.yaml up -d postgres valkey

db-down:
	docker compose -f docker-compose.dev.yaml down

# testing — requires a running PostgreSQL.
# Point DATABASE_URL at it (defaults to the docker-compose.dev.yaml service):
#   make db-up && make test
test:
	go test ./test/... -v
```

(`test:` stays as it is; widening it to `./...` with `-race` is A4.)

- [x] **Step 3: Add the version variable the ldflags point at**

Create `internal/clair/version.go`:

```go
package clair

// Version is stamped at build time by the Makefile and the Dockerfile
// (-ldflags "-X github.com/erancihan/clair/internal/clair.Version=…"). "dev"
// means the binary was built without it, which is how a `go run` reports.
var Version = "dev"
```

- [x] **Step 4: README**

In *Installation → Steps*, step 2 becomes: "`go mod download`, then `go mod vendor`, then `npm ci`." In *Building*: "writes the binary to `./builds/clair`". In *Running*, add before the `go run` examples: "Or, after `make build`: `make migrate`, `make seed`, `make run-server`, and in another terminal `make run-scheduler`."

- [x] **Step 5: Verify**

```bash
make build && ./builds/clair --help | head -3 && make -n run-server migrate seed run-scheduler
```
Expected: the binary builds; `--help` lists `migrate`, `scheduler`, `seed`, `server`; the four dry-runs print `./builds/clair server|migrate|seed|scheduler`.

- [x] **Step 6: Checkpoint** — `Makefile`, `README.md`, `internal/clair/version.go`.

---

### Task 2: A Docker image that starts, a Compose that migrates (O3, O4, O10)

**Files:**
- Modify: `Dockerfile`, `.dockerignore`, `docker-compose.yaml`, `docker-compose.dev.yaml`
- Modify: `README.md` (*With Docker Compose*)

**Interfaces:**
- Image: `ENTRYPOINT ["./clair"]`, `CMD ["server"]`; build arg `VERSION`.
- Compose services: `postgres` (with a healthcheck), `valkey`, `migrate` (one-shot), `server`, `scheduler`, and `seed` behind the `seed` profile.

- [x] **Step 1: Failing check**

```bash
docker compose -f docker-compose.yaml config --services; grep -c "ENTRYPOINT\|CMD" Dockerfile
```
Expected today: three services, `0`.

- [x] **Step 2: Dockerfile**

```dockerfile
# make css files
FROM node:24-alpine AS css-builder

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY internal/web ./internal/web
COPY gulpfile.js ./
RUN npm run css

# create binary
FROM golang:1.25-alpine AS builder

ARG VERSION=dev

RUN apk --no-cache add git

WORKDIR /go/src/app

# Dependencies first, so an edit to a source file does not re-download them.
COPY go.mod go.sum ./
RUN go mod download

COPY . ./
COPY --from=css-builder /app/internal/web/static/css ./internal/web/static/css/

RUN go generate ./...
RUN CGO_ENABLED=0 GOOS=linux go build \
      -ldflags "-X github.com/erancihan/clair/internal/clair.Version=${VERSION}" \
      -o /go/bin/clair ./cmd/clair

# create final image
FROM alpine:3.22

RUN apk --no-cache add ca-certificates
WORKDIR /opt/clair
COPY --from=builder /go/bin/clair ./clair

# One binary, one subcommand per container: server, scheduler, migrate, seed.
ENTRYPOINT ["./clair"]
CMD ["server"]
```

Note `go.mod` says `go 1.25`; the builder image stays `golang:1.25-alpine`. If the tree needs 1.26 features, bump both together.

- [x] **Step 3: .dockerignore** (mirror `.gitignore`; drop the fly.io lines)

```
.git
.vscode
.env
*.log
builds/
*.db
*.exe
*.exe~
*.dll
*.so
*.dylib
*.test
*.out
vendor/
node_modules/
internal/server/templates/
internal/web/**/*_templ.go
internal/web/static/css/main.css
docs/
```

- [x] **Step 4: docker-compose.yaml**

```yaml
services:
  postgres:
    image: postgres:16-alpine
    ports:
      - "5432:5432"
    environment:
      POSTGRES_USER: clair
      POSTGRES_PASSWORD: clair
      POSTGRES_DB: clair
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U clair -d clair"]
      interval: 5s
      timeout: 3s
      retries: 10
    volumes:
      - database:/var/lib/postgresql/data

  valkey:
    image: valkey/valkey:8
    ports:
      - "6379:6379"

  # The schema is a deliberate step, not a side effect of booting. This
  # one-shot service runs it, and the long-lived services wait for it.
  migrate:
    image: ghcr.io/erancihan/clair:latest
    depends_on:
      postgres:
        condition: service_healthy
    env_file:
      - .env
    environment:
      - DATABASE_URL=postgres://clair:clair@postgres:5432/clair?sslmode=disable
    command: ["migrate"]

  # Optional: `docker compose --profile seed up seed` fills the Aurenor catalog.
  seed:
    image: ghcr.io/erancihan/clair:latest
    profiles: ["seed"]
    depends_on:
      migrate:
        condition: service_completed_successfully
    env_file:
      - .env
    environment:
      - DATABASE_URL=postgres://clair:clair@postgres:5432/clair?sslmode=disable
    command: ["seed"]

  server:
    image: ghcr.io/erancihan/clair:latest
    depends_on:
      migrate:
        condition: service_completed_successfully
      valkey:
        condition: service_started
    ports:
      - "4000:4000"
    env_file:
      - .env
    environment:
      - DATABASE_URL=postgres://clair:clair@postgres:5432/clair?sslmode=disable
      - VALKEY_HOST=valkey
    command: ["server"]

  # The hold reaper. Without it an abandoned checkout keeps a seat forever.
  scheduler:
    image: ghcr.io/erancihan/clair:latest
    depends_on:
      migrate:
        condition: service_completed_successfully
    env_file:
      - .env
    environment:
      - DATABASE_URL=postgres://clair:clair@postgres:5432/clair?sslmode=disable
    command: ["scheduler"]

volumes:
  database:
    driver: local
```

`docker-compose.dev.yaml`: the same shape, with every `image: ghcr.io/…` replaced by

```yaml
    build:
      context: ./
      dockerfile: Dockerfile
```

on `migrate`, `seed`, `server` and `scheduler` (Compose builds once and reuses), the volume names kept as they are (`clair-postgres`, `clair-public`), and `server` keeping its `clair-public` volume and `PUBLIC_PATH=/opt/clair/public`.

- [x] **Step 5: README** — *With Docker Compose* becomes:

```
make docker                                  # build the image as clair:latest
docker compose -f docker-compose.dev.yaml up # migrate, then server + scheduler
docker compose -f docker-compose.dev.yaml --profile seed up seed   # once, to fill Aurenor
```
and one sentence: "`docker-compose.yaml` runs the published image from ghcr.io instead of building."

- [x] **Step 6: Verify**

```bash
docker compose -f docker-compose.yaml config -q && docker compose -f docker-compose.dev.yaml config -q && echo compose-ok
docker build --build-arg VERSION=a3-check -t clair:a3-check . && docker run --rm clair:a3-check --help | head -3
```
Expected: `compose-ok`; the image builds; `--help` prints the subcommands (the entrypoint works with no compose `command`).

- [x] **Step 7: Checkpoint** — the four files plus README.

---

### Task 3: Every env var the code reads is documented, and Sentry reports from production (O6, O7, S19)

**Files:**
- Modify: `internal/clair/sentry.go`
- Modify: `internal/database/database.go` (log level)
- Modify: `.env.example`, `README.md` (Configuration table)
- Create: `internal/clair/sentry_test.go`, `test/env_docs_test.go`

**Interfaces:**
- `clair.SentryReports(env string) bool` — true only for `"production"`.
- `DATABASE_LOG_LEVEL` ∈ `silent|error|warn|info`, default `warn`.
- `ENVIRONMENT` is no longer read anywhere.

- [x] **Step 1: Write the failing tests**

`internal/clair/sentry_test.go`:

```go
package clair

import "testing"

func TestSentryReportsOnlyInProduction(t *testing.T) {
	cases := map[string]bool{"production": true, "development": false, "": false, "staging": false}
	for env, want := range cases {
		if got := SentryReports(env); got != want {
			t.Errorf("SentryReports(%q) = %v, want %v", env, got, want)
		}
	}
}
```

`test/env_docs_test.go`:

```go
package test

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEnvExampleDocumentsEveryVariableTheCodeReads: an env var the code reads
// and the example file does not name is a setting nobody can discover.
func TestEnvExampleDocumentsEveryVariableTheCodeReads(t *testing.T) {
	documented := map[string]bool{}
	f, err := os.Open("../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if key, _, ok := strings.Cut(line, "="); ok {
			documented[strings.TrimSpace(key)] = true
		}
	}

	read := regexp.MustCompile(`(?:os\.Getenv|utils\.GetEnv|GetEnv|os\.LookupEnv)\("([A-Z0-9_]+)"`)
	var missing []string
	seen := map[string]bool{}
	err = filepath.WalkDir("../internal", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range read.FindAllStringSubmatch(string(src), -1) {
			name := m[1]
			if seen[name] {
				continue
			}
			seen[name] = true
			if !documented[name] {
				missing = append(missing, name+" ("+filepath.Base(path)+")")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) > 0 {
		t.Fatalf("read by the code but absent from .env.example: %s", strings.Join(missing, ", "))
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/clair/ -run TestSentryReports -count=1; go test ./test/ -run TestEnvExampleDocuments -count=1`
Expected: `undefined: SentryReports`; then `absent from .env.example: ENVIRONMENT (sentry.go), PUBLIC_PATH (embed.go), TEST_DATABASE_URL (pg.go)` (and `DATABASE_LOG_LEVEL` once Step 3 adds it).

- [x] **Step 3: Implement**

`sentry.go`:

```go
package clair

import (
	"log"
	"time"

	utils "github.com/erancihan/clair/internal/utils"
	"github.com/getsentry/sentry-go"
)

// SentryReports says whether events should leave the process. Only production
// reports: a developer's panics are theirs to read in the terminal, and one
// switch (APP_ENV) decides every environment-dependent behaviour in the app.
func SentryReports(env string) bool {
	return env == "production"
}

func SetupSentry() {
	dsn := utils.GetEnv("SENTRY_DSN", "")
	env := utils.GetEnv("APP_ENV", "development")

	err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      env,
		Release:          Version,
		TracesSampleRate: 0.1,
		BeforeSend: func(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
			if !SentryReports(env) {
				return nil
			}
			return event
		},
	})
	if err != nil {
		log.Fatalf("Failed sentry.Init: %s\n", err)
	}
	// Flush buffered events before the program terminates.
	defer func() {
		if err := recover(); err != nil {
			sentry.CurrentHub().Recover(err)
			sentry.Flush(5 * time.Second)
		}
	}()
}
```

`database.go` — replace the `TODO` and the `Logger:` line:

```go
	config := &gorm.Config{
		PrepareStmt: true,
		Logger:      logger.LogMode(gormLogLevel()),
	}
```

and add:

```go
// gormLogLevel reads DATABASE_LOG_LEVEL. The default is warn: at info every
// statement is a log line, and a seat map that streams to a hundred tabs
// turns that into a firehose. Set info when you want to see the SQL.
func gormLogLevel() gormlogger.LogLevel {
	switch strings.ToLower(os.Getenv("DATABASE_LOG_LEVEL")) {
	case "silent":
		return gormlogger.Silent
	case "error":
		return gormlogger.Error
	case "info":
		return gormlogger.Info
	default:
		return gormlogger.Warn
	}
}
```

(add `"strings"` to the imports.)

`.env.example` — append after `DATABASE_AUTO_MIGRATE`:

```
## Optional. How much GORM logs: silent, error, warn (default) or info (every
## statement). Leave it at warn outside a debugging session.
DATABASE_LOG_LEVEL=warn
```

and add a new section before `# Server configuration`:

```
# Files
## Optional. Directory served at /public/. Defaults to internal/web/public.
PUBLIC_PATH=

# Tests
## Optional. Database the test suite uses when set; otherwise DATABASE_URL.
TEST_DATABASE_URL=
```

Also replace the `# TODO: make sentry optional` line with `## Optional. Error reporting; events leave the process only when APP_ENV=production.`

README Configuration table: add rows for `DATABASE_LOG_LEVEL`, `PUBLIC_PATH`, `TEST_DATABASE_URL`; change the `SENTRY_DSN` description to "Error reporting; reports only when `APP_ENV=production`."

- [x] **Step 4: Run the tests**

Run: `go build ./... && go test ./internal/clair/ ./test/ -run 'TestSentryReports|TestEnvExampleDocuments' -count=1`
Expected: PASS.

- [x] **Step 5: Checkpoint** — the six files.

---

### Task 4: The process starts honestly and stops cleanly (S10, S11, S12, S14, S20, S21, S22)

**Files:**
- Modify: `internal/server/context/context.go` (a `Closing` channel)
- Modify: `internal/server/server.go` (`NewBackEnd`, `Server`, `Close`)
- Modify: `internal/booking/live/hub.go` (`Close`)
- Modify: `internal/server/booking/api.go` (`Close` on the API; hub DSN unchanged)
- Modify: `internal/server/booking/api_stream.go`, `api_availability_stream.go`, `api_summary.go` (select on `Closing`)
- Modify: `internal/server/authentication/guest.go` (`RegisterGuestMigrator` replaces by name)
- Modify: `internal/cmd/server.go`, `internal/cmd/scheduler.go`, `internal/cmd/root.go`
- Test: `test/server_lifecycle_test.go` (new), `internal/server/authentication/guest_test.go` (new or existing)

**Interfaces:**
- `server_context.BackEndContext.Closing <-chan struct{}` — closed when the server is shutting down. Every long-lived handler selects on it.
- `(*backend).Close()` — closes `Closing` (once) and calls every registered closer (the hub).
- `(*Hub).Close()` — stops the listener and refuses to restart it.
- `(*API).Close()` — closes the hub.
- `server.NewBackEnd` unchanged in signature. `(*backend).Server(port)` sets `ReadHeaderTimeout: 10 * time.Second`, `IdleTimeout: 120 * time.Second`, `MaxHeaderBytes: 1 << 20`, and registers `Close` via `RegisterOnShutdown`.
- `cmd.ServerCmd`: a database failure returns the error. `cmd.SchedulerCmd`: no HTTP listener. `cmd.Execute`: the pprof listener runs only with `--profile`, on its own mux, and logs its error; the dead `--port` flag is removed.

- [x] **Step 1: Write the failing tests**

`test/server_lifecycle_test.go`:

```go
package test

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erancihan/clair/internal/cmd"
	"github.com/erancihan/clair/internal/server"
	"go.uber.org/zap"
)

// TestServerCmdFailsFastOnAnUnreachableDatabase: a container with a bad
// DATABASE_URL must exit non-zero, or the orchestrator believes it is up.
func TestServerCmdFailsFastOnAnUnreachableDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://clair:clair@127.0.0.1:1/clair?sslmode=disable&connect_timeout=1")
	t.Setenv("SERVER_PORT", "5099")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := cmd.ServerCmd(ctx).ExecuteContext(ctx)
	if err == nil {
		t.Fatal("server command returned nil with an unreachable database")
	}
	if ctx.Err() != nil {
		t.Fatal("server command only failed because the test timed out")
	}
}

// TestShutdownEndsOpenStreams: an SSE handler never returns on its own, so a
// graceful shutdown that waits for handlers would wait the whole timeout and
// then cut the connections anyway. The server has to tell its streams to end.
func TestShutdownEndsOpenStreams(t *testing.T) {
	db := newAuthTestDB(t)
	backend := server.NewBackEnd(context.Background(), zap.NewNop(), nil, db)
	srv := backend.Server(0)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(listener) }()
	base := "http://" + listener.Addr().String()

	// Seed one event so the stream has something to open on.
	seedOneShowing(t, db)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/events/summary/stream?slugs=grand-dune-part-two-1000", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status %d", resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	if _, err := reader.ReadString('\n'); err != nil { // the first frame
		t.Fatal(err)
	}

	start := time.Now()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("shutdown with one open stream took %s; the stream was not told to end", took)
	}
	// The client sees its stream end.
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if !strings.HasPrefix(line, ":") && strings.TrimSpace(line) != "" {
			continue
		}
	}
}
```

Add a helper `seedOneShowing(t, db)` in the same file that calls `aurenor.Seed(db, time.Now(), "seed@aurenor.test")` and installs the notify triggers (copy the two calls from `newTheaterServer` in `test/aurenor_theater_test.go`).

For `RegisterGuestMigrator`, add to `internal/server/authentication/guest_test.go` (package `authentication`; create the file if there is none):

```go
package authentication

import (
	"context"
	"testing"

	"gorm.io/gorm"
)

func TestRegisterGuestMigratorReplacesByName(t *testing.T) {
	guestMigratorsMu.Lock()
	guestMigrators = nil
	guestMigratorsMu.Unlock()

	noop := func(context.Context, *gorm.DB, string, string) error { return nil }
	RegisterGuestMigrator("booking", noop)
	RegisterGuestMigrator("booking", noop)
	RegisterGuestMigrator("games", noop)

	guestMigratorsMu.RLock()
	defer guestMigratorsMu.RUnlock()
	if len(guestMigrators) != 2 {
		t.Fatalf("registering the same name twice left %d migrators, want 2", len(guestMigrators))
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./test/ -run 'TestServerCmdFailsFast|TestShutdownEndsOpenStreams' -count=1 -v; go test ./internal/server/authentication/ -run TestRegisterGuestMigrator -count=1`
Expected: the cmd test FAILS (`returned nil`); the shutdown test FAILS on the 5s timeout (`shutdown: context deadline exceeded` or `took 5s`); the registry test FAILS with `left 3 migrators`.

- [x] **Step 3: Implement**

`internal/server/context/context.go`:

```go
type BackEndContext struct {
	DBConn *gorm.DB
	Logger *zap.Logger
	ValKey valkey.Client

	// Closing is closed when the server is shutting down. A handler that would
	// otherwise run for the life of the connection — a stream — selects on it,
	// so a graceful shutdown drains instead of waiting out its timeout.
	Closing <-chan struct{}
}
```

`internal/server/server.go`:

```go
type backend struct {
	context server_context.BackEndContext

	closing   chan struct{}
	closeOnce sync.Once
	closers   []func()
}

func NewBackEnd(ctx context.Context, logger *zap.Logger, valkey valkey.Client, pool *gorm.DB) *backend {
	closing := make(chan struct{})
	return &backend{
		context: server_context.BackEndContext{
			DBConn:  pool,
			Logger:  logger,
			ValKey:  valkey,
			Closing: closing,
		},
		closing: closing,
	}
}

// Server wraps the handler in an http.Server with the timeouts a public
// listener needs. WriteTimeout is deliberately unset: an event stream writes
// for as long as the tab is open.
func (s *backend) Server(port int) *http.Server {
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           s.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	srv.RegisterOnShutdown(s.Close)
	return srv
}

// Close tells every long-lived part of the server to stop: open streams end,
// the notification listener disconnects. Safe to call more than once.
func (s *backend) Close() {
	s.closeOnce.Do(func() {
		close(s.closing)
		for _, c := range s.closers {
			c()
		}
	})
}
```

In `Routes()`, after `api := …` is not available (MountBooking builds it internally), so change `routes.MountBooking` to return the closer: `s.closers = append(s.closers, routes.MountBooking(app, s.context))`. In `routes/booking.go`: `func MountBooking(r *router.Router, ctx server_context.BackEndContext) func()` returning `api.Close`.

`internal/server/booking/api.go`: `func (a *API) Close() { a.hub.Close() }`.

`internal/booking/live/hub.go`:

```go
// Close stops the listener for good. Subscribers keep their channels (they
// end on their own when their handlers return) but nothing will signal them.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	if h.running {
		h.cancel()
		h.running = false
		h.connected.Store(false)
	}
}
```

add `closed bool` to the struct, and in `SubscribeAll` change the start condition to `if !h.running && !h.closed && h.dsn != ""`.

The three stream loops (`api_stream.go`, `api_availability_stream.go`, `api_summary.go`) gain a case at the top of their `select`:

```go
			case <-ctx.Closing:
				return
```

`internal/server/authentication/guest.go` — `RegisterGuestMigrator`:

```go
func RegisterGuestMigrator(name string, m GuestMigrator) {
	if m == nil {
		return
	}
	guestMigratorsMu.Lock()
	defer guestMigratorsMu.Unlock()
	// One migrator per name. The booking API registers on construction, and a
	// test suite constructs it dozens of times; without this every login would
	// run the same migration once per construction.
	for i := range guestMigrators {
		if guestMigrators[i].name == name {
			guestMigrators[i].migrate = m
			return
		}
	}
	guestMigrators = append(guestMigrators, registeredMigrator{name: name, migrate: m})
}
```

`internal/cmd/server.go`: `return nil` after the database error becomes `return err`. Nothing else: `Server(port)` now registers `Close` itself.

`internal/cmd/scheduler.go`: delete everything from `// Same rule as the server: bind first` through the `select`, replacing it with:

```go
			// The scheduler has no HTTP surface. It runs until asked to stop.
			<-ctx.Done()
			scheduler.Stop()
			return nil
```

and drop the now-unused `errors`, `net`, `net/http` imports.

`internal/cmd/root.go`: remove the `port` persistent flag; replace the bare goroutine with:

```go
	if profile {
		// Only when asked, and on a mux of its own: the default mux is a
		// global anything can register on.
		mux := http.NewServeMux()
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		go func() {
			if err := http.ListenAndServe("localhost:6060", mux); err != nil {
				log.Printf("pprof listener: %v", err)
			}
		}()
	}
```

Since `profile` is bound by cobra during `Execute`, this block has to run inside `PersistentPreRunE` (where `profile` is already parsed), not before `ExecuteContext`. Move it there, under the existing `if !profile { return nil }` guard. Imports: `net/http/pprof` as `pprof` conflicts with `runtime/pprof` already imported as `pprof` — alias the net one: `httppprof "net/http/pprof"`, and `"log"`.

- [x] **Step 4: Run the tests**

Run: `go build ./... && go vet ./... && go test ./internal/server/... ./internal/booking/live/ -count=1 && go test ./test/ -run 'Lifecycle|ServerCmd|Shutdown|Health|Authentication|Stream' -count=1`
Expected: PASS.

- [x] **Step 5: Checkpoint** — the eleven files.

---

### Task 5: Honest statuses: 404 is 404, 409 is a duplicate, the cancel token is in the body, the webhook names a real provider, health pings the database (S13, S15, S23, S24, S27)

**Files:**
- Modify: `internal/server/server.go` (404 status; health; delete `/protected`)
- Modify: `internal/server/booking/api_http.go` (`isUniqueViolation`)
- Modify: `internal/server/booking/api_events.go`, `api_schedules.go` (create error mapping)
- Modify: `internal/server/booking/api_appointments.go` (`cancelByToken` reads JSON)
- Modify: `internal/server/booking/api_webhook.go` (provider allowlist)
- Modify: `internal/web/pages/aurenor_service_script.templ` (cancel sends a body)
- Modify: `test/mount_seam_test.go`, `test/server_test.go`, `test/appointments_http_test.go`, `test/aurenor_appointments_test.go`, `test/booking_seam_test.go`

**Interfaces:**
- `POST /appointments/cancel` body `{"token": "..."}`; the query form is gone.
- `POST /webhooks/payments/{provider}` answers 404 for any provider not in `paymentProviders` (`mock` today).
- `GET /api/health` → `{"status":"ok"|"degraded","version":…,"database":"ok"|"error","valkey":…}`; 503 when the database ping fails.

- [x] **Step 1: Write the failing tests**

`test/mount_seam_test.go`: change the last table row to `{name: "unknown path is a 404", path: "/no-such-page", status: http.StatusNotFound, contains: "Page not found"}`.

`test/server_test.go`: in `TestAuthentication`, replace both `/api/protected` URLs with `/appointments/me` (an authenticated JSON route that exists).

Cancel-by-token: in `test/appointments_http_test.go` lines 228 and 472 and `test/aurenor_appointments_test.go` line 202, the request becomes `doJSON(t, guest, "POST", ts.URL+"/appointments/cancel", map[string]any{"token": appt.CancelToken})` (for the aurenor test, build `cancelURL := ts.URL + "/appointments/cancel"` and pass the body where the token was in the URL).

Append to `test/booking_seam_test.go`:

```go
// TestPaymentWebhookRefusesUnknownProvider: the provider segment is persisted
// on the payment row, so it has to name a provider this application knows.
func TestPaymentWebhookRefusesUnknownProvider(t *testing.T) {
	ts, secret := newWebhookServer(t) // reuse whatever helper the file already uses to get a signed-webhook server + secret
	body := []byte(`{"delivery_id":"d-unknown","order_id":1}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/webhooks/payments/evil", bytes.NewReader(body))
	req.Header.Set("X-Payment-Signature", sign(secret, body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown provider answered %d, want 404", resp.StatusCode)
	}
}
```

(Read the file's existing webhook test at lines 130–200 first and use its exact server/secret/sign helpers; the names above are placeholders for *those* helpers — if the file inlines them, inline the same three lines here.)

Append to `test/server_test.go`:

```go
func TestHealthReportsTheDatabase(t *testing.T) {
	baseURL, teardown := setupTestServer(t, "5055")
	defer teardown()
	resp, err := http.Get(baseURL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["database"] != "ok" {
		t.Fatalf("health database = %q, want ok", body["database"])
	}
	if body["version"] == "" {
		t.Fatal("health carries no version")
	}
}
```

Duplicate slug: append to `test/appointments_http_test.go` (use the file's authenticated-host client helper; the create-schedule test near line 60–110 shows the shape):

```go
func TestCreateScheduleDuplicateSlugIs409AndOtherErrorsAreNot(t *testing.T) {
	ts, host := newHostClient(t) // the file's helper that logs a host in and returns a CSRF-bearing client
	body := map[string]any{"slug": "dup-office", "name": "Dup", "timezone": "UTC", "slot_duration_min": 30, "capacity": 1, "window_days": 30}
	first := doJSON(t, host, "POST", ts.URL+"/appointments/schedules", body)
	mustStatus(t, first, http.StatusCreated, "first create")
	first.Body.Close()
	second := doJSON(t, host, "POST", ts.URL+"/appointments/schedules", body)
	mustStatus(t, second, http.StatusConflict, "duplicate slug")
	second.Body.Close()

	bad := doJSON(t, host, "POST", ts.URL+"/appointments/schedules",
		map[string]any{"slug": "bad-tz", "name": "Bad", "timezone": "Mars/Olympus", "slot_duration_min": 30, "capacity": 1, "window_days": 30})
	if bad.StatusCode == http.StatusConflict {
		t.Fatalf("an invalid timezone was reported as a duplicate slug (409)")
	}
	bad.Body.Close()
}
```

(Adjust field names to the request struct `createSchedule` decodes — read `api_schedules.go:60-120` for the exact JSON tags before writing this test.)

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./test/ -run 'TestMountedRoutes|TestAuthentication|TestPaymentWebhookRefusesUnknownProvider|TestHealthReportsTheDatabase|TestCreateScheduleDuplicate|Cancel' -count=1`
Expected: FAIL — unknown path is 200; `/appointments/me` behaves like `/protected` did (this one may pass already); unknown provider is 401/404-by-signature or 200; health lacks `database`; the bad-timezone create returns 409; cancel with a body is 400 `token is required`.

- [x] **Step 3: Implement**

`server.go` `mountPages` 404: `templ.Handler(web.Base(...), templ.WithStatus(http.StatusNotFound)).ServeHTTP(w, r)`.

`server.go` health (still in `mountAPI` for this task; Task 7 moves it):

```go
		api.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
			status := "ok"
			code := http.StatusOK

			// The database is the one dependency without which nothing works.
			dbStatus := "ok"
			if sqlDB, err := s.context.DBConn.DB(); err != nil || sqlDB.PingContext(r.Context()) != nil {
				dbStatus, status, code = "error", "degraded", http.StatusServiceUnavailable
			}

			// Valkey is optional; a degraded Valkey is not an outage.
			valkeyStatus := utils.ValKeyStatus(r.Context(), s.context.ValKey)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status":   status,
				"version":  clair.Version,
				"database": dbStatus,
				"valkey":   valkeyStatus,
			})
		})
```

Delete the `/protected` group. Import `github.com/erancihan/clair/internal/clair`.

`api_http.go`:

```go
// isUniqueViolation reports a Postgres unique-constraint failure (SQLSTATE
// 23505), which is what "that slug is taken" looks like from here. Anything
// else is not the caller's fault and must not be reported as if it were.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
```

In `createEvent` and `createSchedule`:

```go
		if err != nil {
			if isUniqueViolation(err) {
				http.Error(w, "slug already taken", http.StatusConflict)
				return
			}
			ctx.Logger.Error("create schedule", zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
```

`cancelByToken`:

```go
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
			http.Error(w, "token is required", http.StatusBadRequest)
			return
		}
		token := body.Token
```

(the rest unchanged). Its doc comment: "The token travels in the body, not the query string: it is the whole authorization, and query strings land in proxy logs and browser history."

`api_webhook.go`: add `var paymentProviders = map[string]bool{"mock": true}` with a comment, and at the top of the handler:

```go
		provider := r.PathValue("provider")
		if !paymentProviders[provider] {
			http.NotFound(w, r)
			return
		}
```

using `provider` in the `BookingPayment` literal.

`aurenor_service_script.templ` line ~299:

```js
						const res = await fetch('/appointments/cancel', {
							method: 'POST',
							headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': aurenorCSRF() },
							body: JSON.stringify({ token: token }),
						});
```

(keep whatever else that fetch already sets). Then `go generate ./...`.

- [x] **Step 4: Run the tests**

Run: `go generate ./... && go build ./... && go test ./test/ -count=1`
Expected: PASS across the whole HTTP suite.

- [x] **Step 5: Checkpoint** — the listed files.

---

### Task 6: Rate limits on guest writes and streams (S7)

**Files:**
- Create: `internal/utils/middleware/ratelimit.go`, `internal/utils/middleware/ratelimit_test.go`
- Modify: `internal/server/routes/booking.go`
- Modify: `.env.example`, `README.md`
- Test: `test/ratelimit_http_test.go` (new)

**Interfaces:**
- `middleware.PerIP(rps float64, burst int) func(http.Handler) http.Handler` — token bucket per client IP; over budget → `429` with `Retry-After: 1`.
- `middleware.MaxConcurrentPerIP(n int) func(http.Handler) http.Handler` — in-flight requests per IP; over → `429`.
- `middleware.ClientIP(r *http.Request) string` — `RemoteAddr` host; the first `X-Forwarded-For` hop only when `TRUST_PROXY_HEADERS=true`.
- Env: `RATE_LIMIT_GUEST_RPS` (default `5`), `RATE_LIMIT_GUEST_BURST` (default `20`), `RATE_LIMIT_STREAMS_PER_IP` (default `12`), `TRUST_PROXY_HEADERS` (default `false`).

- [x] **Step 1: Write the failing tests**

`internal/utils/middleware/ratelimit_test.go`:

```go
package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestPerIPAllowsBurstThenRefuses(t *testing.T) {
	h := PerIP(1, 3)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	codes := make([]int, 0, 5)
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("POST", "/x", nil)
		req.RemoteAddr = "203.0.113.7:4444"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	want := []int{204, 204, 204, 429, 429}
	for i := range want {
		if codes[i] != want[i] {
			t.Fatalf("request %d: got %d, want %d (all: %v)", i+1, codes[i], want[i], codes)
		}
	}
}

func TestPerIPKeepsClientsApart(t *testing.T) {
	h := PerIP(1, 1)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, ip := range []string{"203.0.113.1:1", "203.0.113.2:1"} {
		req := httptest.NewRequest("POST", "/x", nil)
		req.RemoteAddr = ip
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 204 {
			t.Fatalf("first request from %s got %d", ip, rec.Code)
		}
	}
}

func TestPerIPRefills(t *testing.T) {
	h := PerIP(100, 1)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	req := httptest.NewRequest("POST", "/x", nil)
	req.RemoteAddr = "203.0.113.9:1"
	first := httptest.NewRecorder()
	h.ServeHTTP(first, req)
	second := httptest.NewRecorder()
	h.ServeHTTP(second, req)
	if second.Code != 429 {
		t.Fatalf("second immediate request got %d, want 429", second.Code)
	}
	time.Sleep(30 * time.Millisecond)
	third := httptest.NewRecorder()
	h.ServeHTTP(third, req)
	if third.Code != 204 {
		t.Fatalf("after refill got %d, want 204", third.Code)
	}
}

func TestMaxConcurrentPerIP(t *testing.T) {
	release := make(chan struct{})
	h := MaxConcurrentPerIP(2)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(204)
	}))
	var wg sync.WaitGroup
	codes := make([]int, 3)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/stream", nil)
			req.RemoteAddr = "203.0.113.5:1"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i)
	}
	time.Sleep(50 * time.Millisecond) // both in flight
	req := httptest.NewRequest("GET", "/stream", nil)
	req.RemoteAddr = "203.0.113.5:1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	codes[2] = rec.Code
	close(release)
	wg.Wait()
	if codes[2] != 429 || codes[0] != 204 || codes[1] != 204 {
		t.Fatalf("codes = %v, want [204 204 429]", codes)
	}
}

func TestClientIPHonoursForwardedForOnlyWhenTrusted(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.2:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.4, 10.0.0.2")
	if got := ClientIP(req); got != "10.0.0.2" {
		t.Fatalf("untrusted: got %q, want the socket address", got)
	}
	t.Setenv("TRUST_PROXY_HEADERS", "true")
	if got := ClientIP(req); got != "198.51.100.4" {
		t.Fatalf("trusted: got %q, want the first forwarded hop", got)
	}
}
```

`test/ratelimit_http_test.go`:

```go
package test

import (
	"net/http"
	"testing"
)

// TestGuestWritesAreRateLimited: a hold endpoint with no per-buyer cap and no
// rate limit is a loop away from an empty salon.
func TestGuestWritesAreRateLimited(t *testing.T) {
	t.Setenv("RATE_LIMIT_GUEST_RPS", "1")
	t.Setenv("RATE_LIMIT_GUEST_BURST", "2")
	ts := newTheaterServer(t)
	slug, _ := firstSeated(t, ts)
	client := newJarClient()

	codes := []int{}
	for i := 0; i < 4; i++ {
		resp := doJSON(t, client, "POST", ts.URL+"/events/"+slug+"/holds/release", map[string]any{"hold_tokens": []string{"nope"}})
		codes = append(codes, resp.StatusCode)
		resp.Body.Close()
	}
	if codes[0] == http.StatusTooManyRequests || codes[3] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v; want the first to pass and the fourth to be 429", codes)
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/utils/middleware/ -count=1; go test ./test/ -run TestGuestWritesAreRateLimited -count=1`
Expected: build FAIL (`undefined: PerIP` …); the HTTP test FAILS with four equal codes.

- [x] **Step 3: Implement**

`internal/utils/middleware/ratelimit.go`:

```go
package middleware

import (
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ClientIP is the address a limit is keyed on. It is the socket's peer unless
// TRUST_PROXY_HEADERS=true, in which case the first X-Forwarded-For hop is
// taken instead — trusting that header from an untrusted peer would let a
// caller pick its own bucket.
func ClientIP(r *http.Request) string {
	if os.Getenv("TRUST_PROXY_HEADERS") == "true" {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			if ip := strings.TrimSpace(first); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type bucket struct {
	tokens float64
	last   time.Time
}

// PerIP is a token bucket per client address: `burst` requests at once, then
// `rps` a second. Buckets idle for ten minutes are dropped on the next sweep,
// so the map is bounded by recent callers rather than by every address ever
// seen.
func PerIP(rps float64, burst int) func(http.Handler) http.Handler {
	var (
		mu      sync.Mutex
		buckets = map[string]*bucket{}
		sweeps  int
	)
	const idle = 10 * time.Minute

	allow := func(ip string, now time.Time) bool {
		mu.Lock()
		defer mu.Unlock()

		if sweeps++; sweeps%1000 == 0 {
			for k, b := range buckets {
				if now.Sub(b.last) > idle {
					delete(buckets, k)
				}
			}
		}

		b := buckets[ip]
		if b == nil {
			b = &bucket{tokens: float64(burst), last: now}
			buckets[ip] = b
		}
		b.tokens += now.Sub(b.last).Seconds() * rps
		if b.tokens > float64(burst) {
			b.tokens = float64(burst)
		}
		b.last = now
		if b.tokens < 1 {
			return false
		}
		b.tokens--
		return true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !allow(ClientIP(r), time.Now()) {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "too many requests", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// MaxConcurrentPerIP caps how many requests one address has in flight. It is
// for streams: each one holds a connection for as long as the tab is open, and
// a browser is allowed about six per origin, so a dozen from one address is a
// script, not a person.
func MaxConcurrentPerIP(n int) func(http.Handler) http.Handler {
	var (
		mu       sync.Mutex
		inflight = map[string]int{}
	)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ClientIP(r)
			mu.Lock()
			if inflight[ip] >= n {
				mu.Unlock()
				w.Header().Set("Retry-After", "5")
				http.Error(w, "too many open streams", http.StatusTooManyRequests)
				return
			}
			inflight[ip]++
			mu.Unlock()
			defer func() {
				mu.Lock()
				if inflight[ip]--; inflight[ip] <= 0 {
					delete(inflight, ip)
				}
				mu.Unlock()
			}()
			next.ServeHTTP(w, r)
		})
	}
}
```

`routes/booking.go` — read the settings once in `MountBooking`:

```go
	guestLimit := middleware.PerIP(envFloat("RATE_LIMIT_GUEST_RPS", 5), envInt("RATE_LIMIT_GUEST_BURST", 20))
	streamLimit := middleware.MaxConcurrentPerIP(envInt("RATE_LIMIT_STREAMS_PER_IP", 12))
```

and pass them into `mountAppointments`/`mountEvents`: the two guest-write groups become `}, guestLimit, api_auth.CSRF())`; the three stream routes are registered through a group `r.Group("", func(streams *router.Router) { … }, streamLimit)` around each `…/stream` handler (or wrap the handler: `streamLimit(api.SeatStream()).ServeHTTP` — the router's `Middleware(...)` returns a sub-router, so `events.Middleware(streamLimit).HandleFunc("GET /{slug}/seats/stream", api.SeatStream())` is the cleanest). Add `envFloat`/`envInt` helpers at the bottom of the file:

```go
func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func envFloat(name string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(name), 64); err == nil && v > 0 {
		return v
	}
	return def
}
```

`.env.example` — a new section:

```
# Rate limits (per client address)
## Guest writes: holds, releases, bookings, cancellations, waitlist joins.
RATE_LIMIT_GUEST_RPS=5
RATE_LIMIT_GUEST_BURST=20
## Open event streams one address may hold at once.
RATE_LIMIT_STREAMS_PER_IP=12
## Set to true only behind a proxy you control; the first X-Forwarded-For hop
## then keys the limits instead of the socket address.
TRUST_PROXY_HEADERS=false
```

README table: four rows.

- [x] **Step 4: Run the tests**

Run: `go build ./... && go test ./internal/utils/middleware/ -count=1 && go test ./test/ -count=1`
Expected: PASS. If any existing HTTP test trips the default burst of 20, raise the default rather than the test — a real page never posts twenty times in four seconds.

- [x] **Step 5: Checkpoint** — the listed files.

---

### Task 7: The whole route table lives in `routes` (S26)

**Files:**
- Create: `internal/server/routes/site.go`, `internal/server/routes/games.go`
- Modify: `internal/server/server.go` (Routes shrinks to static + three mounts)
- Delete the `Mount` function from `internal/server/games/mount.go` (keep the file's doc comment in `routes/games.go`); the games package keeps its handlers and `Models()`.
- Modify: `test/users_endpoint_test.go` (path without trailing slash; assert the slash form is 404)

**Interfaces:**
- `routes.MountSite(r *router.Router, ctx server_context.BackEndContext)` — health, auth, users, the site's own pages.
- `routes.MountGames(r *router.Router, ctx server_context.BackEndContext)`.
- `GET /api/v1/users` is registered exactly; `GET /api/v1/users/` is a 404, not a subtree.

- [x] **Step 1: Write the failing test**

In `test/users_endpoint_test.go` change the request URL to `baseURL+"/api/v1/users"` and add at the end of the test:

```go
	t.Run("the collection path is exact, not a subtree", func(t *testing.T) {
		resp := get(t, adminCookies)
		resp.Body.Close()
		req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/users/", nil)
		for _, c := range adminCookies {
			req.AddCookie(c)
		}
		slash, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		slash.Body.Close()
		if slash.StatusCode != http.StatusNotFound {
			t.Fatalf("/api/v1/users/ answered %d, want 404", slash.StatusCode)
		}
	})
```

(`adminCookies` and `get` — use the names the test already has for the admin's cookies and its GET helper; read the file before editing.)

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./test/ -run TestUsersEndpoint -count=1`
Expected: FAIL — the exact path 301s or 404s today, and the slash form answers 200.

- [x] **Step 3: Implement**

`routes/site.go` holds, verbatim, what `mountAPI` and `mountPages` do today (health as rewritten in Task 5, auth, users, pages), with the users route registered as:

```go
	r.Group("api/v1", func(v1 *router.Router) {
		// … auth group unchanged …

		// Registered from the v1 router with its full path so the pattern is
		// "/api/v1/users" exactly. A "GET /" inside a "users" group would be the
		// subtree pattern "/api/v1/users/", which answers everything beneath it.
		v1.Middleware(api_auth.AdminMiddleware(ctx)).HandleFunc("GET /users", listUsers(ctx))
	})
```

with `listUsers` the existing closure lifted into a named function. `routes/games.go` is the body of `games.Mount` moved over (imports `github.com/erancihan/clair/internal/server/games` for `games.TicTacToe`/`games.Chess`), keeping the CSRF caveat comment. `server.go`'s `Routes()`:

```go
	app := mux.Middleware(api_auth.OptionalAuthMiddleware(s.context))

	// ---- the route table ---------------------------------------------------
	// Every path the server answers is registered in internal/server/routes.
	routes.MountSite(app, s.context)
	routes.MountGames(app, s.context)
	s.closers = append(s.closers, routes.MountBooking(app, s.context))
```

and `mountAPI`/`mountPages` are deleted along with their now-unused imports. Update the `Routes` doc comment: the reference implementation to copy is now `routes/games.go`, and the sentence about `games.Mount` goes.

- [x] **Step 4: Run the tests**

Run: `go build ./... && go vet ./... && go test ./test/ -count=1`
Expected: PASS, including `TestMountedRoutes` (every page still where it was).

- [x] **Step 5: Checkpoint** — the listed files.

---

### Task 8: Frontend build hygiene (W19, W21)

**Files:**
- Delete: `tailwind.config.js`
- Modify: `gulpfile.js` (drop the config argument and the `go-server` task)
- Modify: `internal/web/page.go:30` (comment)

- [x] **Step 1: Failing check**

Run: `npx gulp watch --tasks-simple 2>&1 | head -2; npm run dev 2>&1 | tail -3` (Ctrl-C after it errors)
Expected today: `Task never defined: go-server`.

- [x] **Step 2: Implement**

`gulpfile.js`:

```js
const gulp = require('gulp');

const postcss = require('gulp-postcss');
const tailwindcss = require('@tailwindcss/postcss');
const autoprefixer = require('autoprefixer');

// Build the stylesheet from the templ sources. Tailwind v4 finds the class
// names by scanning the project itself; there is no config file to point it
// at, and none is needed.
gulp.task('css', function () {
    return gulp
        .src('./internal/web/css/main.css')
        .pipe(postcss([tailwindcss(), autoprefixer(), require('cssnano')()]))
        .pipe(require('gulp-rename')({ extname: '.css' }))
        .pipe(gulp.dest('./internal/web/static/css'));
});

// Rebuild the CSS when a template changes. The Go binary is rebuilt by air
// (`make dev`), which is the other half of the loop.
gulp.task('watch', function () {
    gulp.watch('./internal/web/**/*.templ', gulp.series('css'));
});

gulp.task('default', gulp.series('css', 'watch'));
```

Delete `tailwind.config.js`. In `internal/web/page.go`, the `User` field comment becomes: "User is the signed-in visitor, or nil for an anonymous one — in which case the account slot in the header renders nothing. There is deliberately no public sign-in link."

- [x] **Step 3: Verify**

Run: `npm run css && grep -c "max-w-3xl" internal/web/static/css/main.css && go generate ./... && go build ./...`
Expected: the CSS builds without the config and still contains a class the pages use; the tree builds.

- [x] **Step 4: Checkpoint** — `gulpfile.js`, deleted `tailwind.config.js`, `internal/web/page.go`.

---

### Task 9: Full verification

- [x] **Step 1:** `export $(grep -E '^DATABASE_URL=' .env | xargs) && go generate ./... && go build ./... && go vet ./... && go test -race -count=1 ./...` — every package `ok`.
- [x] **Step 2:** `make build && make -n migrate seed run-server run-scheduler` prints the four `./builds/clair …` lines.
- [x] **Step 3:** `docker compose -f docker-compose.dev.yaml config -q && docker compose -f docker-compose.yaml config -q`.
- [x] **Step 4:** `./builds/clair server` with the dev database running: `curl -s localhost:4000/api/health` shows `"database":"ok"` and a version; `curl -si localhost:4000/no-such-page | head -1` is `404`; Ctrl-C with a board open in a browser returns within a second. (`SERVER_PORT` from `.env`; if 4000 is taken the command now says so and exits 1.)
- [x] **Step 5: Definition-of-done walk**, each with its test: fresh-clone path (Task 1 Step 5); compose migrates (Task 2 Step 6); bad DB exits non-zero (`TestServerCmdFailsFastOnAnUnreachableDatabase`); shutdown drains SSE (`TestShutdownEndsOpenStreams`); 404 (`TestMountedRoutes`); health pings (`TestHealthReportsTheDatabase`); rate limits (`TestGuestWritesAreRateLimited`); env documented (`TestEnvExampleDocumentsEveryVariableTheCodeReads`).

---

### Task 10: Paper trail

- [x] **Step 1:** Evaluation doc: S16's Step cell → `C1` with the note "changes what the pages parse; land it with the frontend pass". S25's Step cell → `—` with the note "kept: the accessor layer is what lets the route table live outside the domain package; 25 one-liners are the price of that boundary". Append ` *(done 2026-09-02)*` to the A3 bullet in §3.
- [x] **Step 2:** `aurenor.md`: the A3 bullet gets `*(done)*`.
- [x] **Step 3:** Tick every box in this plan and add a status banner under the title with any deviations.
