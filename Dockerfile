# create binary
FROM golang:1.25-alpine AS builder

RUN apk --no-cache add gcc git musl-dev

WORKDIR /go/src/app
COPY . ./
COPY go.mod go.sum ./
RUN go generate ./...
RUN go mod download
RUN CGO_ENABLED=1 GOOS=linux go build -tags netgo -a -v -o /go/bin/clair.bin cmd/clair/main.go

# create final image
FROM alpine:3.21

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
