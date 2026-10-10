// Client-side router + shared helpers. Plain vanilla JS, no build step.
// All user data is rendered with textContent / input .value (never innerHTML).
//
// URL scheme (all served by the same index.html, see pkg/api/pages.go):
//   /, /transactions            transactions list (browse only)
//   /<res>                      balances / categories list
//   /<res>/new                  add page
//   /<res>/<uid>/edit           edit page (same form as add)
// Navigation between them uses history.pushState, so the browser back and
// forward buttons work and every page can be bookmarked or reloaded.
(function () {
  "use strict";

  const $ = (sel, root = document) => root.querySelector(sel);
  const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

  // ---- Shared DOM helpers ----------------------------------------------------

  function cell(text, cls) {
    const td = document.createElement("td");
    td.textContent = text;
    if (cls) td.className = cls;
    return td;
  }

  function button(text, cls, onClick) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = cls;
    b.textContent = text;
    b.addEventListener("click", onClick);
    return b;
  }

  // An in-app link (router-handled; still a real href for new tabs).
  function link(text, href, cls) {
    const a = document.createElement("a");
    a.href = href;
    a.className = cls || "";
    a.textContent = text;
    return a;
  }

  function clearErrors(form) {
    form.querySelectorAll(".err").forEach((el) => (el.textContent = ""));
    form.querySelectorAll("input, select").forEach((el) => el.classList.remove("invalid"));
  }

  // Field errors from a 422 body next to the matching inputs (by name).
  function showFieldErrors(form, fields) {
    for (const [name, msg] of Object.entries(fields || {})) {
      const errEl = form.querySelector(`.err[data-for="${CSS.escape(name)}"]`);
      if (errEl) errEl.textContent = msg;
      const input = form.querySelector(`[name="${CSS.escape(name)}"]`);
      if (input) input.classList.add("invalid");
    }
  }

  function setStatus(el, text, kind) {
    el.textContent = text;
    el.className = kind || "";
  }

  // Optimistic locking: a record's ETag value for If-Match.
  function ifMatch(version) { return { "If-Match": `"${version}"` }; }

  // Show a version-conflict box (409 version_conflict) with a reload button.
  function showConflict(box, text, onReload) {
    box.querySelector(".conflict-msg").textContent = text;
    box.querySelector(".conflict-reload").onclick = onReload;
    box.hidden = false;
  }
  function hideConflict(box) { box.hidden = true; }

  // ---- Flash messages (shown once on the next list render) -------------------

  let flash = null; // {list, text, uid}

  function setFlash(list, text, uid) { flash = { list, text, uid }; }

  // Show (and consume) the flash for list `name`; returns the uid to highlight.
  function takeFlash(name) {
    const el = $(`#flash-${name}`);
    if (!flash || flash.list !== name) {
      if (el) el.hidden = true;
      return null;
    }
    const f = flash;
    flash = null;
    if (el) { el.textContent = f.text; el.className = "flash"; el.hidden = false; }
    return f.uid || null;
  }

  // ---- Router ----------------------------------------------------------------

  const routes = []; // {re, view, section, title, enter}

  // route("/balances/:uid/edit", {view, section, title, enter(params, query)})
  function route(pattern, opts) {
    const names = [];
    const re = new RegExp("^" + pattern.replace(/:([a-z]+)/g, (_, n) => { names.push(n); return "([^/]+)"; }) + "/?$");
    routes.push({ re, names, ...opts });
  }

  let current = null; // {route, path, url}

  function currentURL() { return location.pathname + location.search; }

  function match(pathname) {
    for (const r of routes) {
      const m = r.re.exec(pathname);
      if (!m) continue;
      const params = {};
      r.names.forEach((n, i) => (params[n] = decodeURIComponent(m[i + 1])));
      if (params.uid !== undefined) {
        params.uid = params.uid.toLowerCase();
        if (!UUID_RE.test(params.uid)) return null;
      }
      return { r, params };
    }
    return null;
  }

  function render() {
    if (!signedIn) return;
    let path = location.pathname;
    // Old hash links (/#balances) from before real paths: map them once.
    const legacy = { "#transactions": "/transactions", "#balances": "/balances", "#categories": "/categories" };
    if (path === "/" && legacy[location.hash]) {
      history.replaceState(history.state, "", legacy[location.hash]);
      path = location.pathname;
    }
    if (path === "/") path = "/transactions";
    const m = match(path);
    document.querySelectorAll(".view").forEach((v) => (v.hidden = true));
    const section = m ? m.r.section : "";
    for (const s of ["transactions", "balances", "categories"]) {
      const tab = $(`#tab-${s}`);
      tab.classList.toggle("active", s === section);
      if (s === section) tab.setAttribute("aria-current", "page"); else tab.removeAttribute("aria-current");
    }
    if (!m) {
      $("#view-notfound").hidden = false;
      document.title = "Not found · Expense Log";
      current = null;
      return;
    }
    $(m.r.view).hidden = false;
    document.title = (m.r.title ? m.r.title + " · " : "") + "Expense Log";
    current = { route: m.r, path, url: currentURL() };
    window.scrollTo(0, 0);
    Promise.resolve(m.r.enter && m.r.enter(m.params, new URLSearchParams(location.search)))
      .catch((err) => console.error(err));
  }

  // Go to an in-app URL, adding a history entry (or replacing the current one).
  // The new entry remembers where we came from so "Save"/"Cancel" can go back.
  function navigate(url, { replace = false } = {}) {
    const state = { from: replace ? (history.state && history.state.from) || null : currentURL() };
    if (replace) history.replaceState(state, "", url);
    else history.pushState(state, "", url);
    render();
  }

  // Leave a form page for its list. When we got here from that list in this
  // tab (the usual case), step back in history so the list keeps its filters
  // and the add/edit page does not linger in the back stack; otherwise (page
  // opened directly) replace this entry with the list.
  function returnToList(listPath) {
    const from = history.state && history.state.from;
    if (from) {
      const fromPath = new URL(from, location.origin).pathname;
      if (fromPath === listPath || (listPath === "/transactions" && fromPath === "/")) {
        history.back();
        return;
      }
    }
    navigate(listPath, { replace: true });
  }

  // Remember the current list URL (incl. filters) without a new history entry.
  function replaceQuery(search) {
    history.replaceState(history.state, "", location.pathname + (search ? "?" + search : ""));
    if (current) current.url = currentURL();
  }

  // Intercept clicks on same-origin links that the router knows.
  document.addEventListener("click", (ev) => {
    if (ev.defaultPrevented || ev.button !== 0 || ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) return;
    const a = ev.target.closest("a[href]");
    if (!a || a.target || a.hasAttribute("download")) return;
    // "← List" on a form page behaves like Cancel (keeps the list's filters).
    if (a.classList.contains("back-link") && current && current.route.onEscape) {
      ev.preventDefault();
      current.route.onEscape();
      return;
    }
    const u = new URL(a.href, location.href);
    if (u.origin !== location.origin || u.pathname.startsWith("/api/") || u.pathname.startsWith("/static/")) return;
    if (u.pathname !== "/" && !match(u.pathname)) return;
    ev.preventDefault();
    if (u.pathname + u.search === currentURL()) { render(); return; }
    navigate(u.pathname + u.search);
  });

  window.addEventListener("popstate", render);

  // Escape on a form page = Cancel (onEscape is the form's cancel handler).
  document.addEventListener("keydown", (ev) => {
    if (ev.key !== "Escape" || !current || !current.route.onEscape) return;
    current.route.onEscape();
  });

  // ---- Currencies (issue #25) -------------------------------------------
  // The list comes from GET /api/currencies (pkg/money is the source of
  // truth). Fetched once; a small built-in list is used if it can't load.
  const FALLBACK_CURRENCIES = ["AUD", "EUR", "GBP", "JPY", "MYR", "SGD", "THB", "USD"].map((code) => ({ code, name: "" }));
  let currenciesP = null;
  function loadCurrencies() {
    if (!currenciesP) {
      currenciesP = fetch("/api/currencies")
        .then((r) => (r.ok ? r.json() : Promise.reject()))
        .then((b) => (b.currencies && b.currencies.length ? b.currencies : FALLBACK_CURRENCIES))
        .catch(() => { currenciesP = null; return FALLBACK_CURRENCIES; });
    }
    return currenciesP;
  }
  // Select `code` in a currency <select>, adding an option if it is unknown
  // (so a stored value is never silently dropped).
  function setCurrency(sel, code) {
    code = (code || "").toUpperCase();
    if (code && ![...sel.options].some((o) => o.value === code)) {
      sel.append(Object.assign(document.createElement("option"), { value: code, textContent: code }));
    }
    sel.value = code;
  }
  // Fill a currency <select> with "THB — Baht" style options (value = code).
  async function fillCurrencySelect(sel) {
    const list = await loadCurrencies();
    const keep = sel.value;
    sel.replaceChildren(Object.assign(document.createElement("option"), { value: "", textContent: "Select a currency…" }));
    for (const c of list) {
      sel.append(Object.assign(document.createElement("option"), { value: c.code, textContent: c.name ? `${c.code} — ${c.name}` : c.code }));
    }
    setCurrency(sel, keep);
  }

  window.App = {
    loadCurrencies, setCurrency, fillCurrencySelect,
    $, cell, button, link, clearErrors, showFieldErrors, setStatus,
    setFlash, takeFlash, route, navigate, returnToList, replaceQuery, ifMatch, showConflict, hideConflict,
    isUUID: (s) => UUID_RE.test(s),
  };

  // ---- Authentication (issue #30) -----------------------------------------
  // The app needs a session. On start (and whenever an API call answers 401)
  // the login view replaces the app; on first run (no account yet) the same
  // form creates the first account. Only the display name / username is shown.

  let signedIn = false;
  let setupMode = false;

  function showApp(user) {
    signedIn = true;
    $("#view-login").hidden = true;
    $("#app-nav").hidden = false;
    $("#user-name").textContent = user.display_name || user.username || "";
    $("#user-bar").hidden = false;
    render();
  }

  function showLogin(setup) {
    signedIn = false;
    setupMode = !!setup;
    document.querySelectorAll(".view").forEach((v) => (v.hidden = true));
    $("#app-nav").hidden = true;
    $("#user-bar").hidden = true;
    $("#user-name").textContent = "";
    $("#login-title").textContent = setup ? "Create your account" : "Log in";
    $("#login-intro").textContent = setup
      ? "No account exists yet. Choose a username (3–32 characters: lowercase letters, digits, . _ -) and a password (at least 8 characters)."
      : "";
    $("#login-display-row").hidden = !setup;
    $("#login-password").autocomplete = setup ? "new-password" : "current-password";
    $("#login-submit").textContent = setup ? "Create account" : "Log in";
    $("#login-error").hidden = true;
    $("#login-password").value = "";
    clearErrors($("#login-form"));
    document.title = (setup ? "Create account" : "Log in") + " · Expense Log";
    $("#view-login").hidden = false;
    $("#login-username").focus();
  }

  // Any API 401 (expired / revoked session) drops back to the login view.
  const origFetch = window.fetch.bind(window);
  window.fetch = async (input, init) => {
    const res = await origFetch(input, init);
    const url = typeof input === "string" ? input : input.url;
    if (res.status === 401 && signedIn && url.startsWith("/api/") && !url.startsWith("/api/auth/")) {
      res.clone().json().then((b) => showLogin(b && b.code === "setup_required"), () => showLogin(false));
    }
    return res;
  };

  $("#login-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const form = ev.target;
    clearErrors(form);
    const err = $("#login-error");
    err.hidden = true;
    const body = { username: $("#login-username").value.trim(), password: $("#login-password").value };
    if (setupMode && $("#login-display").value.trim()) body.display_name = $("#login-display").value.trim();
    const btn = $("#login-submit");
    btn.disabled = true;
    try {
      const res = await origFetch(setupMode ? "/api/auth/register" : "/api/auth/login", {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
      });
      const data = await res.json().catch(() => ({}));
      if (res.ok) {
        $("#login-password").value = "";
        showApp(data);
        return;
      }
      if (res.status === 422) showFieldErrors(form, data.fields);
      if (res.status === 403 && data.code === "registration_closed") { showLogin(false); }
      err.textContent = data.error === "validation failed" ? "Please fix the fields above." : (data.error || "Something went wrong.");
      err.hidden = false;
      $("#login-password").value = "";
      $("#login-password").focus();
    } catch (e) {
      err.textContent = "Network error: " + e.message;
      err.hidden = false;
    } finally {
      btn.disabled = false;
    }
  });

  $("#logout-btn").addEventListener("click", async () => {
    await origFetch("/api/auth/logout", { method: "POST" }).catch(() => {});
    showLogin(false);
  });

  async function boot() {
    try {
      const res = await origFetch("/api/auth/me");
      if (res.ok) { showApp(await res.json()); return; }
      const b = await res.json().catch(() => ({}));
      showLogin(b.code === "setup_required");
    } catch (e) {
      showLogin(false);
    }
  }

  // Start once every script has registered its routes.
  document.addEventListener("DOMContentLoaded", boot);
})();
