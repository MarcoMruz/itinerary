// Hash format: #<id>[/guide | /d/<day>/<activity>][~<owner key>]. Ids never contain '/' or '~'.
export function parseHash(hash) {
  const [path, key = ''] = hash.replace(/^#/, '').split('~');
  const [rawId, kind, day, activity] = path.split('/');
  const id = decodeURIComponent(rawId || '');
  if (kind === 'guide') return { id, key, view: { id, type: 'guide' } };
  const place = kind === 'd' && /^\d+$/.test(day || '') && /^\d+$/.test(activity || '');
  return { id, key, view: place ? { id, type: 'place', day: Number(day), activity: Number(activity) } : null };
}

export function viewHash(view) {
  const id = '#' + encodeURIComponent(view.id);
  return view.type === 'guide' ? id + '/guide' : id + '/d/' + view.day + '/' + view.activity;
}

const sectionTitles = {
  gettingAround: 'Ako sa pohybovať',
  tips: 'Rady od miestnych',
  avoid: 'Na čo si dať pozor',
};

function sections(source, keys) {
  return keys
    .filter(key => (source[key] || []).length)
    .map(key => ({ key, title: sectionTitles[key], items: source[key] }));
}

// Page data for a view, or null when it does not match the loaded itinerary.
export function insightPage(itinerary, view) {
  if (!itinerary || !view || view.id !== itinerary.id) return null;
  if (view.type === 'guide') {
    const guide = itinerary.localGuide;
    if (!guide) return null;
    return {
      kind: 'guide', eyebrow: 'Rady od miestnych', title: 'Miestny sprievodca', summary: guide.summary || '', bestTime: '',
      sections: sections(guide, ['gettingAround', 'tips', 'avoid']), sources: guide.sources || [], place: '',
    };
  }
  const day = itinerary.days[view.day];
  const activity = day && day.activities[view.activity];
  if (!activity || !activity.insight) return null;
  const insight = activity.insight;
  return {
    kind: 'place', eyebrow: day.label + (activity.time ? ' · ' + activity.time : ''), title: activity.title,
    summary: insight.summary || '', bestTime: insight.bestTime || '',
    sections: sections(insight, ['tips', 'avoid']), sources: insight.sources || [], place: activity.place || '',
  };
}

export function insightCount(itinerary) {
  return itinerary.days.reduce((count, day) => count + day.activities.filter(activity => activity.insight).length, 0);
}

export function hostname(url) {
  try { return new URL(url).hostname.replace(/^www\./, ''); } catch { return url; }
}
