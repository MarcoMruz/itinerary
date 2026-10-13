import { afterEach, test } from 'node:test';
import assert from 'node:assert/strict';
import { itineraryApp } from './static/app.mjs';
import { checklistChanges, checklistProgress } from './static/domain/checklist.mjs';
import { activityRoute, fmtLeg, totalLegs } from './static/domain/routes.mjs';
import { chatMessage, exportUrl, previewStats } from './static/domain/import-export.mjs';
import { hostname, insightPage, parseHash, viewHash } from './static/domain/insights.mjs';
import { dayDate, describeWeather, fmtDate, fmtTemp, isPastDate, localDate } from './static/domain/weather.mjs';

const globalNames = ['localStorage', 'fetch', 'navigator', 'window', 'location', 'history'];
const originalGlobals = Object.fromEntries(globalNames.map(name => [name, Object.getOwnPropertyDescriptor(globalThis, name)]));
afterEach(() => globalNames.forEach(name => {
  if (originalGlobals[name]) Object.defineProperty(globalThis, name, originalGlobals[name]);
  else delete globalThis[name];
}));

function setup(fetch, navigator = {}) {
  const current = { id: 'trip', days: [], checklist: ['Water', 'Hat'] };
  const globals = {
    localStorage: { getItem: () => null, setItem() {} }, fetch, navigator, window: { confirm: () => true },
    location: { hash: '#trip' }, history: { replaceState() {} },
  };
  Object.entries(globals).forEach(([name, value]) => Object.defineProperty(globalThis, name, { value, writable: true, configurable: true }));
  return itineraryApp({ active: current });
}

test('checklist rules trim edits, preserve renamed checks and reject blank items', () => {
  const draft = Object.freeze([
    Object.freeze({ original: 'Water', text: ' Fresh water ' }),
    Object.freeze({ original: null, text: ' Hat ' }),
  ]);
  assert.deepEqual(checklistChanges(draft, ['Water', 'Hat']), {
    checklist: ['Fresh water', 'Hat'], checked: ['Fresh water'], valid: true,
  });
  assert.equal(checklistChanges([{ original: null, text: ' ' }], []).valid, false);
  assert.deepEqual(checklistChanges([], []), { checklist: [], checked: [], valid: true });
  assert.deepEqual(checklistProgress([], ['Water']), { done: 0, total: 0, pct: 0 });
  assert.deepEqual(checklistProgress(['Water', 'Hat'], ['Water', 'Removed']), { done: 1, total: 2, pct: 50 });
});

test('route rules preserve return visits, skip missing places and require complete totals', () => {
  const activities = Object.freeze([
    Object.freeze({ place: 'Lake' }), Object.freeze({}), Object.freeze({ place: 'Lake' }),
    Object.freeze({ place: 'Town' }), Object.freeze({ place: 'Lake' }),
  ]);
  assert.deepEqual(activityRoute(activities), {
    stops: ['Lake', 'Town', 'Lake'], nums: [1, 0, 1, 2, 3], first: [true, false, false, true, true],
  });
  assert.equal(totalLegs([]), null);
  assert.equal(totalLegs([{ meters: 100, seconds: 10 }, null]), null);
  assert.deepEqual(totalLegs([{ meters: 100, seconds: 10 }, { meters: 200, seconds: 20 }]), { meters: 300, seconds: 30 });
  assert.equal(fmtLeg(null), '');
  assert.equal(fmtLeg({ meters: 999, seconds: 0 }), '999 m · 1 min');
  assert.equal(fmtLeg({ meters: 1500, seconds: 3660 }), '1,5 km · 1 h 01 min');
});

test('feature state is isolated and composed getters stay live', () => {
  const first = setup(async () => ({}));
  const second = itineraryApp();
  first.io.text = 'draft';
  first.checked = ['Water'];
  assert.equal(second.io.text, '');
  assert.deepEqual(second.checked, []);
  assert.equal(first.progress.pct, 50);
  first.current = { ...first.current, checklist: ['Water'] };
  assert.equal(first.progress.pct, 100);
  assert.equal(second.progress.pct, 0);
  assert.equal(second.tripUrl, '');
});

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

