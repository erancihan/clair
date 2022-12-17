#!make
include .env
export $(shell sed 's/=.*//' .env)

.PHONY: run

OUT_DIR := ./make-build-release
GO_ARGS := -mod vendor
GO_BUILD_CMD := go build ${GO_ARGS}

GNUMAKEFLAGS=-j3

vet:
	go vet ./...

# Bot ----------------------
bot-build:
	${GO_BUILD_CMD} -o "${OUT_DIR}/bot" cmd/discord-bot/main.go

bot-dev:
	AWS_ACCESS_KEY_ID=${AWS_ACCESS_KEY_ID} AWS_SECRET_ACCESS_KEY=${AWS_SECRET_ACCESS_KEY} \
	go run ${GO_ARGS} cmd/discord-bot/main.go

bot-dev-noenv:
	go run ${GO_ARGS} cmd/discord-bot/main.go

bot-run:
	AWS_ACCESS_KEY_ID=${AWS_ACCESS_KEY_ID} AWS_SECRET_ACCESS_KEY=${AWS_SECRET_ACCESS_KEY} \
	"${OUT_DIR}/bot"

bot-run-noenv:
	"${OUT_DIR}/bot"

# Notification Bot ----------
notification-bot-build:
	${GO_BUILD_CMD} -o "${OUT_DIR}/notification-bot" cmd/discord-notification-bot/main.go

notification-bot-dev:
	AWS_ACCESS_KEY_ID=${AWS_ACCESS_KEY_ID} AWS_SECRET_ACCESS_KEY=${AWS_SECRET_ACCESS_KEY} \
	go run ${GO_ARGS} cmd/discord-notification-bot/main.go

notification-bot-dev-noenv:
	go run ${GO_ARGS} cmd/discord-notification-bot/main.go

notification-bot-run:
	AWS_ACCESS_KEY_ID=${AWS_ACCESS_KEY_ID} AWS_SECRET_ACCESS_KEY=${AWS_SECRET_ACCESS_KEY} \
	"${OUT_DIR}/notification-bot"

notification-bot-run-noenv:
	"${OUT_DIR}/notification-bot"

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

# Server --------------------
server-build:
	${GO_BUILD_CMD} -o "${OUT_DIR}/server" cmd/server/main.go

server-run:
	go run ${GO_ARGS} cmd/server/main.go

# Website --------------------
website-build:
	cd web; yarn; yarn export
	rm -rf website/web-ui
	mv -v  web/out website/web-ui
	find website/web-ui/ -empty -type d -delete
	${GO_BUILD_CMD} -o "${OUT_DIR}/clair-website" website/main.go

website-run:
	go run ${GO_ARGS} website/main.go
