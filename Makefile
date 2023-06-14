#!make
include .env
export $(shell sed -e '/^\#/d' -e 's/=.*//' .env)

.PHONY: build

OUT_DIR := ./make-build-release
OUTFILE := ${OUT_DIR}/clair.bin
GO_ARGS := -mod vendor
GO_BUILD_CMD := go build ${GO_ARGS}

GNUMAKEFLAGS=-j3

build:
	${GO_BUILD_CMD} -o "${OUTFILE}" cmd/clair/main.go

build-linux-amd64:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
		${GO_BUILD_CMD} -o "${OUTFILE}" cmd/clair/main.go

devel:
	go run ${GO_ARGS} cmd/clair/main.go

dev: devel

devel-noenv:
	go run ${GO_ARGS} cmd/clair/main.go

run:
	"${OUTFILE}" --verbose

run-noenv:
	${OUTFILE} --verbose

all: build

# docker
docker-build:
	docker build -t clair .

docker-run:
	docker run -it --rm --env-file=.env clair

docker-dev: docker-build docker-run

# Lambda --------------------
lambda-build:
	${GO_BUILD_CMD} -o "${OUT_DIR}/lambda" cmd/lambda/main.go

lambda-build-and-upload: lambda-build
	cd ${OUT_DIR}; zip function.zip lambda
	cd ${OUT_DIR}; \
		aws lambda update-function-code \
			--function-name clair-sqs-lambda \
			--zip-file fileb://function.zip \
			--region ${AWS_SQS_REGION}
