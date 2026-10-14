import { insightCount } from './insights.mjs';

export const fieldLabels = { title: 'názov', subtitle: 'podnadpis', duration: 'dĺžka', targetAudience: 'pre koho', startLocation: 'štart', startDate: 'dátum začiatku', mapsUrl: 'odkaz na mapu', days: 'program dní', checklist: 'checklist', localGuide: 'miestny sprievodca' };

export const insiderAsk = 'Buď náš miestny sprievodca: niekto, kto v cieli roky žije a plánuje tento výlet osobne pre nás. ' +
  'Urob hĺbkový prieskum (miestne fóra, čerstvé recenzie, miestne a dopravné weby, zdroje v miestnom jazyku) a ku každému miestu dopíš to, ' +
  'čo turisti a bežní sprievodcovia nevedia: kedy prísť, kde parkovať, čo si dať, skryté miesta, čomu sa vyhnúť. ' +
  'Pridaj aj celkový návod, ako sa tam pohybovať. Ak zistíš lepšie miesto, čas alebo poradie, plán uprav. Pravidlá sú v časti LOCAL INSIDER MODE.';

export function exportUrl(base, insider) {
  return insider ? base + '?insider=true' : base;
}

export function chatMessage(request, action, fileText, insider = false) {
  const ask = (request || '').trim() || (action === 'update'
    ? 'Pomôž mi upraviť tento itinerár. Najprv sa ma opýtaj, čo chcem zmeniť.'
    : 'Vytvor nový itinerár podľa tejto šablóny. Najprv sa ma opýtaj na cieľ, termín, kto cestuje a čo nás baví.');
  return ask + '\n\n' + (insider ? insiderAsk + '\n\n' : '') +
    'Postupuj podľa pravidiel v poli "instructions" v JSON nižšie a výsledok mi vráť ako celý dokument v rovnakom formáte ' +
    '(ideálne ako súbor .json), aby som ho mohol importovať do aplikácie Rodinné itineráre.\n\n' +
    '```json\n' + fileText.trim() + '\n```\n';
}

function insightStats(itinerary) {
  const count = insightCount(itinerary);
  return (count ? ' · tipy od miestnych: ' + count : '') + (itinerary.localGuide ? ' · miestny sprievodca' : '');
}

export function previewStats(itinerary) {
  if (!itinerary) return '';
  const days = itinerary.days.length;
  const activities = itinerary.days.reduce((count, day) => count + day.activities.length, 0);
  return days + (days === 1 ? ' deň' : days < 5 ? ' dni' : ' dní') + ' · ' +
    activities + (activities === 1 ? ' aktivita' : activities > 1 && activities < 5 ? ' aktivity' : ' aktivít') + ' · ' +
    itinerary.checklist.length + ' v checkliste' + insightStats(itinerary);
}
