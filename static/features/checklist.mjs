import { checklistChanges, checklistProgress } from '../domain/checklist.mjs';

export function checklist() {
  return {
    checked: [],
    canEdit: false,
    sharingChecklist: false,
    shareUrl: '',
    shareMessage: '',
    editingChecklist: false,
    savingChecklist: false,
    checklistDraft: [],
    checklistMessage: '',
    checklistFailed: false,
    draftSequence: 0,

    get progress() { return checklistProgress(this.current ? this.current.checklist : [], this.checked); },
    resetChecklist() {
      this.canEdit = false;
      this.shareUrl = '';
      this.shareMessage = '';
      this.editingChecklist = false;
      this.checklistMessage = '';
    },
    async activateOwner(id, key) {
      try {
        const response = await fetch('/api/v1/itineraries/' + encodeURIComponent(id) + '/owner-session', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ key }) });
        if (!response.ok) throw new Error('Odkaz na úpravu je neplatný.');
        if (this.current && this.current.id === id) this.canEdit = true;
      } catch (error) { this.error = error.message || 'Nepodarilo sa povoliť úpravy. Otvorte odkaz znova.'; }
    },
    async checkChecklistAccess() {
      if (!this.current) return;
      const id = this.current.id;
      try {
        const response = await fetch('/api/v1/itineraries/' + encodeURIComponent(id) + '/checklist-access');
        if (!response.ok) return;
        const access = await response.json();
        if (this.current.id === id && access.canEdit) this.canEdit = true;
      } catch { /* read-only until access can be verified */ }
    },
    beginChecklistEdit() {
      this.checklistDraft = this.current.checklist.map((text, index) => ({ id: index, text, original: text }));
      this.draftSequence = this.checklistDraft.length;
      this.checklistMessage = '';
      this.editingChecklist = true;
    },
    async shareChecklist() {
      if (this.sharingChecklist || !this.canEdit) return;
      const id = this.current.id;
      this.sharingChecklist = true;
      this.shareMessage = '';
      this.shareUrl = '';
      try {
        const response = await fetch('/api/v1/itineraries/' + encodeURIComponent(id) + '/checklist-share');
        if (!response.ok) throw new Error(response.status === 401 ? 'Otvorte súkromný odkaz na úpravu znova.' : 'Odkaz sa nepodarilo načítať. Skúste znova.');
        const body = await response.json();
        if (this.current.id !== id) return;
        this.shareUrl = body.url;
        try {
          await navigator.clipboard.writeText(body.url);
          if (this.current.id === id) this.shareMessage = 'Odkaz je skopírovaný.';
        } catch {
          if (this.current.id === id) this.shareMessage = 'Skopírujte odkaz z poľa vyššie.';
        }
      } catch (error) {
        if (this.current.id === id) this.shareMessage = error.message || 'Odkaz sa nepodarilo načítať. Skúste znova.';
      } finally { this.sharingChecklist = false; }
    },
    changeChecklistItem(id, text) {
      this.checklistDraft = this.checklistDraft.map(entry => entry.id === id ? { ...entry, text } : entry);
    },
    addChecklistItem() {
      this.checklistDraft = this.checklistDraft.concat({ id: this.draftSequence++, text: '', original: null });
    },
    async saveChecklist() {
      if (this.savingChecklist) return;
      const id = this.current.id;
      const draft = this.checklistDraft;
      const changes = checklistChanges(draft, this.checked);
      this.checklistFailed = false;
      this.checklistMessage = '';
      if (!changes.valid) {
        this.checklistFailed = true;
        this.checklistMessage = 'Vyplňte alebo odstráňte prázdne položky.';
        return;
      }
      this.savingChecklist = true;
      try {
        const response = await fetch('/api/v1/itineraries/' + encodeURIComponent(id) + '/checklist', { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ checklist: changes.checklist }) });
        if (!response.ok) throw new Error(response.status === 401 ? 'Otvorte súkromný odkaz na úpravu znova.' : 'Zmeny sa nepodarilo uložiť. Skúste znova.');
        const updated = await response.json();
        if (this.current.id !== id) return;
        this.checked = checklistChanges(draft, this.checked).checked;
        this.current = updated;
        this.save();
        this.editingChecklist = false;
        this.checklistMessage = 'Zmeny sú uložené.';
      } catch (error) {
        if (this.current.id === id) { this.checklistFailed = true; this.checklistMessage = error.message || 'Zmeny sa nepodarilo uložiť.'; }
      } finally { this.savingChecklist = false; }
    },
    storageKey() { return 'checklist_' + (this.current ? this.current.id : ''); },
    loadChecked() {
      try { this.checked = JSON.parse(localStorage.getItem(this.storageKey())) || []; }
      catch { this.checked = []; }
    },
    save() {
      try { localStorage.setItem(this.storageKey(), JSON.stringify(this.checked)); } catch { /* storage unavailable */ }
    },
    toggle(item) {
      this.checked = this.checked.includes(item) ? this.checked.filter(value => value !== item) : this.checked.concat(item);
      this.save();
    },
  };
}
