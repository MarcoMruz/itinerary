import { checklist } from './features/checklist.mjs';
import { importExport } from './features/import-export.mjs';
import { insights } from './features/insights.mjs';
import { routes } from './features/routes.mjs';
import { sharing } from './features/sharing.mjs';
import { parseHash, viewHash } from './domain/insights.mjs';

export function itineraryApp(boot = {}) {
  const app = {
    list: boot.list || [],
    current: boot.active || null,
    selectedId: boot.active ? boot.active.id : '',
    dayIndex: 0,
    loading: false,
    error: '',
    notice: '',
    shared: !!boot.shared,
    signIn: !!boot.signIn,

    init() {
      this.loadChecked();
      this.loadStart();
      this.openFromHash();
      this.checkChecklistAccess();
      this.$watch('legsKey', () => this.loadLegs(0));
      this.loadLegs(0);
      window.addEventListener('hashchange', () => this.openFromHash());
      window.addEventListener('beforeunload', event => {
        if (this.editingChecklist) { event.preventDefault(); event.returnValue = ''; }
      });
      document.addEventListener('visibilitychange', () => {
        if (document.visibilityState === 'visible') this.refreshList();
      });
    },
    get day() { return this.current ? this.current.days[this.dayIndex] : null; },
    openFromHash() {
      const { id, key, view } = parseHash(location.hash);
      if (key) {
        history.replaceState(null, '', view ? viewHash(view) : '#' + encodeURIComponent(id));
        this.activateOwner(id, key);
      }
      if (id && id !== this.selectedId) this.select(id, view);
      else this.showView(view);
    },
    async select(id, view = null) {
      if (!id) return;
      if (this.savingChecklist || (this.editingChecklist && !window.confirm('Zahodiť neuložené zmeny?'))) {
        this.selectedId = this.current.id;
        history.replaceState(null, '', '#' + encodeURIComponent(this.current.id));
        return;
      }
      this.resetChecklist();
      this.notice = '';
      this.selectedId = id;
      this.error = '';
      this.showView(null);
      if (!view && decodeURIComponent(location.hash.slice(1)) !== id) history.replaceState(null, '', '#' + encodeURIComponent(id));
      this.loading = true;
      let itinerary;
      try {
        const response = await fetch('/api/v1/itineraries/' + encodeURIComponent(id), { headers: { Accept: 'application/json' } });
        if (!response.ok) throw new Error(response.status === 404 ? 'Itinerár neexistuje.' : 'Chyba servera (' + response.status + ').');
        itinerary = await response.json();
      } catch (error) {
        if (this.selectedId === id) this.error = error.message || 'Nepodarilo sa načítať itinerár.';
        return;
      } finally {
        if (this.selectedId === id) this.loading = false;
      }
      if (this.selectedId !== id) return;
      this.current = itinerary;
      this.checkChecklistAccess();
      this.dayIndex = 0;
      this.showView(view);
      this.loadChecked();
      this.loadStart();
    },
    startSignIn() {
      // The login page drops the URL, so hand the itinerary over to the signed-in page.
      try { sessionStorage.setItem('itinerary_hash', '#' + encodeURIComponent(this.current.id)); } catch { /* storage unavailable */ }
      location.assign('/auth/login');
    },
    async refreshList() {
      try {
        const response = await fetch('/api/v1/itineraries', { headers: { Accept: 'application/json' } });
        if (response.ok) this.list = await response.json();
      } catch { /* offline: keep current list */ }
    },
  };
  // Preserve reactive getters; object spread would evaluate them before Alpine binds the app.
  return Object.defineProperties(app, Object.assign({}, ...[checklist(), routes(), sharing(), importExport(), insights()].map(Object.getOwnPropertyDescriptors)));
}
