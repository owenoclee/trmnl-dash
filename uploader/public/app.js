import {encode, parse} from './format.js';
import {SLOT_W, SLOT_H, SLOT_ASPECT, displayRect, renderDithered, putGrey} from './display.js';

// Upload sizing. The frame's slot is 783 px wide; the focus is kept at up to
// FOCUS_MAX_W so a larger slot later still has the pixels, and as much of the
// photo around it as fits in MAX_OUT_PX. Sources are capped at MAX_SRC on the
// long edge first, which keeps iOS inside its canvas memory limit.
const FOCUS_MAX_W = 1200;
const MAX_OUT_PX = 3_000_000;
const MAX_SRC = 4096;
const THUMB_W = 400, THUMB_H = Math.round(THUMB_W / SLOT_ASPECT);

const $ = id => document.getElementById(id);
const show = (id, on = true) => {$(id).hidden = !on;};

// ---------------------------------------------------------------------------
// Key: carried in the link's fragment (#t=…) so it never reaches a server log,
// remembered locally so a home-screen launch without it still works.
// ---------------------------------------------------------------------------
const TOKEN_KEY = 'photo-frame-key';
const token = (() => {
  const fromHash = new URLSearchParams(location.hash.slice(1)).get('t');
  try {
    if (fromHash) localStorage.setItem(TOKEN_KEY, fromHash);
    return fromHash || localStorage.getItem(TOKEN_KEY);
  } catch (e) {
    return fromHash;
  }
})();

class Unauthorised extends Error {}

async function api(path, opts = {}) {
  const res = await fetch(path, {...opts, headers: {Authorization: `Bearer ${token}`, ...opts.headers}});
  if (res.status === 401) throw new Unauthorised();
  if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || `HTTP ${res.status}`);
  return res;
}

function lock(msg) {
  ['library', 'editor', 'sheet', 'scrim'].forEach(id => show(id, false));
  $('locked-msg').textContent = msg;
  show('locked');
}

let toastTimer;
function toast(msg) {
  const t = $('toast');
  t.textContent = msg;
  t.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.remove('show'), 2200);
}

// A TGRY image as a canvas at its own size, for drawImage.
function tgryCanvas(bytes) {
  const info = parse(bytes);
  const c = document.createElement('canvas');
  c.width = info.width; c.height = info.height;
  putGrey(c.getContext('2d'), bytes.subarray(20, info.size), info.width, info.height);
  return {canvas: c, ...info};
}

// ---------------------------------------------------------------------------
// Library
// ---------------------------------------------------------------------------
let photos = [];          // from the server, newest first
const pending = [];       // uploads in flight: {key, thumb (canvas), progress, error}

async function loadLibrary() {
  try {
    photos = (await (await api('/api/photos')).json()).photos;
  } catch (e) {
    if (e instanceof Unauthorised) return lock("This link's key isn't valid any more. Ask for a new link.");
    toast("Couldn't load the photos. Pull down to try again.");
    return;
  }
  renderLibrary();
}

function renderLibrary() {
  const n = photos.length;
  $('lib-lede').textContent = n
    ? `${n} ${n === 1 ? 'photo' : 'photos'} from you, taking turns with the others. The frame changes every half hour.`
    : 'Photos you add here take turns on the frame. It changes every half hour.';
  show('lib-empty', n === 0 && pending.length === 0);

  const grid = $('grid');
  grid.replaceChildren();
  for (const p of pending) grid.append(pendingTile(p));
  for (const p of photos) grid.append(photoTile(p));
}

function pendingTile(p) {
  const li = document.createElement('li');
  li.append(p.thumb);
  if (p.error) {
    const e = document.createElement('p');
    e.className = 'failed';
    e.textContent = p.error;
    li.append(e);
  } else {
    const bar = document.createElement('div');
    bar.className = 'progress';
    bar.innerHTML = '<span></span>';
    bar.firstChild.style.width = `${Math.round(p.progress * 100)}%`;
    p.bar = bar.firstChild;
    li.append(bar);
  }
  return li;
}

