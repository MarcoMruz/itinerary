// Apple and Waze expect spaces as %20, not URLSearchParams' '+'.
export function qs(pairs) {
  return pairs.map(([key, value]) => encodeURIComponent(key) + '=' + encodeURIComponent(value)).join('&');
}

export const mapLinks = {
  google(stops, origin) {
    const pairs = [
      ['api', '1'], ['travelmode', 'driving'],
      ...(origin ? [['origin', origin]] : []),
      ['destination', stops[stops.length - 1]],
      ...(stops.length > 1 ? [['waypoints', stops.slice(0, -1).join('|')]] : []),
    ];
    return 'https://www.google.com/maps/dir/?' + qs(pairs);
  },
  apple(stops, origin) {
    const pairs = [
      ...(origin ? [['source', origin]] : []),
      ...stops.slice(0, -1).map(place => ['waypoint', place]),
      ['destination', stops[stops.length - 1]], ['mode', 'driving'],
    ];
    return 'https://maps.apple.com/directions?' + qs(pairs);
  },
  waze(place) {
    return 'https://waze.com/ul?' + qs([['q', place], ['navigate', 'yes']]);
  },
};

// Match Day.RouteStops: merge consecutive places, including across unlocated activities.
export function activityRoute(activities) {
  return activities.reduce((route, activity) => {
    const isNew = !!activity.place && activity.place !== route.stops[route.stops.length - 1];
    const stops = isNew ? [...route.stops, activity.place] : route.stops;
    return {
      stops,
      nums: [...route.nums, activity.place ? stops.length : 0],
      first: [...route.first, isNew],
    };
  }, { stops: [], nums: [], first: [] });
}

export function totalLegs(legs) {
  if (!legs.length || legs.some(leg => !leg)) return null;
  return legs.reduce((sum, leg) => ({ meters: sum.meters + leg.meters, seconds: sum.seconds + leg.seconds }), { meters: 0, seconds: 0 });
}

export function fmtLeg(leg) {
  if (!leg) return '';
  const meters = leg.meters;
  const distance = meters < 1000 ? Math.round(meters) + ' m'
    : (meters < 10000 ? (meters / 1000).toFixed(1).replace('.', ',') : Math.round(meters / 1000)) + ' km';
  const minutes = Math.max(1, Math.round(leg.seconds / 60));
  const duration = minutes < 60 ? minutes + ' min' : Math.floor(minutes / 60) + ' h ' + String(minutes % 60).padStart(2, '0') + ' min';
  return distance + ' · ' + duration;
}

export function navigationApps(stops, origin) {
  if (!stops.length) return [];
  const count = stops.length;
  const whole = count > 1 ? 'Celá trasa · ' + count + (count < 5 ? ' zastávky' : ' zastávok') : 'Navigácia do cieľa';
  const waze = stops.map((place, index) => ({ label: (count > 1 ? (index + 1) + ' · ' : '') + place, url: mapLinks.waze(place) }));
  return [
    { id: 'google', name: 'Google Maps', letter: 'G', color: 'bg-blue-600', note: whole, url: mapLinks.google(stops, origin), more: [] },
    { id: 'apple', name: 'Apple Maps', letter: 'A', color: 'bg-stone-900', note: whole, url: mapLinks.apple(stops, origin), more: [] },
    { id: 'waze', name: 'Waze', letter: 'W', color: 'bg-sky-400',
      note: '→ ' + waze[0].label + (count > 1 || origin ? ' · Waze vie len 1 cieľ z aktuálnej polohy' : ''),
      url: waze[0].url, more: waze.slice(1) },
  ];
}
