# ── Stage 1: Build ──────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

# Install git for go mod download (private modules if needed).
RUN apk add --no-cache git

WORKDIR /src

# Copy go.mod first — Docker layer cache: only re-downloads deps when mod changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build a fully static binary (CGO_ENABLED=0).
# -ldflags "-s -w" strips debug info → smaller binary.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /bin/server ./cmd/server

# ── Stage 2: Runtime ─────────────────────────────────────────────────────────
# scratch = zero OS, zero shell, zero attack surface.
FROM scratch

# Copy CA certificates so HTTPS calls to Redis/Prometheus work.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy the binary.
COPY --from=builder /bin/server /server

# Run as non-root (uid 65534 = nobody in scratch).
USER 65534

EXPOSE 8080

ENTRYPOINT ["/server"]
