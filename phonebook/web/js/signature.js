// E-Mail-Signatur: Formular aus dem eigenen Telefonbucheintrag (mit eigenen Ergänzungen) und
// Verwaltung der Grunddaten. Das HTML der Signatur erzeugt der Server (signature.go); die
// Vorschau läuft in einem eigenen Rahmen, weil die Signatur style-Attribute für Outlook braucht.
import { h, api, toast } from './util.js';
import { openDialog, confirmDialog, field } from './dialog.js';

const ICON_COPY = '⧉';

// ---------- Signatur der angemeldeten Person ----------

export async function signatureDialog() {
  let st;
  try { st = await api('GET', '/api/signature'); } catch (e) { toast(e.message, 'err'); return; }
  const cfg = st.config, base = st.base || {};
  const input = { ...(st.input || {}) };
  let saved = JSON.stringify(input);

  // Feld aus dem Eintrag: zeigt die eigene Angabe oder den Wert aus dem Telefonbuch
  const fromEntry = new Map(); // key → {el, hint}
  const entryField = (key, label, attrs = {}, hintText = '') => {
    const el = h('input', { ...attrs, value: key in input ? input[key] : (base[key] || '') });
    const hint = h('small', { class: 'sig-src' });
    const reset = h('button', { type: 'button', class: 'linkish', title: 'Wieder den Wert aus dem Telefonbuch verwenden' }, '↺ aus dem Telefonbuch');
    reset.addEventListener('click', () => { delete input[key]; el.value = base[key] || ''; refresh(); changed(); });
    fromEntry.set(key, { el, hint, reset });
    el.addEventListener('input', () => {
      const v = el.value.trim();
      if (v === (base[key] || '').trim()) delete input[key]; else input[key] = v;
      refresh(); changed();
    });
    return h('label', { class: 'field' }, h('span', null, label), el, h('div', { class: 'sig-hintline' }, hint, reset), hintText ? h('small', null, hintText) : null);
  };
  const ownField = (key, label, attrs = {}, hintText = '') => {
    const el = h('input', { ...attrs, value: input[key] || '' });
    el.addEventListener('input', () => { const v = el.value.trim(); if (v) input[key] = v; else delete input[key]; changed(); });
    return h('label', { class: 'field' }, h('span', null, label), el, h('div', { class: 'sig-hintline' }), hintText ? h('small', null, hintText) : null);
  };
  const refresh = () => {
    for (const [key, f] of fromEntry) {
      const own = key in input;
      const hasBase = !!(base[key] || '');
      f.hint.textContent = own ? (hasBase ? 'eigene Angabe – im Telefonbuch: ' + base[key] : 'eigene Angabe') : (hasBase ? 'aus dem Telefonbuch' : (st.hasEntry ? 'im Telefonbuch leer' : ''));
      f.hint.classList.toggle('own', own);
      f.reset.hidden = !own || !st.hasEntry;
    }
    for (const s of [amtSel, sgSel, siteSel]) {
      const key = s.dataset.key;
      const own = key in input;
      s.closest('.field').querySelector('.sig-src').textContent = own ? 'eigene Auswahl' : (key === 'site' ? 'aus der Zuordnung der Einheit' : (st.hasEntry && base[key] ? 'aus dem Organigramm' : ''));
      s.closest('.field').querySelector('.linkish').hidden = !own;
    }
  };

  // ---- Organisation (aus dem Organigramm)
  const opt = (v, t) => h('option', { value: v }, t);
  const sel = (key, label, build, onPick) => {
    const s = h('select', { 'data-key': key });
    const reset = h('button', { type: 'button', class: 'linkish', title: 'Wieder aus dem Telefonbuch übernehmen' }, '↺ zurücksetzen');
    reset.addEventListener('click', () => { delete input[key]; if (key === 'amt') delete input.sg; fill(); refresh(); changed(); });
    s.addEventListener('change', () => {
      const v = s.value;
      if (v === (base[key] || '')) delete input[key]; else input[key] = v;
      if (onPick) onPick(v);
      refresh(); changed();
    });
    return { s, el: h('label', { class: 'field' }, h('span', null, label), s, h('div', { class: 'sig-hintline' }, h('small', { class: 'sig-src' }), reset)), build };
  };
  const cur = (key) => (key in input ? input[key] : (base[key] || ''));
  const amt = sel('amt', st.amtLabel || 'Amt', () => {
    const groups = new Map();
    for (const a of st.amts) { if (!groups.has(a.group)) groups.set(a.group, []); groups.get(a.group).push(a); }
    const kids = [];
    for (const [g, list] of groups) {
      const o = list.map((a) => opt(a.id, a.name));
      kids.push(g ? h('optgroup', { label: g }, o) : o);
    }
    return [opt('', '– keine Angabe –'), ...kids];
  }, () => {
    // anderes Amt: Sachgebiet aus dem Eintrag passt nicht mehr
    if (input.amt !== undefined && cur('sg') && !st.sgs.some((x) => x.id === cur('sg') && x.amt === cur('amt'))) input.sg = '';
    if (input.amt === undefined && input.sg === '') delete input.sg;
    fillSG();
  });
  const sg = sel('sg', st.sgLabel || 'Sachgebiet', () => [opt('', '– keine Angabe –'), ...st.sgs.filter((x) => x.amt === cur('amt')).map((x) => opt(x.id, x.name))]);
  const siteGroups = () => {
    const groups = new Map();
    for (const s of cfg.sites) { if (!groups.has(s.group)) groups.set(s.group, []); groups.get(s.group).push(s); }
    const out = [];
    for (const [g, list] of groups) {
      const o = list.map((s) => opt(s.id, [s.extra, s.street, `${s.zip} ${s.city}`.trim()].filter(Boolean).join(', ')));
      out.push(g ? h('optgroup', { label: g }, o) : o);
    }
    out.push(h('optgroup', { label: 'Nicht in der Liste' }, opt('frei', 'Andere Adresse eingeben …')));
    return out;
  };
  const site = sel('site', 'Dienstgebäude', siteGroups, () => { freeBox.hidden = cur('site') !== 'frei'; });
  const amtSel = amt.s, sgSel = sg.s, siteSel = site.s;
  const fillSG = () => {
    sgSel.replaceChildren(...sg.build());
    sgSel.value = cur('sg');
    if (sgSel.value !== cur('sg')) sgSel.value = '';
    sg.el.hidden = !st.sgLabel || sgSel.options.length <= 1;
  };
  const fill = () => {
    amtSel.replaceChildren(...amt.build()); amtSel.value = cur('amt');
    siteSel.replaceChildren(...site.build()); siteSel.value = cur('site') || (cfg.sites[0] && cfg.sites[0].id) || 'frei';
    fillSG();
    freeBox.hidden = cur('site') !== 'frei';
  };
  const freeBox = h('div', { class: 'form-grid full sig-free' },
    h('div', { class: 'full' }, ownField('freeExtra', 'Gebäude oder Einrichtung (optional)', { placeholder: 'z. B. Stadtarchiv' })),
    h('div', { class: 'full' }, ownField('freeStreet', 'Straße und Hausnummer', { placeholder: 'z. B. Am Rathaus 1' })),
    ownField('freeZip', 'PLZ', { inputmode: 'numeric', maxlength: 5 }),
    ownField('freeCity', 'Ort', { placeholder: 'Ort' }));

  const tel = 'Durchwahl genügt (z. B. ' + (cfg.exchange ? cfg.exchange + '-1104' : '1104') + '). Die Schreibweise setzt der Generator nach Styleguide.';
  const form = h('div', { class: 'sig-form' },
    cfg.note ? h('p', { class: 'hint sig-note' }, cfg.note) : null,
    st.hasEntry ? h('p', { class: 'hint' }, 'Vorausgefüllt aus deinem Telefonbucheintrag. Was du hier änderst, gilt nur für die Signatur – Fehler im Eintrag bitte über „Fehler melden“ an der Visitenkarte.')
      : h('p', { class: 'hint warn' }, 'Für dein Konto gibt es keinen Telefonbucheintrag – bitte alles selbst ausfüllen.'),
    h('h3', null, 'Person'),
    h('div', { class: 'form-grid' },
      entryField('firstName', 'Vorname', { autocomplete: 'given-name' }),
      entryField('lastName', 'Nachname', { autocomplete: 'family-name' }),
      h('div', { class: 'full' }, ownField('position', 'Position (optional)', { placeholder: 'z. B. Sachbearbeitung Veranstaltungen' }))),
    st.amtLabel ? h('h3', null, 'Organisation') : null,
    st.amtLabel ? h('div', { class: 'form-grid' }, h('div', { class: 'full' }, amt.el), h('div', { class: 'full' }, sg.el)) : null,
    h('h3', null, 'Kontakt'),
    h('div', { class: 'form-grid' },
      entryField('phone', 'Telefon', { inputmode: 'tel' }),
      ownField('fax', 'Fax (optional)', { inputmode: 'tel', placeholder: cfg.exchange ? cfg.exchange + '-601' : '' }),
      h('p', { class: 'hint full' }, tel),
      h('div', { class: 'full' }, entryField('mobile', 'Mobil (optional)', { inputmode: 'tel' })),
      h('div', { class: 'full' }, entryField('email', 'E-Mail-Adresse', { type: 'email', spellcheck: 'false' }, cfg.mailDomain ? 'Leer = aus dem Namen gebildet (vorname.nachname@' + cfg.mailDomain + '). Für ein Funktionspostfach einfach überschreiben.' : ''))),
    h('h3', null, 'Standort'),
    h('div', { class: 'form-grid' },
      h('div', { class: 'full' }, site.el),
      freeBox,
      entryField('room', 'Zimmer (optional)')));

  // ---- Vorschau und Kopieren
  const frame = h('iframe', { class: 'sig-frame', src: '/signature-frame.html', title: 'Vorschau der Signatur', referrerpolicy: 'same-origin' });
  const copyBtn = h('button', { type: 'button', class: 'primary sig-copy' }, ICON_COPY + ' Signatur kopieren');
  const status = h('p', { class: 'sig-status', role: 'status', 'aria-live': 'polite' });
  const mailWarn = h('p', { class: 'hint warn', hidden: true });
  const preview = h('div', { class: 'sig-stage' },
    h('div', { class: 'sig-sheet' }, h('div', { class: 'sig-mailtext', 'aria-hidden': 'true' }, h('i'), h('i'), h('i')), frame),
    h('div', { class: 'sig-actions' }, copyBtn, status),
    mailWarn,
    h('details', { class: 'sig-help' },
      h('summary', null, 'So kommt die Signatur in Outlook'),
      h('div', { class: 'sig-cols' },
        h('div', null, h('h4', null, 'Outlook (klassisch, Windows)'), h('ol', null,
          h('li', null, 'Datei → Optionen → E-Mail → Signaturen …'),
          h('li', null, 'Neu wählen und einen Namen vergeben, z. B. „' + cfg.org + '“.'),
          h('li', null, 'In das große Textfeld klicken und Strg + V drücken.'),
          h('li', null, 'Rechts die Standardsignatur für neue Nachrichten und Antworten setzen, dann OK.'))),
        h('div', null, h('h4', null, 'Neues Outlook und Outlook im Web'), h('ol', null,
          h('li', null, 'Zahnrad (Einstellungen) → Konten → Signaturen'),
          h('li', null, 'Neue Signatur anlegen und benennen.'),
          h('li', null, 'In das Textfeld klicken und Strg + V drücken.'),
          h('li', null, 'Standardsignaturen festlegen, dann Speichern.')))),
      h('h4', null, 'Logo fehlt nach dem Einfügen?'),
      h('p', null, 'Ältere Outlook-Versionen übernehmen eingebettete Bilder nicht immer. Dann das Logo als Datei speichern, in der Signatur ganz oben klicken und es über das Bildsymbol einfügen. Es hat schon die richtige Größe.'),
      h('a', { class: 'button-link', href: '/api/signature/logo.png?download=1', download: 'Signatur-Logo.png' }, '⬇ Logo als Datei speichern')),
    cfg.stand ? h('p', { class: 'muted sig-stand' }, cfg.stand) : null);

  // Vorschau-Rahmen: Höhe folgt dem Inhalt
  let frameReady = false, lastPreview = '';
  const onMsg = (e) => {
    if (e.origin !== location.origin || e.source !== frame.contentWindow) return;
    const m = e.data || {};
    if (m.type === 'sig-ready') { frameReady = true; if (lastPreview) post(lastPreview); }
    if (m.type === 'sig-height') frame.style.height = Math.min(Math.max(m.height, 120), 900) + 'px';
  };
  window.addEventListener('message', onMsg);
  const post = (html) => { lastPreview = html; if (frameReady) frame.contentWindow.postMessage({ type: 'sig', html }, location.origin); };

  // Server rendert; immer nur die neueste Antwort zählt
  let seq = 0, last = null, pending = null, timer = 0;
  const render = () => {
    const my = ++seq;
    const body = { input: { ...input }, preview: true };
    pending = api('POST', '/api/signature/render', body).then((r) => {
      if (my !== seq) return last;
      last = r;
      post(r.previewHtml || r.html);
      const em = cur('email');
      mailWarn.hidden = true;
      if (em && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(em)) { mailWarn.hidden = false; mailWarn.textContent = 'Die E-Mail-Adresse sieht noch nicht vollständig aus.'; }
      else if (em && cfg.mailDomain && !em.toLowerCase().endsWith('@' + cfg.mailDomain)) { mailWarn.hidden = false; mailWarn.textContent = 'Die Adresse endet nicht auf @' + cfg.mailDomain + ' – bitte prüfen.'; }
      return r;
    }).catch((e) => { status.textContent = e.message; status.classList.add('err'); return null; })
      .finally(() => { if (my === seq) pending = null; });
    return pending;
  };
  const changed = () => {
    status.textContent = ''; status.classList.remove('err');
    clearTimeout(timer); timer = setTimeout(() => { timer = 0; render(); }, 180);
  };

  const save = async (quiet) => {
    const now = JSON.stringify(input);
    if (now === saved) return true;
    try { await api('PUT', '/api/signature', { input }); saved = now; if (!quiet) toast('Angaben gespeichert', 'ok'); return true; }
    catch (e) { if (!quiet) toast(e.message, 'err'); return false; }
  };

  const copy = async () => {
    let r = last;
    if (timer) { clearTimeout(timer); timer = 0; r = await render(); } // Eingabe noch nicht gerendert
    else if (pending) r = await pending;
    else if (!r) r = await render();
    if (!r) return;
    if (r.missing && r.missing.length) {
      status.textContent = 'Bitte noch ausfüllen: ' + r.missing.join(', ') + '.'; status.classList.add('err');
      return;
    }
    let ok = false, set = false;
    const onCopy = (e) => { e.clipboardData.setData('text/html', r.html); e.clipboardData.setData('text/plain', r.text); e.preventDefault(); set = true; };
    document.addEventListener('copy', onCopy);
    try { ok = document.execCommand('copy') && set; } catch { /* Rückfall unten */ }
    document.removeEventListener('copy', onCopy);
    if (!ok && navigator.clipboard && window.ClipboardItem) {
      try {
        await navigator.clipboard.write([new ClipboardItem({ 'text/html': new Blob([r.html], { type: 'text/html' }), 'text/plain': new Blob([r.text], { type: 'text/plain' }) })]);
        ok = true;
      } catch { /* bleibt false */ }
    }
    if (ok) {
      copyBtn.textContent = '✓ Kopiert'; copyBtn.classList.add('ok');
      status.textContent = 'Jetzt in Outlook in das Signaturfeld klicken und Strg + V drücken.'; status.classList.remove('err');
      setTimeout(() => { copyBtn.textContent = ICON_COPY + ' Signatur kopieren'; copyBtn.classList.remove('ok'); }, 2400);
      save(true); // was kopiert wurde, steht beim nächsten Mal wieder im Formular
    } else {
      status.textContent = 'Kopieren hat nicht geklappt. Bitte die Vorschau markieren und mit Strg + C kopieren.'; status.classList.add('err');
    }
  };
  copyBtn.addEventListener('click', copy);

  fill(); refresh(); render();
  openDialog({
    title: '✉ E-Mail-Signatur', cls: 'sig-dlg', wide: true,
    body: h('div', { class: 'sig-layout' }, form, preview),
    onClose: () => window.removeEventListener('message', onMsg),
    buttons: [
      { label: 'Alles aus dem Telefonbuch', title: 'Eigene Angaben verwerfen', onClick: async () => {
        if (!Object.keys(input).length) return false;
        if (!(await confirmDialog('Zurücksetzen', 'Eigene Angaben für die Signatur verwerfen? Es gelten wieder die Werte aus dem Telefonbuch.', 'Verwerfen', true))) { signatureDialog(); return false; }
        try { await api('PUT', '/api/signature', { input: {} }); toast('Zurückgesetzt', 'ok'); } catch (e) { toast(e.message, 'err'); }
        signatureDialog(); return false;
      } },
      'spacer', { label: 'Schließen' },
      { label: 'Angaben speichern', cls: 'primary', onClick: async () => { await save(false); return false; } },
    ],
  });
}

