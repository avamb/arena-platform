// node --test ops/vino-cutover/wp-event-media.test.mjs
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { parseMapping, planEvent, syncPhotos, syncLinks, sha256, fileNameOf } from './wp-event-media-lib.mjs';

const doc = JSON.parse(readFileSync(new URL('./testdata/wp-event-media.sample.json', import.meta.url), 'utf8'));

test('parseMapping reads a CSV with a header and ignores junk', () => {
  const m = parseMapping('action_id,event_id\n3300001,0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b\n\nbad,line\n3300002;0190A1B2-C3D4-7E5F-8A9B-0C1D2E3F4A5C\n');
  assert.equal(m.size, 2);
  assert.equal(m.get('3300001'), '0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b');
  assert.equal(m.get('3300002'), '0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c');
});

test('planEvent takes the ru set, merges videos, dedupes, reports own en/he sets', () => {
  const p = planEvent(doc.events[0]);
  assert.deepEqual(p.gallery, ['https://wp.example.test/uploads/a1.jpg', 'https://wp.example.test/uploads/a2.jpg']);
  assert.deepEqual(p.reportPhotos, ['https://wp.example.test/uploads/r1.jpg']);
  assert.deepEqual(p.videos, ['https://www.youtube.com/watch?v=abcdefghijk', 'https://vimeo.com/123456']);
  assert.equal(p.partners.length, 1);
  assert.equal(p.reportText, 'Thank you all for coming.');
  assert.ok(p.warnings.some((w) => w.startsWith('he:')));
  assert.ok(!p.warnings.some((w) => w.startsWith('en:')));
});

test('planEvent cuts to the platform limits and says so', () => {
  const urls = Array.from({ length: 9 }, (_, i) => `https://wp.example.test/u/${i}.jpg`);
  const p = planEvent({ languages: { ru: { gallery: urls } } });
  assert.equal(p.gallery.length, 6);
  assert.ok(p.warnings[0].includes('gallery'));
});

function fakeApi(existing = []) {
  const items = [...existing];
  const sums = new Map(existing.map((e) => [e.media_id, e.sum]));
  const calls = { uploads: 0, adds: 0 };
  return {
    calls,
    list: async () => items,
    checksumOf: async (id) => sums.get(id),
    upload: async (_o, _n, _t, bytes) => { calls.uploads++; const id = `m${calls.uploads}`; sums.set(id, sha256(bytes)); return id; },
    add: async (_s, id) => { calls.adds++; items.push({ media_id: id }); },
  };
}
const images = { 'https://x/a.jpg': Buffer.from('AAA'), 'https://x/b.jpg': Buffer.from('BBB'), 'https://x/a-copy.jpg': Buffer.from('AAA') };
const download = async (u) => { if (!images[u]) throw new Error('HTTP 404'); return { bytes: images[u], type: 'image/jpeg' }; };

test('syncPhotos is idempotent by hash and skips a duplicate under another url', async () => {
  const api = fakeApi();
  const urls = ['https://x/a.jpg', 'https://x/b.jpg', 'https://x/a-copy.jpg'];
  const first = await syncPhotos({ api, section: 'media-items', ownerType: 'event_gallery', urls, max: 6, download, dry: false });
  assert.equal(first.uploaded, 2); assert.equal(first.present, 1);
  const second = await syncPhotos({ api, section: 'media-items', ownerType: 'event_gallery', urls, max: 6, download, dry: false });
  assert.equal(second.uploaded, 0); assert.equal(second.present, 3);
  assert.equal(api.calls.uploads, 2);
});

test('syncPhotos dry run writes nothing, honours the limit and reports failed downloads', async () => {
  const api = fakeApi([{ media_id: 'old', sum: sha256(Buffer.from('ZZZ')) }]);
  const r = await syncPhotos({ api, section: 'media-items', ownerType: 'event_gallery', urls: ['https://x/a.jpg', 'https://x/b.jpg', 'https://x/gone.jpg'], max: 2, download, dry: true });
  assert.equal(api.calls.uploads, 0);
  assert.equal(r.uploaded, 1); assert.equal(r.full, 1); assert.equal(r.failed, 1);
});

test('syncLinks adds a video only when the address is not there', async () => {
  const added = [];
  const api = { list: async () => [{ url: 'https://v/1' }], addVideo: async (u) => added.push(u) };
  const r = await syncLinks({ api, kind: 'video', items: ['https://v/1', 'https://v/2'], max: 4, dry: false });
  assert.deepEqual(added, ['https://v/2']);
  assert.equal(r.present, 1); assert.equal(r.added, 1);
});

test('fileNameOf makes a safe name', () => {
  assert.equal(fileNameOf('https://wp.example.test/uploads/2026/10/My%20Photo%20(1).jpg'), 'My_Photo_1_.jpg');
  assert.equal(fileNameOf('not a url'), 'image');
});
