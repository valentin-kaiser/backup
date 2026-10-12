FROM node:24-alpine AS ui
# The Angular build writes to ../pkg/web/static, which control-portal embeds.
WORKDIR /src/worker/portal/ui
COPY worker/portal/ui/package.json worker/portal/ui/package-lock.json ./
RUN npm ci --ignore-scripts --no-audit --no-fund
COPY worker/portal/ui ./
RUN npm run build

FROM golang:1.27-alpine AS build
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY pkg ./pkg
COPY worker ./worker
COPY --from=ui /src/worker/portal/pkg/web/static ./worker/portal/pkg/web/static
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w \
      -X github.com/valentin-kaiser/go-core/version.GitTag=${VERSION} \
      -X github.com/valentin-kaiser/go-core/version.GitCommit=${COMMIT} \
      -X github.com/valentin-kaiser/go-core/version.GitShort=${COMMIT} \
      -X github.com/valentin-kaiser/go-core/version.BuildDate=${BUILD_DATE}" \
    -o /out/control ./worker/control/cmd \
    && CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w \
      -X github.com/valentin-kaiser/go-core/version.GitTag=${VERSION} \
      -X github.com/valentin-kaiser/go-core/version.GitCommit=${COMMIT} \
      -X github.com/valentin-kaiser/go-core/version.GitShort=${COMMIT} \
      -X github.com/valentin-kaiser/go-core/version.BuildDate=${BUILD_DATE}" \
    -o /out/control-portal ./worker/portal/cmd

FROM alpine:3.21.3
ARG RESTIC_VERSION=~0.17
RUN apk add --no-cache restic=$RESTIC_VERSION ca-certificates tzdata su-exec \
    && mkdir -p /var/lib/control /var/lib/portal /var/cache/restic /restore \
    && chown 1000:1000 /var/lib/portal \
    && touch /.backupignore

COPY --from=build /out/control /out/control-portal /usr/local/bin/
COPY scripts/entrypoint.sh /entrypoint.sh
RUN chmod 0755 /entrypoint.sh /usr/local/bin/control /usr/local/bin/control-portal

# One image: the backup in standalone mode (default of the image) or portal mode (CONTROL_MODE=portal, the daemon of the
# web portal), and the web portal itself (`web`).
ENV PUID=1000 \
    PGID=1000 \
    CONTROL_PATH=/var/lib/control \
    CONTROL_URL=http://127.0.0.1:9080 \
    CONTROL_API_ADDRESS=0.0.0.0 \
    CONTROL_API_PORT=9080 \
    CONTROL_API_TLS=false \
    PORTAL_PATH=/var/lib/portal \
    PORTAL_ADDRESS=0.0.0.0 \
    PORTAL_PORT=8443 \
    PORTAL_TLS=true

VOLUME ["/var/lib/control", "/var/lib/portal", "/var/cache/restic", "/backup"]
EXPOSE 8443 9080

HEALTHCHECK --interval=1m --timeout=10s --start-period=30s --retries=3 \
    CMD ["control", "health"]

ENTRYPOINT ["/entrypoint.sh"]