// ---------- Verwaltung der Grunddaten ----------

const sid = () => 's' + Array.from(crypto.getRandomValues(new Uint8Array(4)), (b) => b.toString(16).padStart(2, '0')).join('');

export async function signatureAdmin() {
  let st;
  try { st = await api('GET', '/api/admin/signature'); } catch (e) { toast(e.message, 'err'); return; }
  const c = st.config;
  const inp = (v, attrs = {}) => h('input', { value: v ?? '', ...attrs });
  const enabled = h('input', { type: 'checkbox', checked: c.enabled || null });
  const org = inp(c.org), post = inp(c.postAddress, { placeholder: 'Postfach 10 20 30 . 12340 Musterstadt' });
  const area = inp(c.areaCode, { inputmode: 'numeric', placeholder: '01234' }), exch = inp(c.exchange, { inputmode: 'numeric', placeholder: '56' });
  const domain = inp(c.mailDomain, { placeholder: 'musterstadt.de' }), stand = inp(c.stand, { placeholder: 'z. B. Stand Januar 2027' });
  const note = h('textarea', { rows: 2, placeholder: 'Hinweis über dem Formular (optional)' }, c.note || '');
  const lvOpts = (cur) => [h('option', { value: 0 }, '– nicht anzeigen –'), ...st.levels.map((l, i) => h('option', { value: i + 1, selected: cur === i + 1 || null }, (i + 1) + '. ' + l.name))];
  const amtLv = h('select', null, lvOpts(c.amtLevel)), sgLv = h('select', null, lvOpts(c.sgLevel));
  const font = inp(c.font), size = inp(c.fontSize, { placeholder: '11pt' }), lh = inp(c.lineHeight, { placeholder: '14pt' });
  const color = inp(c.color, { type: 'color' });
  const lw = inp(c.logoWidth, { type: 'number', min: 20, max: 400 }), lhgt = inp(c.logoHeight, { type: 'number', min: 10, max: 200 });
  const alt = inp(c.logoAlt);

  // Logo
  const logoImg = h('img', { class: 'sig-logo-prev', alt: 'Logo', src: '/api/signature/logo.png?t=' + Date.now() });
  const logoInfo = h('small', { class: 'muted' });
  const showLogo = (info) => {
    const has = info.bytes > 0;
    logoImg.hidden = !has;
    logoInfo.textContent = !has ? 'kein Logo' : (info.custom ? 'hochgeladenes Logo' : 'Logo aus der Datei') + (info.width ? ` · ${info.width} × ${info.height} px` : '') + ` · ${Math.round(info.bytes / 1024 * 10) / 10} KB`;
    if (has) logoImg.src = '/api/signature/logo.png?t=' + Date.now();
    logoReset.hidden = !info.custom;
  };
  const file = h('input', { type: 'file', accept: 'image/png', hidden: true });
  file.addEventListener('change', async () => {
    const f = file.files[0]; file.value = '';
    if (!f) return;
    const b64 = await new Promise((res) => { const r = new FileReader(); r.onload = () => res(String(r.result).split(',')[1] || ''); r.readAsDataURL(f); });
    try { const r = await api('PUT', '/api/admin/signature/logo', { png: b64 }); showLogo({ custom: true, ...r }); toast('Logo gespeichert', 'ok'); }
    catch (e) { toast(e.message, 'err'); }
  });
  const logoReset = h('button', { type: 'button', title: 'Hochgeladenes Logo entfernen (dann gilt das Logo aus der Datei, falls vorhanden)' }, 'Hochgeladenes Logo entfernen');
  logoReset.addEventListener('click', async () => {
    try { await api('DELETE', '/api/admin/signature/logo'); const r = await api('GET', '/api/admin/signature'); showLogo(r.logo); toast('Logo entfernt', 'ok'); } catch (e) { toast(e.message, 'err'); }
  });

  showLogo(st.logo);

  // Links
  const linkRows = h('div', { class: 'sig-rows' });
  const addLink = (l = { text: '', url: '' }) => {
    const row = h('div', { class: 'sig-row link' }, inp(l.text, { placeholder: 'www.musterstadt.de', 'data-k': 'text' }), inp(l.url, { placeholder: 'https://www.musterstadt.de', 'data-k': 'url' }),
      h('button', { type: 'button', class: 'icon', title: 'Entfernen', onclick: () => row.remove() }, '✕'));
    linkRows.append(row);
  };
  (c.links || []).forEach(addLink);

  // Dienstgebäude
  const siteBody = h('tbody');
  const addSite = (s = { id: sid(), group: '', extra: '', street: '', zip: '', city: '' }) => {
    const radio = h('input', { type: 'radio', name: 'sig-default', value: s.id, checked: s.id === c.defaultSite || null, title: 'Standard für Einheiten ohne Zuordnung' });
    const tr = h('tr', { 'data-id': s.id },
      h('td', null, inp(s.group, { 'data-k': 'group', placeholder: 'Gruppe' })),
      h('td', null, inp(s.extra, { 'data-k': 'extra', placeholder: 'Gebäude, Etage' })),
      h('td', null, inp(s.street, { 'data-k': 'street', placeholder: 'Straße Nr.' })),
      h('td', null, inp(s.zip, { 'data-k': 'zip', placeholder: 'PLZ', inputmode: 'numeric', class: 'zip' })),
      h('td', null, inp(s.city, { 'data-k': 'city', placeholder: 'Ort' })),
      h('td', { class: 'num' }, radio),
      h('td', null, h('button', { type: 'button', class: 'icon', title: 'Entfernen', onclick: () => { tr.remove(); syncSiteSelects(); } }, '✕')));
    tr.addEventListener('change', syncSiteSelects);
    siteBody.append(tr);
  };
  const sites = () => [...siteBody.rows].map((tr) => {
    const o = { id: tr.dataset.id };
    tr.querySelectorAll('input[data-k]').forEach((i) => { o[i.dataset.k] = i.value.trim(); });
    return o;
  });
  const siteLabel = (s) => [s.extra, s.street, `${s.zip} ${s.city}`.trim()].filter(Boolean).join(', ') || '(ohne Adresse)';

  // Dienstgebäude je Einheit (erbt nach unten)
  const unitSites = { ...(c.unitSites || {}) };
  const unitBody = h('tbody');
  const unitSels = [];
  const byId = new Map(st.units.map((u) => [u.id, u]));
  // Zuordnung der nächsten Einheit darüber, sonst der Standard
  const inherited = (u) => {
    for (let p = byId.get(u.parent), n = 0; p && n < 64; p = byId.get(p.parent), n++) if (unitSites[p.id]) return unitSites[p.id];
    return defaultSite();
  };
  const defaultSite = () => (siteBody.querySelector('input[name=sig-default]:checked') || {}).value || (sites()[0] || {}).id || '';
  const syncSiteSelects = () => {
    const list = sites();
    for (const { s, u } of unitSels) {
      const v = unitSites[u.id] || '';
      const inh = list.find((x) => x.id === inherited(u));
      s.replaceChildren(h('option', { value: '' }, '– erbt: ' + (inh ? siteLabel(inh) : 'keins') + ' –'), ...list.map((x) => h('option', { value: x.id }, siteLabel(x))));
      s.value = list.some((x) => x.id === v) ? v : '';
    }
  };
  const maxDepth = Math.max(c.amtLevel || 0, c.sgLevel || 0, 1);
  for (const u of st.units.filter((x) => x.depth <= maxDepth)) {
    const s = h('select');
    s.addEventListener('change', () => { if (s.value) unitSites[u.id] = s.value; else delete unitSites[u.id]; syncSiteSelects(); });
    unitSels.push({ s, u });
    unitBody.append(h('tr', null, h('td', { style: { paddingLeft: (8 + (u.depth - 1) * 18) + 'px' } }, (u.short ? u.short + ' ' : '') + u.name), h('td', null, s)));
  }
  (c.sites || []).forEach(addSite);
  syncSiteSelects();

  const SRC = { admin: 'in dieser Verwaltung gespeichert (gilt vor der Datei)', file: 'aus der Datei ' + (st.file || ''), default: 'neutrale Vorgaben – keine Datei eingerichtet (signature.file bzw. TB_SIGNATURE_FILE)' };
  const body = h('div', { class: 'sig-admin' },
    h('p', { class: 'hint' }, 'Grunddaten: ' + (SRC[st.source] || st.source)),
    h('label', { class: 'check' }, enabled, ' Signatur-Generator im Benutzermenü anbieten'),
    h('h3', null, 'Grunddaten'),
    h('div', { class: 'form-grid' },
      field('Organisation (fett über der Adresse)', org), field('Postanschrift (letzte Adresszeile)', post),
      field('Ortsvorwahl', area, 'Nummern mit dieser Vorwahl werden nach Styleguide gesetzt.'), field('Anlagen-Rufnummer', exch, 'z. B. 56 → 01234 56-1104'),
      field('E-Mail-Domain', domain, 'Für den Vorschlag aus dem Namen und den Hinweis bei fremden Adressen.'), field('Stand (Fußzeile)', stand),
      h('div', { class: 'full' }, field('Hinweis', note))),
    h('h3', null, 'Organigramm'),
    h('p', { class: 'hint' }, 'Amt und Sachgebiet kommen aus der Einheit des Eintrags: Angezeigt wird die Einheit auf der gewählten Ebene über (oder gleich) der eigenen Einheit.'),
    h('div', { class: 'form-grid' }, field('Ebene „Amt“', amtLv), field('Ebene „Sachgebiet“', sgLv)),
    h('h3', null, 'Darstellung'),
    h('div', { class: 'form-grid' },
      h('div', { class: 'full' }, field('Schrift', font, 'Korrespondenzschrift laut Styleguide, z. B. Arial,Helvetica,sans-serif')),
      field('Schriftgröße', size), field('Zeilenabstand', lh), field('Schriftfarbe', color), field('Alternativtext des Logos', alt),
      h('div', { class: 'full sig-logo-box' }, logoImg, h('div', null, logoInfo, h('div', { class: 'toolbar' },
        h('button', { type: 'button', onclick: () => file.click() }, '⬆ Logo hochladen (PNG)'), logoReset, file),
        h('small', { class: 'muted' }, 'PNG in doppelter Anzeigegröße (z. B. 310 × 90 für 155 × 45), RGB, höchstens 40 KB – das Logo geht in jeder Mail mit.'))),
      field('Anzeigebreite (px)', lw), field('Anzeigehöhe (px)', lhgt)),
    h('h3', null, 'Links unter der Adresse'),
    linkRows,
    h('button', { type: 'button', onclick: () => addLink() }, '+ Link'),
    h('h3', null, 'Dienstgebäude'),
    h('div', { class: 'table-wrap' }, h('table', { class: 'tbl sig-sites' },
      h('thead', null, h('tr', null, h('th', null, 'Gruppe'), h('th', null, 'Gebäude/Etage'), h('th', null, 'Straße'), h('th', null, 'PLZ'), h('th', null, 'Ort'), h('th', null, 'Standard'), h('th'))),
      siteBody)),
    h('button', { type: 'button', onclick: () => { addSite(); syncSiteSelects(); } }, '+ Dienstgebäude'),
    h('h3', null, 'Dienstgebäude je Einheit'),
    h('p', { class: 'hint' }, 'Vorbelegung im Formular. Gilt für die Einheit und alles darunter, solange dort nichts anderes steht.'),
    unitSels.length ? h('div', { class: 'table-wrap' }, h('table', { class: 'tbl' }, h('thead', null, h('tr', null, h('th', null, 'Einheit'), h('th', null, 'Dienstgebäude'))), unitBody))
      : h('p', { class: 'muted' }, 'Noch keine Einheiten im Organigramm.'));

  const save = async () => {
    const cfg = {
      enabled: enabled.checked, org: org.value, postAddress: post.value, areaCode: area.value, exchange: exch.value, mailDomain: domain.value,
      stand: stand.value, note: note.value, amtLevel: +amtLv.value, sgLevel: +sgLv.value,
      font: font.value, fontSize: size.value, lineHeight: lh.value, color: color.value.toUpperCase(), logoWidth: +lw.value, logoHeight: +lhgt.value, logoAlt: alt.value,
      links: [...linkRows.children].map((r) => ({ text: r.querySelector('[data-k=text]').value, url: r.querySelector('[data-k=url]').value })),
      sites: sites(), defaultSite: defaultSite(), unitSites,
    };
    try { await api('PUT', '/api/admin/signature', cfg); toast('Gespeichert', 'ok'); }
    catch (e) { toast(e.message, 'err'); return false; }
  };
  openDialog({
    title: '✉ Signatur verwalten', body, wide: true,
    buttons: [...(st.source === 'admin' && st.file ? [{ label: 'Auf Datei zurücksetzen', onClick: async () => {
      if (!(await confirmDialog('Zurücksetzen', 'Die hier gespeicherten Grunddaten verwerfen? Es gilt wieder die Datei ' + st.file + '.', 'Zurücksetzen', true))) { signatureAdmin(); return false; }
      try { await api('DELETE', '/api/admin/signature'); toast('Zurückgesetzt', 'ok'); } catch (e) { toast(e.message, 'err'); }
      signatureAdmin(); return false;
    } }] : []), { label: 'Vorschau öffnen', onClick: async () => { if ((await save()) === false) return false; signatureDialog(); return false; } }, 'spacer', { label: 'Schließen' }, { label: 'Speichern', cls: 'primary', onClick: save }],
  });
}
