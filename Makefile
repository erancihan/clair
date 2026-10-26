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
        docker docker-build docker-run docker-dev tidy db-up db-down test lint

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
# Same steps as .github/workflows/test.yaml, so a green `make test` means a
# green CI run.
test:
	go generate ./...
	go vet ./...
	CGO_ENABLED=0 go build ./...
	go test -race -count=1 ./...

# lint with the .golangci.yml baseline (needs golangci-lint v2 on PATH)
lint:
	golangci-lint run ./...
