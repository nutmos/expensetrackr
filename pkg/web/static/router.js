// Client-side router + shared helpers. Plain vanilla JS, no build step.
// All user data is rendered with textContent / input .value (never innerHTML).
//
// URL scheme (all served by the same index.html, see pkg/api/pages.go):
//   /, /transactions            transactions list (browse only)
//   /<res>                      balances / categories list
//   /<res>/new                  add page
//   /<res>/<uid>/edit           edit page (same form as add)
//   /reports, /reports/<id>     reports (reports.js)
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
    if (path === "/login" || path === "/register") {
      history.replaceState(null, "", nextParam() || "/transactions");
      path = location.pathname;
    }
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
    for (const s of ["transactions", "balances", "categories", "reports", "settings"]) {
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

  window.addEventListener("popstate", () => { if (signedIn) render(); else showAuth(); });

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
  // Display-only flag emoji per code (the API's "flag"); never submitted.
  const currencyFlags = new Map();
  function loadCurrencies() {
    if (!currenciesP) {
      currenciesP = fetch("/api/currencies")
        .then((r) => (r.ok ? r.json() : Promise.reject()))
        .then((b) => (b.currencies && b.currencies.length ? b.currencies : FALLBACK_CURRENCIES))
        .catch(() => { currenciesP = null; return FALLBACK_CURRENCIES; })
        .then((list) => { for (const c of list) if (c.flag) currencyFlags.set(c.code, c.flag); return list; });
    }
    return currenciesP;
  }
  // "🇹🇭 THB" for display (just "THB" if the flag is unknown or not loaded);
  // a no-break space keeps the flag on the same line as its code.
  function currencyText(code) {
    const f = currencyFlags.get(code);
    return f ? `${f}\u00a0${code}` : code || "";
  }
  // Select `code` in a currency <select>, adding an option if it is unknown
  // (so a stored value is never silently dropped).
  function setCurrency(sel, code) {
    code = (code || "").toUpperCase();
    if (code && ![...sel.options].some((o) => o.value === code)) {
      sel.append(Object.assign(document.createElement("option"), { value: code, textContent: currencyText(code) }));
    }
    sel.value = code;
  }
  // Fill a currency <select> with "🇹🇭 THB · Baht" style options (value =
  // plain code; the flag is only in the label, so search by code/name works).
  async function fillCurrencySelect(sel) {
    const list = await loadCurrencies();
    const keep = sel.value;
    sel.replaceChildren(Object.assign(document.createElement("option"), { value: "", textContent: "Select a currency…" }));
    for (const c of list) {
      sel.append(Object.assign(document.createElement("option"), { value: c.code, textContent: c.name ? `${currencyText(c.code)} · ${c.name}` : currencyText(c.code) }));
    }
    setCurrency(sel, keep);
  }

  // "3361.50" -> "3,361.50" in the viewer's locale (display only; keeps the
  // API's digits and scale). Signs (-, +, −) are kept. Empty stays empty.
  function fmtAmount(v) {
    const s = String(v ?? "").trim();
    if (!s) return s;
    const m = /^([+\-\u2212]?)(\d+(?:\.\d+)?)$/.exec(s);
    if (!m) return s;
    const scale = (m[2].split(".")[1] || "").length;
    const opts = { minimumFractionDigits: scale, maximumFractionDigits: scale };
    try { return m[1] + new Intl.NumberFormat(undefined, opts).format(m[2]); } catch (_) { return s; }
  }

  // "06 Oct 2026 21:06" (DD MMM YYYY HH:mm) in the device's time zone, for any
  // value Date accepts (RFC 3339 string, Date). The raw value if unparsable
  // (issue #163: one friendly date-time format across the web UI).
  const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
  function fmtDateTime(v) {
    if (v === null || v === undefined || v === "") return "";
    const d = v instanceof Date ? v : new Date(v);
    if (isNaN(d)) return String(v);
    const p2 = (n) => String(n).padStart(2, "0");
    return `${p2(d.getDate())} ${MONTHS[d.getMonth()]} ${d.getFullYear()} ${p2(d.getHours())}:${p2(d.getMinutes())}`;
  }

  // Copies each column header's text onto the row's cells as data-label, so
  // the narrow-screen card layout (.stack-table in style.css) can show
  // "Label  value" lines (issue #171). Call after the rows are rendered.
  function labelCells(table) {
    const head = [...table.querySelectorAll("thead th")].map((th) => th.textContent.trim());
    for (const tr of table.querySelectorAll("tbody tr, tfoot tr")) {
      [...tr.children].forEach((td, i) => { if (head[i]) td.dataset.label = head[i]; });
    }
  }

  // setDateRange adds a whole-day date filter (inclusive YYYY-MM-DD bounds,
  // either may be empty) to params, read in this device's time zone: the
  // dates go as is with tz=<IANA zone>. If the browser can't name its zone,
  // the bounds are sent as RFC 3339 instants with the device's offset.
  function setDateRange(params, from, to) {
    let tz = "";
    try { tz = Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch (_) { /* old browser */ }
    if (tz) {
      if (from) params.set("from", from);
      if (to) params.set("to", to);
      if (from || to) params.set("tz", tz);
      return;
    }
    const p2 = (n) => String(n).padStart(2, "0");
    const stamp = (s, h, mi, sec) => {
      const [y, m, d] = s.split("-").map(Number);
      const t = new Date(y, m - 1, d, h, mi, sec);
      const off = -t.getTimezoneOffset();
      return `${s}T${p2(h)}:${p2(mi)}:${p2(sec)}${off >= 0 ? "+" : "-"}${p2(Math.floor(Math.abs(off) / 60))}:${p2(Math.abs(off) % 60)}`;
    };
    if (from) params.set("from", stamp(from, 0, 0, 0));
    if (to) params.set("to", stamp(to, 23, 59, 59));
  }

  window.App = {
    setDateRange, labelCells, loadCurrencies, currencyText, setCurrency, fillCurrencySelect, fmtAmount, fmtDateTime,
    $, cell, button, link, clearErrors, showFieldErrors, setStatus,
    setFlash, takeFlash, route, navigate, returnToList, replaceQuery, ifMatch, showConflict, hideConflict,
    isUUID: (s) => UUID_RE.test(s),
  };

  // ---- Authentication (issues #30, #32) -----------------------------------
  // Signed-out visitors always get the Log in page (/login). A separate
  // Create account page (/register) is reachable from it only while
  // registration is open, i.e. on first run when no account exists yet (the
  // server answers GET /api/auth/me with 401 code setup_required). Creating
  // the account does not sign in: the user then logs in on /login. Deep links
  // go to /login?next=<original URL> and return there after logging in.
  // Only the display name / username is shown, never uids.

  let signedIn = false;
  let setupMode = false;        // current page is Create account
  let registrationOpen = false; // from setup_required

  // Only same-site app paths are valid return targets.
  function safeNext(n) {
    if (!n || n[0] !== "/" || n.startsWith("//") || n.startsWith("/\\")) return "";
    const p = new URL(n, location.origin).pathname;
    if (p === "/login" || p === "/register" || p.startsWith("/api/") || p.startsWith("/static/")) return "";
    return n;
  }
  function nextParam() { return safeNext(new URLSearchParams(location.search).get("next")); }
  function authURL(path, next) { return path + (next ? "?next=" + encodeURIComponent(next) : ""); }

  function showApp(user) {
    signedIn = true;
    $("#view-login").hidden = true;
    $("#app-nav").hidden = false;
    $("#user-name").textContent = user.display_name || user.username || "";
    $("#user-bar").hidden = false;
    const p = location.pathname;
    if (p === "/login" || p === "/register") history.replaceState(null, "", nextParam() || "/transactions");
    render();
  }

  // Show the signed-out page for the current URL. Anything other than
  // /login or /register becomes /login?next=<that URL>.
  function showAuth(notice) {
    signedIn = false;
    let p = location.pathname;
    if (p !== "/login" && p !== "/register") {
      const next = safeNext(currentURL());
      history.replaceState(null, "", authURL("/login", next && next !== "/" ? next : ""));
      p = "/login";
    }
    if (p === "/register" && !registrationOpen) {
      history.replaceState(null, "", authURL("/login", nextParam()));
      p = "/login";
      notice = notice || "Registration is closed. Ask an existing user to create an account for you.";
    }
    const setup = p === "/register";
    setupMode = setup;
    document.querySelectorAll(".view").forEach((v) => (v.hidden = true));
    $("#app-nav").hidden = true;
    $("#user-bar").hidden = true;
    $("#user-name").textContent = "";
    $("#login-title").textContent = setup ? "Create account" : "Log in";
    // Rules only matter when choosing credentials (#150).
    $("#username-info").hidden = !setup;
    setUsernameHint(false);
    $("#password-rules").hidden = !setup;
    const nt = $("#login-notice");
    nt.textContent = notice || "";
    nt.hidden = !notice;
    $("#login-display-row").hidden = !setup;
    $("#login-password").autocomplete = setup ? "new-password" : "current-password";
    $("#login-submit").textContent = setup ? "Create account" : "Log in";
    // Switch button: login -> Create account only while registration is open;
    // register -> back to Log in always.
    const sw = $("#auth-switch"), swb = $("#auth-switch-btn");
    if (setup) {
      $("#auth-switch-hint").textContent = "Already have an account?";
      swb.textContent = "Back to log in";
      swb.href = authURL("/login", nextParam());
      sw.hidden = false;
    } else {
      $("#auth-switch-hint").textContent = "No account yet — create one to get started.";
      swb.textContent = "Create account";
      swb.href = authURL("/register", nextParam());
      sw.hidden = !registrationOpen;
    }
    $("#login-error").hidden = true;
    $("#login-password").value = "";
    clearErrors($("#login-form"));
    updatePasswordRules(false);
    document.title = (setup ? "Create account" : "Log in") + " · Expense Log";
    $("#view-login").hidden = false;
    $("#login-username").focus();
  }

  // ---- Create account: username (i) hint and password checklist (#150) ----
  // Mirrors pkg/auth.CheckPasswordRules: 8..72 bytes (bcrypt's limit), so the
  // length is measured in UTF-8 bytes like the server does.
  const PASSWORD_RULES = {
    min: (n) => n >= 8,
    max: (n) => n <= 72,
  };
  const utf8 = new TextEncoder();

  function setUsernameHint(open) {
    $("#username-info").setAttribute("aria-expanded", open ? "true" : "false");
    $("#username-hint").hidden = !open;
  }
  $("#username-info").addEventListener("click", () => {
    setUsernameHint($("#username-hint").hidden);
  });

  // Marks each rule met / not met for the current password and returns whether
  // all are met. While the box is empty the rules stay neutral; an unmet rule
  // only turns red once it can no longer be fixed by typing more (max), or
  // when strict (on submit).
  function updatePasswordRules(strict) {
    const pw = $("#login-password").value;
    const n = utf8.encode(pw).length;
    let ok = true;
    $("#password-rules").querySelectorAll("li[data-rule]").forEach((li) => {
      const met = PASSWORD_RULES[li.dataset.rule](n);
      ok = ok && met;
      const bad = !met && (strict || (pw !== "" && li.dataset.rule === "max"));
      li.classList.toggle("met", pw !== "" && met);
      li.classList.toggle("unmet", bad);
    });
    return ok;
  }
  $("#login-password").addEventListener("input", () => {
    if (setupMode) updatePasswordRules(false);
  });

  function showLogin(setup) { registrationOpen = !!setup; showAuth(); }

  // Switching between Log in and Create account (no full reload).
  $("#auth-switch-btn").addEventListener("click", (ev) => {
    if (ev.button !== 0 || ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) return;
    ev.preventDefault();
    ev.stopPropagation();
    history.pushState(null, "", ev.currentTarget.getAttribute("href"));
    showAuth();
  });

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
    if (setupMode && !updatePasswordRules(true)) {
      $("#login-password").classList.add("invalid");
      $("#login-password").focus();
      return;
    }
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
        if (setupMode) {
          // Creating an account does not log in (#165): go to Log in with the
          // new username filled in, and let the user sign in themselves.
          // Registration is closed now that an account exists.
          registrationOpen = false;
          history.replaceState(null, "", authURL("/login", nextParam()));
          showAuth("Account created. Log in with your new username and password.");
          $("#login-username").value = data.username || body.username;
          $("#login-display").value = "";
          $("#login-password").focus();
          return;
        }
        showApp(data);
        return;
      }
      if (res.status === 422) showFieldErrors(form, data.fields);
      if (res.status === 403 && data.code === "registration_closed") {
        registrationOpen = false;
        history.replaceState(null, "", authURL("/login", nextParam()));
        showAuth("Registration is closed. Ask an existing user to create an account for you.");
        return;
      }
      err.textContent = data.error === "validation failed" ? "Please fix the fields above." : (data.error || "Something went wrong.");
      err.hidden = false;
      $("#login-password").value = "";
      updatePasswordRules(false);
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
    registrationOpen = false;
    history.pushState(null, "", "/login");
    showAuth();
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
