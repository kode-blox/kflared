# syntax=docker/dockerfile:1.7

ARG NODE_VERSION=26.13.0
ARG NGINX_VERSION=1.30.7

FROM docker.io/node:${NODE_VERSION}-trixie-slim AS builder

USER node
RUN mkdir -p /home/node/workspace
WORKDIR /home/node/workspace

COPY --from=workspace --chown=node:node package.json package-lock.json ./
COPY --chown=node:node package.json docs/package.json

RUN npm ci --ignore-scripts

COPY --chown=node:node . docs/

RUN npm run build --workspace @kode-blox/kflared-docs

FROM docker.io/nginx/nginx-unprivileged:${NGINX_VERSION}-trixie AS production

ARG NGINX_VERSION

LABEL org.opencontainers.image.authors="Sayak Mukhopadhyay" \
      org.opencontainers.image.url="https://kflared.kodeblox.com" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.title="KFlared Docs" \
      org.opencontainers.image.description="Documentation for the KFlared Cloudflare Tunnel controller" \
      org.opencontainers.image.base.name="docker.io/nginx/nginx-unprivileged:${NGINX_VERSION}-trixie"

COPY --chown=nginx:nginx --from=builder /home/node/workspace/docs/out/ /usr/share/nginx/html/
COPY --chown=nginx:nginx nginx.conf /etc/nginx/conf.d/default.conf

USER nginx
EXPOSE 8080
