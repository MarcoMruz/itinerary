import { activityRoute, fmtLeg, mapLinks, navigationApps, qs, totalLegs } from '../domain/routes.mjs';

export function routes() {
  const legsInFlight = new Set();
  return {
    start: '',
    routeStart: '', // Commit on blur/Enter so typing does not trigger geocoding.
    legsByKey: {},
    nav: { title: '', stops: [], origin: '' },
    lastApp: (() => { try { return localStorage.getItem('nav_app') || ''; } catch { return ''; } })(),
    fmtLeg,

    get dayRoute() { return activityRoute(this.day ? this.day.activities : []); },
    get legsKey() {
      return this.current ? JSON.stringify([this.current.id, this.dayIndex, this.routeStart, this.dayRoute.stops]) : '';
    },
    get legs() { return this.legsByKey[this.legsKey]?.legs || []; },
    get startMatch() {
      const entry = this.legsByKey[this.legsKey];
      if (!this.routeStart || !entry) return null;
      return entry.places[0] || (entry.complete ? '' : null);
    },
    legBefore(index) {
      const route = this.dayRoute, stop = route.nums[index];
      if (!route.first[index]) return null;
      const leg = this.legs[this.routeStart ? stop - 1 : stop - 2];
      return leg && leg.meters > 0 ? leg : null;
    },
    get legTotal() { return totalLegs(this.legs); },

    // Keep the key in flight throughout retries so revisiting a day cannot duplicate requests.
    async loadLegs(attempt) {
      const key = this.legsKey, known = this.legsByKey[key];
      if (!key || this.dayRoute.stops.length + (this.routeStart ? 1 : 0) < 2 || (known && known.complete)) return;
      if (attempt === 0 && legsInFlight.has(key)) return;
      legsInFlight.add(key);
      let retryIn = 0;
      try {
        const url = '/api/v1/itineraries/' + encodeURIComponent(this.current.id) + '/days/' + this.dayIndex + '/legs' +
          (this.routeStart ? '?' + qs([['start', this.routeStart]]) : '');
        const response = await fetch(url, { headers: { Accept: 'application/json' } });
        if (response.ok) {
          const body = await response.json();
          this.legsByKey = { ...this.legsByKey, [key]: { legs: body.legs || [], places: body.places || [], complete: !!body.complete } };
          if (!body.complete) retryIn = 3;
        } else if (response.status === 429 || response.status >= 500) {
          retryIn = Number(response.headers.get('Retry-After')) || 5;
        }
      } catch {
        retryIn = 5; // Offline: show the route without distances until a retry succeeds.
      }
      if (!retryIn || attempt >= 4) {
        legsInFlight.delete(key);
        return;
      }
      setTimeout(() => {
        if (this.legsKey === key) this.loadLegs(attempt + 1);
        else legsInFlight.delete(key);
      }, retryIn * 1000);
    },

    get tripUrl() {
      if (!this.current) return '';
      if (this.current.mapsUrl) return this.current.mapsUrl;
      const { stops } = activityRoute(this.current.days.flatMap(day => day.activities));
      return stops.length ? mapLinks.google(stops, this.routeStart) : '';
    },
    get navApps() { return navigationApps(this.nav.stops, this.nav.origin); },
    openNav(title, stops, origin) {
      this.nav = { title, stops: stops.slice(), origin: origin || '' };
      this.$refs.nav.showModal();
    },
    pick(app) {
      this.lastApp = app;
      try { localStorage.setItem('nav_app', app); } catch { /* storage unavailable */ }
      this.$refs.nav.close();
    },

    // A saved empty start deliberately overrides the itinerary default.
    loadStart() {
      let saved = null;
      try { saved = localStorage.getItem('start_' + (this.current ? this.current.id : '')); } catch { /* storage unavailable */ }
      this.start = saved !== null ? saved : ((this.current && this.current.startLocation) || '');
      this.routeStart = this.start.trim();
    },
    saveStart() {
      try { localStorage.setItem('start_' + this.current.id, this.start); } catch { /* storage unavailable */ }
    },
  };
}
