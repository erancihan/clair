FROM golang:1.24 AS base

WORKDIR /go/src/app
COPY go.mod go.sum ./
RUN go mod download

FROM base AS builder
WORKDIR /go/src/app
COPY . ./
RUN go generate ./...
RUN CGO_ENABLED=0 GOOS=linux go build -v -o /go/bin/clair.bin cmd/clair/main.go

FROM alpine:latest

ARG AWS_ACCESS_KEY_ID
ARG AWS_SECRET_ACCESS_KEY
ARG AWS_SQS_QUEUE_NAME
ARG AWS_REGION
ARG DISCORD_BOT_AUTH_KEY
ARG DISCORD_BOT_IDENTIFIER
ARG DISCORD_CHANNEL_ID
ARG ENVIRONMENT
ARG SENTRY_DSN

RUN apk --no-cache add ca-certificates
COPY --from=builder /go/bin/clair.bin ./clair.bin
CMD ["./clair.bin", "-delay=60"]