const thumbCache = new Map();   // id → dithered canvas

function photoTile(p) {
  const li = document.createElement('li');
  const btn = document.createElement('button');
  btn.type = 'button';
  btn.setAttribute('aria-label', `Photo added ${new Date(p.uploaded).toLocaleDateString()}`);
  let c = thumbCache.get(p.id);
  if (!c) {
    c = document.createElement('canvas');
    thumbCache.set(p.id, c);
    loadThumb(p.id, c);
  }
  btn.append(c);
  btn.addEventListener('click', () => openSheet(p));
  li.append(btn);
  return li;
}

// Thumbnails load a few at a time rather than all at once.
const thumbQueue = [];
let thumbsActive = 0;
function loadThumb(id, canvas) {
  thumbQueue.push({id, canvas});
  pumpThumbs();
}
function pumpThumbs() {
  while (thumbsActive < 4 && thumbQueue.length) {
    const {id, canvas} = thumbQueue.shift();
    thumbsActive++;
    api(`/api/photos/${id}/thumb`)
      .then(r => r.arrayBuffer())
      .then(buf => {
        const t = tgryCanvas(new Uint8Array(buf));
        renderDithered(canvas, t.canvas, [0, 0, t.width, t.height], THUMB_W, THUMB_H);
      })
      .catch(() => {})
      .finally(() => {thumbsActive--; pumpThumbs();});
  }
}

// ---------------------------------------------------------------------------
// Sheet: one photo at the frame's size, from the full upload.
// ---------------------------------------------------------------------------
let sheetPhoto = null;

async function openSheet(p) {
  sheetPhoto = p;
  const remove = $('sheet-remove');
  remove.classList.remove('armed');
  remove.textContent = 'Remove from frame';
  remove.disabled = false;
  $('sheet-meta').textContent = `Added ${new Date(p.uploaded).toLocaleDateString(undefined, {day: 'numeric', month: 'long', year: 'numeric'})}`;
  const c = $('sheet-preview');
  const thumb = thumbCache.get(p.id);
  if (thumb?.width) renderDithered(c, thumb, [0, 0, thumb.width, thumb.height]);   // stand-in while the full one loads
  show('scrim'); show('sheet');
  $('sheet-close').focus();
  try {
    const t = tgryCanvas(new Uint8Array(await (await api(`/api/photos/${p.id}`)).arrayBuffer()));
    if (sheetPhoto === p) renderDithered(c, t.canvas, displayRect(t.width, t.height, t.focus));
  } catch (e) { /* keep the thumbnail */ }
}

function closeSheet() {
  sheetPhoto = null;
  show('sheet', false); show('scrim', false);
}

async function removeSheetPhoto() {
  const btn = $('sheet-remove');
  if (!btn.classList.contains('armed')) {
    btn.classList.add('armed');
    btn.textContent = 'Tap again to remove';
    return;
  }
  const p = sheetPhoto;
  btn.disabled = true;
  try {
    await api(`/api/photos/${p.id}`, {method: 'DELETE'});
    photos = photos.filter(q => q.id !== p.id);
    thumbCache.delete(p.id);
    closeSheet();
    renderLibrary();
    toast('Removed from frame');
  } catch (e) {
    btn.disabled = false;
    toast("Couldn't remove it. Try again.");
  }
}

// ---------------------------------------------------------------------------
// Editor: frame each picked photo in turn.
//
// The frame is a fixed cut-out on the stage, the slot's shape; the photo moves
// and scales behind it. The state is the frame's rect in source pixels
// (cx, cy, w; h follows from the slot's aspect), always kept inside the photo.
// ---------------------------------------------------------------------------
let queue = [];           // files still to frame
let total = 0;
let src = null;           // the current photo as a canvas, long edge ≤ MAX_SRC
let F = null;             // {cx, cy, w}
let limits = null;        // {minW, maxW}
let layout = null;        // stage geometry in CSS px

