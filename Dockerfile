# make css files
FROM node:24-alpine AS css-builder

WORKDIR /app
COPY . ./

RUN npm install
RUN npm run css

# create binary
FROM golang:1.25-alpine AS builder

RUN apk --no-cache add gcc git musl-dev

WORKDIR /go/src/app
COPY . ./
COPY go.mod go.sum ./

COPY --from=css-builder /app/internal/web/static/css/ ./internal/web/static/css/

RUN go mod download
RUN go generate ./...

RUN CGO_ENABLED=1 GOOS=linux go build -tags netgo -a -v -o /go/bin/clair.bin cmd/clair/main.go

# create final image
FROM alpine:3.22

ARG APP_ENV

ARG DB_FOLDER

ARG SERVER_PORT

ARG AWS_ACCESS_KEY_ID
ARG AWS_SECRET_ACCESS_KEY
ARG AWS_SQS_QUEUE_NAME
ARG AWS_REGION

ARG DISCORD_BOT_AUTH_KEY
ARG DISCORD_BOT_IDENTIFIER
ARG DISCORD_CHANNEL_ID

ARG ENVIRONMENT
ARG SENTRY_DSN

ARG PUBLIC_PATH

RUN apk --no-cache add ca-certificates
COPY --from=builder /go/bin/clair.bin ./clair.bin