const insightTrip = Object.freeze({
  id: 'trip', title: 'Trip', checklist: [],
  localGuide: { summary: 'Locals walk', gettingAround: ['Day pass 5 €'], avoid: [], sources: ['https://www.example.com/a'] },
  days: [{ label: 'Sobota', activities: [
    { time: '09:00', title: 'Lake', place: 'Lake', insight: { summary: 'Come early', bestTime: 'Before 9', tips: ['North lot'], avoid: ['Boat rental'] } },
    { time: '12:00', title: 'Lunch' },
  ] }],
});

test('insight hash round-trips views and keeps owner keys', () => {
  assert.deepEqual(parseHash('#trip'), { id: 'trip', key: '', view: null });
  assert.deepEqual(parseHash('#trip~secret'), { id: 'trip', key: 'secret', view: null });
  assert.deepEqual(parseHash('#trip/guide').view, { id: 'trip', type: 'guide' });
  assert.deepEqual(parseHash('#trip/d/1/x').view, null);
  const place = { id: 'trip', type: 'place', day: 2, activity: 3 };
  assert.equal(viewHash(place), '#trip/d/2/3');
  assert.deepEqual(parseHash(viewHash(place)).view, place);
});

test('insight pages expose only filled sections and reject stale views', () => {
  const place = insightPage(insightTrip, { id: 'trip', type: 'place', day: 0, activity: 0 });
  assert.equal(place.eyebrow, 'Sobota · 09:00');
  assert.deepEqual(place.sections.map(section => section.key), ['tips', 'avoid']);
  assert.equal(place.place, 'Lake');
  const guide = insightPage(insightTrip, { id: 'trip', type: 'guide' });
  assert.deepEqual(guide.sections.map(section => section.title), ['Ako sa pohybovať']);
  assert.equal(insightPage(insightTrip, { id: 'other', type: 'guide' }), null);
  assert.equal(insightPage(insightTrip, { id: 'trip', type: 'place', day: 0, activity: 1 }), null);
  assert.equal(insightPage(insightTrip, { id: 'trip', type: 'place', day: 5, activity: 0 }), null);
  assert.equal(hostname('https://www.example.com/a'), 'example.com');
  assert.equal(previewStats(insightTrip), '1 deň · 2 aktivity · 0 v checkliste · tipy od miestnych: 1 · miestny sprievodca');
});

test('insider toggle changes export links and the chat request', () => {
  assert.equal(exportUrl('/x/export', false), '/x/export');
  assert.equal(exportUrl('/x/export', true), '/x/export?insider=true');
  assert.ok(!chatMessage('Uprav', 'update', '{}').includes('miestny sprievodca'));
  const message = chatMessage('Uprav', 'update', '{}', true);
  assert.ok(message.startsWith('Uprav\n\nBuď náš miestny sprievodca'));
  const app = setup(async () => ({}));
  app.io.insider = true;
  assert.equal(app.exportUrl, '/api/v1/itineraries/trip/export?insider=true');
  assert.equal(app.templateUrl, '/api/v1/import/template?insider=true');
});

test('opening a tip page from the hash selects its day and closing returns to the program', () => {
  const app = setup(async () => ({}));
  app.current = insightTrip;
  location.hash = '#trip/d/0/0';
  globalThis.window.scrollTo = () => {};
  app.$nextTick = callback => callback();
  app.openFromHash();
  assert.equal(app.page.title, 'Lake');
  assert.equal(app.dayIndex, 0);
  let replaced;
  history.replaceState = (state, title, url) => { replaced = url; };
  app.closePage();
  assert.equal(app.page, null);
  assert.equal(replaced, '#trip');
});

test('a stale tip link falls back to the program without changing the day', () => {
  const app = setup(async () => ({}));
  app.current = insightTrip;
  globalThis.window.scrollTo = () => {};
  app.$nextTick = callback => callback();
  let replaced;
  history.replaceState = (state, title, url) => { replaced = url; };
  location.hash = '#trip/d/9/0';
  app.openFromHash();
  assert.equal(app.page, null);
  assert.equal(app.dayIndex, 0);
  assert.equal(replaced, '#trip');
  location.hash = '#trip/guide';
  app.openGuide();
  assert.equal(app.pagesOpened, 0);
});

