export function checklistProgress(items, checked) {
  const done = items.filter(item => checked.includes(item)).length;
  return { done, total: items.length, pct: items.length ? Math.round(done * 100 / items.length) : 0 };
}

export function checklistChanges(draft, checked) {
  const checklist = draft.map(entry => entry.text.trim());
  return {
    checklist,
    valid: checklist.every(Boolean),
    checked: draft.filter(entry => entry.original !== null && checked.includes(entry.original)).map(entry => entry.text.trim()),
  };
}
