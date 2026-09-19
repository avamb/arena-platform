# syntax=docker/dockerfile:1.7
# ============================================================
# arena_new — tickets-page production image
# ============================================================
# The hosted sales page at tickets.arenasoldout.com/{org_slug}/{event_slug}.
# Build context is the REPO ROOT (this Dockerfile copies from both
# apps/widget/ and apps/tickets-page/), e.g.:
#
#   docker build -f apps/tickets-page/Dockerfile \
#     --build-arg VITE_API_BASE_URL=https://api.arenasoldout.com \
#     -t arena-tickets-page:dev .
#
# Stage 1 (widget-build): compiles the SAME apps/widget custom element
#   (<arena-tickets>) this repo ships to sites via jsDelivr, but here it is
#   served from OUR OWN domain — the whole point of the hosted page is to
#   not depend on a third-party CDN.
# Stage 2 (page-build): deterministic npm ci + vite build of this app.
#   VITE_API_BASE_URL is embedded into the JS bundle at build time, same
#   convention as apps/admin-web/Dockerfile.
# Stage 3 (runtime): nginx:1.27-alpine serving the page from
#   /usr/share/nginx/html and the widget bundle from
#   /usr/share/nginx/html/widget/v1/ — both from this one container, one
#   origin, so the widget's own requests are same-origin by default.
#
# § Build arguments
#   VITE_API_BASE_URL — backend API base URL (required; no trailing slash).
#     Baked into both the JS bundle (Vite) and the nginx CSP connect-src
#     directive (see nginx.conf.template). Never embed credentials via
#     VITE_* — they become public.
# ============================================================

ARG VITE_API_BASE_URL

# ---- Stage 1: build the widget bundle ----
FROM node:20-alpine AS widget-build
WORKDIR /widget
COPY apps/widget/package.json apps/widget/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY apps/widget/ ./
# `vite build` only — NOT `npm run build` (which also runs
# scripts/postbuild.mjs to copy demo/iframe.html into dist/v1/). The repo
# root .dockerignore excludes apps/widget/demo/ from every image's build
# context (it documents "not required in any production image"), and
# tickets-page only needs arena-tickets.js, not the iframe fallback page.
RUN npx vite build

# ---- Stage 2: build the page app ----
FROM node:20-alpine AS page-build
WORKDIR /app
COPY apps/tickets-page/package.json apps/tickets-page/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY apps/tickets-page/ ./

ARG VITE_API_BASE_URL
ENV VITE_API_BASE_URL=${VITE_API_BASE_URL}

RUN npm run build

# ---- Stage 3: runtime ----
FROM nginx:1.27-alpine AS runtime

ARG VITE_API_BASE_URL
ENV API_ORIGIN=${VITE_API_BASE_URL}

RUN rm /etc/nginx/conf.d/default.conf

# Bake the API origin into the CSP connect-src directive at build time
# (same lifecycle as VITE_API_BASE_URL — both are fixed per image, not
# runtime-configurable, matching apps/admin-web's convention).
RUN mkdir -p /etc/nginx/snippets
COPY apps/tickets-page/nginx.conf.template /tmp/nginx.conf.template
COPY apps/tickets-page/security-headers.conf.template /tmp/security-headers.conf.template
RUN sed "s|__API_ORIGIN__|${API_ORIGIN}|g" /tmp/nginx.conf.template > /etc/nginx/conf.d/arena-tickets-page.conf \
    && sed "s|__API_ORIGIN__|${API_ORIGIN}|g" /tmp/security-headers.conf.template > /etc/nginx/snippets/security-headers.conf \
    && rm /tmp/nginx.conf.template /tmp/security-headers.conf.template

# Page app.
COPY --from=page-build /app/dist /usr/share/nginx/html
# Self-hosted widget bundle, served from this same origin.
COPY --from=widget-build /widget/dist/v1 /usr/share/nginx/html/widget/v1

RUN chown -R nginx:nginx \
        /var/cache/nginx \
        /var/log/nginx \
        /etc/nginx/conf.d \
        /usr/share/nginx/html && \
    touch /var/run/nginx.pid && \
    chown nginx:nginx /var/run/nginx.pid

USER nginx

EXPOSE 8080

HEALTHCHECK \
    --interval=30s \
    --timeout=5s \
    --start-period=5s \
    --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://127.0.0.1:8080/health || exit 1
