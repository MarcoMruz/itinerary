import { chatMessage, fieldLabels, previewStats } from '../domain/import-export.mjs';

export function importExport() {
  return {
    io: { request: '', text: '', token: '', needToken: false, busy: false, error: '', preview: null, copyMessage: '' },
    fieldLabels,

    get exportUrl() { return this.current ? '/api/v1/itineraries/' + encodeURIComponent(this.current.id) + '/export' : ''; },
    get previewStats() { return previewStats(this.io.preview && this.io.preview.itinerary); },
    openIO() {
      this.io.error = '';
      this.io.copyMessage = '';
      this.io.preview = null;
      this.$refs.io.showModal();
    },
    async copyForChat(url, action) {
      try {
        const response = await fetch(url, { headers: { Accept: 'application/json' } });
        if (!response.ok) throw new Error();
        await navigator.clipboard.writeText(chatMessage(this.io.request, action, await response.text()));
        this.io.copyMessage = 'Skopírované. Vložte to do chatu s asistentom ako jednu správu.';
      } catch {
        this.io.copyMessage = 'Kopírovanie zlyhalo, použite stiahnutie.';
      }
    },
    async readImportFile(event) {
      const file = event.target.files[0];
      if (!file) return;
      this.io.text = await file.text();
      this.io.error = '';
      event.target.value = '';
    },
    async importRequest(dryRun) {
      const headers = { 'Content-Type': 'application/json', Accept: 'application/json' };
      if (this.io.token.trim()) headers.Authorization = 'Bearer ' + this.io.token.trim();
      this.io.busy = true;
      this.io.error = '';
      try {
        const response = await fetch('/api/v1/import' + (dryRun ? '?dryRun=true' : ''), { method: 'POST', headers, body: this.io.text });
        const body = await response.json().catch(() => ({}));
        if (response.status === 401) {
          this.io.error = this.io.needToken && this.io.token ? 'Token nemá právo zápisu.' : 'Na import zadajte API token s právom zápisu.';
          this.io.needToken = true;
          return null;
        }
        if (!response.ok) throw new Error(body.error || 'Chyba servera (' + response.status + ').');
        return body;
      } catch (error) {
        this.io.error = 'Súbor sa nedá importovať: ' + (error.message || 'skúste znova.');
        return null;
      } finally { this.io.busy = false; }
    },
    async checkImport() { this.io.preview = await this.importRequest(true); },
    async saveImport() {
      const result = await this.importRequest(false);
      if (!result) return;
      this.io.text = '';
      this.io.preview = null;
      this.$refs.io.close();
      await this.refreshList();
      await this.select(result.id);
      this.notice = result.action === 'update' ? 'Itinerár „' + result.itinerary.title + '“ bol upravený.' : 'Itinerár „' + result.itinerary.title + '“ bol pridaný.';
    },
  };
}
