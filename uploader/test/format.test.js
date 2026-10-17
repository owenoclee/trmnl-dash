import {test} from 'node:test';
import assert from 'node:assert/strict';
import {encode, parse, parseUpload, HEADER} from '../public/format.js';

const img = (w, h, focus = [0, 0, w, h]) => encode(w, h, focus, new Uint8Array(w * h).fill(128));
const join = (...parts) => {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let o = 0;
  for (const p of parts) {out.set(p, o); o += p.length;}
  return out;
};

test('round trip', () => {
  const b = img(100, 50, [10, 5, 60, 30]);
  assert.deepEqual(parse(b), {width: 100, height: 50, focus: [10, 5, 60, 30], size: HEADER + 5000});
});

test('upload is image then thumbnail, exactly', () => {
  const body = join(img(400, 300, [20, 20, 300, 146]), img(200, 97));
  assert.equal(parseUpload(body).thumb.width, 200);
  assert.throws(() => parseUpload(join(body, new Uint8Array([0]))), /trailing/);
  assert.throws(() => parseUpload(body.subarray(0, body.length - 1)), /truncated/);
  assert.throws(() => parseUpload(img(400, 300)), /too short/);
});

test('rejects malformed headers', () => {
  const bad = mut => {const b = img(64, 64); mut(b, new DataView(b.buffer)); return b;};
  assert.throws(() => parse(bad(b => {b[0] = 0x89;})), /magic/);
  assert.throws(() => parse(bad(b => {b[4] = 2;})), /version/);
  assert.throws(() => parse(bad(b => {b[6] = 1;})), /reserved/);
  assert.throws(() => parse(bad((b, dv) => dv.setUint16(8, 8, true))), /dimensions/);
  assert.throws(() => parse(bad((b, dv) => dv.setUint16(8, 5000, true))), /dimensions/);
  assert.throws(() => parse(bad((b, dv) => dv.setUint16(12, 60, true))), /focus/);
  assert.throws(() => parse(bad((b, dv) => dv.setUint16(16, 0, true))), /focus/);
});

test('caps pixel count and thumbnail size', () => {
  const big = new Uint8Array(HEADER);
  big.set(img(16, 16).subarray(0, HEADER));
  const dv = new DataView(big.buffer);
  dv.setUint16(8, 4096, true); dv.setUint16(10, 4096, true);
  assert.throws(() => parse(big), /too many pixels/);
  assert.throws(() => parseUpload(join(img(64, 64), img(900, 20))), /dimensions/);
});
