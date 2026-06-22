# syntax=docker/dockerfile:1.7
# ============================================================
# arena_new — production multi-stage Dockerfile
# ============================================================
# Stage 1: build all Go binaries with CGO disabled so the resulting
# images are fully static and can run on distroless.
# Stage 2: gcr.io/distroless/static-debian12 — no shell, no curl,
# minimal attack surface. Health-checking is performed by the
# arena-healthcheck binary (also built in stage 1) which does a
# single GET /healthz and exits 0 or 1. This satisfies the
# Dockerfile HEALTHCHECK requirement without needing a shell.
# ============================================================

# ---- Stage 1: build ----
FROM golang:1.24-alpine AS build

ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOFLAGS="-trimpath"

WORKDIR /src

# Cache module downloads in a dedicated layer.
COPY go.mod go.sum* ./

# Copy .env.example so documentation tests can verify env var defaults.
COPY .env.example ./

# Copy the rest of the source tree.
COPY apps ./apps

# Materialize go.sum if missing (foundation milestone — first real deps) then
# build all four binaries. Splitting this into one RUN keeps the build cache
# semantics simple for the scaffold phase.
RUN go mod tidy \
 && go build -ldflags="-s -w" -o /out/arena-api         ./apps/backend/cmd/arena-api         \
 && go build -ldflags="-s -w" -o /out/arena-worker      ./apps/backend/cmd/arena-worker      \
 && go build -ldflags="-s -w" -o /out/arena-migrate     ./apps/backend/cmd/arena-migrate     \
 && go build -ldflags="-s -w" -o /out/arena-healthcheck ./apps/backend/cmd/arena-healthcheck

# ---- Stage 2: runtime ----
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=build /out/arena-api          /app/arena-api
COPY --from=build /out/arena-worker       /app/arena-worker
COPY --from=build /out/arena-migrate      /app/arena-migrate
COPY --from=build /out/arena-healthcheck  /app/arena-healthcheck

USER nonroot:nonroot
EXPOSE 8080

# HEALTHCHECK uses the arena-healthcheck binary (statically linked Go) which
# performs GET /healthz and exits 0 on HTTP 200. distroless has no shell or
# curl so the dedicated binary is the idiomatic solution for this base image.
#   --interval=30s  — check every 30 s in steady state
#   --timeout=5s    — fail the check if the server does not respond in 5 s
#   --start-period=10s — give the server 10 s to boot before counting failures
#   --retries=3     — mark UNHEALTHY only after 3 consecutive failures
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/app/arena-healthcheck"]

ENTRYPOINT ["/app/arena-api"]
