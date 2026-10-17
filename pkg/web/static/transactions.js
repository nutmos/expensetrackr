// Transactions: list page (/transactions) and add/edit page
// (/transactions/new, /transactions/<uid>/edit). Plain vanilla JS, no build step.
// All user data is rendered with textContent / input .value (never innerHTML).
(function () {
  "use strict";

  const { $, cell, button, link } = App;
  const form = $("#transaction-form");
  const spentAtInput = $("#spent_at");
  const statusEl = $("#form-status");
  const tbody = $("#transaction-table tbody");
  const listStatus = $("#list-status");
  const totalsEl = $("#totals");
  const LIST = "/transactions";
  const LS_KEY = "expense-log:last";
  // Form state: null on the add page, else the transaction being edited.
  let editing = null;
  // All balances (for the source/destination dropdowns), from GET /api/balances.
  let allBalances = [];
  // All categories (dropdown + list names), from GET /api/categories.
  let allCategories = [];
  // Cross-currency transfer state: fxManual is true once the user typed the
  // received amount (then it is sent as to_amount); otherwise the server
  // converts with the Frankfurter rate and the field shows a preview.
  let fxManual = false;
  let fxGen = 0;
  let fxTimer = null;
  // Last market rate loaded for the form ({from, to, date, rate, day}); lets
  // an amount change re-convert at once instead of waiting for a lookup.
  let fxQuote = null;
  // Bumped on every page entry so a slow response for a page we already
  // left is ignored.
  let gen = 0;

  const pad = (n, w = 2) => String(Math.abs(n)).padStart(w, "0");

  // "YYYY-MM-DDTHH:MM:SS" in the browser's local time (value for datetime-local).
  function toLocalInputValue(d) {
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` +
      `T${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  }

  // "+08:00" style offset for the given instant in the browser's time zone
  // (computed per date, so daylight-saving changes are handled).
  function offsetString(d) {
    const mins = -d.getTimezoneOffset();
    const sign = mins >= 0 ? "+" : "-";
    return `${sign}${pad(Math.floor(Math.abs(mins) / 60))}:${pad(Math.abs(mins) % 60)}`;
  }

  // Full RFC 3339 timestamp in the browser's zone, e.g. 2026-10-06T21:06:00+08:00.
  function toRFC3339(d) {
    return toLocalInputValue(d) + offsetString(d);
  }

  // Parse a datetime-local value ("YYYY-MM-DDTHH:MM[:SS]") as browser-local time.
  function parseLocalInput(value) {
    const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?/.exec(value);
    if (!m) return null;
    return new Date(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +(m[6] || 0));
  }

  // The RFC 3339 value the form would submit: the entered wall-clock time in
  // the device's time zone, with the device's offset for that date (DST-aware).
  function composedSpentAt() {
    const d = parseLocalInput(spentAtInput.value);
    return d ? toRFC3339(d) : null;
  }

  function setNow() {
    spentAtInput.value = toLocalInputValue(new Date());
    updatePreview();
  }

  function updatePreview() {
    const v = composedSpentAt();
    let text = v ? "Will be saved as " + shownTime(v) : "Enter a date and time";
    if (v && editing && v !== editing.spent_at) text += ` (was ${shownTime(editing.spent_at)})`;
    $("#spent-at-preview").textContent = text;
  }

  // "YYYY-MM-DD HH:MM:SS" in the device's time zone (raw value if unparsable).
  function shownTime(rfc3339) {
    const d = new Date(rfc3339);
    return isNaN(d) ? rfc3339 : toLocalInputValue(d).replace("T", " ");
  }

  const setStatus = (text, kind) => App.setStatus(statusEl, text, kind);
  const conflictBox = $("#conflict");

  function rememberDefaults(currency, balanceUID) {
    try { localStorage.setItem(LS_KEY, JSON.stringify({ currency, balance_uid: balanceUID })); } catch (_) {}
  }

  function restoreDefaults() {
    try {
      const v = JSON.parse(localStorage.getItem(LS_KEY) || "{}");
      if (v.currency) App.setCurrency($("#currency"), v.currency);
      if (v.balance_uid) $("#balance_uid").value = v.balance_uid;
    } catch (_) {}
    // Default the currency to the selected account's currency.
    currencyFromBalance();
  }

  // Which balance types may be the source (balance_uid) for each type.
  const SOURCE_TYPES = {
    expense: ["payment_account", "credit_card"],
    income: ["payment_account", "other_asset"],
    transfer: ["payment_account", "credit_card", "other_asset", "other_liability"],
    asset_purchase: ["payment_account", "credit_card"],
  };
  // Which balance types may be the destination (to_balance_uid).
  const DEST_TYPES = {
    transfer: SOURCE_TYPES.transfer,
    asset_purchase: ["other_asset"],
  };
  const DEST_HINT = {
    transfer: "Any balance (e.g. pay a credit card, save to an asset, repay a loan).",
    asset_purchase: "Other assets only (the asset you bought).",
  };
  // Category type used by each transaction type (asset purchases use expense categories).
  const CATEGORY_TYPE = { expense: "expense", income: "income", asset_purchase: "expense" };
  const SOURCE_LABEL = { expense: "Payment account", income: "Received into", transfer: "From (source)", asset_purchase: "Paid from" };
  const SOURCE_HINT = {
    expense: "Payment accounts and credit cards.",
    income: "Payment accounts and other assets.",
    transfer: "Any balance.",
    asset_purchase: "Payment accounts and credit cards.",
  };
  // Display names of transaction types (the API uses the lowercase values).
  const TYPE_LABEL = { expense: "Expense", income: "Income", transfer: "Transfer", asset_purchase: "Asset Purchase", balance_adjustment: "Balance Adjustment" };
  const LIABILITY = new Set(["credit_card", "other_liability"]);
  // Minor-unit digits per currency code, from GET /api/currencies (scale).
  let currencyScale = new Map();

  // Amount shown in a balance option (issue #28): assets show their balance,
  // liabilities show what is owed as a negative amount.
  function balanceAmountText(b) {
    let v = String((LIABILITY.has(b.type) ? b.debt : b.balance) ?? "0").trim() || "0";
    if (LIABILITY.has(b.type) && !/^-?0*\.?0*$/.test(v)) v = v.startsWith("-") ? v.slice(1) : "-" + v;
    const scale = currencyScale.get(b.currency);
    const opts = { style: "currency", currency: b.currency };
    if (Number.isInteger(scale)) { opts.minimumFractionDigits = scale; opts.maximumFractionDigits = scale; }
    try { return new Intl.NumberFormat(undefined, opts).format(v); } catch (_) { return v; }
  }
  // "Wallet · $50.00": name and current amount only (no uid, code or kind).
  function balanceLabel(b) { return `${b.name} · ${balanceAmountText(b)}`; }

  function fillSelect(sel, types, keep) {
    sel.replaceChildren(Object.assign(document.createElement("option"), { value: "", textContent: "Select a balance…" }));
    for (const b of allBalances) {
      if (!types.includes(b.type)) continue;
      const opt = document.createElement("option");
      opt.value = b.uid;
      opt.textContent = balanceLabel(b);
      sel.append(opt);
    }
    if (keep && [...sel.options].some((o) => o.value === keep)) sel.value = keep;
  }

  function currentType() { return $("#type").value || "expense"; }

  // ---- Cross-currency transfers (docs/exchange-rates.md) --------------------

  const balanceByUID = (uid) => allBalances.find((x) => x.uid === uid);
  // Decimal string -> BigInt minor units at scale (null if not a plain decimal).
  function toMinor(str, scale) {
    const m = /^(\d+)(?:\.(\d+))?$/.exec(String(str).trim());
    if (!m) return null;
    const frac = (m[2] || "");
    if (frac.length > scale) return null;
    return BigInt(m[1] + frac.padEnd(scale, "0"));
  }
  function minorToString(v, scale) {
    let s = v.toString().padStart(scale + 1, "0");
    return scale ? s.slice(0, -scale) + "." + s.slice(-scale) : s;
  }
  // amount (source scale) * rate, rounded half up to the destination scale.
  function convert(amount, fromScale, rate, toScale) {
    const a = toMinor(amount, fromScale);
    const rm = /^(\d+)(?:\.(\d+))?$/.exec(rate);
    if (a === null || !rm) return null;
    const k = (rm[2] || "").length;
    const r = BigInt(rm[1] + (rm[2] || ""));
    const shift = fromScale + k - toScale; // product has fromScale+k decimals
    let num = a * r;
    if (shift <= 0) return minorToString(num * 10n ** BigInt(-shift), toScale);
    const div = 10n ** BigInt(shift);
    return minorToString((num * 2n + div) / (div * 2n), toScale);
  }
  // ---- Conversion rates (both directions; display only, never sent) --------
  // The API returns conversion_rates ([{from,to,rate}], rate = to_amount /
  // amount and its inverse, 10 significant digits). For the live form preview
  // the same values are computed here exactly with BigInt.
  const RATE_SIG = 10;   // like the server's exchange_rate / conversion_rates
  const SHOW_SIG = 6;    // display rounding; the full value is in the tooltip
  // num/den (positive BigInts) as a decimal string with sig significant
  // digits, rounded half up, trailing zeros trimmed. "" if not positive.
  function roundSig(num, den, sig) {
    if (num <= 0n || den <= 0n) return "";
    const lo = 10n ** BigInt(sig - 1), hi = 10n ** BigInt(sig);
    let n = num, d = den, k = 0; // value = (n/d) / 10^k
    while (n / d >= hi) { d *= 10n; k--; }
    while (n / d < lo) { n *= 10n; k++; }
    let q = (n * 2n + d) / (d * 2n);
    if (q >= hi) { q /= 10n; k--; }
    let str = q.toString();
    if (k <= 0) return str + "0".repeat(-k);
    if (str.length <= k) str = "0".repeat(k - str.length + 1) + str;
    return (str.slice(0, -k) + "." + str.slice(-k)).replace(/\.?0+$/, "");
  }
  // "0.0397" -> [397n, 10000n]; null if not a plain positive decimal.
  function decimalFrac(str) {
    const m = /^(\d+)(?:\.(\d+))?$/.exec(String(str ?? "").trim());
    if (!m) return null;
    const num = BigInt(m[1] + (m[2] || "")), den = 10n ** BigInt((m[2] || "").length);
    return num > 0n ? [num, den] : null;
  }
  // Display form of a rate string: SHOW_SIG significant digits (but never
  // fewer than its whole-number digits, e.g. 1 BTC = 4123456 USD).
  function showRate(rate) {
    const f = decimalFrac(rate);
    if (!f) return "";
    return roundSig(f[0], f[1], Math.max(SHOW_SIG, (f[0] / f[1]).toString().length));
  }
  // Both directions from the form values, with the server's formula:
  // to_amount / amount and amount / to_amount (exact, from minor units).
  function formConversionRates(p, amount, toAmount) {
    const fs = currencyScale.get(p.from) ?? 2, ts = currencyScale.get(p.to) ?? 2;
    const a = toMinor(amount, fs), t = toMinor(toAmount, ts);
    if (a === null || t === null || a <= 0n || t <= 0n) return null;
    const fwdNum = t * 10n ** BigInt(fs), fwdDen = a * 10n ** BigInt(ts);
    return [
      { from: p.from, to: p.to, rate: roundSig(fwdNum, fwdDen, RATE_SIG) },
      { from: p.to, to: p.from, rate: roundSig(fwdDen, fwdNum, RATE_SIG) },
    ];
  }
  // [{from,to,rate}] -> ["1 THB = 0.0397 SGD", "1 SGD = 25.1889 THB"] ([] when absent).
  function conversionLines(rates) {
    if (!Array.isArray(rates)) return [];
    return rates.filter((r) => r && r.from && r.to && showRate(r.rate))
      .map((r) => ({ text: `1 ${r.from} = ${showRate(r.rate)} ${r.to}`, full: `1 ${r.from} = ${r.rate} ${r.to}` }));
  }
  // Render both directions under the Received amount field (hidden if none).
  function renderConversion(rates) {
    const box = $("#fx-conv");
    const lines = $("#fx-field").hidden ? [] : conversionLines(rates);
    box.replaceChildren(...lines.map((l) => {
      const span = document.createElement("span");
      span.className = "fx-conv-line";
      span.textContent = l.text;
      span.title = l.full;
      return span;
    }));
    box.hidden = lines.length === 0;
  }
  // Live preview from the current Amount and Received amount.
  function updateConversion() {
    const p = fxPair();
    renderConversion(p ? formConversionRates(p, $("#amount").value, $("#to_amount").value) : null);
  }

  function fxPair() {
    if (currentType() !== "transfer") return null;
    const src = balanceByUID($("#balance_uid").value);
    const dst = balanceByUID($("#to_balance_uid").value);
    if (!src || !dst) return null;
    const from = ($("#currency").value || src.currency).toUpperCase();
    if (from === dst.currency) return null;
    return { from, to: dst.currency };
  }
  function setRateLine(text) { $("#fx-rate").textContent = text; }
  function showManualRate(p) {
    const r = formConversionRates(p, $("#amount").value, $("#to_amount").value);
    setRateLine(r ? "Rates from your amounts:" : `Enter the amount received in ${p.to}.`);
    $("#fx-reset").hidden = false;
    updateConversion();
  }
  // Show/hide the received-amount field and refresh the preview.
  function updateFx(immediate) {
    const p = fxPair();
    $("#fx-field").hidden = !p;
    clearTimeout(fxTimer);
    if (!p) { fxGen++; renderConversion(null); return; }
    $("#fx-currency").textContent = App.currencyText(p.to);
    if (fxManual) { showManualRate(p); return; }
    $("#fx-reset").hidden = true;
    const q = fxQuote;
    if (q && q.from === p.from && q.to === p.to && q.day === fxDay()) {
      // Same pair and date as the rate already loaded: convert right away.
      fxGen++;
      applyQuote(p, q);
      return;
    }
    renderConversion(null);
    fxTimer = setTimeout(() => fetchRate(p), immediate ? 0 : 300);
  }
  function fxDay() { return spentAtInput.value ? spentAtInput.value.slice(0, 10) : ""; }
  // Fill Received amount from a market quote and refresh both rate lines.
  function applyQuote(p, q) {
    const conv = convert($("#amount").value, currencyScale.get(p.from) ?? 2, q.rate, currencyScale.get(p.to) ?? 2);
    $("#to_amount").value = conv && !/^0*\.?0*$/.test(conv) ? conv : "";
    setRateLine(`ECB market rate of ${q.date} (1 ${p.from} = ${q.rate} ${p.to}):`);
    updateConversion();
  }
  async function fetchRate(p) {
    const my = ++fxGen;
    const local = fxDay();
    const q = new URLSearchParams({ from: p.from, to: p.to });
    if (local) q.set("date", local);
    setRateLine("Looking up the exchange rate…");
    try {
      const res = await fetch("/api/exchange-rates?" + q);
      const body = await res.json().catch(() => ({}));
      if (my !== fxGen || fxManual) return;
      if (!res.ok) {
        $("#to_amount").value = "";
        renderConversion(null);
        setRateLine((res.status === 422 ? `No market rate for ${p.from} → ${p.to}.` : "The exchange-rate service is unavailable.") + " Enter the amount received.");
        return;
      }
      fxQuote = { from: p.from, to: p.to, rate: body.rate, date: body.date, day: local };
      applyQuote(p, fxQuote);
    } catch (err) {
      if (my === fxGen) setRateLine("Could not load the exchange rate: " + err.message);
    }
  }

  // Re-render dropdowns for the selected type, keeping selections when valid.
  function applyType(keepFrom, keepTo) {
    const t = currentType();
    $("#balance-label").textContent = SOURCE_LABEL[t];
    $("#balance-hint").textContent = SOURCE_HINT[t];
    fillSelect($("#balance_uid"), SOURCE_TYPES[t], keepFrom ?? $("#balance_uid").value);
    const dest = DEST_TYPES[t];
    $("#to-field").hidden = !dest;
    if (dest) $("#to-hint").textContent = DEST_HINT[t];
    fillCategories(t);
    fillSelect($("#to_balance_uid"), dest || [], keepTo ?? $("#to_balance_uid").value);
    updateFx();
  }

  // Category dropdown: only categories of the selected type; hidden for transfers.
  function fillCategories(t, keep) {
    const sel = $("#category_uid");
    const want = keep ?? sel.value;
    $("#category-field").hidden = t === "transfer";
    sel.replaceChildren(Object.assign(document.createElement("option"), { value: "", textContent: "No category" }));
    for (const c of allCategories) {
      if (c.type !== CATEGORY_TYPE[t]) continue;
      sel.append(Object.assign(document.createElement("option"), { value: c.uid, textContent: c.name }));
    }
    sel.value = want && [...sel.options].some((o) => o.value === want) ? want : "";
  }

  // ---- Form page: /transactions/new and /transactions/<uid>/edit ------------

  async function enterForm(params) {
    const my = ++gen;
    editing = null;
    form.reset();
    App.clearErrors(form);
    App.hideConflict(conflictBox);
    setStatus("");
    form.hidden = false;
    $("#submit-btn").disabled = false;
    $("#type").value = "expense";
    $("#note").value = "";
    $("#amount").value = "";
    $("#currency").value = "";
    $("#to_amount").value = "";
    fxManual = false;
    fxQuote = null;
    renderConversion(null);
    if (!params.uid) {
      $("#form-title").textContent = "Add a transaction";
      $("#submit-btn").textContent = "Save transaction";
      await Promise.all([loadBalancesList(), loadCategoriesList(), App.fillCurrencySelect($("#currency"))]);
      if (my !== gen) return;
      applyType("", "");
      restoreDefaults();
      setNow();
      $("#amount").focus();
      return;
    }
    $("#form-title").textContent = "Edit transaction";
    $("#submit-btn").textContent = "Save changes";
    form.hidden = true; // until loaded
    let e;
    try {
      const [res] = await Promise.all([fetch(`/api/transactions/${params.uid}`), loadBalancesList(), loadCategoriesList(), App.fillCurrencySelect($("#currency"))]);
      const body = await res.json().catch(() => ({}));
      if (my !== gen) return;
      if (!res.ok) {
        setStatus(res.status === 404 ? "This transaction does not exist (it may have been deleted)." : body.error || `Could not load transaction (${res.status})`, "bad");
        return;
      }
      e = body;
    } catch (err) {
      if (my === gen) setStatus("Network error: " + err.message, "bad");
      return;
    }
    if (e.type === "balance_adjustment") {
      // Recorded automatically by a manual balance edit; read-only in the API.
      $("#form-title").textContent = "Balance Adjustment";
      setStatus("Balance adjustments are recorded automatically when a balance or debt is edited by hand, and can't be edited or deleted. Edit the balance instead.", "bad");
      return;
    }
    editing = e;
    form.hidden = false;
    $("#form-title").textContent = `Edit transaction: ${e.amount} ${e.currency}, ${shownTime(e.spent_at)}`;
    $("#amount").value = e.amount;
    App.setCurrency($("#currency"), e.currency);
    $("#type").value = e.type || "expense";
    // Stored conversion: shown as saved; it is kept unless the amount, date
    // or balances change (then the market rate is looked up again) or the
    // received amount is edited (manual).
    fxManual = e.rate_source === "manual";
    applyType(e.balance_uid, e.to_balance_uid || "");
    fillCategories(currentType(), e.category_uid || "");
    $("#note").value = e.note || "";
    // Show the saved instant converted to the device's local time; saving
    // re-stamps it with the device's current offset.
    const d = new Date(e.spent_at);
    spentAtInput.value = isNaN(d) ? "" : toLocalInputValue(d);
    updatePreview();
    if (e.to_amount && e.to_currency) {
      clearTimeout(fxTimer);
      fxGen++;
      $("#fx-field").hidden = false;
      $("#fx-currency").textContent = App.currencyText(e.to_currency);
      $("#to_amount").value = e.to_amount;
      $("#fx-reset").hidden = !fxManual;
      setRateLine(fxManual ? "Rates from your amounts:"
        : `ECB market rate of ${e.rate_date} (1 ${e.currency} = ${e.exchange_rate} ${e.to_currency}); effective rates:`);
      // Saved transaction: show the API's conversion_rates as returned.
      renderConversion(e.conversion_rates);
    }
    $("#amount").focus();
  }

  async function submitTransaction(ev) {
    ev.preventDefault();
    App.clearErrors(form);
    setStatus("");

    const type = currentType();
    const payload = {
      type,
      amount: $("#amount").value.trim(),
      currency: $("#currency").value.toUpperCase(),
      balance_uid: $("#balance_uid").value.trim(),
      spent_at: composedSpentAt() || "",
      note: $("#note").value.trim(),
    };
    if (DEST_TYPES[type]) payload.to_balance_uid = $("#to_balance_uid").value.trim();
    if (type !== "transfer" && $("#category_uid").value) payload.category_uid = $("#category_uid").value;
    // Received amount: sent only when typed by the user; otherwise the server
    // converts with the market rate (and keeps a saved conversion on edit if
    // nothing it depends on changed).
    if (fxPair() && fxManual) payload.to_amount = $("#to_amount").value.trim();

    const isEdit = !!editing;
    const url = isEdit ? `/api/transactions/${editing.uid}` : "/api/transactions";
    const btn = $("#submit-btn");
    btn.disabled = true;
    App.hideConflict(conflictBox);
    try {
      const res = await fetch(url, {
        method: isEdit ? "PUT" : "POST",
        headers: { "Content-Type": "application/json", ...(isEdit ? App.ifMatch(editing.version) : {}) },
        body: JSON.stringify(payload),
      });
      const body = await res.json().catch(() => ({}));
      if (isEdit && res.status === 409 && body.code === "version_conflict") {
        const cur = body.current || {};
        const uid = editing.uid;
        App.showConflict(conflictBox,
          `This transaction was changed after you opened this page (in another tab or by someone else), so your changes were not saved. ` +
          `It is now ${cur.amount} ${cur.currency} (${cur.type}). Reload to see the latest values, then make your change again.`,
          () => enterForm({ uid }));
        setStatus("Not saved: changed elsewhere.", "bad");
        return;
      }
      if (!res.ok) {
        App.showFieldErrors(form, body.fields);
        let msg = body.error || `Request failed (${res.status})`;
        if (isEdit && res.status === 404) msg = "This transaction no longer exists (it may have been deleted).";
        setStatus(msg, "bad");
        return;
      }
      if (!isEdit && type === "expense") rememberDefaults(payload.currency, payload.balance_uid);
      App.setFlash("transactions", `${isEdit ? "Updated" : "Saved"} ${body.amount} ${body.currency} (${body.type}).`, body.uid);
      editing = null;
      App.returnToList(LIST);
    } catch (err) {
      setStatus("Network error: " + err.message, "bad");
    } finally {
      btn.disabled = false;
    }
  }

  function cancelForm() { App.returnToList(LIST); }

  // ---- List page: /transactions ---------------------------------------------

  function filterParams() {
    const params = new URLSearchParams();
    const from = $("#filter-from").value;
    const to = $("#filter-to").value;
    // Date filters are interpreted as whole days in the browser's time zone.
    if (from) {
      const [y, m, dd] = from.split("-").map(Number);
      params.set("from", toRFC3339(new Date(y, m - 1, dd, 0, 0, 0)));
    }
    if (to) {
      const [y, m, dd] = to.split("-").map(Number);
      params.set("to", toRFC3339(new Date(y, m - 1, dd, 23, 59, 59)));
    }
    if ($("#filter-type").value) params.set("type", $("#filter-type").value);
    return params;
  }

  function formatMinor(minor, scale) {
    let s = minor.toString();
    if (scale > 0) {
      s = s.padStart(scale + 1, "0");
      s = s.slice(0, -scale) + "." + s.slice(-scale);
    }
    return s;
  }

  // Per-currency totals of the shown rows, exact via BigInt minor units.
  // Expenses and income are summed separately; transfers and balance
  // adjustments (manual corrections, not spending or income) are excluded.
  function renderTotals(items) {
    const sums = new Map(); // currency -> {scale, expense, income}
    for (const e of items) {
      const t = e.type || "expense";
      if (t !== "expense" && t !== "income") continue;
      const scale = (e.amount.split(".")[1] || "").length;
      const cur = sums.get(e.currency) || { scale, expense: 0n, income: 0n };
      cur[t] += BigInt(e.amount_minor);
      sums.set(e.currency, cur);
    }
    const parts = [];
    for (const [code, { scale, expense, income }] of sums) {
      parts.push(`${App.currencyText(code)}: expenses ${formatMinor(expense, scale)}, income ${formatMinor(income, scale)}`);
    }
    totalsEl.textContent = parts.length ? "Totals (shown rows, transfers, asset purchases and balance adjustments excluded): " + parts.join(" · ") : "";
  }

  // Category name for a transaction, looked up client-side by category_uid
  // (transactions carry only the uid).
  function categoryName(uid) {
    if (!uid) return "";
    const c = allCategories.find((x) => x.uid === uid);
    return c ? c.name : "(unknown category)";
  }

  // Refetched every time the form opens, so amounts reflect the latest saves.
  async function loadBalancesList() {
    const list = await App.loadCurrencies();
    currencyScale = new Map(list.filter((c) => Number.isInteger(c.scale)).map((c) => [c.code, c.scale]));
    try {
      const res = await fetch("/api/balances");
      const body = await res.json();
      if (res.ok) allBalances = body.balances || [];
    } catch (_) { /* keep previous list */ }
  }

  async function loadCategoriesList() {
    try {
      const res = await fetch("/api/categories");
      const body = await res.json();
      if (res.ok) allCategories = body.categories || [];
    } catch (_) { /* keep previous list */ }
  }

  // Filters live in the URL (?from=YYYY-MM-DD&to=YYYY-MM-DD) so they survive
  // add/edit round trips, reloads and the back button.
  async function enterList(_params, query) {
    $("#filter-from").value = /^\d{4}-\d{2}-\d{2}$/.test(query.get("from") || "") ? query.get("from") : "";
    $("#filter-to").value = /^\d{4}-\d{2}-\d{2}$/.test(query.get("to") || "") ? query.get("to") : "";
    const ft = query.get("type") || "";
    $("#filter-type").value = [...$("#filter-type").options].some((o) => o.value === ft) ? ft : "";
    const highlight = App.takeFlash("transactions");
    await loadTransactions(highlight);
  }

  function applyFilters() {
    const q = new URLSearchParams();
    if ($("#filter-from").value) q.set("from", $("#filter-from").value);
    if ($("#filter-to").value) q.set("to", $("#filter-to").value);
    if ($("#filter-type").value) q.set("type", $("#filter-type").value);
    App.replaceQuery(q.toString());
    $("#flash-transactions").hidden = true;
    loadTransactions();
  }

  async function loadTransactions(highlightUID) {
    const my = ++gen;
    listStatus.textContent = "Loading…";
    try {
      const params = filterParams();
      const [res] = await Promise.all([
        fetch("/api/transactions" + (params.toString() ? "?" + params : "")),
        loadCategoriesList(),
        App.loadCurrencies(), // cached; flags for the Currency column
      ]);
      const body = await res.json();
      if (my !== gen) return;
      if (!res.ok) {
        listStatus.textContent = body.error + (body.fields ? ": " + Object.values(body.fields).join("; ") : "");
        return;
      }
      tbody.replaceChildren();
      for (const e of body.transactions) {
        const tr = document.createElement("tr");
        tr.dataset.uid = String(e.uid);
        if (highlightUID && e.uid === highlightUID) tr.classList.add("just-saved");
        const timeTd = cell(shownTime(e.spent_at), "time");
        if (e.updated_at) {
          const mark = document.createElement("span");
          mark.className = "edited";
          mark.textContent = "edited";
          mark.title = "Last edited " + shownTime(e.updated_at);
          timeTd.append(mark);
        }
        const t = e.type || "expense";
        const isAdj = t === "balance_adjustment";
        const typeTd = cell(TYPE_LABEL[t] || t, "type type-" + t);
        const acctTd = cell(DEST_TYPES[t] ? `${e.account || "?"} → ${e.to_account || "?"}` : (e.account || ""));
        // An adjustment shows its direction as a sign: + the edited balance
        // or debt went up, − it went down.
        const fxRow = e.to_amount && e.to_currency;
        const amountTd = cell(isAdj ? (e.adjustment_direction === "decrease" ? "−" : "+") + e.amount : fxRow ? "" : e.amount, "num");
        if (fxRow) {
          // "1,000.00 THB" / "→ 39.70 SGD" (each kept on one line, may wrap
          // between them), then both rates from the API's conversion_rates.
          amountTd.classList.add("fx");
          const amounts = document.createElement("div");
          amounts.className = "fx-amounts";
          for (const text of [`${fmtAmount(e.amount)} ${e.currency}`, `→ ${fmtAmount(e.to_amount)} ${e.to_currency}`]) {
            const part = document.createElement("span");
            part.textContent = text;
            amounts.append(part);
          }
          amountTd.append(amounts);
          const lines = conversionLines(e.conversion_rates);
          if (lines.length) {
            const box = document.createElement("div");
            box.className = "fx-rates";
            for (const l of lines) {
              const line = document.createElement("span");
              line.textContent = l.text;
              box.append(line);
            }
            amountTd.append(box);
          }
          amountTd.title = [
            e.rate_source === "manual" ? "Amount received entered by hand" : `ECB rate of ${e.rate_date} via Frankfurter: 1 ${e.currency} = ${e.exchange_rate} ${e.to_currency}`,
            ...lines.map((l) => l.full),
          ].join("\n");
        }
        if (isAdj) {
          tr.classList.add("row-adjustment");
          amountTd.title = `Manual balance edit: ${e.adjustment_direction === "decrease" ? "decreased" : "increased"} by ${e.amount} ${e.currency}`;
        }
        if (fxRow) tr.classList.add("fx-row");
        acctTd.classList.add("acct");
        tr.append(timeTd, typeTd, amountTd, cell(fxRow ? `${App.currencyText(e.currency)} → ${App.currencyText(e.to_currency)}` : App.currencyText(e.currency), "cur"), acctTd, cell(categoryName(e.category_uid), "cat"), cell(e.note || "", "note"));
        const actions = document.createElement("td");
        actions.className = "actions-cell";
        if (isAdj) {
          // Read-only: recorded automatically, no Edit/Delete.
          const auto = document.createElement("span");
          auto.className = "auto-label";
          auto.textContent = "Automatic";
          auto.title = "Recorded when the balance was edited by hand; edit the balance to correct it.";
          actions.append(auto);
        } else {
          actions.append(
            link("Edit", `/transactions/${encodeURIComponent(e.uid)}/edit`, "button edit"),
            button("Delete", "danger", () => deleteTransaction(e)),
          );
        }
        tr.append(actions);
        tbody.append(tr);
      }
      listStatus.textContent = body.count === 0 ? "No transactions yet." : `${body.count} transaction(s), newest first.`;
      renderTotals(body.transactions);
    } catch (err) {
      if (my === gen) listStatus.textContent = "Failed to load transactions: " + err.message;
    }
  }

  // "3361.50" -> "3,361.50" (display only; digits as stored).
  function fmtAmount(v) {
    // The API already formats amounts in the currency's scale; keep its digits.
    const scale = (String(v).split(".")[1] || "").length;
    const opts = { minimumFractionDigits: scale, maximumFractionDigits: scale };
    try { return new Intl.NumberFormat(undefined, opts).format(v); } catch (_) { return v; }
  }

  async function deleteTransaction(e) {
    const acct = DEST_TYPES[e.type] ? `${e.account || "?"} → ${e.to_account || "?"}` : e.account || "unknown account";
    const fxText = e.to_amount && e.to_currency ? ` → ${e.to_amount} ${e.to_currency}` : "";
    if (!confirm(`Delete ${e.amount} ${e.currency}${fxText} (${acct}) at ${shownTime(e.spent_at)}?`)) return;
    const res = await fetch(`/api/transactions/${e.uid}`, { method: "DELETE", headers: App.ifMatch(e.version) });
    if (res.status === 409) {
      alert("This transaction changed after the list was loaded, so it was not deleted. The list has been refreshed; check it and delete again if you still want to.");
    } else if (!res.ok && res.status !== 404) {
      alert("Delete failed (" + res.status + ")");
    }
    $("#flash-transactions").hidden = true;
    await loadTransactions();
  }

  // ---- Init ----------------------------------------------------------------

  // Searchable dropdowns (issue #24); the Type select stays a plain select.
  Combobox.enhance($("#balance_uid"));
  Combobox.enhance($("#to_balance_uid"));
  Combobox.enhance($("#category_uid"), { clearable: true });
  Combobox.enhance($("#currency")); // issue #25; search matches code and name

  spentAtInput.addEventListener("input", updatePreview);
  $("#type").addEventListener("change", () => applyType());
  // A transaction's currency must match its (source) balance: picking a
  // balance fills in its currency (still selectable; the server reports a
  // mismatch under the currency field).
  function currencyFromBalance() {
    const b = allBalances.find((x) => x.uid === $("#balance_uid").value);
    if (b) App.setCurrency($("#currency"), b.currency);
  }
  $("#balance_uid").addEventListener("change", () => { currencyFromBalance(); fxAuto(); });
  // Changing what the conversion depends on goes back to the market rate
  // (like the API: a PUT without to_amount refetches).
  function fxAuto() { fxManual = false; updateFx(); }
  $("#to_balance_uid").addEventListener("change", fxAuto);
  $("#currency").addEventListener("change", fxAuto);
  $("#amount").addEventListener("input", () => { if (fxManual) { const p = fxPair(); if (p) showManualRate(p); } else updateFx(); });
  spentAtInput.addEventListener("change", () => { if (!fxManual) updateFx(); });
  $("#to_amount").addEventListener("input", () => { fxManual = true; fxGen++; const p = fxPair(); if (p) showManualRate(p); });
  $("#fx-reset").addEventListener("click", () => { fxAuto(); updateFx(true); });
  $("#now-btn").addEventListener("click", setNow);
  $("#cancel-btn").addEventListener("click", cancelForm);
  form.addEventListener("submit", submitTransaction);
  $("#filter-form").addEventListener("submit", (ev) => { ev.preventDefault(); applyFilters(); });
  $("#filter-clear").addEventListener("click", () => {
    $("#filter-from").value = "";
    $("#filter-to").value = "";
    $("#filter-type").value = "";
    applyFilters();
  });

  const list = { view: "#view-transactions", section: "transactions", title: "", enter: enterList };
  App.route("/transactions", list);
  const formRoute = { view: "#view-transaction-form", section: "transactions", enter: enterForm, onEscape: cancelForm };
  App.route("/transactions/new", { ...formRoute, title: "Add transaction" });
  App.route("/transactions/:uid/edit", { ...formRoute, title: "Edit transaction" });
})();
