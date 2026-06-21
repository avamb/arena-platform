# syntax=docker/dockerfile:1.7
# ============================================================
# arena_new — production multi-stage Dockerfile
# ============================================================
# Stage 1: build all three Go binaries with CGO disabled, so the
# resulting images are fully static and can run on distroless.
# Stage 2: distroless static, non-root, no shell, minimal surface.
#
# Health-checking is performed by the orchestrator (Compose /
# Dokploy / k8s probe) against the /healthz and /readyz HTTP
# endpoints. We deliberately omit a Dockerfile HEALTHCHECK because
# distroless has no shell or curl binary to invoke.
# ============================================================

# ---- Stage 1: build ----
FROM golang:1.24-alpine AS build

ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOFLAGS="-trimpath"

WORKDIR /src

# Cache module downloads in a dedicated layer.
COPY go.mod go.sum* ./

# Copy the rest of the source tree.
COPY apps ./apps

# Materialize go.sum if missing (foundation milestone — first real deps) then
# build all three binaries. Splitting this into one RUN keeps the build cache
# semantics simple for the scaffold phase.
RUN go mod tidy \
 && go build -ldflags="-s -w" -o /out/arena-api      ./apps/backend/cmd/arena-api      \
 && go build -ldflags="-s -w" -o /out/arena-worker   ./apps/backend/cmd/arena-worker   \
 && go build -ldflags="-s -w" -o /out/arena-migrate  ./apps/backend/cmd/arena-migrate

# ---- Stage 2: runtime ----
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=build /out/arena-api     /app/arena-api
COPY --from=build /out/arena-worker  /app/arena-worker
COPY --from=build /out/arena-migrate /app/arena-migrate

USER nonroot:nonroot
EXPOSE 8080

ENTRYPOINT ["/app/arena-api"]
