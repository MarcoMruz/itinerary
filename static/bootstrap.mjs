import Alpine from 'https://cdn.jsdelivr.net/npm/alpinejs@3.14.8/dist/module.esm.js';
import { itineraryApp } from './app.mjs';

// Google sign-in redirects omit the private checklist link's fragment.
try {
  const hash = sessionStorage.getItem('itinerary_hash');
  sessionStorage.removeItem('itinerary_hash');
  if (hash && !location.hash) history.replaceState(null, '', hash);
} catch { /* storage unavailable */ }

Alpine.data('itineraryApp', () => itineraryApp(JSON.parse(document.getElementById('boot').textContent) || {}));
Alpine.start();
