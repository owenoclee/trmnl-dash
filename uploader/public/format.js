// TGRY and TRGB — the only things the uploader stores. Raw pixels behind a
// fixed 20-byte header, produced in the browser from whatever the phone can
// decode. Nothing server-side ever runs an image decoder: the Worker and the
// TRMNL server check this header field by field and the length exactly, and
// that is the whole parse.
//
// TGRY is 8-bit greyscale, for the photo the frame shows. TRGB is the same
// with 3 bytes (R, G, B) per pixel, for the colour thumbnail the phone lists.
//
//   0  "TGRY"|"TRGB"  magic
//   4  u8  version    1
//   5  u8[3]          zero
//   8  u16 width      little-endian, like every field below
//  10  u16 height
//  12  u16 focus x    the frame chosen on the phone, in pixels. The display
//  14  u16 focus y    zooms to fit this rect for whatever shape its photo slot
//  16  u16 focus w    is; the pixels around it are kept so a different slot
//  18  u16 focus h    shape still has something to show.
//  20  u8[w*h*c]      pixels, row-major, c = 1 (TGRY) or 3 (TRGB), 0 = black
//
// The TRMNL server (server.go, parseTGRY) re-checks the same rules; keep the
// two in step.

export const HEADER = 20;
export const MIN_DIM = 16;
export const MAX_DIM = 4096;
export const MAX_PIXELS = 4_000_000;
export const MAX_THUMB_DIM = 800;
export const CHANNELS = {TGRY: 1, TRGB: 3};

// parse validates one image of the given kind ("TGRY" or "TRGB") at the start
// of `bytes` (a Uint8Array) and returns its fields plus `size`, its length in
// bytes. Throws on anything else.
export function parse(bytes, {kind = 'TGRY', maxDim = MAX_DIM, maxPixels = MAX_PIXELS} = {}) {
  if (bytes.length < HEADER) throw new Error('too short for a header');
  const dv = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  if (String.fromCharCode(...bytes.subarray(0, 4)) !== kind) throw new Error(`bad magic, want ${kind}`);
  if (bytes[4] !== 1) throw new Error('unsupported version');
  if (bytes[5] | bytes[6] | bytes[7]) throw new Error('reserved bytes set');
  const width = dv.getUint16(8, true), height = dv.getUint16(10, true);
  const fx = dv.getUint16(12, true), fy = dv.getUint16(14, true);
  const fw = dv.getUint16(16, true), fh = dv.getUint16(18, true);
  if (width < MIN_DIM || height < MIN_DIM || width > maxDim || height > maxDim) throw new Error('bad dimensions');
  if (width * height > maxPixels) throw new Error('too many pixels');
  if (fw < MIN_DIM || fh < MIN_DIM || fx + fw > width || fy + fh > height) throw new Error('focus outside image');
  const size = HEADER + width * height * CHANNELS[kind];
  if (bytes.length < size) throw new Error('truncated pixels');
  return {width, height, focus: [fx, fy, fw, fh], size};
}

// parseUpload validates an upload body: the greyscale photo followed
// immediately by its colour thumbnail (the focus rect, small, for the phone's
// list), and nothing else.
export function parseUpload(bytes) {
  const image = parse(bytes);
  const thumb = parse(bytes.subarray(image.size), {kind: 'TRGB', maxDim: MAX_THUMB_DIM});
  if (image.size + thumb.size !== bytes.length) throw new Error('trailing bytes');
  return {image, thumb};
}

// encode builds an image of the given kind. Used by the browser and the tests.
export function encode(width, height, focus, pixels, kind = 'TGRY') {
  const out = new Uint8Array(HEADER + width * height * CHANNELS[kind]);
  const dv = new DataView(out.buffer);
  out.set([...kind].map(c => c.charCodeAt(0)));
  out[4] = 1;
  [width, height, ...focus].forEach((v, i) => dv.setUint16(8 + 2 * i, v, true));
  out.set(pixels, HEADER);
  return out;
}
