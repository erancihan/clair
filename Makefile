#!make
include .env
export $(shell sed -e '/^\#/d' -e 's/=.*//' .env)
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

dev-server: build
	./builds/clair server

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
	