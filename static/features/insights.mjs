import { hostname, insightPage, viewHash } from '../domain/insights.mjs';

export function insights() {
  return {
    view: null,
    pagesOpened: 0, // "Back" uses history only for pages the app itself pushed.
    returnScroll: 0,
    hostname,

    get page() { return insightPage(this.current, this.view); },
    get hasGuide() { return !!(this.current && this.current.localGuide); },
    openGuide() { this.openView({ id: this.current.id, type: 'guide' }); },
    openPlace(activity) { this.openView({ id: this.current.id, type: 'place', day: this.dayIndex, activity }); },
    openView(view) {
      if (location.hash === viewHash(view)) return;
      this.returnScroll = window.scrollY || 0;
      this.pagesOpened += 1;
      location.hash = viewHash(view);
    },
    // Stale or mistyped links fall back to the program.
    showView(requested) {
      const view = requested && insightPage(this.current, requested) ? requested : null;
      if (requested && !view && this.current) history.replaceState(null, '', '#' + encodeURIComponent(this.current.id));
      if (view && view.type === 'place') this.dayIndex = view.day;
      const leaving = this.view && !view;
      this.view = view;
      if (view) {
        window.scrollTo(0, 0);
        return;
      }
      this.pagesOpened = 0;
      if (leaving) this.$nextTick(() => window.scrollTo(0, this.returnScroll));
    },
    closePage() {
      if (this.pagesOpened > 0) {
        this.pagesOpened -= 1;
        history.back();
        return;
      }
      history.replaceState(null, '', '#' + encodeURIComponent(this.current.id));
      this.showView(null);
    },
  };
}
