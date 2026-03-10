# trmnl

A [Typst](https://typst.app)-based renderer for the [TRMNL](https://usetrmnl.com) ePaper display device, with a local preview tool that roughly simulates the physical device's size and color gamut on a MacBook screen.

## Device specs

| Property | Value |
|---|---|
| Display size | 10.3" diagonal |
| Resolution | 1872 × 1404 px |
| Color depth | 16 shades of gray (4-bit grayscale) |
| Physical PPI | ~227 |
| Aspect ratio | 4:3 |

## Prerequisites

```bash
brew install typst go
# Xcode command line tools for swiftc (already present on most Macs)
xcode-select --install
```

## Usage

```bash
make build      # compile dashboard.typ → dashboard.png at 1872×1404
make preview    # build + open in true-size e-ink simulator (macOS only)
make open       # build + open the raw PNG in Preview
make clean      # remove dashboard.png, .viewer binary, and server binary
```

## BYOS server

`server.go` implements the [TRMNL BYOS](https://docs.trmnl.com/go/diy/byos) (Bring Your Own Server) protocol so the physical device can pull content directly from your machine.

It handles all three endpoints the firmware expects:

| Endpoint | Purpose |
|---|---|
| `GET /api/setup` | One-time device provisioning - issues an API key |
| `GET /api/display` | Main poll - compiles `dashboard.typ` on demand and returns the image URL |
| `POST /api/log` | Device diagnostics - logs payload and returns 204 |

### Running

```bash
make serve BASE_URL=http://192.168.1.100:8080
```

`BASE_URL` must be reachable from the device on your local network. Find your Mac's IP with `ipconfig getifaddr en0`.

Optional overrides:

```bash
make serve BASE_URL=http://192.168.1.100:8080 ADDR=:9090 REFRESH_RATE=900
```

### Device setup

In the TRMNL app/firmware, point the device at your server's IP instead of the TRMNL cloud. On first boot the device calls `/api/setup`, receives an API key, and stores it. Subsequent polls hit `/api/display` - the server compiles a fresh PNG and returns its URL; the device downloads and renders it, then sleeps for `refresh_rate` seconds.

Device registrations are persisted to `devices.json` (gitignored).

## Typst page setup

The page dimensions are derived from the device PPI so that
`typst compile --format png --ppi 227` produces exactly 1872×1404 pixels:

```typst
#set page(
  width:  8.247in,   // 1872 / 227
  height: 6.185in,   // 1404 / 227
  margin: (x: 0.45in, y: 0.4in),
  fill:   white,
)
```

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

- [x] Web server with `/api/display` endpoint that compiles `.typ` on request
- [ ] Dynamic content injection via `typst compile --input key=value`
- [ ] Real content widgets: weather, calendar, tasks, etc.
- [ ] Post-process output to true 4-bit grayscale (device dithers anyway)
- [ ] Hot-reload in preview: watch `dashboard.typ` and reopen on change