test('weather rules derive day dates and describe WMO codes', () => {
  const days = Object.freeze([{}, { date: '2026-08-01' }, {}]);
  assert.equal(dayDate(days, 0, '2026-07-30'), '2026-07-30');
  assert.equal(dayDate(days, 1, '2026-07-30'), '2026-08-01');
  assert.equal(dayDate(days, 2, '2026-12-30'), '2027-01-01');
  assert.equal(dayDate(days, 0, ''), '');
  assert.equal(dayDate(days, 0, 'tomorrow'), '');
  assert.equal(dayDate(days, 5, '2026-07-30'), '');
  assert.equal(fmtDate('2026-10-16'), 'pi 16. 10.');
  assert.equal(fmtTemp(12.6), '13 °C');
  assert.equal(fmtTemp(null), '–');
  assert.equal(describeWeather(63).label, 'Dážď');
  assert.equal(describeWeather(1234).label, 'Počasie');
  const now = new Date(2026, 6, 5, 23, 30);
  assert.equal(localDate(now), '2026-07-05');
  assert.equal(isPastDate('2026-07-04', now), true);
  assert.equal(isPastDate('2026-07-05', now), false);
  assert.equal(isPastDate('', now), false);
});

test('weather loads once per day and date, and the browser start date overrides the itinerary', async () => {
  const urls = [];
  const app = setup(async url => {
    urls.push(url);
    return { ok: true, json: async () => ({ status: 'ok', weatherCode: 0, tempMax: 20, tempMin: 10 }) };
  });
  app.current = { ...app.current, startDate: '2099-07-01', days: [{ activities: [] }, { activities: [] }] };
  app.loadTripStart();
  app.dayIndex = 1;
  assert.equal(app.dayDate, '2099-07-02');
  assert.equal(app.dayWeather, undefined);
  await app.loadWeather(0);
  await app.loadWeather(0);
  assert.deepEqual(urls, ['/api/v1/itineraries/trip/days/1/weather?date=2099-07-02']);
  assert.equal(app.dayWeather.status, 'ok');
  app.saveTripStart('2099-08-10');
  assert.equal(app.dayDate, '2099-08-11');
  assert.equal(app.dayWeather, undefined);
  app.saveTripStart('');
  assert.equal(app.dayDate, '2099-07-02');
  assert.equal(app.dayWeather.status, 'ok');
});

test('disabled weather hides instead of failing', async () => {
  const app = setup(async () => ({ ok: true, json: async () => ({ status: 'disabled' }) }));
  app.current = { ...app.current, days: [{ date: '2099-07-01', activities: [] }] };
  await app.loadWeather(0);
  assert.equal(app.dayWeather.status, 'disabled');
  assert.equal(app.showWeather, false);
});

test('a missing day shows the failure instead of hiding as disabled', async () => {
  const app = setup(async () => ({ ok: false, status: 404 }));
  app.current = { ...app.current, days: [{ date: '2099-07-01', activities: [] }] };
  await app.loadWeather(0);
  assert.equal(app.dayWeather, null);
  assert.equal(app.showWeather, true);
});

test('past days neither load nor show weather, and a finished browser trip start is ignored', async () => {
  const urls = [];
  const app = setup(async url => { urls.push(url); return { ok: true, json: async () => ({ status: 'ok' }) }; });
  app.current = { ...app.current, startDate: '2020-05-01', days: [{ activities: [] }, { activities: [] }] };
  globalThis.localStorage.getItem = () => '2020-01-01';
  app.loadTripStart();
  assert.equal(app.tripStart, '2020-05-01');
  assert.equal(app.isPastDay, true);
  assert.equal(app.weatherKey, '');
  assert.equal(app.showWeather, false);
  await app.loadWeather(0);
  assert.deepEqual(urls, []);
  app.current = { ...app.current, days: [{ activities: [] }, { date: '2099-01-01', activities: [] }] };
  app.dayIndex = 1;
  assert.equal(app.showWeather, true);
});
