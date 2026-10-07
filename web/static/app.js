// Expense Log front end: plain vanilla JS, no build step.
// All user data is rendered with textContent / input .value (never innerHTML).
(function () {
  "use strict";

  const $ = (sel) => document.querySelector(sel);
  const form = $("#transaction-form");
  const formCard = form.closest(".card");
  const spentAtInput = $("#spent_at");
  const offsetInput = $("#spent_offset");
  const statusEl = $("#form-status");
  const tbody = $("#transaction-table tbody");
  const listStatus = $("#list-status");
  const totalsEl = $("#totals");
  const LS_KEY = "expense-log:last";
  const OFFSET_RE = /^(Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;

  // Edit state: null when logging a new transaction, else the transaction being edited.
  let editing = null;
  // When true, the offset field follows the browser's offset for the chosen date.
  let offsetAuto = true;

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

  // Wall-clock part with seconds, from a datetime-local value.
  function wallClock(value) {
    const m = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2})(:\d{2})?/.exec(value);
    return m ? m[1] + (m[2] || ":00") : null;
  }

  // The RFC 3339 value the form would submit, or null if incomplete/invalid.
  function composedSpentAt() {
    const wall = wallClock(spentAtInput.value);
    const off = offsetInput.value.trim().toUpperCase();
    if (!wall || !OFFSET_RE.test(off)) return null;
    return wall + off;
  }

  function syncAutoOffset() {
    if (!offsetAuto) return;
    const d = parseLocalInput(spentAtInput.value) || new Date();
    offsetInput.value = offsetString(d);
  }

  function setNow() {
    spentAtInput.value = toLocalInputValue(new Date());
    offsetAuto = true;
    syncAutoOffset();
    updatePreview();
  }

  function updatePreview() {
    const v = composedSpentAt();
    let text = v ? "Will be saved as " + v : "Enter a date-time and an offset like +08:00";
    if (v && editing && v !== editing.spent_at) text += ` (was ${editing.spent_at})`;
    $("#spent-at-preview").textContent = text;
  }

  function clearErrors() {
    form.querySelectorAll(".err").forEach((el) => (el.textContent = ""));
    form.querySelectorAll("input").forEach((el) => el.classList.remove("invalid"));
  }

  function showFieldErrors(fields) {
    for (const [name, msg] of Object.entries(fields || {})) {
      const errEl = form.querySelector(`.err[data-for="${name}"]`);
      if (errEl) errEl.textContent = msg;
      const input = document.getElementById(name);
      if (input) input.classList.add("invalid");
      if (name === "spent_at") offsetInput.classList.add("invalid");
    }
  }

  function setStatus(text, kind) {
    statusEl.textContent = text;
    statusEl.className = kind || "";
  }

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

  async function loadPayableBalances(selected) {
    const sel = $("#balance_uid");
    const keep = selected || sel.value;
    try {
      const res = await fetch("/api/balances?payable=1");
      const body = await res.json();
      if (!res.ok) return;
      sel.replaceChildren(Object.assign(document.createElement("option"), { value: "", textContent: "Select a balance…" }));
      for (const b of body.balances || []) {
        const opt = document.createElement("option");
        opt.value = b.uid;
        const kind = b.type === "credit_card" ? "card" : "account";
        opt.textContent = `${b.name} (${b.currency}, ${kind})`;
        sel.append(opt);
      }
      if (keep && [...sel.options].some((o) => o.value === keep)) sel.value = keep;
    } catch (_) { /* leave existing options */ }
  }

  function highlightEditingRow() {
    tbody.querySelectorAll("tr").forEach((tr) => {
      tr.classList.toggle("editing", !!editing && tr.dataset.id === String(editing.id));
    });
  }

  // ---- Edit mode ---------------------------------------------------------

  async function startEdit(id) {
    clearErrors();
    setStatus("");
    let e;
    try {
      const res = await fetch(`/api/transactions/${id}`);
      const body = await res.json().catch(() => ({}));
      if (!res.ok) {
        setStatus(body.error || `Could not load transaction (${res.status})`, "bad");
        if (res.status === 404) await loadTransactions();
        return;
      }
      e = body;
    } catch (err) {
      setStatus("Network error: " + err.message, "bad");
      return;
    }

    editing = e;
    $("#amount").value = e.amount;
    $("#currency").value = e.currency;
    await loadPayableBalances(e.balance_uid);
    $("#balance_uid").value = e.balance_uid || "";
    $("#note").value = e.note || "";
    // Show the saved time in its ORIGINAL offset (not converted to browser time).
    const m = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(Z|[+-]\d{2}:\d{2})$/.exec(e.spent_at);
    if (m) {
      spentAtInput.value = m[1];
      offsetInput.value = m[2];
    } else {
      // Should not happen (server always stores this shape); fall back safely.
      const d = new Date(e.spent_at);
      spentAtInput.value = isNaN(d) ? "" : toLocalInputValue(d);
      offsetInput.value = isNaN(d) ? "" : offsetString(d);
    }
    offsetAuto = false;

    $("#form-title").textContent = `Edit transaction #${e.id}`;
    $("#submit-btn").textContent = "Save changes";
    $("#cancel-btn").hidden = false;
    formCard.classList.add("editing");
    updatePreview();
    highlightEditingRow();
    formCard.scrollIntoView({ behavior: "smooth", block: "start" });
    $("#amount").focus();
  }

  async function exitEdit(resetFields) {
    editing = null;
    $("#form-title").textContent = "Log a transaction";
    $("#submit-btn").textContent = "Save transaction";
    $("#cancel-btn").hidden = true;
    formCard.classList.remove("editing");
    clearErrors();
    if (resetFields) {
      $("#amount").value = "";
      $("#note").value = "";
      $("#currency").value = "";
      await loadPayableBalances();
      restoreDefaults();
      setNow();
    }
    highlightEditingRow();
  }

  async function cancelEdit() {
    if (!editing) return;
    await exitEdit(true);
    setStatus("Edit cancelled.");
  }

  // ---- Submit (create or update) ----------------------------------------

  async function submitTransaction(ev) {
    ev.preventDefault();
    clearErrors();
    setStatus("");

    const offset = offsetInput.value.trim().toUpperCase();
    if (!OFFSET_RE.test(offset)) {
      showFieldErrors({ spent_at: "UTC offset must look like +08:00, -05:00 or Z" });
      setStatus("validation failed", "bad");
      return;
    }
    const wall = wallClock(spentAtInput.value);
    const payload = {
      amount: $("#amount").value.trim(),
      currency: $("#currency").value.trim().toUpperCase(),
      balance_uid: $("#balance_uid").value.trim(),
      spent_at: wall ? wall + offset : "",
      note: $("#note").value.trim(),
    };

    const isEdit = !!editing;
    const url = isEdit ? `/api/transactions/${editing.id}` : "/api/transactions";
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
        showFieldErrors(body.fields);
        let msg = body.error || `Request failed (${res.status})`;
        if (isEdit && res.status === 404) msg = "This transaction no longer exists (it may have been deleted).";
        setStatus(msg, "bad");
        return;
      }
      if (isEdit) {
        await exitEdit(true);
        setStatus(`Updated transaction #${body.id}: ${body.amount} ${body.currency}.`, "ok");
      } else {
        rememberDefaults(payload.currency, payload.balance_uid);
        setStatus(`Saved ${body.amount} ${body.currency}.`, "ok");
        $("#amount").value = "";
        $("#note").value = "";
        setNow();
      }
      $("#amount").focus();
      await loadTransactions();
    } catch (err) {
      setStatus("Network error: " + err.message, "bad");
    } finally {
      btn.disabled = false;
    }
  }

  // ---- List ----------------------------------------------------------------

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

  function cell(text, cls) {
    const td = document.createElement("td");
    td.textContent = text;
    if (cls) td.className = cls;
    return td;
  }

  // Sum amounts per currency exactly using BigInt minor units.
  function renderTotals(items) {
    const sums = new Map();
    for (const e of items) {
      const scale = (e.amount.split(".")[1] || "").length;
      const cur = sums.get(e.currency) || { minor: 0n, scale };
      cur.minor += BigInt(e.amount_minor);
      sums.set(e.currency, cur);
    }
    const parts = [];
    for (const [code, { minor, scale }] of sums) {
      let s = minor.toString();
      if (scale > 0) {
        s = s.padStart(scale + 1, "0");
        s = s.slice(0, -scale) + "." + s.slice(-scale);
      }
      parts.push(`${s} ${code}`);
    }
    totalsEl.textContent = parts.length ? "Totals (shown rows): " + parts.join(" · ") : "";
  }

  function button(text, cls, onClick) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = cls;
    b.textContent = text;
    b.addEventListener("click", onClick);
    return b;
  }

  async function loadTransactions() {
    listStatus.textContent = "Loading…";
    try {
      const params = filterParams();
      const res = await fetch("/api/transactions" + (params.toString() ? "?" + params : ""));
      const body = await res.json();
      if (!res.ok) {
        listStatus.textContent = body.error + (body.fields ? ": " + Object.values(body.fields).join("; ") : "");
        return;
      }
      tbody.replaceChildren();
      for (const e of body.transactions) {
        const tr = document.createElement("tr");
        tr.dataset.id = String(e.id);
        const timeTd = cell(e.spent_at.replace("T", " "), "time");
        timeTd.title = "Stored as " + e.spent_at;
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
        const acctTd = cell(e.account || "");
        if (e.balance_uid) {
          const uidEl = document.createElement("div");
          uidEl.className = "uid muted";
          uidEl.textContent = e.balance_uid;
          uidEl.title = "balance uid";
          acctTd.append(uidEl);
        }
        tr.append(timeTd, cell(e.amount, "num"), cell(e.currency), acctTd, cell(e.note || "", "note"));
        const actions = document.createElement("td");
        actions.className = "actions-cell";
        actions.append(
          button("Edit", "edit", () => startEdit(e.id)),
          button("Delete", "danger", () => deleteTransaction(e)),
        );
        tr.append(actions);
        tbody.append(tr);
      }
      listStatus.textContent = body.count === 0 ? "No transactions yet." : `${body.count} transaction(s), newest first.`;
      renderTotals(body.transactions);
      highlightEditingRow();
    } catch (err) {
      listStatus.textContent = "Failed to load transactions: " + err.message;
    }
  }

  async function deleteTransaction(e) {
    if (!confirm(`Delete ${e.amount} ${e.currency} (${e.account || e.balance_uid}) at ${e.spent_at}?`)) return;
    const res = await fetch(`/api/transactions/${e.id}`, { method: "DELETE" });
    if (!res.ok && res.status !== 404) {
      alert("Delete failed (" + res.status + ")");
    }
    if (editing && editing.id === e.id) {
      await exitEdit(true);
      setStatus("The transaction you were editing was deleted.");
    }
    await loadTransactions();
  }

  // ---- Init ----------------------------------------------------------------

  let tzName = "";
  try { tzName = Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch (_) {}
  $("#tz-label").textContent = `(your zone: ${tzName ? tzName + ", " : ""}UTC${offsetString(new Date())})`;
  window.loadPayableBalances = loadPayableBalances;
  loadPayableBalances().then(restoreDefaults);
  setNow();
  spentAtInput.addEventListener("input", () => { syncAutoOffset(); updatePreview(); });
  offsetInput.addEventListener("input", () => { offsetAuto = false; updatePreview(); });
  $("#now-btn").addEventListener("click", setNow);
  $("#cancel-btn").addEventListener("click", cancelEdit);
  document.addEventListener("keydown", (ev) => {
    if (ev.key === "Escape" && !$("#view-transactions").hidden) cancelEdit();
  });
  form.addEventListener("submit", submitTransaction);
  $("#filter-form").addEventListener("submit", (ev) => { ev.preventDefault(); loadTransactions(); });
  $("#filter-clear").addEventListener("click", () => {
    $("#filter-from").value = "";
    $("#filter-to").value = "";
    loadTransactions();
  });
  loadTransactions();
})();
