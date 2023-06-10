FROM golang:1.20 as base

WORKDIR /go/src/app
COPY go.mod go.sum ./
RUN go mod download

FROM base AS builder
WORKDIR /go/src/app
COPY . ./
RUN go generate ./...
RUN CGO_ENABLED=0 GOOS=linux go build -v -o /go/bin/clair.bin cmd/clair/main.go

FROM alpine:latest
RUN apk --no-cache add ca-certificates
COPY --from=builder /go/bin/clair.bin ./clair.bin
CMD ["./clair.bin", "-delay=60"]
