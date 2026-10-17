# Photo uploader (photos.pog.so)

A private page for adding photos to the TRMNL's rotation from a phone: pick
several, frame each one by dragging and pinching behind a cut-out of the
display's photo slot, check the dithered preview, add. Photos already added can
be looked at and removed.

```
phone ──(UPLOAD_TOKEN)──▶ Worker ──▶ R2 bucket trmnl-photos
                            ▲
TRMNL server ─(DISPLAY_TOKEN, every 5 min)─┘  mirrors into --uploads-dir
```

## No image decoding off the phone

The phone decodes the photo (its own browser, whatever format it can open),
crops and scales it, and sends **TGRY**: raw 8-bit greyscale behind a fixed
20-byte header that also records the chosen frame (`public/format.js`), plus a
small colour thumbnail of the frame in **TRGB**, the same with RGB pixels. The
Worker checks every header field and the exact length and stores the bytes as
they came; the TRMNL server checks them again (`parseTGRY` in `server.go`) and
encodes PNGs from the validated pixels for its own page. No image library ever
parses anything uploaded.

What's kept is the frame at up to 1200 px wide plus as much of the photo
around it as fits in 3 MP, so a differently shaped slot later can still be
filled. The display zooms to the smallest rect of its slot's shape that
contains the frame (`displayRect` in `public/display.js`, `photoRect` in
`dashboard.html`), which for today's slot is exactly what was framed.

## Keys

- `UPLOAD_TOKEN` — list, add, remove, fetch. Goes in the phone's link as a URL
  fragment, `https://photos.pog.so/#t=<token>`, which browsers never send to a
  server, so it stays out of request logs. The page sends it as a header and
  remembers it locally.
- `DISPLAY_TOKEN` — list and fetch only. The TRMNL server's
  `TRMNL_UPLOADS_TOKEN` (homeops `apps/trmnl/uploads-secrets.sops.yaml`).

To change a key, `wrangler secret put` the new one; the old link stops working.

## Develop and deploy

```bash
npm test                        # format validation tests (node:test)
printf 'UPLOAD_TOKEN=dev-upload\nDISPLAY_TOKEN=dev-display\n' > .dev.vars
wrangler dev                    # http://localhost:8787/#t=dev-upload, local R2
wrangler deploy                 # photos.pog.so, on the "Owen" account
```

One-off setup (already done): `wrangler r2 bucket create trmnl-photos`, then
`wrangler secret put UPLOAD_TOKEN` and `DISPLAY_TOKEN`.

The page's slot size, corner radius, crop and dither mirror `dashboard.html`;
change them together.
