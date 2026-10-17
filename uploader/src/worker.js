// photos.pog.so — lets a phone add photos to the TRMNL's rotation.
//
// The page (public/) is static and holds nothing private. Every /api route
// needs a bearer token, and there are two:
//   UPLOAD_TOKEN   the phone: list, add, remove, fetch thumbnails and photos
//   DISPLAY_TOKEN  the TRMNL server: list and fetch photos, nothing else
// The phone's link carries its token in the URL fragment, which browsers never
// send, so it stays out of request logs; the page sends it as a header.
//
// Uploads are TGRY (see public/format.js), produced in the browser. This Worker
// checks the header fields and exact length and stores the bytes as they came;
// no image decoder runs here, or on the TRMNL.

import {parseUpload} from '../public/format.js';

const MAX_BODY = 2 * (20 + 4_000_000);   // generous; parseUpload is the real check
const MAX_PHOTOS = 200;
const ID = /^[0-9a-z]{8,12}-[0-9a-f]{8}$/;

export default {
  async fetch(req, env) {
    const url = new URL(req.url);
    if (!url.pathname.startsWith('/api/')) return env.ASSETS.fetch(req);
    try {
      return await api(req, env, url);
    } catch (e) {
      console.error(e);
      return json({error: 'internal error'}, 500);
    }
  },
};

async function api(req, env, url) {
  const role = await authorise(req, env);
  if (!role) return json({error: 'unauthorised'}, 401);

  const parts = url.pathname.split('/').filter(Boolean);   // ['api', 'photos', id?, 'thumb'?]
  if (parts[1] !== 'photos' || parts.length > 4) return json({error: 'not found'}, 404);
  const id = parts[2];
  if (id !== undefined && !ID.test(id)) return json({error: 'not found'}, 404);
  const thumb = parts[3] === 'thumb';
  if (parts.length === 4 && !thumb) return json({error: 'not found'}, 404);

  const m = req.method;
  if (!id && m === 'GET') return list(env);
  if (!id && m === 'POST' && role === 'upload') return upload(req, env);
  if (id && m === 'GET' && (!thumb || role === 'upload')) return get(env, `${thumb ? 't' : 'p'}/${id}`);
  if (id && !thumb && m === 'DELETE' && role === 'upload') {
    await env.PHOTOS.delete([`p/${id}`, `t/${id}`]);
    return json({deleted: id});
  }
  return json({error: 'not allowed'}, 405);
}

// authorise returns 'upload', 'display' or null for the request's bearer token.
async function authorise(req, env) {
  const m = /^Bearer (\S+)$/.exec(req.headers.get('Authorization') || '');
  if (!m) return null;
  if (env.UPLOAD_TOKEN && await same(m[1], env.UPLOAD_TOKEN)) return 'upload';
  if (env.DISPLAY_TOKEN && await same(m[1], env.DISPLAY_TOKEN)) return 'display';
  return null;
}

// Constant-time comparison of two strings, via their digests so the lengths match.
async function same(a, b) {
  const enc = new TextEncoder();
  const [da, db] = await Promise.all([a, b].map(s => crypto.subtle.digest('SHA-256', enc.encode(s))));
  return crypto.subtle.timingSafeEqual(da, db);
}

async function list(env) {
  const photos = [];
  let cursor;
  do {
    const page = await env.PHOTOS.list({prefix: 'p/', include: ['customMetadata'], cursor});
    for (const o of page.objects) {
      const md = o.customMetadata || {};
      photos.push({
        id: o.key.slice(2),
        uploaded: md.uploaded || o.uploaded.toISOString(),
        width: Number(md.width), height: Number(md.height),
        focus: (md.focus || '').split(',').map(Number),
        size: o.size,
      });
    }
    cursor = page.truncated ? page.cursor : undefined;
  } while (cursor);
  photos.sort((a, b) => b.uploaded.localeCompare(a.uploaded));
  return json({photos});
}

async function get(env, key) {
  const o = await env.PHOTOS.get(key);
  if (!o) return json({error: 'not found'}, 404);
  return new Response(o.body, {
    headers: {
      'Content-Type': 'application/octet-stream',
      'Content-Length': String(o.size),
      'Cache-Control': 'private, max-age=31536000, immutable',   // ids are never reused
    },
  });
}

async function upload(req, env) {
  const declared = Number(req.headers.get('Content-Length'));
  if (!declared || declared > MAX_BODY) return json({error: 'too large'}, 413);
  const body = new Uint8Array(await req.arrayBuffer());
  if (body.length > MAX_BODY) return json({error: 'too large'}, 413);

  let parsed;
  try {
    parsed = parseUpload(body);
  } catch (e) {
    return json({error: `not a valid upload: ${e.message}`}, 422);
  }

  const existing = await env.PHOTOS.list({prefix: 'p/', limit: MAX_PHOTOS});
  if (existing.objects.length >= MAX_PHOTOS) return json({error: `the frame holds at most ${MAX_PHOTOS} photos`}, 409);

  // Sortable by time, unguessable enough to never collide.
  const rand = [...crypto.getRandomValues(new Uint8Array(4))].map(b => b.toString(16).padStart(2, '0')).join('');
  const id = `${Date.now().toString(36)}-${rand}`;
  const {image, thumb} = parsed;
  const customMetadata = {
    uploaded: new Date().toISOString(),
    width: String(image.width), height: String(image.height), focus: image.focus.join(','),
  };
  const http = {contentType: 'application/octet-stream'};
  // Thumbnail first: a listed photo must always have one.
  await env.PHOTOS.put(`t/${id}`, body.subarray(image.size), {httpMetadata: http});
  await env.PHOTOS.put(`p/${id}`, body.subarray(0, image.size), {httpMetadata: http, customMetadata});
  return json({id, ...customMetadata}, 201);
}

function json(v, status = 200) {
  return new Response(JSON.stringify(v), {
    status,
    headers: {'Content-Type': 'application/json', 'Cache-Control': 'no-store'},
  });
}
