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
