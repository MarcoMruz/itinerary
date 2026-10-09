const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

function setup(fetch, navigator = {}) {
  const current = { id: 'trip', days: [], checklist: ['Water', 'Hat'] };
  const context = vm.createContext({
    document: { getElementById: () => ({ textContent: JSON.stringify({ active: current }) }) },
    localStorage: { getItem: () => null, setItem() {} }, fetch, navigator,
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