async function startEditing(files) {
  queue = [...files];
  total = queue.length;
  if (!total) return;
  show('library', false); show('editor');
  window.scrollTo(0, 0);
  await nextPhoto();
}

async function nextPhoto() {
  $('ed-error').hidden = true;
  if (!queue.length) return finishEditing();
  const file = queue.shift();
  $('ed-count').textContent = total > 1 ? `${total - queue.length} of ${total}` : '';
  $('ed-add').disabled = true;
  src = null;
  clearStage();
  $('ed-hint').textContent = 'Opening photo…';
  try {
    src = await loadSource(file);
    $('ed-hint').textContent = 'Drag to move, pinch to zoom.';
  } catch (e) {
    $('ed-hint').textContent = '';
    $('ed-error').textContent = `${file.name || 'This photo'} couldn't be opened. Skip it to carry on.`;
    $('ed-error').hidden = false;
    clearStage();
    return;
  }
  const maxW = Math.min(src.width, src.height * SLOT_ASPECT);
  limits = {maxW, minW: Math.min(maxW, Math.max(maxW / 10, 300))};
  F = {cx: src.width / 2, cy: src.height / 2, w: maxW};   // what the frame shows untouched
  $('ed-add').disabled = false;
  drawStage();
  schedulePreview(true);
}

function finishEditing() {
  src = null;
  show('editor', false); show('library');
  renderLibrary();
}

// Decode through an <img>, which applies EXIF orientation and handles whatever
// the phone's browser can open (HEIC included on iOS), then draw it into a
// canvas no larger than MAX_SRC on its long edge.
async function loadSource(file) {
  const url = URL.createObjectURL(file);
  try {
    const img = new Image();
    img.src = url;
    await img.decode();
    const k = Math.min(1, MAX_SRC / Math.max(img.naturalWidth, img.naturalHeight));
    const c = document.createElement('canvas');
    c.width = Math.round(img.naturalWidth * k);
    c.height = Math.round(img.naturalHeight * k);
    const ctx = c.getContext('2d');
    ctx.imageSmoothingQuality = 'high';
    ctx.drawImage(img, 0, 0, c.width, c.height);
    return c;
  } finally {
    URL.revokeObjectURL(url);
  }
}

function clamp() {
  F.w = Math.min(limits.maxW, Math.max(limits.minW, F.w));
  const h = F.w / SLOT_ASPECT;
  F.cx = Math.min(src.width - F.w / 2, Math.max(F.w / 2, F.cx));
  F.cy = Math.min(src.height - h / 2, Math.max(h / 2, F.cy));
}

function measureStage() {
  const canvas = $('stage-canvas');
  const W = canvas.clientWidth, H = canvas.clientHeight, dpr = window.devicePixelRatio || 1;
  if (canvas.width !== Math.round(W * dpr)) canvas.width = Math.round(W * dpr);
  if (canvas.height !== Math.round(H * dpr)) canvas.height = Math.round(H * dpr);
  const fw = W - 32, fh = fw / SLOT_ASPECT;
  layout = {W, H, dpr, fx: 16, fy: (H - fh) / 2, fw, fh};
}

// Source → stage (CSS px) scale, and the frame's top-left in source px.
const scale = () => layout.fw / F.w;
const frameLeft = () => F.cx - F.w / 2;
const frameTop = () => F.cy - F.w / SLOT_ASPECT / 2;

function roundRectPath(ctx, x, y, w, h, r) {
  ctx.moveTo(x + r, y);
  ctx.arcTo(x + w, y, x + w, y + h, r);
  ctx.arcTo(x + w, y + h, x, y + h, r);
  ctx.arcTo(x, y + h, x, y, r);
  ctx.arcTo(x, y, x + w, y, r);
  ctx.closePath();
}

