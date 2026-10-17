#!/usr/bin/env python3
"""Upload a folder of photos to the photo frame, as if added from the phone.

    scripts/import.py <folder> [--url https://photos.pog.so] [--keys ~/.config/trmnl-photos.env] [--dry-run]

Each photo's frame is the display's plain centred crop (what a photo in the old
photos/ folder showed), and it's encoded exactly as the page does it
(public/app.js, encodeUpload): the frame at up to FOCUS_MAX_W wide plus as much
of the photo around it as fits in MAX_OUT_PX, greyscale, with a colour
thumbnail of the frame. Decoding happens here with Pillow, on files you chose;
the Worker validates the result like any other upload.
"""
import argparse
import math
import os
import struct
import sys
import urllib.error
import urllib.request

from PIL import Image, ImageOps

SLOT_ASPECT = 783 / 380
FOCUS_MAX_W = 1200
MAX_OUT_PX = 3_000_000
THUMB_W, THUMB_H = 400, round(400 / SLOT_ASPECT)
EXTS = {'.jpg', '.jpeg', '.png', '.gif', '.webp', '.heic'}


def header(kind, w, h, focus):
    return kind.encode() + bytes([1, 0, 0, 0]) + struct.pack('<6H', w, h, *focus)


def encode(path):
    img = ImageOps.exif_transpose(Image.open(path)).convert('RGB')
    W, H = img.size
    # The frame: the largest centred rect of the slot's shape.
    fw = min(W, H * SLOT_ASPECT)
    fh = fw / SLOT_ASPECT
    fl, ft = (W - fw) / 2, (H - fh) / 2
    s = min(1, FOCUS_MAX_W / fw)
    # Keep the whole photo if it fits, else as much around the frame as does.
    rw, rh = W, H
    if rw * rh * s * s > MAX_OUT_PX:
        k = math.sqrt(MAX_OUT_PX / (fw * fh * s * s))
        rw, rh = min(W, fw * k), min(H, fh * k)
    rx = min(max(W / 2 - rw / 2, 0), W - rw)
    ry = min(max(H / 2 - rh / 2, 0), H - rh)
    ow, oh = max(16, round(rw * s)), max(16, round(rh * s))
    fx, fy = round((fl - rx) * s), round((ft - ry) * s)
    focus = (min(fx, ow - 16), min(fy, oh - 16),
             max(16, min(round(fw * s), ow - fx)), max(16, min(round(fh * s), oh - fy)))

    grey = img.resize((ow, oh), Image.LANCZOS, box=(rx, ry, rx + rw, ry + rh)).convert('L')
    thumb = img.resize((THUMB_W, THUMB_H), Image.LANCZOS, box=(fl, ft, fl + fw, ft + fh))
    body = (header('TGRY', ow, oh, focus) + grey.tobytes() +
            header('TRGB', THUMB_W, THUMB_H, (0, 0, THUMB_W, THUMB_H)) + thumb.tobytes())
    return body, (W, H), (ow, oh), focus


def read_key(path):
    # Parsed literally, never sourced.
    with open(os.path.expanduser(path)) as f:
        for line in f:
            if line.startswith('UPLOAD_TOKEN='):
                return line.split('=', 1)[1].strip()
    sys.exit(f'no UPLOAD_TOKEN in {path}')


def main():
    ap = argparse.ArgumentParser(description=__doc__.split('\n')[0])
    ap.add_argument('folder')
    ap.add_argument('--url', default='https://photos.pog.so')
    ap.add_argument('--keys', default='~/.config/trmnl-photos.env')
    ap.add_argument('--dry-run', action='store_true', help='encode and report, upload nothing')
    args = ap.parse_args()

    key = None if args.dry_run else read_key(args.keys)
    names = sorted(n for n in os.listdir(args.folder)
                   if not n.startswith('.') and os.path.splitext(n)[1].lower() in EXTS)
    failed = 0
    for n in names:
        try:
            body, src, out, focus = encode(os.path.join(args.folder, n))
        except Exception as e:
            print(f'{n}: could not read: {e}')
            failed += 1
            continue
        line = f'{n}: {src[0]}x{src[1]} -> {out[0]}x{out[1]}, frame {focus}, {len(body) / 1e6:.1f} MB'
        if args.dry_run:
            print(line)
            continue
        req = urllib.request.Request(f'{args.url}/api/photos', data=body, method='POST', headers={
            'Authorization': f'Bearer {key}', 'Content-Type': 'application/octet-stream',
            # Cloudflare's browser check blocks Python's default User-Agent (error 1010).
            'User-Agent': 'trmnl-photo-import/1'})
        try:
            with urllib.request.urlopen(req, timeout=120) as r:
                print(f'{line} -> {r.read().decode()[:40]}…')
        except urllib.error.HTTPError as e:
            print(f'{line} -> FAILED {e.code} {e.read().decode()}')
            failed += 1
    sys.exit(1 if failed else 0)


if __name__ == '__main__':
    main()
