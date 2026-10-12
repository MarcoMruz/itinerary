export const fieldLabels = { title: 'názov', subtitle: 'podnadpis', duration: 'dĺžka', targetAudience: 'pre koho', startLocation: 'štart', mapsUrl: 'odkaz na mapu', days: 'program dní', checklist: 'checklist' };

export function chatMessage(request, action, fileText) {
  const ask = (request || '').trim() || (action === 'update'
    ? 'Pomôž mi upraviť tento itinerár. Najprv sa ma opýtaj, čo chcem zmeniť.'
    : 'Vytvor nový itinerár podľa tejto šablóny. Najprv sa ma opýtaj na cieľ, termín, kto cestuje a čo nás baví.');
  return ask + '\n\n' +
    'Postupuj podľa pravidiel v poli "instructions" v JSON nižšie a výsledok mi vráť ako celý dokument v rovnakom formáte ' +
    '(ideálne ako súbor .json), aby som ho mohol importovať do aplikácie Rodinné itineráre.\n\n' +
    '```json\n' + fileText.trim() + '\n```\n';
}

export function previewStats(itinerary) {
  if (!itinerary) return '';
  const days = itinerary.days.length;
  const activities = itinerary.days.reduce((count, day) => count + day.activities.length, 0);
  return days + (days === 1 ? ' deň' : days < 5 ? ' dni' : ' dní') + ' · ' +
    activities + (activities === 1 ? ' aktivita' : activities > 1 && activities < 5 ? ' aktivity' : ' aktivít') + ' · ' +
    itinerary.checklist.length + ' v checkliste';
}
