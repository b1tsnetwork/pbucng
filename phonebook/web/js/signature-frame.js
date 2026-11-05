// Rahmen für die Signatur-Vorschau. Das HTML kommt fertig (und maskiert) vom Server; der Rahmen
// zeigt es nur an. Nachrichten werden nur von der eigenen Seite angenommen.
'use strict';
window.addEventListener('message', (e) => {
  if (e.origin !== location.origin || e.source !== window.parent) return;
  const m = e.data || {};
  if (m.type !== 'sig' || typeof m.html !== 'string') return;
  document.getElementById('sig').innerHTML = m.html;
  const h = Math.ceil(document.documentElement.scrollHeight);
  window.parent.postMessage({ type: 'sig-height', height: h }, location.origin);
});
window.parent.postMessage({ type: 'sig-ready' }, location.origin);
