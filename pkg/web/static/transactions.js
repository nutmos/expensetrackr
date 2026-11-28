// Transactions: list page (/transactions) and add/edit page
// (/transactions/new, /transactions/<uid>/edit). Plain vanilla JS, no build step.
// All user data is rendered with textContent / input .value (never innerHTML).
(function () {
  "use strict";

  const { $, cell, button, link } = App;
  const form = $("#transaction-form");
  // Date and time are separate fields (issue #163): a date picker and an
  // hour:minute time; seconds and milliseconds are always saved as 0.
  const spentDateInput = $("#spent_date");
  const spentTimeInput = $("#spent_time");
  const statusEl = $("#form-status");
  const tbody = $("#transaction-table tbody");
  const listStatus = $("#list-status");
  const totalsEl = $("#totals");
  const fmtAmount = App.fmtAmount;
  const loadMoreBtn = $("#tx-load-more");
  // The list loads PAGE_SIZE rows at a time; "Load more" fetches the next
  // page with the API's next_cursor (issue #86).
  const PAGE_SIZE = 100;
  let loaded = []; // rows shown in the list, in order
  let nextCursor = null; // cursor for the next page, or null on the last one
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

  // "YYYY-MM-DD" and "HH:MM" in the browser's local time (values for the
  // date and time inputs).
  function toDateValue(d) {
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  }
  function toTimeValue(d) {
    return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }

  // "+08:00" style offset for the given instant in the browser's time zone
  // (computed per date, so daylight-saving changes are handled).
  function offsetString(d) {
    const mins = -d.getTimezoneOffset();
    const sign = mins >= 0 ? "+" : "-";
    return `${sign}${pad(Math.floor(Math.abs(mins) / 60))}:${pad(Math.abs(mins) % 60)}`;
  }

  // RFC 3339 timestamp in the browser's zone with seconds zeroed,
  // e.g. 2026-10-06T21:06:00+08:00.
  function toRFC3339(d) {
    return `${toDateValue(d)}T${toTimeValue(d)}:00${offsetString(d)}`;
  }

  // The entered date ("YYYY-MM-DD") and time ("HH:MM", any seconds ignored)
  // as a browser-local Date, or null if either is missing or invalid.
  function parseLocalInput(date, time) {
    const dm = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date || "");
    const tm = /^(\d{2}):(\d{2})/.exec(time || "");
    if (!dm || !tm) return null;
    const d = new Date(+dm[1], +dm[2] - 1, +dm[3], +tm[1], +tm[2], 0, 0);
    return isNaN(d) ? null : d;
  }

  // Put an instant into the date and time fields (device-local, minute precision).
  function setSpentAt(d) {
    const ok = d && !isNaN(d);
    spentDateInput.value = ok ? toDateValue(d) : "";
    spentTimeInput.value = ok ? toTimeValue(d) : "";
    showPickerText();
  }

  // Show the date as "DD MMM YYYY" and the time as "HH:mm" over the native
  // inputs, whose own text follows the browser locale (issue #163).
  function showPickerText() {
    const date = spentDateInput.value, time = spentTimeInput.value;
    const d = parseLocalInput(date, "00:00");
    const dateText = d ? App.fmtDateTime(d).slice(0, -6) : "";
    const timeText = /^\d{2}:\d{2}/.test(time) ? time.slice(0, 5) : "";
    for (const [inp, text] of [[spentDateInput, dateText], [spentTimeInput, timeText]]) {
      inp.parentElement.querySelector(".picker-text").textContent = text;
      inp.parentElement.classList.toggle("has-value", !!text);
    }
  }

  // The RFC 3339 value the form would submit: the entered wall-clock time in
  // the device's time zone, with the device's offset for that date (DST-aware).
  function composedSpentAt() {
    const d = parseLocalInput(spentDateInput.value, spentTimeInput.value);
    return d ? toRFC3339(d) : null;
  }

  function setNow() {
    setSpentAt(new Date());
    updatePreview();
  }

  function updatePreview() {
    showPickerText();
    const v = composedSpentAt();
    let text = v ? "Will be saved as " + shownTime(v) : "Enter a date and time";
    if (v && editing && shownTime(v) !== shownTime(editing.spent_at)) text += ` (was ${shownTime(editing.spent_at)})`;
    $("#spent-at-preview").textContent = text;
  }

  // "DD MMM YYYY HH:mm" in the device's time zone (raw value if unparsable).
  const shownTime = (rfc3339) => App.fmtDateTime(rfc3339);

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
    income: ["payment_account", "credit_card"],
    transfer: ["payment_account", "credit_card", "other_asset", "other_liability"],
    asset_purchase: ["payment_account", "credit_card"],
    return_on_investment: ["payment_account", "credit_card"],
    asset_sale: ["payment_account", "credit_card"],
    repay_debt: ["payment_account"],
    // Only when editing one (they are recorded by manual balance edits).
    balance_adjustment: ["payment_account", "credit_card", "other_asset", "other_liability"],
    // Only when editing one (recorded with each balance; its balance is fixed).
    beginning_balance: ["payment_account", "credit_card", "other_asset", "other_liability"],
  };
  // Which balance types may be the destination (to_balance_uid).
  const DEST_TYPES = {
    transfer: SOURCE_TYPES.transfer,
    asset_purchase: ["other_asset"],
    // Link only: the investment the return came from; its value does not change.
    return_on_investment: ["other_asset"],
    asset_sale: ["other_asset"],
    repay_debt: ["credit_card", "other_liability"],
  };
  const DEST_HINT = {
    transfer: "Any balance (e.g. pay a credit card, save to an asset, repay a loan).",
    asset_purchase: "Other assets only (the asset you bought).",
    return_on_investment: "Other assets only (the investment this return is tied to).",
    asset_sale: "Other assets only (its value goes down by the principal).",
    repay_debt: "Credit cards and other liabilities (its debt goes down).",
  };
  const DEST_LABEL = { return_on_investment: "Investment", asset_sale: "Asset sold", repay_debt: "Debt repaid" };
  // Category type used by each transaction type (asset purchases use expense
  // categories; returns on investment use income categories; an asset sale
  // uses an income category for a gain and an expense category for a loss).
  const CATEGORY_TYPE = { expense: "expense", income: "income", asset_purchase: "expense", return_on_investment: "income", repay_debt: "expense" };
  function categoryType(t) {
    if (t === "asset_sale") return isLoss() ? "expense" : "income";
    return CATEGORY_TYPE[t];
  }
  // Types that do not require a category. A transfer never has one; a debt
  // repayment may have an (expense) category for its interest and fees only,
  // and an asset sale one for its gain or loss only, so their category field
  // is shown only while those are filled in.
  const NO_CATEGORY = new Set(["transfer", "repay_debt", "asset_sale", "balance_adjustment", "beginning_balance"]);
  // Whether the category field applies to type t in the current form.
  function categoryShown(t) {
    return !NO_CATEGORY.has(t) || (t === "repay_debt" && hasCharges()) || (t === "asset_sale" && hasGainLoss());
  }
  // Gain / loss typed into the form (asset sales only); a leading "-" is a loss.
  function hasGainLoss() { return /[1-9]/.test($("#gain_loss").value); }
  function isLoss() { return $("#gain_loss").value.trim().startsWith("-"); }
  // Total received and gain / loss of an asset sale fill each other in
  // (issue #204): received = principal + gain_loss. saleFrom is the input
  // the user typed in last; changing the principal (or the currency) keeps
  // that one and recomputes the other. Exact via BigInt minor units.
  let saleFrom = "gain";
  function saleMinor(v, scale) {
    const neg = v.startsWith("-");
    const [i, f = ""] = v.replace("-", "").split(".");
    const n = BigInt(i + f.padEnd(scale, "0"));
    return neg ? -n : n;
  }
  function saleFormat(n, scale) { return (n < 0n ? "-" : "") + formatMinor(n < 0n ? -n : n, scale); }
  function syncSale() {
    const recv = $("#sale_received"), gain = $("#gain_loss"), err = $("#sale-received-err");
    err.textContent = "";
    if (currentType() !== "asset_sale") return;
    const p = $("#amount").value.trim();
    const g = gain.value.trim();
    const r = recv.value.trim().replace(/[,\s]/g, "");
    const principalOK = /^\d+(\.\d+)?$/.test(p);
    const scaleOf = (...vs) => Math.max(currencyScale.get($("#currency").value.toUpperCase()) ?? 0, ...vs.map((v) => (v.split(".")[1] || "").length));
    if (saleFrom === "received") {
      if (!r) { if (gain.value) { gain.value = ""; fillCategories(currentType()); } return; }
      if (!/^\d+(\.\d+)?$/.test(r)) { err.textContent = "Enter a positive amount, e.g. 1050.00."; return; }
      if (!principalOK) return;
      const scale = scaleOf(p, r);
      const diff = saleMinor(r, scale) - saleMinor(p, scale);
      const next = diff === 0n ? "" : saleFormat(diff, scale);
      if (gain.value !== next) { gain.value = next; fillCategories(currentType()); }
      return;
    }
    recv.value = "";
    if (!principalOK || (g && !/^-?\d+(\.\d+)?$/.test(g))) return;
    const scale = scaleOf(p, g);
    const total = saleMinor(p, scale) + (g ? saleMinor(g, scale) : 0n);
    if (total < 0n) return; // the server explains: a loss can't exceed the principal
    recv.value = formatMinor(total, scale);
  }
  // Entire asset sale: switching it on (or picking another asset while it is
  // on) fills in the principal with the asset's current balance (what is
  // left to sell); the user can still change it afterwards.
  function principalFromAsset() {
    if (currentType() !== "asset_sale" || !$("#entire_asset_sale").checked) return;
    const b = balanceByUID($("#to_balance_uid").value);
    const v = b ? String(b.balance ?? "").trim() : "";
    if (/^\d+(\.\d+)?$/.test(v) && /[1-9]/.test(v)) {
      $("#amount").value = v;
      syncSale();
    }
  }
  // Interest / fees typed into the form (debt repayments only).
  function hasCharges() { return /[1-9]/.test($("#interest_amount").value) || /[1-9]/.test($("#fee_amount").value); }
  // "Total paid" line under the interest/fee inputs, exact via BigInt.
  function updateChargesTotal() {
    const out = $("#charges-total");
    out.textContent = "";
    if (currentType() !== "repay_debt" || !hasCharges()) return;
    const parts = [$("#amount").value, $("#interest_amount").value, $("#fee_amount").value].map((v) => v.trim()).filter(Boolean);
    if (!parts.every((v) => /^\d+(\.\d+)?$/.test(v))) return;
    const cur = $("#currency").value.toUpperCase();
    const scale = Math.max(currencyScale.get(cur) ?? 0, ...parts.map((v) => (v.split(".")[1] || "").length));
    const total = parts.reduce((s, v) => {
      const [i, f = ""] = v.split(".");
      return s + BigInt(i + f.padEnd(scale, "0"));
    }, 0n);
    out.textContent = `Total paid: ${fmtAmount(formatMinor(total, scale))}${cur ? " " + cur : ""}.`;
  }
  // Types that may record a foreign-currency amount (reference only; never
  // moves a balance). Transfers use to_amount instead.
  const FOREIGN_TYPES = new Set(["expense", "income", "asset_purchase", "return_on_investment"]);
  const SOURCE_LABEL = { expense: "Payment account", income: "Received into", transfer: "From (source)", asset_purchase: "Paid from", return_on_investment: "Received into", asset_sale: "Received into", repay_debt: "Paid from", balance_adjustment: "Adjusted balance", beginning_balance: "Balance" };
  const SOURCE_HINT = {
    expense: "Payment accounts and credit cards.",
    income: "Payment accounts and credit cards (e.g. cash rebates).",
    transfer: "Any balance.",
    asset_purchase: "Payment accounts and credit cards.",
    return_on_investment: "Payment accounts and credit cards.",
    asset_sale: "Payment accounts and credit cards (where the sale proceeds arrive).",
    repay_debt: "Payment accounts.",
    balance_adjustment: "Any balance.",
    beginning_balance: "The balance it starts; it cannot change.",
  };
  // Display names of transaction types (the API uses the lowercase values).
  const TYPE_LABEL = { expense: "Expense", income: "Income", transfer: "Transfer", asset_purchase: "Asset Purchase", return_on_investment: "Return on Investment", asset_sale: "Asset Sale", repay_debt: "Repay Debt", balance_adjustment: "Balance Adjustment", beginning_balance: "Beginning Balance" };
  // Types recorded by the server (by balance edits and with each balance):
  // editable, but they keep their type; they have a direction (+/−).
  const SERVER_TYPES = new Set(["balance_adjustment", "beginning_balance"]);
  // Account column text: "from → to"; a return on investment and an asset
  // sale flow from the asset into the receiving account.
  function accountText(e) {
    if (e.type === "return_on_investment" || e.type === "asset_sale") return `${e.to_account || "?"} → ${e.account || "?"}`;
    if (DEST_TYPES[e.type]) return `${e.account || "?"} → ${e.to_account || "?"}`;
    return e.account || "";
  }
  const LIABILITY = new Set(["credit_card", "other_liability"]);
  // Minor-unit digits per currency code, from GET /api/currencies (scale).
  let currencyScale = new Map();

  // Amount shown in a balance option (issues #28, #188): assets show their
  // balance, liabilities their debt as a positive amount; negatives (an
  // overdrawn account, a card in credit) in brackets.
  function balanceAmountText(b) {
    const v = String((LIABILITY.has(b.type) ? b.debt : b.balance) ?? "0").trim() || "0";
    const scale = currencyScale.get(b.currency);
    const opts = { style: "currency", currency: b.currency };
    if (Number.isInteger(scale)) { opts.minimumFractionDigits = scale; opts.maximumFractionDigits = scale; }
    const neg = v.startsWith("-") && !/^-0*\.?0*$/.test(v);
    const abs = v.replace(/^[-+]/, "");
    let text;
    try { text = new Intl.NumberFormat(undefined, opts).format(abs); } catch (_) { text = abs; }
    return neg ? `(${text})` : text;
  }
  // Plural headings for <optgroup>s in the balance dropdowns, matching
  // the Balances list page. Empty types are omitted.
  const BALANCE_TYPE_GROUP = {
    payment_account: "Payment accounts",
    credit_card: "Credit cards",
    other_asset: "Other assets",
    other_liability: "Other liabilities",
  };
  // "Wallet · $50.00": name and current amount (no uid, code or type).
  // The type is the optgroup heading; typing a group name (e.g. "card")
  // still lists that group because the combobox matches against it.
  function balanceLabel(b) {
    return `${b.name} · ${balanceAmountText(b)}`;
  }

  function fillSelect(sel, types, keep) {
    sel.replaceChildren(Object.assign(document.createElement("option"), { value: "", textContent: "Select a balance…" }));
    for (const type of types) {
      // Hidden balances are offered only when already selected (editing an
      // older transaction on one).
      const rows = allBalances.filter((b) => b.type === type && (!b.hidden || b.uid === keep));
      if (!rows.length) continue;
      const group = document.createElement("optgroup");
      group.label = BALANCE_TYPE_GROUP[type] || type;
      for (const b of rows) {
        const opt = document.createElement("option");
        opt.value = b.uid;
        opt.textContent = balanceLabel(b) + (b.hidden ? " (hidden)" : "");
        group.append(opt);
      }
      sel.append(group);
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
  function fxDay() { return spentDateInput.value || ""; }
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
    if (dest) {
      $("#to-label").textContent = DEST_LABEL[t] || "To (destination)";
      $("#to-hint").textContent = DEST_HINT[t];
    }
    fillCategories(t);
    fillSelect($("#to_balance_uid"), dest || [], keepTo ?? $("#to_balance_uid").value);
    $("#foreign-field").hidden = !FOREIGN_TYPES.has(t);
    $("#charges-field").hidden = t !== "repay_debt";
    $("#sale-field").hidden = t !== "asset_sale";
    $("#received-field").hidden = t !== "asset_sale";
    $("#entire-field").hidden = t !== "asset_sale";
    // An asset sale starts from the asset: "Asset sold" and the "Entire
    // asset sale" switch come before the principal they fill in. Every other
    // type keeps the destination after the currency.
    const amountField = $("#amount").closest("label"), currencyField = $("#currency").closest("label");
    if (t === "asset_sale") amountField.before($("#to-field"), $("#entire-field"));
    else currencyField.after($("#to-field"), $("#entire-field"));
    // A repayment's (optional) category belongs to its interest and fees,
    // and a sale's to its gain or loss, so it goes after them; that also
    // keeps the fields above from moving when it appears.
    if (t === "repay_debt") $("#charges-field").after($("#category-field"));
    else if (t === "asset_sale") $("#sale-field").after($("#category-field"));
    else $("#to-field").before($("#category-field"));
    $("#amount-label").textContent = t === "repay_debt" || t === "asset_sale" ? "Principal" : "Amount";
    updateChargesTotal();
    syncSale();
    $("#direction-field").hidden = !SERVER_TYPES.has(t);
    updateDirectionHint();
    updateFx();
  }

  // Balance adjustments: what the direction does to the selected balance
  // (its balance for an asset, its debt for a liability). Beginning
  // balances: whether the balance started at or below zero.
  function updateDirectionHint() {
    const t = currentType();
    if (!SERVER_TYPES.has(t)) return;
    const b = balanceByUID($("#balance_uid").value);
    const what = b && LIABILITY.has(b.type) ? "debt" : "balance";
    const down = $("#adjustment_direction").value === "decrease";
    $("#direction-hint").textContent = t === "beginning_balance"
      ? (down ? `The ${what} started below zero, at minus the amount.` : `The ${what} started at the amount.`)
      : (down ? `Lowers the ${what} by the amount.` : `Raises the ${what} by the amount.`);
  }

  // Category dropdown: only categories of the selected type; hidden for
  // transfers, debt repayments without interest or fees and asset sales
  // without a gain or loss (categoryShown). Required for every type not in NO_CATEGORY (the server
  // reports a missing one under category_uid).
  function fillCategories(t, keep) {
    const sel = $("#category_uid");
    const want = keep ?? sel.value;
    $("#category-field").hidden = !categoryShown(t);
    $("#category-label").textContent = t === "repay_debt" ? "Category of interest & fees (optional)"
      : t === "asset_sale" ? `Category of the ${isLoss() ? "loss" : "gain"} (optional)` : "Category";
    const ct = categoryType(t);
    sel.replaceChildren(Object.assign(document.createElement("option"), { value: "", textContent: "Select a category…" }));
    let n = 0;
    for (const c of allCategories) {
      if (c.type !== ct) continue;
      sel.append(Object.assign(document.createElement("option"), { value: c.uid, textContent: c.name }));
      n++;
    }
    sel.value = want && [...sel.options].some((o) => o.value === want) ? want : "";
    $("#category-hint").textContent = n || !categoryShown(t) ? "" : `No ${ct} categories yet; add one first.`;
  }

  // ---- Form page: /transactions/new and /transactions/<uid>/edit ------------

  // Types a new transaction can have (balance_adjustment is recorded only by
  // manual balance edits). /transactions/new without one of these shows the
  // type picker first.
  const NEW_TYPES = new Set(["expense", "income", "transfer", "asset_purchase", "return_on_investment", "asset_sale", "repay_debt"]);
  // Form heading for each type on /transactions/new?type=<type>.
  const NEW_TITLE = { expense: "Add an expense", income: "Add income", transfer: "Add a transfer", asset_purchase: "Add an asset purchase", return_on_investment: "Add a return on investment", asset_sale: "Sell an asset", repay_debt: "Repay a debt" };

  // Show the type picker or the form (exactly one).
  function showPicker(on) {
    $("#tx-type-picker").hidden = !on;
    $("#form-title").hidden = on;
    form.hidden = on;
  }

  async function enterForm(params, query) {
    const my = ++gen;
    editing = null;
    const newType = query ? query.get("type") : null;
    if (!params.uid && !NEW_TYPES.has(newType)) {
      // Step 1 of adding: pick the type, then the form opens with it set.
      showPicker(true);
      const first = $("#tx-type-picker .type-choice");
      if (first) first.focus();
      return;
    }
    showPicker(false);
    form.reset();
    App.clearErrors(form);
    App.hideConflict(conflictBox);
    $("#delete-btn").hidden = true; // shown once an existing transaction loads
    setStatus("");
    form.hidden = false;
    $("#submit-btn").disabled = false;
    $("#type").disabled = false;
    $("#type-hint").hidden = true;
    $("#balance_uid").disabled = false;
    $("#type").value = newType && NEW_TYPES.has(newType) ? newType : "expense";
    $("#note").value = "";
    $("#amount").value = "";
    $("#currency").value = "";
    $("#to_amount").value = "";
    $("#foreign_amount").value = "";
    $("#foreign_currency").value = "";
    $("#interest_amount").value = "";
    $("#fee_amount").value = "";
    $("#gain_loss").value = "";
    $("#sale_received").value = "";
    saleFrom = "gain";
    $("#entire_asset_sale").checked = false;
    fxManual = false;
    fxQuote = null;
    renderConversion(null);
    if (!params.uid) {
      $("#form-title").textContent = NEW_TITLE[currentType()] || "Add a transaction";
      $("#submit-btn").textContent = "Save transaction";
      await Promise.all([loadBalancesList(), loadCategoriesList(), App.fillCurrencySelect($("#currency")), App.fillCurrencySelect($("#foreign_currency"))]);
      if (my !== gen) return;
      applyType("", "");
      restoreDefaults();
      setNow();
      // Account comes first (issue #137): its currency fills in the currency
      // field. With a remembered account already selected, start at amount.
      if ($("#balance_uid").value) $("#amount").focus();
      else $("#balance_uid")._combo.input.focus();
      return;
    }
    $("#form-title").textContent = "Edit transaction";
    $("#submit-btn").textContent = "Save changes";
    form.hidden = true; // until loaded
    let e;
    try {
      const [res] = await Promise.all([fetch(`/api/transactions/${params.uid}`), loadBalancesList(), loadCategoriesList(), App.fillCurrencySelect($("#currency")), App.fillCurrencySelect($("#foreign_currency"))]);
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
    editing = e;
    form.hidden = false;
    const isAdj = SERVER_TYPES.has(e.type);
    const isBegin = e.type === "beginning_balance";
    const what = isBegin ? "beginning balance" : isAdj ? "balance adjustment" : "transaction";
    $("#form-title").textContent = `Edit ${what}: ${isAdj && e.adjustment_direction === "decrease" ? "−" : ""}${e.amount} ${e.currency}, ${shownTime(e.spent_at)}`;
    $("#amount").value = e.amount;
    App.setCurrency($("#currency"), e.currency);
    $("#type").value = e.type || "expense";
    // A balance adjustment (recorded by a manual balance edit) or beginning
    // balance (recorded with its balance) can be edited, but it keeps its
    // type; a beginning balance also stays on its balance.
    $("#type").disabled = isAdj;
    $("#type-hint").hidden = !isAdj;
    $("#type-hint").textContent = isBegin
      ? "A beginning balance keeps its type and balance. It must not be dated after the balance's other transactions; an earlier transaction moves it back."
      : "A balance adjustment keeps its type.";
    $("#balance_uid").disabled = isBegin;
    // Deletable from here too (balance adjustments included, issue #194);
    // a beginning balance goes only together with its balance.
    $("#delete-btn").hidden = isBegin;
    if (isAdj) $("#adjustment_direction").value = e.adjustment_direction === "decrease" ? "decrease" : "increase";
    // Stored conversion: shown as saved; it is kept unless the amount, date
    // or balances change (then the market rate is looked up again) or the
    // received amount is edited (manual).
    fxManual = e.rate_source === "manual";
    $("#interest_amount").value = e.interest_amount || "";
    $("#fee_amount").value = e.fee_amount || "";
    $("#gain_loss").value = e.gain_loss || "";
    saleFrom = "gain";
    $("#entire_asset_sale").checked = !!e.entire_asset_sale;
    applyType(e.balance_uid, e.to_balance_uid || "");
    fillCategories(currentType(), e.category_uid || "");
    $("#note").value = e.note || "";
    $("#foreign_amount").value = e.foreign_amount || "";
    App.setCurrency($("#foreign_currency"), e.foreign_currency || "");
    // Show the saved instant converted to the device's local time; saving
    // re-stamps it with the device's current offset.
    setSpentAt(new Date(e.spent_at));
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
    if (SERVER_TYPES.has(type)) payload.adjustment_direction = $("#adjustment_direction").value;
    if (categoryShown(type) && $("#category_uid").value) payload.category_uid = $("#category_uid").value;
    // Interest and fees of a debt repayment; omitting them on an edit clears
    // them (PUT is a full replace).
    if (type === "repay_debt") {
      const ia = $("#interest_amount").value.trim(), fa = $("#fee_amount").value.trim();
      if (ia) payload.interest_amount = ia;
      if (fa) payload.fee_amount = fa;
    }
    // Gain / loss and the entire-asset flag of an asset sale; omitted on an
    // edit means none / false (PUT is a full replace).
    if (type === "asset_sale") {
      const gl = $("#gain_loss").value.trim();
      if (gl) payload.gain_loss = gl;
      if ($("#entire_asset_sale").checked) payload.entire_asset_sale = true;
    }
    // Received amount: sent only when typed by the user; otherwise the server
    // converts with the market rate (and keeps a saved conversion on edit if
    // nothing it depends on changed).
    if (fxPair() && fxManual) payload.to_amount = $("#to_amount").value.trim();
    // Optional foreign-currency amount (reference only). Sent when either half
    // is filled so the server can report the missing one; omitting both on an
    // edit clears it (PUT is a full replace).
    if (FOREIGN_TYPES.has(type)) {
      const fa = $("#foreign_amount").value.trim(), fc = $("#foreign_currency").value.toUpperCase();
      if (fa) payload.foreign_amount = fa;
      if (fc) payload.foreign_currency = fc;
    }

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
      App.setFlash("transactions", `${isEdit ? "Updated" : "Saved"} ${body.amount} ${body.currency} (${TYPE_LABEL[body.type] || body.type}).`, body.uid);
      editing = null;
      App.returnToList(LIST, App.BALANCE_PAGE_RE);
    } catch (err) {
      setStatus("Network error: " + err.message, "bad");
    } finally {
      btn.disabled = false;
    }
  }

  function cancelForm() { App.returnToList(LIST, App.BALANCE_PAGE_RE); }

  // Delete the transaction being edited (Delete on the edit page).
  async function deleteFromForm() {
    const e = editing;
    if (!e) return;
    const acct = accountText(e) || "unknown account";
    const revert = e.type === "balance_adjustment"
      ? `\n\nThis balance adjustment ${e.adjustment_direction === "decrease" ? "lowered" : "raised"} ${acct}; deleting it reverts that change.` : "";
    if (!confirm(`Delete ${e.amount} ${e.currency} (${acct}) at ${shownTime(e.spent_at)}?${revert}`)) return;
    const btn = $("#delete-btn");
    btn.disabled = true;
    setStatus("");
    try {
      const res = await fetch(`/api/transactions/${encodeURIComponent(e.uid)}`, { method: "DELETE", headers: App.ifMatch(e.version) });
      if (res.status === 409) {
        App.showConflict(conflictBox,
          "This transaction was changed after you opened this page (in another tab or by someone else), so it was not deleted. Reload to see the latest values, then delete again if you still want to.",
          () => enterForm({ uid: e.uid }));
        setStatus("Not deleted: changed elsewhere.", "bad");
        return;
      }
      if (!res.ok && res.status !== 404) {
        setStatus(`Delete failed (${res.status})`, "bad");
        return;
      }
      App.setFlash("transactions", `Deleted ${e.amount} ${e.currency} (${TYPE_LABEL[e.type] || e.type}).`);
      editing = null;
      App.returnToList(LIST, App.BALANCE_PAGE_RE);
    } catch (err) {
      setStatus("Network error: " + err.message, "bad");
    } finally {
      btn.disabled = false;
    }
  }

  // ---- List page: /transactions ---------------------------------------------

  function filterParams() {
    const params = new URLSearchParams();
    const from = $("#filter-from").value;
    const to = $("#filter-to").value;
    // Date filters are whole days in this device's time zone.
    App.setDateRange(params, from, to);
    if ($("#filter-type").value) params.set("type", $("#filter-type").value);
    const q = $("#filter-q").value.trim();
    if (q) params.set("q", q);
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
  // Expenses and income are summed separately (returns on investment count as
  // income; the interest and fees of a debt repayment count as spent; the
  // gain of an asset sale as received and its loss as spent); transfers,
  // asset purchases, the principal of debt repayments and asset sales, and
  // balance adjustments (manual corrections, not spending or income) and
  // beginning balances are excluded.
  function renderTotals(items) {
    const sums = new Map(); // currency -> {scale, expense, income}
    for (const e of items) {
      const t = e.type || "expense";
      const gl = t === "asset_sale" ? BigInt(e.gain_loss_minor || 0) : 0n;
      const kind = t === "return_on_investment" ? "income" : t === "repay_debt" ? "expense"
        : t === "asset_sale" ? (gl < 0n ? "expense" : "income") : t;
      if (kind !== "expense" && kind !== "income") continue;
      const v = t === "repay_debt" ? BigInt(e.interest_amount_minor || 0) + BigInt(e.fee_amount_minor || 0)
        : t === "asset_sale" ? (gl < 0n ? -gl : gl) : BigInt(e.amount_minor);
      if ((t === "repay_debt" || t === "asset_sale") && !v) continue;
      const scale = (e.amount.split(".")[1] || "").length;
      const cur = sums.get(e.currency) || { scale, expense: 0n, income: 0n };
      cur[kind] += v;
      sums.set(e.currency, cur);
    }
    // One line per currency: "🇹🇭 THB  Spent 8,680.00  Received 66,200.00".
    totalsEl.replaceChildren();
    if (!sums.size) return;
    const title = document.createElement("div");
    title.className = "totals-title";
    title.textContent = "Totals of the loaded rows";
    title.title = "Transfers, asset purchases, the principal of debt repayments and asset sales, balance adjustments and beginning balances are excluded; returns on investment and asset sale gains count as received, debt interest and fees and asset sale losses as spent.";
    totalsEl.append(title);
    for (const [code, { scale, expense, income }] of sums) {
      const row = document.createElement("div");
      row.className = "totals-row";
      const cur = document.createElement("span");
      cur.className = "totals-cur";
      cur.textContent = App.currencyText(code);
      row.append(cur);
      for (const [label, v, cls] of [["Spent", expense, "amt-expense"], ["Received", income, "amt-income"]]) {
        const item = document.createElement("span");
        item.className = "totals-item";
        const l = document.createElement("span");
        l.className = "muted";
        l.textContent = label + " ";
        const n = document.createElement("strong");
        n.className = cls;
        n.textContent = fmtAmount(formatMinor(v, scale));
        item.append(l, n);
        row.append(item);
      }
      totalsEl.append(row);
    }
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
      // Hidden ones too: an edited transaction may use one (fillSelect keeps
      // them out of the choices otherwise).
      const res = await fetch("/api/balances?include_hidden=true");
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
  // add/edit round trips, reloads and the back button. Without a range in the
  // URL the list shows the date range last applied on any transaction list
  // (within 24 hours), else the current month (issue #183).
  async function enterList(_params, query) {
    const range = App.dateFilter(query);
    $("#filter-from").value = range.from;
    $("#filter-to").value = range.to;
    const ft = query.get("type") || "";
    $("#filter-type").value = [...$("#filter-type").options].some((o) => o.value === ft) ? ft : "";
    $("#filter-q").value = query.get("q") || "";
    const highlight = App.takeFlash("transactions");
    await loadTransactions(highlight);
  }

  function applyFilters() {
    App.rememberDateFilter($("#filter-from").value, $("#filter-to").value);
    const q = new URLSearchParams();
    if ($("#filter-from").value) q.set("from", $("#filter-from").value);
    if ($("#filter-to").value) q.set("to", $("#filter-to").value);
    if ($("#filter-type").value) q.set("type", $("#filter-type").value);
    if ($("#filter-q").value.trim()) q.set("q", $("#filter-q").value.trim());
    App.replaceQuery(q.toString());
    $("#flash-transactions").hidden = true;
    loadTransactions();
  }

  // One list row with Edit/Delete (balance adjustments too).
  function transactionRow(e, highlightUID) {
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
    const isAdj = SERVER_TYPES.has(t);
    const isBegin = t === "beginning_balance";
    const typeTd = cell(TYPE_LABEL[t] || t, "type type-" + t);
    const acctTd = cell(accountText(e));
    // An adjustment shows its direction as a sign: + the edited balance
    // or debt went up, − it went down.
    const fxRow = e.to_amount && e.to_currency;
    const amountTd = cell(isAdj ? (e.adjustment_direction === "decrease" ? "−" : "+") + fmtAmount(e.amount) : fxRow ? "" : fmtAmount(e.amount), "num amt-" + t);
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
    if (e.foreign_amount && e.foreign_currency) {
      // Reference only: shown under the amount, not part of any total.
      const fa = document.createElement("span");
      fa.className = "foreign-amt";
      fa.textContent = `${fmtAmount(e.foreign_amount)} ${e.foreign_currency}`;
      fa.title = `Foreign currency amount (for reference; not counted in balances or totals): ${e.foreign_amount} ${e.foreign_currency}`;
      amountTd.append(fa);
    }
    if (e.interest_amount || e.fee_amount) {
      // Debt repayment charges: paid on top of the principal (amount).
      const parts = [];
      if (e.interest_amount) parts.push(`${fmtAmount(e.interest_amount)} interest`);
      if (e.fee_amount) parts.push(`${fmtAmount(e.fee_amount)} fees`);
      const ch = document.createElement("span");
      ch.className = "foreign-amt";
      ch.textContent = "+ " + parts.join(" + ");
      ch.title = `Principal ${e.amount} ${e.currency}, paid on top: ${parts.join(", ")} (counted as spent)`;
      amountTd.append(ch);
    }
    if (e.gain_loss || e.entire_asset_sale) {
      // Asset sale: the principal is the amount; the gain or loss changes
      // what the account received.
      const loss = String(e.gain_loss || "").startsWith("-");
      const parts = [];
      if (e.gain_loss) parts.push(`${loss ? "−" : "+"} ${fmtAmount(String(e.gain_loss).replace(/^-/, ""))} ${loss ? "loss" : "gain"}`);
      if (e.entire_asset_sale) parts.push("entire asset");
      const gl = document.createElement("span");
      gl.className = "foreign-amt";
      gl.textContent = parts.join(", ");
      gl.title = `Principal ${e.amount} ${e.currency}` + (e.gain_loss ? `, ${loss ? "loss" : "gain"} ${String(e.gain_loss).replace(/^-/, "")} (counted as ${loss ? "spent" : "received"})` : "") +
        (e.entire_asset_sale ? "; the whole asset was sold" : "");
      amountTd.append(gl);
    }
    if (isAdj) {
      tr.classList.add("row-adjustment");
      amountTd.title = isBegin
        ? `Beginning balance: the balance started at ${e.adjustment_direction === "decrease" ? "−" : ""}${e.amount} ${e.currency}`
        : `Manual balance edit: ${e.adjustment_direction === "decrease" ? "decreased" : "increased"} by ${e.amount} ${e.currency}`;
    }
    if (fxRow) tr.classList.add("fx-row");
    acctTd.classList.add("acct");
    tr.append(timeTd, typeTd, amountTd, cell(fxRow ? `${App.currencyText(e.currency)} → ${App.currencyText(e.to_currency)}` : App.currencyText(e.currency), "cur"), acctTd, cell(categoryName(e.category_uid), "cat"), cell(e.note || "", "note"));
    const actions = document.createElement("td");
    actions.className = "actions-cell";
    actions.append(link("Edit", `/transactions/${encodeURIComponent(e.uid)}/edit`, "button edit"));
    // A beginning balance goes only together with its balance.
    if (!isBegin) actions.append(button("Delete", "danger", () => deleteTransaction(e)));
    tr.append(actions);
    return tr;
  }

  // GET one page of the list with the current filters; cursor = null for
  // the first page.
  async function fetchPage(cursor) {
    const params = filterParams();
    params.set("limit", String(PAGE_SIZE));
    if (cursor) params.set("cursor", cursor);
    const res = await fetch("/api/transactions?" + params);
    return { res, body: await res.json() };
  }

  function showListState() {
    const searching = $("#filter-q").value.trim() !== "";
    if (loaded.length === 0) {
      listStatus.textContent = searching ? "No transactions match the search." : "No transactions yet.";
    } else if (nextCursor) {
      listStatus.textContent = `Showing the ${loaded.length} most recent transactions, newest first.`;
    } else {
      listStatus.textContent = `${loaded.length} transaction(s), newest first.`;
    }
    loadMoreBtn.hidden = !nextCursor;
    loadMoreBtn.disabled = false;
    loadMoreBtn.textContent = "Load more";
    renderTotals(loaded);
  }

  async function loadTransactions(highlightUID) {
    const my = ++gen;
    listStatus.textContent = "Loading…";
    loadMoreBtn.hidden = true;
    try {
      const [{ res, body }] = await Promise.all([
        fetchPage(null),
        loadCategoriesList(),
        App.loadCurrencies(), // cached; flags for the Currency column
      ]);
      if (my !== gen) return;
      if (!res.ok) {
        listStatus.textContent = body.error + (body.fields ? ": " + Object.values(body.fields).join("; ") : "");
        return;
      }
      tbody.replaceChildren(...body.transactions.map((e) => transactionRow(e, highlightUID)));
      loaded = body.transactions.slice();
      nextCursor = body.next_cursor || null;
      showListState();
    } catch (err) {
      if (my === gen) listStatus.textContent = "Failed to load transactions: " + err.message;
    }
  }

  // Appends the next page. A filter change or reload meanwhile (gen moved
  // on) discards the result.
  async function loadMore() {
    if (!nextCursor) return;
    const my = gen;
    loadMoreBtn.disabled = true;
    loadMoreBtn.textContent = "Loading…";
    try {
      const { res, body } = await fetchPage(nextCursor);
      if (my !== gen) return;
      if (!res.ok) {
        listStatus.textContent = body.error + (body.fields ? ": " + Object.values(body.fields).join("; ") : "");
        loadMoreBtn.disabled = false;
        loadMoreBtn.textContent = "Load more";
        return;
      }
      tbody.append(...body.transactions.map((e) => transactionRow(e)));
      loaded = loaded.concat(body.transactions);
      nextCursor = body.next_cursor || null;
      showListState();
    } catch (err) {
      if (my !== gen) return;
      listStatus.textContent = "Failed to load more transactions: " + err.message;
      loadMoreBtn.disabled = false;
      loadMoreBtn.textContent = "Load more";
    }
  }
  loadMoreBtn.addEventListener("click", loadMore);

  async function deleteTransaction(e) {
    const acct = accountText(e) || "unknown account";
    const fxText = e.to_amount && e.to_currency ? ` → ${e.to_amount} ${e.to_currency}` : "";
    const revert = e.type === "balance_adjustment"
      ? `\n\nThis balance adjustment ${e.adjustment_direction === "decrease" ? "lowered" : "raised"} ${acct}; deleting it reverts that change.` : "";
    if (!confirm(`Delete ${e.amount} ${e.currency}${fxText} (${acct}) at ${shownTime(e.spent_at)}?${revert}`)) return;
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
  Combobox.enhance($("#category_uid"));
  Combobox.enhance($("#currency")); // issue #25; search matches code and name
  Combobox.enhance($("#foreign_currency"), { clearable: true });

  for (const inp of [spentDateInput, spentTimeInput]) {
    inp.addEventListener("input", updatePreview);
    // Clicking anywhere on the field opens the picker (where supported); it
    // opens on the date currently entered.
    inp.addEventListener("click", () => { try { inp.showPicker(); } catch (_) {} });
  }
  $("#type").addEventListener("change", () => applyType());
  // A transaction's currency must match its (source) balance: picking a
  // balance fills in its currency (still selectable; the server reports a
  // mismatch under the currency field).
  function currencyFromBalance() {
    const b = allBalances.find((x) => x.uid === $("#balance_uid").value);
    if (b) App.setCurrency($("#currency"), b.currency);
  }
  $("#balance_uid").addEventListener("change", () => { currencyFromBalance(); fxAuto(); updateDirectionHint(); });
  $("#adjustment_direction").addEventListener("change", updateDirectionHint);
  // Changing what the conversion depends on goes back to the market rate
  // (like the API: a PUT without to_amount refetches).
  function fxAuto() { fxManual = false; updateFx(); }
  $("#to_balance_uid").addEventListener("change", fxAuto);
  $("#currency").addEventListener("change", fxAuto);
  for (const id of ["#interest_amount", "#fee_amount"]) {
    $(id).addEventListener("input", () => { fillCategories(currentType()); updateChargesTotal(); });
  }
  $("#amount").addEventListener("input", updateChargesTotal);
  $("#amount").addEventListener("input", syncSale);
  $("#gain_loss").addEventListener("input", () => { saleFrom = "gain"; fillCategories(currentType()); syncSale(); });
  $("#sale_received").addEventListener("input", () => { saleFrom = "received"; syncSale(); });
  $("#entire_asset_sale").addEventListener("change", principalFromAsset);
  $("#to_balance_uid").addEventListener("change", principalFromAsset);
  $("#currency").addEventListener("change", updateChargesTotal);
  $("#currency").addEventListener("change", syncSale);
  $("#amount").addEventListener("input", () => { if (fxManual) { const p = fxPair(); if (p) showManualRate(p); } else updateFx(); });
  // The market rate depends on the date only.
  spentDateInput.addEventListener("change", () => { updatePreview(); if (!fxManual) updateFx(); });
  spentTimeInput.addEventListener("change", updatePreview);
  $("#to_amount").addEventListener("input", () => { fxManual = true; fxGen++; const p = fxPair(); if (p) showManualRate(p); });
  $("#fx-reset").addEventListener("click", () => { fxAuto(); updateFx(true); });
  // Picking a type replaces the picker in history, so Back (and Cancel /
  // Save, which return to the list) skip it.
  $("#tx-type-picker").addEventListener("click", (ev) => {
    if (ev.button !== 0 || ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) return;
    const a = ev.target.closest("a.type-choice");
    if (!a) return;
    ev.preventDefault();
    const u = new URL(a.href, location.href);
    App.navigate(u.pathname + u.search, { replace: true });
  });
  $("#now-btn").addEventListener("click", setNow);
  $("#cancel-btn").addEventListener("click", cancelForm);
  $("#delete-btn").addEventListener("click", deleteFromForm);
  form.addEventListener("submit", submitTransaction);
  $("#filter-form").addEventListener("submit", (ev) => { ev.preventDefault(); applyFilters(); });
  $("#filter-clear").addEventListener("click", () => {
    $("#filter-from").value = "";
    $("#filter-to").value = "";
    $("#filter-type").value = "";
    $("#filter-q").value = "";
    applyFilters();
  });

  const list = { view: "#view-transactions", section: "transactions", title: "", enter: enterList };
  App.route("/transactions", list);
  const formRoute = { view: "#view-transaction-form", section: "transactions", enter: enterForm, onEscape: cancelForm };
  App.route("/transactions/new", { ...formRoute, title: "Add transaction" }); // type picker, or the form with ?type=
  App.route("/transactions/:uid/edit", { ...formRoute, title: "Edit transaction" });
})();
