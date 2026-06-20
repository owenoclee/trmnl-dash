# syntax=docker/dockerfile:1

# ── build stage ───────────────────────────────────────────────────────────────
FROM golang:1.24-bookworm AS build
WORKDIR /src

# Download modules first so this layer caches across source-only changes.
COPY go.mod go.sum ./
RUN go mod download

COPY server.go ./
# Static binary (CGO off) so it runs on the slim runtime image unchanged.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/trmnl .

# ── runtime stage ─────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

# chromium       — headless renderer (chromedp drives this binary)
# ca-certificates— outbound HTTPS to Open-Meteo + the Google Fonts CDN
# tzdata         — the calendar/clock render from the container clock; set TZ
# fonts-*        — fallback faces so text never renders as tofu if the Inter
#                  web font (loaded from the CDN at render time) is slow/blocked
RUN apt-get update && apt-get install -y --no-install-recommends \
      chromium \
      ca-certificates \
      tzdata \
      fontconfig \
      fonts-liberation \
      fonts-noto-core \
      fonts-noto-color-emoji \
 && rm -rf /var/lib/apt/lists/*

# Run unprivileged. Chromium launches with --no-sandbox (see CHROME_NO_SANDBOX),
# so root isn't needed and we avoid Chrome's "running as root" refusal.
RUN useradd --system --create-home --uid 10001 trmnl

ENV CHROME_BIN=/usr/bin/chromium \
    CHROME_NO_SANDBOX=1 \
    TZ=UTC \
    TRMNL_ADDR=:8080 \
    TRMNL_TEMPLATE=/app/dashboard.html \
    TRMNL_OUT=/tmp/dashboard.png \
    TRMNL_PHOTOS_DIR=/photos \
    TRMNL_DEVICES_FILE=/data/devices.json

WORKDIR /app
COPY --from=build /out/trmnl /usr/local/bin/trmnl
COPY dashboard.html /app/dashboard.html
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# /photos — mount your image library here (read-only is fine).
# /data   — persists devices.json (the device's adopted token) across restarts.
RUN mkdir -p /photos /data && chown -R trmnl:trmnl /photos /data /app
VOLUME ["/data"]

USER trmnl
EXPOSE 8080
ENTRYPOINT ["docker-entrypoint.sh"]
