const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

function setup(fetch, navigator = {}) {
  const current = { id: 'trip', days: [], checklist: ['Water', 'Hat'] };
  const context = vm.createContext({
    document: { getElementById: () => ({ textContent: JSON.stringify({ active: current }) }) },
    localStorage: { getItem: () => null, setItem() {} }, fetch, navigator, window: { confirm: () => true },
    location: { hash: '#trip' }, history: { replaceState() {} },
  });
  const html = fs.readFileSync('templates/index.html', 'utf8');
  vm.runInContext(html.split('<script>')[1].split('</script>')[0], context);
  return context.itineraryApp();
}

test('add, rename and remove save while preserving checked state', async () => {
  let sent;
  const app = setup(async (url, options) => {
    sent = JSON.parse(options.body);
    return { ok: true, json: async () => ({ id: 'trip', days: [], ...sent }) };
  });
  app.checked = ['Water'];
  app.beginChecklistEdit();
  app.changeChecklistItem(0, ' Fresh water ');
  app.checklistDraft = app.checklistDraft.filter(entry => entry.id !== 1);
  app.addChecklistItem();
  app.changeChecklistItem(2, 'Socks');
  await app.saveChecklist();
  assert.deepEqual(sent.checklist, ['Fresh water', 'Socks']);
  assert.equal(JSON.stringify(app.checked), '["Fresh water"]');
  assert.equal(app.editingChecklist, false);
});

test('reselecting an itinerary fetches edits made by another owner', async () => {
  const app = setup(async url => ({
    ok: true,
    json: async () => url.endsWith('/checklist-access')
      ? { canEdit: false }
      : { id: 'trip', days: [], checklist: ['Updated elsewhere'] },
  }));
  await app.select('trip');
  assert.equal(app.current.checklist[0], 'Updated elsewhere');
});

test('switching back while another itinerary loads clears loading state', async () => {
  let finish;
  const app = setup(url => {
    if (url.endsWith('/other')) return new Promise(resolve => { finish = resolve; });
    return Promise.resolve({ ok: true, json: async () => url.endsWith('/checklist-access')
      ? { canEdit: false } : { id: 'trip', days: [], checklist: ['Water'] } });
  });
  const pending = app.select('other');
  await app.select('trip');
  finish({ ok: true, json: async () => ({ id: 'other', days: [], checklist: [] }) });
  await pending;
  assert.equal(app.current.id, 'trip');
  assert.equal(app.loading, false);
});

test('changed route stops invalidate distances for the same itinerary and day', () => {
  const app = setup(async () => ({}));
  app.current = { ...app.current, days: [{ activities: [{ place: 'Lake' }, { place: 'Castle' }] }] };
  const oldKey = app.legsKey;
  app.legsByKey = { [oldKey]: { legs: [{ meters: 1000, seconds: 60 }], complete: true } };
  app.current = { ...app.current, days: [{ activities: [{ place: 'Lake' }, { place: 'Town' }] }] };
  assert.notEqual(app.legsKey, oldKey);
  assert.equal(app.legs.length, 0);
});

test('navigation preserves stop numbering, waypoints and encoded place names', () => {
  const app = setup(async () => ({}));
  app.current = { ...app.current, days: [{ activities: [
    { place: 'Lake & park' }, {}, { place: 'Lake & park' }, { place: 'Town square' },
  ] }] };
  assert.equal(JSON.stringify(app.dayRoute), JSON.stringify({
    stops: ['Lake & park', 'Town square'], nums: [1, 0, 1, 2], first: [true, false, false, true],
  }));
  app.nav = { stops: app.dayRoute.stops, origin: 'My hotel', title: 'Route' };
  const [google, apple, waze] = app.navApps;
  assert.equal(new URL(google.url).searchParams.get('origin'), 'My hotel');
  assert.equal(new URL(google.url).searchParams.get('waypoints'), 'Lake & park');
  assert.deepEqual(new URL(apple.url).searchParams.getAll('waypoint'), ['Lake & park']);
  assert.equal(new URL(apple.url).searchParams.get('source'), 'My hotel');
  assert.equal(new URL(apple.url).searchParams.get('destination'), 'Town square');
  assert.equal(new URL(waze.url).searchParams.get('q'), 'Lake & park');
  assert.equal(new URL(waze.more[0].url).searchParams.get('q'), 'Town square');
  assert.equal(apple.url.includes('+'), false);
});

