import { dayDate, describeWeather, fmtDate, fmtNumber, fmtTemp, isPastDate, localDate } from '../domain/weather.mjs';

export function weather() {
  const inFlight = new Set();
  return {
    tripStart: '',
    weatherByKey: {},
    describeWeather, fmtDate, fmtNumber, fmtTemp,

    get dayDate() { return this.current ? dayDate(this.current.days, this.dayIndex, this.tripStart) : ''; },
    get isPastDay() { return isPastDate(this.dayDate); },
    get weatherPlace() { return this.day?.activities.find(activity => activity.place)?.place || this.current?.startLocation || ''; },
    get weatherKey() {
      return this.current && this.dayDate && !this.isPastDay ? JSON.stringify([this.current.id, this.dayIndex, this.dayDate, this.weatherPlace]) : '';
    },
    // undefined while loading, null after a failure, otherwise the server answer.
    get dayWeather() { return this.weatherByKey[this.weatherKey]?.data; },
    get showWeather() {
      const status = this.dayWeather && this.dayWeather.status;
      return !!this.day && !this.isPastDay && status !== 'disabled' && status !== 'past';
    },
    get today() { return localDate(); },

    // The browser HTTP cache answers repeats until the server's Cache-Control max-age runs out.
    async loadWeather(attempt) {
      const key = this.weatherKey;
      if (!key || (attempt === 0 && inFlight.has(key))) return;
      inFlight.add(key);
      let retryIn = 0;
      try {
        // place is unused by the server; it only makes an edited stop miss the browser cache.
        const url = '/api/v1/itineraries/' + encodeURIComponent(this.current.id) + '/days/' + this.dayIndex +
          '/weather?date=' + encodeURIComponent(this.dayDate) + '&place=' + encodeURIComponent(this.weatherPlace);
        const response = await fetch(url, { headers: { Accept: 'application/json' } });
        if (response.ok) {
          const data = await response.json();
          this.weatherByKey = { ...this.weatherByKey, [key]: { data } };
        } else if (response.status === 429 || response.status >= 500) {
          retryIn = Number(response.headers.get('Retry-After')) || 5;
        } else {
          this.weatherByKey = { ...this.weatherByKey, [key]: { data: null } };
        }
      } catch {
        retryIn = 5;
      }
      if (retryIn && attempt < 3) {
        setTimeout(() => {
          if (this.weatherKey === key) this.loadWeather(attempt + 1);
          else inFlight.delete(key);
        }, retryIn * 1000);
        return;
      }
      if (retryIn) this.weatherByKey = { ...this.weatherByKey, [key]: { data: null } };
      inFlight.delete(key);
    },

    // The trip start chosen in the browser overrides the itinerary's startDate; empty restores it.
    // A saved start whose trip is over is ignored, so the visitor can pick a new one.
    loadTripStart() {
      let saved = null;
      try { saved = localStorage.getItem('start_date_' + (this.current ? this.current.id : '')); } catch { /* storage unavailable */ }
      const days = this.current ? this.current.days : [];
      if (saved && isPastDate(dayDate(days, days.length - 1, saved))) saved = null;
      this.tripStart = saved || (this.current && this.current.startDate) || '';
    },
    saveTripStart(value) {
      this.tripStart = value || (this.current && this.current.startDate) || '';
      try {
        if (value) localStorage.setItem('start_date_' + this.current.id, value);
        else localStorage.removeItem('start_date_' + this.current.id);
      } catch { /* storage unavailable */ }
    },
  };
}