// Next frame, or straight away when the page is hidden (no frames come then,
// and a queued one would hold up every draw after it).
const nextFrame = cb => (document.hidden ? setTimeout(cb, 0) : requestAnimationFrame(cb));

let stageQueued = false;
function drawStage() {
  if (stageQueued) return;
  stageQueued = true;
  nextFrame(() => {
    stageQueued = false;
    if (!src) return;
    measureStage();
    const {W, H, dpr, fx, fy, fw, fh} = layout;
    const ctx = $('stage-canvas').getContext('2d');
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.fillStyle = '#E4E5E1';
    ctx.fillRect(0, 0, W, H);
    const k = scale();
    ctx.imageSmoothingQuality = 'high';
    ctx.drawImage(src, fx - frameLeft() * k, fy - frameTop() * k, src.width * k, src.height * k);
    // Wash out everything outside the frame, so the photo around it reads as
    // context rather than content.
    const r = fw * 32 / SLOT_W;
    ctx.beginPath();
    ctx.rect(0, 0, W, H);
    roundRectPath(ctx, fx, fy, fw, fh, r);
    ctx.fillStyle = 'rgba(228, 229, 225, 0.78)';
    ctx.fill('evenodd');
    ctx.beginPath();
    roundRectPath(ctx, fx - 1, fy - 1, fw + 2, fh + 2, r + 1);
    ctx.lineWidth = 2;
    ctx.strokeStyle = '#000';
    ctx.stroke();
  });
}

function clearStage() {
  measureStage();
  const ctx = $('stage-canvas').getContext('2d');
  ctx.setTransform(1, 0, 0, 1, 0, 0);
  ctx.clearRect(0, 0, ctx.canvas.width, ctx.canvas.height);
  const p = $('preview');
  p.getContext('2d').clearRect(0, 0, p.width, p.height);
}

// The dithered preview is the slow part; run it at most every 90 ms while the
// photo moves, and once more when it settles.
let previewTimer = null, previewDirty = false;
function schedulePreview(now = false) {
  previewDirty = true;
  if (now) return renderPreview();
  if (!previewTimer) previewTimer = setTimeout(renderPreview, 90);
}
function renderPreview() {
  clearTimeout(previewTimer);
  previewTimer = null;
  if (!src || !previewDirty) return;
  previewDirty = false;
  renderDithered($('preview'), src, [frameLeft(), frameTop(), F.w, F.w / SLOT_ASPECT]);
}

function changed() {
  clamp();
  drawStage();
  schedulePreview();
}

// Zoom by `factor` keeping the source point under stage point (px, py) still.
function zoomAt(factor, px, py) {
  const k = scale();
  const sx = frameLeft() + (px - layout.fx) / k, sy = frameTop() + (py - layout.fy) / k;
  F.w = Math.min(limits.maxW, Math.max(limits.minW, F.w / factor));
  const k2 = scale();
  F.cx = sx - (px - layout.fx) / k2 + F.w / 2;
  F.cy = sy - (py - layout.fy) / k2 + F.w / SLOT_ASPECT / 2;
}

const pointers = new Map();
let gesture = null;   // {mid: {x, y}, dist}

function stagePoint(e) {
  const r = $('stage-canvas').getBoundingClientRect();
  return {x: e.clientX - r.left, y: e.clientY - r.top};
}

function gestureState() {
  const pts = [...pointers.values()];
  if (pts.length === 1) return {mid: pts[0], dist: 0};
  const [a, b] = pts;
  return {mid: {x: (a.x + b.x) / 2, y: (a.y + b.y) / 2}, dist: Math.hypot(a.x - b.x, a.y - b.y)};
}