test('import save refreshes and selects the saved itinerary', async () => {
  const saved = { id: 'imported', title: 'Imported', days: [], checklist: ['Water'] };
  const app = setup(async url => ({ ok: true, json: async () => {
    if (url === '/api/v1/import') return { id: saved.id, itinerary: saved, action: 'create' };
    if (url === '/api/v1/itineraries') return [{ id: saved.id, title: saved.title }];
    if (url.endsWith('/checklist-access')) return { canEdit: false };
    return saved;
  } }));
  let closed = false;
  app.$refs = { io: { close() { closed = true; } } };
  app.io.text = '{}';
  await app.saveImport();
  assert.equal(app.current.id, 'imported');
  assert.equal(app.list[0].id, 'imported');
  assert.equal(app.io.text, '');
  assert.equal(app.io.preview, null);
  assert.equal(closed, true);
  assert.match(app.notice, /Imported/);
});

test('owner share copies the link with a manual fallback', async () => {
  let copied;
  const app = setup(async () => ({ ok: true, json: async () => ({ url: 'https://example.com/#trip~key' }) }), {
    clipboard: { writeText: async text => { copied = text; } },
  });
  app.canEdit = true;
  await app.shareChecklist();
  assert.equal(copied, app.shareUrl);
  assert.equal(app.shareMessage, 'Odkaz je skopírovaný.');
  const fallback = setup(async () => ({ ok: true, json: async () => ({ url: copied }) }));
  fallback.canEdit = true;
  await fallback.shareChecklist();
  assert.equal(fallback.shareUrl, copied);
  assert.equal(fallback.shareMessage, 'Skopírujte odkaz z poľa vyššie.');
});

test('expired owner access never exposes a stale share link', async () => {
  const app = setup(async () => ({ ok: false, status: 401 }));
  app.canEdit = true;
  app.shareUrl = 'old link';
  await app.shareChecklist();
  assert.equal(app.shareUrl, '');
  assert.equal(app.sharingChecklist, false);
  assert.equal(app.shareMessage, 'Otvorte súkromný odkaz na úpravu znova.');
});

test('failed save retains draft and original checklist for retry', async () => {
  const app = setup(async () => ({ ok: false, status: 401 }));
  app.beginChecklistEdit();
  app.changeChecklistItem(0, 'Changed');
  await app.saveChecklist();
  assert.equal(app.current.checklist[0], 'Water');
  assert.equal(app.checklistDraft[0].text, 'Changed');
  assert.equal(app.editingChecklist, true);
  assert.equal(app.checklistFailed, true);
  assert.equal(app.savingChecklist, false);
});

test('blank items never reach the server; empty checklist can be saved', async () => {
  let calls = 0;
  const app = setup(async () => {
    calls++;
    return { ok: true, json: async () => ({ id: 'trip', days: [], checklist: [] }) };
  });
  app.beginChecklistEdit();
  app.changeChecklistItem(0, '  ');
  await app.saveChecklist();
  assert.equal(calls, 0);
  app.checklistDraft = [];
  await app.saveChecklist();
  assert.equal(calls, 1);
  assert.equal(app.current.checklist.length, 0);
});

test('copy for chat puts the request before the export file', async () => {
  let copied;
  const app = setup(async () => ({ ok: true, text: async () => '{"format":"family-itinerary"}\n' }), {
    clipboard: { writeText: async text => { copied = text; } },
  });
  app.io.request = '  Pridaj obed v sobotu. ';
  await app.copyForChat('/api/v1/itineraries/trip/export', 'update');
  assert.ok(copied.startsWith('Pridaj obed v sobotu.\n\nPostupuj podľa pravidiel'));
  assert.ok(copied.endsWith('```json\n{"format":"family-itinerary"}\n```\n'));
  assert.equal(app.io.copyMessage, 'Skopírované. Vložte to do chatu s asistentom ako jednu správu.');

  app.io.request = '';
  await app.copyForChat('/api/v1/import/template', 'create');
  assert.ok(copied.startsWith('Vytvor nový itinerár podľa tejto šablóny.'));
});

test('share link is created, copied and revoked', async () => {
  let copied;
  const calls = [];
  const app = setup(async (url, options) => {
    calls.push(options.method + ' ' + url);
    return options.method === 'DELETE' ? { ok: true } : { ok: true, status: 200, json: async () => ({ url: 'https://example.com/s/trip/key' }) };
  }, { clipboard: { writeText: async text => { copied = text; } } });
  app.$refs = { share: { showModal() {} } };
  app.openShare();
  await new Promise(r => setTimeout(r, 0));
  assert.equal(app.sharing.url, 'https://example.com/s/trip/key');
  await app.copyShare();
  assert.equal(copied, 'https://example.com/s/trip/key');
  await app.revokeShare();
  assert.deepEqual(calls, ['POST /api/v1/itineraries/trip/share-link', 'DELETE /api/v1/itineraries/trip/share-link']);
  assert.equal(app.sharing.url, '');
  assert.equal(app.sharing.revoked, true);
});
