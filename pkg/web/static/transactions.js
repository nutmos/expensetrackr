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
    let text = v ? "Will be saved as " + v : "Enter a date and time";
    if (v && editing && v !== editing.spent_at) text += ` (was ${editing.spent_at})`;
    $("#spent-at-preview").textContent = text;
  }

  const setStatus = (text, kind) => App.setStatus(statusEl, text, kind);

  function rememberDefaults(currency, balanceUID) {
    try { localStorage.setItem(LS_KEY, JSON.stringify({ currency, balance_uid: balanceUID })); } catch (_) {}
  }

  function restoreDefaults() {
    try {
      const v = JSON.parse(localStorage.getItem(LS_KEY) || "{}");
      if (v.currency) $("#currency").value = v.currency;
      if (v.balance_uid) $("#balance_uid").value = v.balance_uid;
    } catch (_) {}
  }

  // Which balance types may be the source (balance_uid) for each type.
  const SOURCE_TYPES = {
    expense: ["payment_account", "credit_card"],
    income: ["payment_account", "other_asset"],
    transfer: ["payment_account", "credit_card", "other_asset", "other_liability"],
  };
  const SOURCE_LABEL = { expense: "Payment account", income: "Received into", transfer: "From (source)" };
  const SOURCE_HINT = {
    expense: "Payment accounts and credit cards.",
    income: "Payment accounts and other assets.",
    transfer: "Any balance.",
  };
  const KIND = { payment_account: "account", credit_card: "card", other_asset: "asset", other_liability: "liability" };

  function fillSelect(sel, types, keep) {
    sel.replaceChildren(Object.assign(document.createElement("option"), { value: "", textContent: "Select a balance…" }));
    for (const b of allBalances) {
      if (!types.includes(b.type)) continue;
      const opt = document.createElement("option");
      opt.value = b.uid;
      opt.textContent = `${b.name} (${b.currency}, ${KIND[b.type] || b.type})`;
      sel.append(opt);
    }
    if (keep && [...sel.options].some((o) => o.value === keep)) sel.value = keep;
  }

  function currentType() { return $("#type").value || "expense"; }

  // Re-render dropdowns for the selected type, keeping selections when valid.
  function applyType(keepFrom, keepTo) {
    const t = currentType();
    $("#balance-label").textContent = SOURCE_LABEL[t];
    $("#balance-hint").textContent = SOURCE_HINT[t];
    fillSelect($("#balance_uid"), SOURCE_TYPES[t], keepFrom ?? $("#balance_uid").value);
    $("#to-field").hidden = t !== "transfer";
    fillCategories(t);
    fillSelect($("#to_balance_uid"), SOURCE_TYPES.transfer, keepTo ?? $("#to_balance_uid").value);
  }

  // Category dropdown: only categories of the selected type; hidden for transfers.
  function fillCategories(t, keep) {
    const sel = $("#category_uid");
    const want = keep ?? sel.value;
    $("#category-field").hidden = t === "transfer";
    sel.replaceChildren(Object.assign(document.createElement("option"), { value: "", textContent: "No category" }));
    for (const c of allCategories) {
      if (c.type !== t) continue;
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
    setStatus("");
    form.hidden = false;
    $("#submit-btn").disabled = false;
    $("#type").value = "expense";
    $("#note").value = "";
    $("#amount").value = "";
    $("#currency").value = "";
    if (!params.uid) {
      $("#form-title").textContent = "Add a transaction";
      $("#submit-btn").textContent = "Save transaction";
      await Promise.all([loadBalancesList(), loadCategoriesList()]);
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
      const [res] = await Promise.all([fetch(`/api/transactions/${params.uid}`), loadBalancesList(), loadCategoriesList()]);
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
    $("#form-title").textContent = `Edit transaction ${e.uid.slice(0, 8)}…`;
    $("#amount").value = e.amount;
    $("#currency").value = e.currency;
    $("#type").value = e.type || "expense";
    applyType(e.balance_uid, e.to_balance_uid || "");
    fillCategories(currentType(), e.category_uid || "");
    $("#note").value = e.note || "";
    // Show the saved instant converted to the device's local time; saving
    // re-stamps it with the device's current offset.
    const d = new Date(e.spent_at);
    spentAtInput.value = isNaN(d) ? "" : toLocalInputValue(d);
    updatePreview();
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
      currency: $("#currency").value.trim().toUpperCase(),
      balance_uid: $("#balance_uid").value.trim(),
      spent_at: composedSpentAt() || "",
      note: $("#note").value.trim(),
    };
    if (type === "transfer") payload.to_balance_uid = $("#to_balance_uid").value.trim();
    else if ($("#category_uid").value) payload.category_uid = $("#category_uid").value;

    const isEdit = !!editing;
    const url = isEdit ? `/api/transactions/${editing.uid}` : "/api/transactions";
    const btn = $("#submit-btn");
    btn.disabled = true;
    try {
      const res = await fetch(url, {
        method: isEdit ? "PUT" : "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      const body = await res.json().catch(() => ({}));
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
  // Expenses and income are summed separately; transfers are excluded.
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
      parts.push(`${code}: expenses ${formatMinor(expense, scale)}, income ${formatMinor(income, scale)}`);
    }
    totalsEl.textContent = parts.length ? "Totals (shown rows, transfers excluded): " + parts.join(" · ") : "";
  }

  // Category name for a transaction, looked up client-side by category_uid
  // (transactions carry only the uid).
  function categoryName(uid) {
    if (!uid) return "";
    const c = allCategories.find((x) => x.uid === uid);
    return c ? c.name : "(unknown category)";
  }

  // Category name for a transaction, looked up client-side by category_uid
  // (transactions carry only the uid).
  function categoryName(uid) {
    if (!uid) return "";
    const c = allCategories.find((x) => x.uid === uid);
    return c ? c.name : "(unknown category)";
  }

  async function loadBalancesList() {
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
    const highlight = App.takeFlash("transactions");
    await loadTransactions(highlight);
  }

  function applyFilters() {
    const q = new URLSearchParams();
    if ($("#filter-from").value) q.set("from", $("#filter-from").value);
    if ($("#filter-to").value) q.set("to", $("#filter-to").value);
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
        const shown = new Date(e.spent_at);
        const timeTd = cell(isNaN(shown) ? e.spent_at : toLocalInputValue(shown).replace("T", " "), "time");
        timeTd.title = "Stored as " + e.spent_at + " (shown in your device's time zone)";
        if (e.updated_at) {
          const mark = document.createElement("span");
          mark.className = "edited";
          mark.textContent = "edited";
          mark.title = "Last edited " + new Date(e.updated_at).toLocaleString() + " (" + e.updated_at + ")";
          timeTd.append(mark);
        }
        if (e.uid) {
          const txUid = document.createElement("div");
          txUid.className = "uid muted";
          txUid.textContent = e.uid;
          txUid.title = "Transaction uid (stable id)";
          timeTd.append(txUid);
        }
        const t = e.type || "expense";
        const typeTd = cell(t, "type type-" + t);
        const acctTd = cell(t === "transfer" ? `${e.account || "?"} → ${e.to_account || "?"}` : (e.account || ""));
        if (e.balance_uid) {
          const uidEl = document.createElement("div");
          uidEl.className = "uid muted";
          uidEl.textContent = e.balance_uid;
          uidEl.title = "balance uid";
          acctTd.append(uidEl);
        }
        tr.append(timeTd, typeTd, cell(e.amount, "num"), cell(e.currency), acctTd, cell(categoryName(e.category_uid)), cell(e.note || "", "note"));
        const actions = document.createElement("td");
        actions.className = "actions-cell";
        actions.append(
          link("Edit", `/transactions/${encodeURIComponent(e.uid)}/edit`, "button edit"),
          button("Delete", "danger", () => deleteTransaction(e)),
        );
        tr.append(actions);
        tbody.append(tr);
      }
      listStatus.textContent = body.count === 0 ? "No transactions yet." : `${body.count} transaction(s), newest first.`;
      renderTotals(body.transactions);
    } catch (err) {
      if (my === gen) listStatus.textContent = "Failed to load transactions: " + err.message;
    }
  }

  async function deleteTransaction(e) {
    if (!confirm(`Delete ${e.amount} ${e.currency} (${e.account || e.balance_uid}) at ${e.spent_at}?`)) return;
    const res = await fetch(`/api/transactions/${e.uid}`, { method: "DELETE" });
    if (!res.ok && res.status !== 404) {
      alert("Delete failed (" + res.status + ")");
    }
    $("#flash-transactions").hidden = true;
    await loadTransactions();
  }

  // ---- Init ----------------------------------------------------------------

  let tzName = "";
  try { tzName = Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch (_) {}
  $("#tz-label").textContent = `(your device's time zone: ${tzName ? tzName + ", " : ""}UTC${offsetString(new Date())})`;
  spentAtInput.addEventListener("input", updatePreview);
  $("#type").addEventListener("change", () => applyType());
  $("#now-btn").addEventListener("click", setNow);
  $("#cancel-btn").addEventListener("click", cancelForm);
  form.addEventListener("submit", submitTransaction);
  $("#filter-form").addEventListener("submit", (ev) => { ev.preventDefault(); applyFilters(); });
  $("#filter-clear").addEventListener("click", () => {
    $("#filter-from").value = "";
    $("#filter-to").value = "";
    applyFilters();
  });

  const list = { view: "#view-transactions", section: "transactions", title: "", enter: enterList };
  App.route("/transactions", list);
  const formRoute = { view: "#view-transaction-form", section: "transactions", enter: enterForm, onEscape: cancelForm };
  App.route("/transactions/new", { ...formRoute, title: "Add transaction" });
  App.route("/transactions/:uid/edit", { ...formRoute, title: "Edit transaction" });
})();
