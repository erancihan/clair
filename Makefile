#!make
-include .env
export $(shell sed -e '/^\#/d' -e 's/=.*//' .env 2>/dev/null)
export GOWORKDIR=./

.PHONY: build

OUT_DIR := ./builds
OUTFILE := ${OUT_DIR}/clair.bin
GO_ARGS := -mod vendor
GO_BUILD_CMD := go build ${GO_ARGS}

GNUMAKEFLAGS=-j3


all: build

deps: 
	go mod download

assets: PATH:=$(PWD)/node_modules/.bin:$(PATH)
assets: deps
	npm run css
	go generate ./...

build: assets
	go build -o ./builds/clair ./cmd/clair

# dev with air. air already calls build
dev: 
	go tool air

# ----------------------
build-linux-amd64:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
		${GO_BUILD_CMD} -o "${OUTFILE}" cmd/clair/main.go

# ----------------------
run:
	"${OUTFILE}" --verbose

run-noenv:
	${OUTFILE} --verbose

all: build

# docker
docker: docker-build
docker-build:
	docker build -t clair .

docker-run:
	docker run -it --rm --env-file=.env clair

docker-dev: docker-build docker-run

# tidy and vendor
tidy:
	go mod tidy
	go mod vendor
	
# local infra for development / tests (PostgreSQL + Valkey)
db-up:
	docker compose -f docker-compose.dev.yaml up -d postgres

db-down:
	docker compose -f docker-compose.dev.yaml down

# testing — requires a running PostgreSQL.
# Point DATABASE_URL at it (defaults to the docker-compose.dev.yaml service):
#   make db-up && make test
.PHONY: test
test:
	go test ./test/... ./internal/games/... ./internal/server/games/... -v