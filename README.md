# trmnl-dash

A personal dashboard for the [TRMNL](https://usetrmnl.com) ePaper display device, with a local preview tool that roughly simulates the physical device's size and color gamut on a MacBook screen.

## Device specs

| Property | Value |
|---|---|
| Display size | 10.3" diagonal |
| Resolution | 1872 × 1404 px |
| Color depth | 16 shades of gray (4-bit grayscale) |
| Physical PPI | ~227 |
| Aspect ratio | 4:3 |

## Requirements

- Go (for the server)
- Swift (for the preview viewer, macOS only)
- Google Chrome or Chromium (for headless rendering)
- [fswatch](https://github.com/emcrisostomo/fswatch) — for live reload in `make preview`

```bash
brew install fswatch
```

## Usage

```bash
make preview    # start server, open e-ink simulator, watch for changes (macOS only)
make open       # render once and open the raw PNG in Preview.app
make serve      # start BYOS server for the physical device
make clean      # remove dashboard.png, .viewer binary, and server binary
```

## BYOS server

`server.go` implements the [TRMNL BYOS](https://docs.trmnl.com/go/diy/byos) (Bring Your Own Server) protocol so the physical device can pull content directly from your machine.

It handles all three endpoints the firmware expects:

| Endpoint | Purpose |
|---|---|
| `GET /api/setup` | Provisioning handshake — issues an API key + friendly ID for the device MAC. Only called by firmware that has **no key stored** (see below). |
| `GET /api/display` | Main poll — adopts the device's token on first contact, renders `dashboard.html` via headless Chrome, returns the image URL |
| `POST /api/log` | Device diagnostics — logs the payload and returns 204 |

### Running

```bash
make serve
```

Optional overrides:

```bash
make serve ADDR=:9090 REFRESH_RATE=900
```

### Device setup

The device points at this server instead of the TRMNL cloud. Find your Mac's LAN IP with `ipconfig getifaddr en0` (e.g. `192.168.0.68`); the device must join the same network.

On the TRMNL X (10.3", touchbar, no power button — it powers on via the magnetic charging dock):

1. **Enter pairing mode:** hold both ends of the touchbar until the screen blinks, then hold the middle to confirm. The device broadcasts a `TRMNL` Wi-Fi network.
2. **Join that network** from a phone or laptop — a captive portal opens.
3. **Set the custom server:** go to **Advanced → Custom Server → Yes** and enter `http://<lan-ip>:8080`, with no trailing slash (the firmware appends `/api/...` itself).
4. **Back out, pick your home Wi-Fi, and connect.**

The device downloads the PNG and sleeps for `refresh_rate` seconds between polls.

### How the device authenticates

The firmware stores a **single API key, independent of the server URL**, and only calls `/api/setup` when it has **no key stored locally**. A device first claimed against TRMNL's cloud at unboxing (with its Friendly ID) already holds a cloud-issued key — so when you point it here it **skips `/api/setup` entirely** and polls `/api/display` directly, presenting that pre-existing key. Switching to a custom server does not clear the key; only a full device reset does.

So in practice `/api/setup` may never be hit. To handle that, `/api/display` **adopts** any unrecognised non-empty token on first contact: it registers the device under the token it presents (keyed by MAC) and serves the dashboard, instead of rejecting it. Without this the firmware reads an unknown token as "not set up" and shows its built-in _visit trmnl.com/start_ screen.

`/api/setup` is still implemented and correct for the case it does fire: a freshly-reset device (no stored key) pointed here calls `GET /api/setup` with its MAC, the server issues an API key + friendly ID, the device stores them, and subsequent polls authenticate normally.

Device registrations are persisted to `devices.json` (gitignored).

## Docker

The server ships as a container image with headless Chromium baked in, so it
can run anywhere (a homelab box, a k3s cluster) without a local Chrome install.

```bash
docker compose up --build      # build + run, photos from ./photos
# or, by hand:
docker build -t trmnl:latest .
docker run -p 8080:8080 \
  -e TZ=Europe/London -e TRMNL_LAT=51.5074 -e TRMNL_LON=-0.1278 -e TRMNL_LOCATION=London \
  -v /path/to/your/photos:/photos:ro \
  -v trmnl-data:/data \
  trmnl:latest
```

The device then points at `http://<docker-host-ip>:8080` exactly as in the
[device setup](#device-setup) above.

### Configuration

All settings are environment variables (see `docker-compose.yml`):

| Variable | Default | Purpose |
|---|---|---|
| `TZ` | `UTC` | Timezone for the rendered calendar/clock — **set this** |
| `TRMNL_LAT` / `TRMNL_LON` / `TRMNL_LOCATION` | _(unset)_ | Weather location; unset disables the weather widget |
| `TRMNL_REFRESH_RATE` | `1800` | Seconds the device sleeps between polls |
| `TRMNL_RENDER_INTERVAL` | `300` | Seconds between server-side re-renders |
| `TRMNL_PHOTO_STRATEGY` | `shuffle` | Photo cycling: `random` \| `shuffle` \| `alphabetical` |
| `TRMNL_ADDR` | `:8080` | Listen address |

### Volumes

| Mount | Mode | Purpose |
|---|---|---|
| `/photos` | read-only | Your image library — bind-mount any local directory here |
| `/data` | read-write | Persists `devices.json` (the device's adopted token) across restarts |

`dashboard.html` is baked into the image; rebuild to pick up template changes.
Inter is loaded from the Google Fonts CDN at render time, so the container needs
outbound internet (it already does, for weather) — bundled Noto/Liberation faces
are the fallback if the CDN is unreachable.

### Deploying to k3s / Kubernetes

Example manifests are in [`k8s/`](k8s/README.md) — Namespace, ConfigMap,
Deployment, and a `LoadBalancer` Service. Apply them with `kubectl` or vendor
them into your GitOps repo (Flux / Argo CD). `make deploy` is a convenience for
the common k3s case of a ClusterIP-only in-cluster registry; see
[`k8s/README.md`](k8s/README.md) for image delivery, photos, and LAN exposure.

## Preview tool (`viewer.swift`)

A small AppKit app compiled by `swiftc`. It:

1. Loads the compiled PNG
2. Applies an e-ink gamut simulation (remaps sRGB using theoretical EPD contrast ratio)
3. Opens a **borderless** `NSWindow` at the correct physical size

The viewer binary (`.viewer`) is cached by Make and only recompiled when
`viewer.swift` changes.

### Preview zoom

The viewer auto-detects the screen's logical PPI at launch and computes the correct
zoom so the image appears at the TRMNL's true physical size:

```
zoom = (8.247in × logical_PPI) / 1872 px
```

For a MacBook 14" MBP this works out to ≈ 56%. If you need to override it
(e.g. for an external display), pass `ZOOM=<percent>` to make:

```bash
make preview ZOOM=50
```

## Next steps / roadmap

- [x] Web server with `/api/display` endpoint that returns png
- [x] Real content widgets: weather (Open-Meteo - temp, high/low, wind, hourly chart)
- [ ] Restrict device access: allow-list or auth flow (`/api/display` currently adopts any non-empty token on first contact)
- [x] Hot-reload in preview
