export function sharing() {
  return {
    sharing: { url: '', busy: false, message: '', failed: false, revoked: false },
    canNativeShare: typeof navigator !== 'undefined' && !!navigator.share,

    shareLinkUrl() {
      return '/api/v1/itineraries/' + encodeURIComponent(this.current.id) + '/share-link';
    },
    openShare() {
      this.sharing = { url: '', busy: false, message: '', failed: false, revoked: false };
      this.$refs.share.showModal();
      this.loadShareLink();
    },
    async loadShareLink() {
      const id = this.current.id;
      this.sharing.busy = true;
      this.sharing.message = '';
      this.sharing.failed = false;
      try {
        const response = await fetch(this.shareLinkUrl(), { method: 'POST', headers: { Accept: 'application/json' } });
        if (response.status === 401) throw new Error('Na zdieľanie sa prihláste (alebo použite MCP nástroj get_share_link).');
        if (!response.ok) throw new Error('Odkaz sa nepodarilo vytvoriť. Skúste znova.');
        const body = await response.json();
        if (this.current.id === id) { this.sharing.url = body.url; this.sharing.revoked = false; }
      } catch (error) {
        this.sharing.failed = true;
        this.sharing.message = error.message;
      } finally { this.sharing.busy = false; }
    },
    async copyShare() {
      try {
        await navigator.clipboard.writeText(this.sharing.url);
        this.sharing.failed = false;
        this.sharing.message = 'Odkaz je skopírovaný.';
      } catch {
        this.sharing.message = 'Skopírujte odkaz z poľa vyššie.';
      }
    },
    async nativeShare() {
      try { await navigator.share({ title: this.current.title, url: this.sharing.url }); } catch { /* closed by the user */ }
    },
    async revokeShare() {
      if (!window.confirm('Zrušiť odkaz? Prestane fungovať aj u tých, ktorým ste ho poslali.')) return;
      try {
        const response = await fetch(this.shareLinkUrl(), { method: 'DELETE' });
        if (!response.ok) throw new Error();
        this.sharing = { url: '', busy: false, message: 'Odkaz je zrušený a už nefunguje.', failed: false, revoked: true };
      } catch {
        this.sharing.failed = true;
        this.sharing.message = 'Odkaz sa nepodarilo zrušiť. Skúste znova.';
      }
    },
  };
}
