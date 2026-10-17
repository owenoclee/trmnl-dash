// How the TRMNL shows a photo, reproduced so the phone can preview it. Keep in
// step with dashboard.html: the slot size and corner (.photo), the crop
// (photoRect) and the dither (ditherAtkinson, DITHER.levels = 2).

export const SLOT_W = 783;
export const SLOT_H = 380;
export const SLOT_ASPECT = SLOT_W / SLOT_H;

// displayRect is the part of a width×height image the frame shows for a focus
// rect [x, y, w, h]: the smallest rect of the slot's shape that contains the
// focus, centred on it, then shrunk and shifted as needed to stay inside the
// image. For the slot shape the phone framed against, that is the focus itself.
export function displayRect(width, height, [fx, fy, fw, fh], aspect = SLOT_ASPECT) {
  let w = Math.max(fw, fh * aspect), h = w / aspect;
  const fit = Math.min(1, width / w, height / h);
  w *= fit; h *= fit;
  const x = Math.min(Math.max(fx + fw / 2 - w / 2, 0), width - w);
  const y = Math.min(Math.max(fy + fh / 2 - h / 2, 0), height - h);
  return [x, y, w, h];
}

// Atkinson error diffusion to two levels, in place on a Float32Array of 0–255.
export function ditherAtkinson(g, w, h) {
  for (let y = 0; y < h; y++) for (let x = 0; x < w; x++) {
    const i = y * w + x, v = Math.max(0, Math.min(255, Math.round(g[i] / 255) * 255)), e = (g[i] - v) / 8;
    g[i] = v;
    if (x + 1 < w) g[i + 1] += e;
    if (x + 2 < w) g[i + 2] += e;
    if (y + 1 < h) {
      if (x > 0) g[i + w - 1] += e;
      g[i + w] += e;
      if (x + 1 < w) g[i + w + 1] += e;
    }
    if (y + 2 < h) g[i + 2 * w] += e;
  }
}

// luminance reads a canvas's pixels as greyscale (the same weights the frame uses).
export function luminance(ctx, w, h) {
  const d = ctx.getImageData(0, 0, w, h).data, g = new Float32Array(w * h);
  for (let i = 0; i < w * h; i++) g[i] = 0.299 * d[i * 4] + 0.587 * d[i * 4 + 1] + 0.114 * d[i * 4 + 2];
  return g;
}

// putGrey writes greyscale values (any array-like of 0–255) to a canvas.
export function putGrey(ctx, g, w, h) {
  const im = ctx.createImageData(w, h), d = im.data;
  for (let i = 0; i < w * h; i++) {
    d[i * 4] = d[i * 4 + 1] = d[i * 4 + 2] = g[i];
    d[i * 4 + 3] = 255;
  }
  ctx.putImageData(im, 0, 0);
}

// renderDithered draws the [sx, sy, sw, sh] part of `source` (anything
// drawImage takes) into `canvas` at w×h and dithers it, as the frame would.
export function renderDithered(canvas, source, [sx, sy, sw, sh], w = SLOT_W, h = SLOT_H) {
  canvas.width = w; canvas.height = h;
  const ctx = canvas.getContext('2d', {willReadFrequently: true});
  ctx.imageSmoothingQuality = 'high';
  ctx.drawImage(source, sx, sy, sw, sh, 0, 0, w, h);
  const g = luminance(ctx, w, h);
  ditherAtkinson(g, w, h);
  putGrey(ctx, g, w, h);
}
