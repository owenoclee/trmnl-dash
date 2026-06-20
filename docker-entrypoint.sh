#!/bin/sh
# Maps TRMNL_* env vars to the server's flags so the container is configured
# entirely through the environment (ConfigMap / `docker run -e` / compose).
#
# Weather (TRMNL_LAT / TRMNL_LON / TRMNL_LOCATION) and TZ are read directly from
# the environment by the binary, so they don't need mapping here.
#
# Anything passed as container args is appended after the flags below, so a
# later "--flag value" overrides the env-derived default.
set -eu

exec trmnl \
  --addr "${TRMNL_ADDR:-:8080}" \
  --tmpl "${TRMNL_TEMPLATE:-/app/dashboard.html}" \
  --out "${TRMNL_OUT:-/tmp/dashboard.png}" \
  --devices "${TRMNL_DEVICES_FILE:-/data/devices.json}" \
  --photos "${TRMNL_PHOTOS_DIR:-/photos}" \
  ${TRMNL_REFRESH_RATE:+--refresh-rate "$TRMNL_REFRESH_RATE"} \
  ${TRMNL_RENDER_INTERVAL:+--render-interval "$TRMNL_RENDER_INTERVAL"} \
  ${TRMNL_PHOTO_STRATEGY:+--photo-strategy "$TRMNL_PHOTO_STRATEGY"} \
  "$@"
