# -- Stage 1: Build --
# alpine variant keeps the builder small; gcc + musl-dev provide the C
# toolchain that mattn/go-sqlite3 needs (CGO is on).
FROM golang:1.27-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /build

# Copy module files first so dependency download is cached between
# source-only changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ENV CGO_ENABLED=1 GOOS=linux
RUN go build -o /out/server ./cmd/server

# -- Stage 2: Runtime --
# Match the builder's alpine version so the musl libc in the runtime
# image is the one the binary was linked against.
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

# Run as a non-root user.
RUN addgroup -S app && adduser -S app -G app

WORKDIR /app
COPY --from=builder /out/server .

# The licence travels with the image. The `.dockerignore` excludes `*.md`, not
# `LICENSE`, so this file is in the build context; the runtime stage is where
# a customer reads it.
COPY LICENSE ./LICENSE

# SQLite writes to /data; the server expects the directory to exist.
RUN mkdir -p /data && chown app:app /data
VOLUME ["/data"]

USER app

EXPOSE 8080

# Readiness, not liveness: /readyz checks the database, so an unhealthy
# container is one that cannot serve rather than one that merely stopped.
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://localhost:8080/readyz >/dev/null || exit 1

ENTRYPOINT ["./server"]
