// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later
// UCNG – Anmeldung über das Telefonbuch (Modus „phonebook“, siehe phonebook.go).
//
// Authorization Code mit PKCE: Der Browser erzeugt state und verifier (sessionStorage, nur
// dieser Tab), geht zum Telefonbuch und kommt mit #pb_code=…&pb_state=… zurück. Das Fragment
// wird sofort aus der Adresse entfernt (Verlauf, Lesezeichen, Bildschirmfotos) und erreicht nie
// einen Server. Der Code allein nützt nichts: Einlösen geht nur mit dem verifier dieses Tabs.
(() => {
  "use strict";

  const KEY = "ucng:pb";
  const enc = new TextEncoder();
  const ss = {
    get() { try { return JSON.parse(sessionStorage.getItem(KEY)) || {}; } catch { return {}; } },
    set(v) { try { sessionStorage.setItem(KEY, JSON.stringify(v)); } catch { /* egal */ } },
    clear() { try { sessionStorage.removeItem(KEY); } catch { /* egal */ } },
  };
  const b64url = (bytes) => btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  const rand = (n) => b64url(crypto.getRandomValues(new Uint8Array(n)));

  // Rücksprung aus dem Telefonbuch sofort aufnehmen und aus der Adresse entfernen – noch bevor
  // app.js das Fragment als Kanalnamen liest.
  let pending = null;
  if (/^#pb_(code|error|state)=/.test(location.hash)) {
    const q = new URLSearchParams(location.hash.slice(1));
    pending = { code: q.get("pb_code") || "", state: q.get("pb_state") || "", error: q.get("pb_error") || "" };
    history.replaceState(null, "", location.pathname + location.search + (ss.get().room ? "#" + ss.get().room : ""));
  }

  let ctx = null; // { settings, setToken, onSession, toast }
  let gate = null;

  async function begin() {
    if (!(window.crypto && crypto.subtle)) { show("nocrypto"); return; }
    const st = ss.get();
    const now = Date.now();
    const tries = (st.tries || []).filter((t) => now - t < 60000);
    if (tries.length >= 3) { show("loop"); return; } // nicht endlos hin und her leiten
    tries.push(now);
    const state = rand(24), verifier = rand(32);
    const challenge = b64url(new Uint8Array(await crypto.subtle.digest("SHA-256", enc.encode(verifier))));
    const room = decodeURIComponent(location.hash.slice(1) || "");
    ss.set({ state, verifier, tries, room: /^[^#&=]{1,80}$/.test(room) ? room : "" });
    const u = new URL(ctx.settings().phonebook.authorize);
    u.search = new URLSearchParams({ state, challenge, method: "S256" }).toString();
    location.assign(u.toString());
  }

  // Bildschirm statt Chat (kein Zugang, Fehler, abgemeldet)
  function show(kind, detail) {
    const T = (k, v) => _t(k, v);
    const texts = {
      forbidden: [T("acct.pb_kein_zugang_title"), T("acct.pb_kein_zugang_text")],
      insecure: [T("acct.pb_nicht_verfuegbar_title"), T("acct.pb_unsicher_text")],
      unavailable: [T("acct.pb_nicht_verfuegbar_title"), T("acct.pb_nicht_verfuegbar_text")],
      down: [T("acct.pb_nicht_verfuegbar_title"), T("acct.pb_down_text")],
      failed: [T("acct.pb_fehlgeschlagen_title"), detail || T("acct.pb_fehlgeschlagen_text")],
      loop: [T("acct.pb_fehlgeschlagen_title"), T("acct.pb_schleife_text")],
      nocrypto: [T("acct.pb_fehlgeschlagen_title"), T("acct.pb_kein_https_text")],
      signedout: [T("acct.pb_abgemeldet_title"), T("acct.pb_abgemeldet_text")],
    };
    const [title, text] = texts[kind] || texts.failed;
    if (!gate) {
      gate = document.createElement("div");
      gate.className = "pb-gate";
      gate.setAttribute("role", "dialog");
      gate.setAttribute("aria-modal", "true");
      document.body.append(gate);
    }
    const card = document.createElement("div");
    card.className = "pb-card";
    const h = document.createElement("h1");
    h.textContent = title;
    const p = document.createElement("p");
    p.textContent = text;
    const row = document.createElement("div");
    row.className = "pb-actions";
    if (kind !== "forbidden" && kind !== "nocrypto") {
      const again = document.createElement("button");
      again.type = "button";
      again.className = "btn-primary";
      again.textContent = kind === "down" ? T("acct.pb_erneut_versuchen") : T("acct.pb_anmelden_button");
      again.addEventListener("click", () => {
        if (kind === "down") { location.reload(); return; } // Sitzung behalten, nur neu prüfen
        const st = ss.get(); st.tries = []; ss.set(st); begin();
      });
      row.append(again);
    }
    const home = document.createElement("a");
    home.className = "btn-quiet";
    home.href = ctx.settings().phonebook.home;
    home.textContent = T("acct.pb_zum_telefonbuch");
    row.append(home);
    card.append(h, p, row);
    gate.replaceChildren(card);
    gate.hidden = false;
    const b = row.querySelector("button, a");
    if (b) b.focus();
  }

  function hide() { if (gate) gate.hidden = true; }

  // start: nach dem Laden der Einstellungen. true = angemeldet (weiter wie gewohnt).
  async function start(c, loggedIn) {
    ctx = c;
    if (pending) {
      const p = pending;
      pending = null;
      const st = ss.get();
      if (p.error) { ss.clear(); show(p.error); return false; }
      if (!st.state || !st.verifier || p.state !== st.state) {
        // fremder oder veralteter Rücksprung (z. B. untergeschobener Link): nicht einlösen
        ss.set({ tries: st.tries || [] });
        if (loggedIn) return true;
        show("failed", _t("acct.pb_fremder_ruecksprung"));
        return false;
      }
      try {
        const res = await fetch("/api/integration/phonebook/exchange", {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ code: p.code, verifier: st.verifier }),
        });
        const data = await res.json().catch(() => ({}));
        if (!res.ok) { ss.set({ tries: st.tries || [] }); show(res.status === 503 ? "unavailable" : "failed", data.error); return false; }
        ss.set({ tries: [] });
        hide();
        ctx.setToken(data.session.token);
        await ctx.onSession(data.session, data.created);
        return true;
      } catch {
        show("unavailable");
        return false;
      }
    }
    if (loggedIn) { hide(); return true; }
    await begin();
    return false;
  }

  window.UCNGPhonebook = {
    start, begin, show, hide,
    signedOut: () => show("signedout"),
    // Telefonbuch-Ausfall beim Laden (PH-04): Hinweis statt Umleitung, die Sitzung bleibt
    down: (c) => { ctx = c; show("down"); },
    active: () => !!(ctx && ctx.settings().phonebook),
  };
})();
