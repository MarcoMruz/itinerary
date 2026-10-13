// WMO weather interpretation codes as used by Open-Meteo.
const conditions = [
  [[0], '☀️', 'Jasno'],
  [[1], '🌤️', 'Prevažne jasno'],
  [[2], '⛅', 'Polojasno'],
  [[3], '☁️', 'Zamračené'],
  [[45, 48], '🌫️', 'Hmla'],
  [[51, 53, 55], '🌦️', 'Mrholenie'],
  [[56, 57], '🌧️', 'Mrznúce mrholenie'],
  [[61], '🌧️', 'Slabý dážď'],
  [[63], '🌧️', 'Dážď'],
  [[65], '🌧️', 'Silný dážď'],
  [[66, 67], '🌧️', 'Mrznúci dážď'],
  [[71], '🌨️', 'Slabé sneženie'],
  [[73], '🌨️', 'Sneženie'],
  [[75, 77], '❄️', 'Silné sneženie'],
  [[80, 81], '🌦️', 'Prehánky'],
  [[82], '⛈️', 'Silné prehánky'],
  [[85, 86], '🌨️', 'Snehové prehánky'],
  [[95], '⛈️', 'Búrka'],
  [[96, 99], '⛈️', 'Búrka s krupobitím'],
];

export function describeWeather(code) {
  const match = conditions.find(([codes]) => codes.includes(code));
  return match ? { icon: match[1], label: match[2] } : { icon: '🌡️', label: 'Počasie' };
}

const isoDate = /^\d{4}-\d{2}-\d{2}$/;

// Match Itinerary.DayDate: the day's own date, else the trip start + index.
export function dayDate(days, index, tripStart) {
  const day = days[index];
  if (!day) return '';
  if (day.date) return day.date;
  if (!isoDate.test(tripStart || '')) return '';
  const date = new Date(tripStart + 'T00:00:00Z');
  if (Number.isNaN(date.getTime())) return '';
  date.setUTCDate(date.getUTCDate() + index);
  return date.toISOString().slice(0, 10);
}

const weekdays = ['ne', 'po', 'ut', 'st', 'št', 'pi', 'so'];

export function fmtDate(iso) {
  if (!isoDate.test(iso || '')) return '';
  const date = new Date(iso + 'T00:00:00Z');
  return weekdays[date.getUTCDay()] + ' ' + date.getUTCDate() + '. ' + (date.getUTCMonth() + 1) + '.';
}

export function fmtTemp(value) {
  return value == null ? '–' : Math.round(value) + ' °C';
}

export function fmtNumber(value) {
  return String(Math.round(value * 10) / 10).replace('.', ',');
}
