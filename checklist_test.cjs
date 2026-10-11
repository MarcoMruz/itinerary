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