function bindStage() {
  const stage = $('stage');
  stage.addEventListener('pointerdown', e => {
    if (!src) return;
    stage.setPointerCapture(e.pointerId);
    pointers.set(e.pointerId, stagePoint(e));
    gesture = gestureState();
  });
  stage.addEventListener('pointermove', e => {
    if (!src || !pointers.has(e.pointerId)) return;
    pointers.set(e.pointerId, stagePoint(e));
    const g = gestureState();
    if (pointers.size >= 2 && gesture.dist && g.dist) zoomAt(g.dist / gesture.dist, g.mid.x, g.mid.y);
    const k = scale();
    F.cx -= (g.mid.x - gesture.mid.x) / k;
    F.cy -= (g.mid.y - gesture.mid.y) / k;
    gesture = g;
    changed();
  });
  const end = e => {
    if (!pointers.delete(e.pointerId)) return;
    gesture = pointers.size ? gestureState() : null;
    if (!pointers.size) schedulePreview(true);
  };
  stage.addEventListener('pointerup', end);
  stage.addEventListener('pointercancel', end);

  stage.addEventListener('wheel', e => {
    if (!src) return;
    e.preventDefault();
    const p = stagePoint(e);
    zoomAt(Math.exp(-e.deltaY * 0.002), p.x, p.y);
    changed();
  }, {passive: false});

  stage.addEventListener('keydown', e => {
    if (!src) return;
    const step = F.w * 0.04;
    const moves = {ArrowLeft: [-step, 0], ArrowRight: [step, 0], ArrowUp: [0, -step], ArrowDown: [0, step]};
    if (moves[e.key]) {
      F.cx += moves[e.key][0]; F.cy += moves[e.key][1];
    } else if (e.key === '+' || e.key === '=') {
      zoomAt(1.1, layout.fx + layout.fw / 2, layout.fy + layout.fh / 2);
    } else if (e.key === '-') {
      zoomAt(1 / 1.1, layout.fx + layout.fw / 2, layout.fy + layout.fh / 2);
    } else return;
    e.preventDefault();
    changed();
  });

  window.addEventListener('resize', () => {if (src) {drawStage(); }});
}

// ---------------------------------------------------------------------------
// Encoding and upload
// ---------------------------------------------------------------------------

// greyBytes draws the [sx, sy, sw, sh] part of `source` at w×h and returns its
// luminance as bytes.
function greyBytes(source, [sx, sy, sw, sh], w, h) {
  const c = document.createElement('canvas');
  c.width = w; c.height = h;
  const ctx = c.getContext('2d', {willReadFrequently: true});
  ctx.imageSmoothingQuality = 'high';
  ctx.drawImage(source, sx, sy, sw, sh, 0, 0, w, h);
  const d = ctx.getImageData(0, 0, w, h).data, out = new Uint8Array(w * h);
  for (let i = 0; i < w * h; i++) out[i] = Math.round(0.299 * d[i * 4] + 0.587 * d[i * 4 + 1] + 0.114 * d[i * 4 + 2]);
  return out;
}

// encodeUpload builds the upload body: the photo around the frame, and a
// thumbnail of the frame itself.
function encodeUpload() {
  const fw = F.w, fh = F.w / SLOT_ASPECT, fl = frameLeft(), ft = frameTop();
  const s = Math.min(1, FOCUS_MAX_W / fw);
  // Keep the whole photo if it fits; otherwise as much around the frame as does.
  let rw = src.width, rh = src.height;
  if (rw * rh * s * s > MAX_OUT_PX) {
    const k = Math.sqrt(MAX_OUT_PX / (fw * fh * s * s));
    rw = Math.min(src.width, fw * k);
    rh = Math.min(src.height, fh * k);
  }
  const rx = Math.min(Math.max(F.cx - rw / 2, 0), src.width - rw);
  const ry = Math.min(Math.max(F.cy - rh / 2, 0), src.height - rh);
  const ow = Math.max(16, Math.round(rw * s)), oh = Math.max(16, Math.round(rh * s));
  const fx = Math.round((fl - rx) * s), fy = Math.round((ft - ry) * s);
  const focus = [
    Math.min(fx, ow - 16), Math.min(fy, oh - 16),
    Math.max(16, Math.min(Math.round(fw * s), ow - fx)), Math.max(16, Math.min(Math.round(fh * s), oh - fy)),
  ];
  const image = encode(ow, oh, focus, greyBytes(src, [rx, ry, rw, rh], ow, oh));
  const thumb = encode(THUMB_W, THUMB_H, [0, 0, THUMB_W, THUMB_H], greyBytes(src, [fl, ft, fw, fh], THUMB_W, THUMB_H));
  const body = new Uint8Array(image.length + thumb.length);
  body.set(image); body.set(thumb, image.length);
  return body;
}

