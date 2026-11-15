# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl git libglu1-mesa python3 unzip xz-utils zip \
    && rm -rf /var/lib/apt/lists/*
ENV FLUTTER_ROOT=/opt/flutter \
    PUB_CACHE=/opt/pub-cache \
    CI=true \
    PATH=/opt/flutter/bin:$PATH
RUN git clone --depth 1 --branch 3.47.6 https://github.com/flutter/flutter.git /opt/flutter \
    && test "$(git -C /opt/flutter rev-parse HEAD)" = 5fc346839b5d0eef006ed8404392afb4dfae428d \
    && flutter config --no-analytics \
    && flutter precache --web

WORKDIR /src
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/opt/pub-cache \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    test "$TARGETOS" = linux \
    && GOOS="$TARGETOS" GOARCH="$TARGETARCH" bash tool/build_web.sh

FROM alpine:3.24.1 AS runtime
RUN apk add --no-cache ca-certificates curl tzdata \
    && addgroup -g 1000 flclash \
    && adduser -D -u 1000 -G flclash flclash \
    && mkdir -p /data /auth /state \
    && chmod 700 /state \
    && chown 1000:1000 /data /auth /state
COPY --from=builder --chmod=0755 /src/dist/FlClash-web /usr/local/bin/flclash-web
COPY LICENSE tool/web/NOTICE tool/web/LICENSE.FlClash-web /usr/share/licenses/flclash/
COPY core/web_api_evaluate_guest/LICENSE* core/web_api_evaluate_guest/manifest.json /usr/share/licenses/flclash/evaluator/
RUN find /usr/share/licenses/flclash -type f -exec chmod 0644 {} +
ARG VCS_REF=unknown
LABEL org.opencontainers.image.title="FlClash Web panel" \
      org.opencontainers.image.description="Authenticated Flutter Web panel and Clash.Meta core" \
      org.opencontainers.image.source="https://github.com/Zmmfly/FlClash" \
      org.opencontainers.image.revision="$VCS_REF" \
      org.opencontainers.image.licenses="GPL-3.0"
ENV FLCLASH_WEB_LISTEN=0.0.0.0:8080 \
    FLCLASH_DATA_DIR=/data \
    FLCLASH_WEB_AUTH_FILE=/auth/credentials.json \
    FLCLASH_WEB_API_STORE_DIR=/state \
    TZ=UTC
WORKDIR /data
USER 1000:1000
EXPOSE 8080/tcp 7890/tcp 7890/udp
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD curl --noproxy '*' --fail --silent --show-error --output /dev/null --max-time 3 http://127.0.0.1:8080/login || exit 1
ENTRYPOINT ["/usr/local/bin/flclash-web"]
