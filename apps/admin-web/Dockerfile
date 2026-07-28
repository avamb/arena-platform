# syntax=docker/dockerfile:1.7
# ============================================================
# arena_new — admin-web production image (PR-07)
# ============================================================
# Stage 1: build — deterministic npm ci + vite build.
#   - Uses Node 20 LTS on Alpine for minimal build image.
#   - Source maps are disabled by default (VITE_ENABLE_SOURCEMAPS != "true").
#   - The build embeds VITE_API_BASE_URL at bundle time; pass it as a
#     build argument so the container is not locked to a single API host.
#     Runtime API-base configuration: see §Runtime Configuration below.
#
# Stage 2: runtime — nginx:1.27-alpine serving /usr/share/nginx/html.
#   - Non-root: runs as the built-in nginx (uid=101) user.
#   - Listens on port 8080 (no CAP_NET_BIND_SERVICE needed).
#   - SPA fallback, immutable cache headers, /health endpoint.
#   - No source, no node_modules, no secrets in the final image.
#
# § Runtime Configuration
#   VITE_API_BASE_URL is baked into the JS bundle at build time by Vite.
#   Never embed credentials or secrets via VITE_* — they become public.
#   Set it to the public-facing URL of arena-api (e.g. https://api.example.com).
#   The nginx container itself needs no environment at runtime.
#
# § Build arguments
#   VITE_API_BASE_URL  — backend API base URL (required; no trailing slash)
#   VITE_ENABLE_SOURCEMAPS — set to "true" to generate .map files (CI only)
#
# § Bundle size budget (documented per PR-07 spec)
#   vendor-react    ≤ 200 kB gzip
#   vendor-router   ≤ 150 kB gzip
#   vendor-query    ≤  80 kB gzip
#   vendor-table    ≤  80 kB gzip
#   vendor-forms    ≤  50 kB gzip
#   vendor-misc     ≤  50 kB gzip
#   index (app)     ≤ 150 kB gzip
#   Total           ≤ 760 kB gzip
# ============================================================

ARG VITE_API_BASE_URL
ARG VITE_ENABLE_SOURCEMAPS=false

# ---- Stage 1: build ----
FROM node:20-alpine AS build

WORKDIR /app

# Copy only the lockfile + package.json first so npm ci is cached
# independently from source changes.
COPY apps/admin-web/package.json apps/admin-web/package-lock.json ./

RUN npm ci --no-audit --no-fund

# Copy the OpenAPI TypeScript declarations (type-only; needed by tsc).
COPY apps/backend/openapi/clients/ts/ /openapi-clients/ts/

# Copy the rest of the admin-web source.
COPY apps/admin-web/ ./

# Rewrite the @openapi alias to point to the copied declarations.
# The tsconfig.app.json paths and vite.config.ts alias both resolve
# relative to the admin-web directory; since we copied them to
# /openapi-clients/ts we need to update vite.config.ts and tsconfig.
# Simpler: create a symlink inside the working directory.
RUN mkdir -p ../backend/openapi/clients && \
    ln -s /openapi-clients/ts ../backend/openapi/clients/ts

ARG VITE_API_BASE_URL
ARG VITE_ENABLE_SOURCEMAPS=false
ENV VITE_API_BASE_URL=${VITE_API_BASE_URL}
ENV VITE_ENABLE_SOURCEMAPS=${VITE_ENABLE_SOURCEMAPS}

RUN npm run build

# ---- Stage 2: runtime ----
FROM nginx:1.27-alpine AS runtime

# Remove the default nginx configuration.
RUN rm /etc/nginx/conf.d/default.conf

# Copy custom nginx configuration (port 8080, SPA fallback, cache headers).
COPY apps/admin-web/nginx.conf /etc/nginx/conf.d/arena-admin.conf

# Copy the built static assets from the build stage.
COPY --from=build /app/dist /usr/share/nginx/html

# nginx:alpine runs the master as root but workers as 'nginx' (uid=101).
# To run entirely non-root we need to:
#   1. Change ownership of runtime directories to the nginx user.
#   2. Write the PID file to a location the nginx user can write.
#   3. Set USER nginx so the container starts with uid=101.
RUN chown -R nginx:nginx \
        /var/cache/nginx \
        /var/log/nginx \
        /etc/nginx/conf.d && \
    touch /var/run/nginx.pid && \
    chown nginx:nginx /var/run/nginx.pid

USER nginx

EXPOSE 8080

# Healthcheck using wget (available in nginx:alpine).
HEALTHCHECK \
    --interval=30s \
    --timeout=5s \
    --start-period=5s \
    --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://127.0.0.1:8080/health || exit 1
