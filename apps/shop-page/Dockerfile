# syntax=docker/dockerfile:1.7
# ============================================================
# arena_new - shop-page production image
# ============================================================
# The organizer storefront (merch shop): shop.arenasoldout.com/{org_slug}/...
# on the platform host, or the organizer's own domain (einatwinery.com) with
# root paths. Build context is the REPO ROOT, e.g.:
#
#   docker build -f apps/shop-page/Dockerfile \
#     --build-arg VITE_API_BASE_URL=https://api.arenasoldout.com \
#     -t arena-shop-page:dev .
#
# Stage 1 (page-build): deterministic npm ci + vite build. VITE_API_BASE_URL is
#   embedded into the JS bundle at build time, same convention as
#   apps/tickets-page and apps/admin-web.
# Stage 2 (runtime): nginx:1.27-alpine, non-root on port 8080.
#
# Build arguments
#   VITE_API_BASE_URL - backend API base URL (required; no trailing slash).
#     Baked into the JS bundle AND the nginx CSP connect-src / img-src
#     directives (see security-headers.conf.template). Never put credentials in
#     a VITE_* variable: they become public.
# ============================================================

ARG VITE_API_BASE_URL

# ---- Stage 1: build the page app ----
FROM node:20-alpine AS page-build
WORKDIR /app
COPY apps/shop-page/package.json apps/shop-page/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY apps/shop-page/ ./

ARG VITE_API_BASE_URL
ENV VITE_API_BASE_URL=${VITE_API_BASE_URL}

RUN npm run build

# ---- Stage 2: runtime ----
FROM nginx:1.27-alpine AS runtime

ARG VITE_API_BASE_URL
ENV API_ORIGIN=${VITE_API_BASE_URL}

RUN rm /etc/nginx/conf.d/default.conf

RUN mkdir -p /etc/nginx/snippets
COPY apps/shop-page/nginx.conf.template /tmp/nginx.conf.template
COPY apps/shop-page/security-headers.conf.template /tmp/security-headers.conf.template
RUN sed "s|__API_ORIGIN__|${API_ORIGIN}|g" /tmp/nginx.conf.template > /etc/nginx/conf.d/arena-shop-page.conf \
    && sed "s|__API_ORIGIN__|${API_ORIGIN}|g" /tmp/security-headers.conf.template > /etc/nginx/snippets/security-headers.conf \
    && rm /tmp/nginx.conf.template /tmp/security-headers.conf.template

COPY --from=page-build /app/dist /usr/share/nginx/html

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
