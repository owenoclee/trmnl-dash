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
| `GET /api/setup` | Device provisioning handshake — issues an API key keyed to the device MAC |
| `GET /api/display` | Main poll — renders `dashboard.html` via headless Chrome and returns the image URL |
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

The TRMNL X ships pre-provisioned with an API token, so it polls `/api/display` directly with that token rather than going through `/api/setup`. The server adopts the token on first contact, registers the device, and serves the dashboard; the device downloads the PNG and sleeps for `refresh_rate` seconds between polls. (`/api/setup` remains available for firmware that provisions by calling it.)

Device registrations are persisted to `devices.json` (gitignored).

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
- [ ] Post-process output to true 4-bit grayscale (device dithers anyway)
- [x] Hot-reload in preview