// Uploads run one at a time in the background while the next photo is framed.
const uploadQueue = [];
let uploading = false;

function addCurrent() {
  if (!src) return;
  const body = encodeUpload();
  const thumb = document.createElement('canvas');
  renderDithered(thumb, src, [frameLeft(), frameTop(), F.w, F.w / SLOT_ASPECT], THUMB_W, THUMB_H);
  const p = {key: Math.random(), thumb, progress: 0, error: null, body};
  pending.unshift(p);
  uploadQueue.push(p);
  pumpUploads();
  nextPhoto();
}

function pumpUploads() {
  if (uploading || !uploadQueue.length) return;
  uploading = true;
  const p = uploadQueue.shift();
  const xhr = new XMLHttpRequest();
  xhr.open('POST', '/api/photos');
  xhr.setRequestHeader('Authorization', `Bearer ${token}`);
  xhr.setRequestHeader('Content-Type', 'application/octet-stream');
  xhr.upload.onprogress = e => {
    if (!e.lengthComputable) return;
    p.progress = e.loaded / e.total;
    if (p.bar) p.bar.style.width = `${Math.round(p.progress * 100)}%`;
  };
  const done = err => {
    uploading = false;
    p.body = null;
    if (err) {
      p.error = err;
    } else {
      pending.splice(pending.indexOf(p), 1);
      const created = JSON.parse(xhr.responseText);
      photos.unshift({id: created.id, uploaded: created.uploaded});
      thumbCache.set(created.id, p.thumb);
      toast('Added to frame');
    }
    if (!$('library').hidden) renderLibrary();
    pumpUploads();
  };
  xhr.onload = () => {
    if (xhr.status === 201) return done(null);
    if (xhr.status === 401) return lock("This link's key isn't valid any more. Ask for a new link.");
    let msg = `Upload failed (${xhr.status}).`;
    try {msg = `Not added: ${JSON.parse(xhr.responseText).error}.`;} catch (e) { }
    done(msg);
  };
  xhr.onerror = () => done('Not added: the connection dropped. Add it again.');
  xhr.send(p.body);
}

window.addEventListener('beforeunload', e => {
  if (uploading || uploadQueue.length) e.preventDefault();
});

// ---------------------------------------------------------------------------
// Wiring
// ---------------------------------------------------------------------------
$('picker').addEventListener('change', e => {
  const files = [...e.target.files];
  e.target.value = '';
  startEditing(files);
});
$('ed-add').addEventListener('click', addCurrent);
$('ed-skip').addEventListener('click', () => nextPhoto());
$('ed-cancel').addEventListener('click', () => {queue = []; finishEditing();});
$('sheet-close').addEventListener('click', closeSheet);
$('scrim').addEventListener('click', closeSheet);
$('sheet-remove').addEventListener('click', removeSheetPhoto);
document.addEventListener('keydown', e => {if (e.key === 'Escape' && !$('sheet').hidden) closeSheet();});
bindStage();

if (!token) {
  lock('This link is missing its key. Open the link you were sent, the whole thing.');
} else {
  show('library');
  renderLibrary();
  loadLibrary();
}
