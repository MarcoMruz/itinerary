import { dayDate, describeWeather, fmtDate, fmtNumber, fmtTemp } from '../domain/weather.mjs';

export function weather() {
  const inFlight = new Set();
  return {
    tripStart: '',
    weatherByKey: {},
    describeWeather, fmtDate, fmtNumber, fmtTemp,

    get dayDate() { return this.current ? dayDate(this.current.days, this.dayIndex, this.tripStart) : ''; },
    get weatherKey() {
      return this.current && this.dayDate ? JSON.stringify([this.current.id, this.dayIndex, this.dayDate]) : '';
    },
    // undefined while loading, null after a failure, otherwise the server answer.
    get dayWeather() { return this.weatherByKey[this.weatherKey]; },

    async loadWeather(attempt) {
      const key = this.weatherKey;
      if (!key || this.weatherByKey[key] || (attempt === 0 && inFlight.has(key))) return;
      inFlight.add(key);
      let retryIn = 0;
      try {
        const url = '/api/v1/itineraries/' + encodeURIComponent(this.current.id) + '/days/' + this.dayIndex +
          '/weather?date=' + encodeURIComponent(this.dayDate);
        const response = await fetch(url, { headers: { Accept: 'application/json' } });
        if (response.ok) {
          this.weatherByKey = { ...this.weatherByKey, [key]: await response.json() };
        } else if (response.status === 404) {
          this.weatherByKey = { ...this.weatherByKey, [key]: { status: 'disabled' } };
        } else if (response.status === 429 || response.status >= 500) {
          retryIn = Number(response.headers.get('Retry-After')) || 5;
        } else {
          this.weatherByKey = { ...this.weatherByKey, [key]: null };
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
      if (retryIn && !this.weatherByKey[key]) this.weatherByKey = { ...this.weatherByKey, [key]: null };
      inFlight.delete(key);
    },

    // The trip start chosen in the browser overrides the itinerary's startDate; empty restores it.
    loadTripStart() {
      let saved = null;
      try { saved = localStorage.getItem('start_date_' + (this.current ? this.current.id : '')); } catch { /* storage unavailable */ }
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
