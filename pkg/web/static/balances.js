// Balances: list page (/balances), a balance's transactions (/balances/<uid>,
// issue #175) and add/edit page (/balances/new, /balances/<uid>/edit).
// Plain vanilla JS, no build step.
// All user data is rendered with textContent / input .value (never innerHTML).
(function () {
  "use strict";

  const { $, cell, button, link } = App;
  const form = $("#balance-form");
  const typeSel = $("#b-type");
  const statusEl = $("#b-form-status");
  const listStatus = $("#b-list-status");
  const groupsEl = $("#b-groups");
  const totalsBody = $("#b-totals tbody");
  const LIST = "/balances";

  const TYPES = [
    { id: "payment_account", label: "Payment accounts", kind: "asset" },
    { id: "credit_card", label: "Credit cards", kind: "liability" },
    { id: "other_asset", label: "Other assets", kind: "asset" },
    { id: "other_liability", label: "Other liabilities", kind: "liability" },
  ];
  const kindOf = (type) => (TYPES.find((t) => t.id === type) || {}).kind;

  let editing = null; // balance being edited (edit page), or null
  let gen = 0; // ignore responses for pages we already left

  const setStatus = (text, kind) => App.setStatus(statusEl, text, kind);
  const conflictBox = $("#b-conflict");
  const amountText = (b) => (kindOf(b.type) === "asset" ? `balance ${b.balance}` : `debt ${b.debt}, limit ${b.limit}`) + ` ${b.currency}`;

  // ---- Form page: /balances/new and /balances/<uid>/edit ----------------------

  // Show the amount fields for the selected type; hidden fields are not sent.
  function syncTypeFields() {
    const kind = kindOf(typeSel.value);
    form.querySelectorAll(".asset-only").forEach((el) => (el.hidden = kind !== "asset"));
    form.querySelectorAll(".liability-only").forEach((el) => (el.hidden = kind !== "liability"));
  }

  function resetForm() {
    form.reset();
    typeSel.value = "payment_account";
    typeSel.disabled = false;
    $("#b-type-note").hidden = true;
    $("#b-edit-hint").hidden = true;
    $("#b-new-hint").hidden = false;
    $("#b-delete-btn").hidden = true; // shown once an existing balance loads
    App.hideConflict(conflictBox);
    syncTypeFields();
    App.clearErrors(form);
    setStatus("");
  }

  // Default currency for a new balance: the signed-in user's base currency
  // from Settings (preferences.base_currency in GET /api/auth/me; the API
  // fills in its default when none was chosen). Read on every visit, so a
  // change in Settings applies at once. "" leaves the picker empty.
  async function defaultCurrency() {
    try {
      const res = await fetch("/api/auth/me");
      if (!res.ok) return "";
      const me = await res.json();
      return (me.preferences && me.preferences.base_currency) || "";
    } catch (_) { return ""; }
  }

  async function enterForm(params) {
    const my = ++gen;
    editing = null;
    resetForm();
    form.hidden = false;
    if (!params.uid) {
      $("#b-form-title").textContent = "Add a balance";
      $("#b-submit-btn").textContent = "Save balance";
      await Promise.all([App.fillCurrencySelect($("#b-currency")), defaultCurrency()]).then(([, cur]) => {
        if (my === gen && cur && !$("#b-currency").value) App.setCurrency($("#b-currency"), cur);
      });
      if (my !== gen) return;
      $("#b-name").focus();
      return;
    }
    $("#b-form-title").textContent = "Edit balance";
    $("#b-submit-btn").textContent = "Save changes";
    form.hidden = true; // until loaded
    try {
      const [res] = await Promise.all([fetch(`/api/balances/${params.uid}`), App.fillCurrencySelect($("#b-currency"))]);
      const b = await res.json().catch(() => ({}));
      if (my !== gen) return;
      if (!res.ok) {
        setStatus(res.status === 404 ? "This balance does not exist (it may have been deleted)." : b.error || `Could not load balance (${res.status})`, "bad");
        return;
      }
      editing = b;
    } catch (err) {
      if (my === gen) setStatus("Network error: " + err.message, "bad");
      return;
    }
    const b = editing;
    form.hidden = false;
    $("#b-form-title").textContent = `Edit balance “${b.name}”`;
    $("#b-name").value = b.name;
    typeSel.value = b.type;
    typeSel.disabled = true; // type is immutable once created
    $("#b-type-note").hidden = false;
    App.setCurrency($("#b-currency"), b.currency);
    $("#b-description").value = b.description || "";
    $("#b-balance").value = b.balance ?? "";
    $("#b-debt").value = b.debt ?? "";
    $("#b-limit").value = b.limit ?? "";
    $("#b-edit-hint").hidden = false;
    $("#b-new-hint").hidden = true;
    $("#b-delete-btn").hidden = false; // Delete lives here, not on the list (issue #226)
    syncTypeFields();
    $("#b-name").focus();
  }

  function payload() {
    const p = {
      name: $("#b-name").value.trim(),
      // The type is fixed after creation; an edit always sends the stored one.
      type: editing ? editing.type : typeSel.value,
      currency: $("#b-currency").value.toUpperCase(),
      description: $("#b-description").value.trim(),
    };
    if (kindOf(p.type) === "asset") {
      p.balance = $("#b-balance").value.trim();
    } else {
      p.debt = $("#b-debt").value.trim();
      p.limit = $("#b-limit").value.trim();
    }
    return p;
  }

  async function submit(ev) {
    ev.preventDefault();
    App.clearErrors(form);
    App.hideConflict(conflictBox);
    setStatus("");
    const isEdit = !!editing;
    const btn = $("#b-submit-btn");
    btn.disabled = true;
    try {
      // Edits send the version they were based on (If-Match); a change made
      // meanwhile (a transaction, another tab) is answered with 409.
      const res = await fetch(isEdit ? `/api/balances/${editing.uid}` : "/api/balances", {
        method: isEdit ? "PUT" : "POST",
        headers: { "Content-Type": "application/json", ...(isEdit ? App.ifMatch(editing.version) : {}) },
        body: JSON.stringify(payload()),
      });
      const body = await res.json().catch(() => ({}));
      if (isEdit && res.status === 409 && body.code === "version_conflict") {
        const cur = body.current || {};
        const uid = editing.uid;
        App.showConflict(conflictBox,
          `“${cur.name || editing.name}” was changed after you opened this page (for example by a transaction or in another tab), so your changes were not saved. ` +
          `It now has ${amountText(cur)}. Reload to see the latest values, then make your change again.`,
          () => enterForm({ uid }));
        setStatus("Not saved: changed elsewhere.", "bad");
        return;
      }
      if (!res.ok) {
        App.showFieldErrors(form, body.fields);
        let msg = body.error || `Request failed (${res.status})`;
        if (isEdit && res.status === 404) msg = "This balance no longer exists (it may have been deleted).";
        setStatus(msg, "bad");
        return;
      }
      App.setFlash("balances", `${isEdit ? "Updated" : "Saved"} “${body.name}”.`, body.uid);
      editing = null;
      App.returnToList(LIST, App.BALANCE_PAGE_RE);
    } catch (err) {
      setStatus("Network error: " + err.message, "bad");
    } finally {
      btn.disabled = false;
    }
  }

  function cancelForm() { App.returnToList(LIST, App.BALANCE_PAGE_RE); }

  // ---- Delete (edit page only, issue #226) ------------------------------------

  const deleteDialog = $("#b-delete-dialog");

  // How many transactions use the balance (they are deleted with it); null if
  // unknown. Counts up to the API's page limit, then reports "more than".
  async function transactionCount(b) {
    try {
      const res = await fetch(`/api/transactions?balance_uid=${encodeURIComponent(b.uid)}&limit=5000`);
      if (!res.ok) return null;
      const body = await res.json();
      return { n: body.count, more: !!body.next_cursor };
    } catch (_) { return null; }
  }

  // Asks how to remove b: resolves "hide", "delete" or "cancel".
  function askDelete(b, count) {
    const txText = count === null ? "all transactions that use it"
      : count.n === 0 ? "" : `${count.more ? "more than " : ""}${count.n} transaction${count.n === 1 && !count.more ? "" : "s"} that use${count.n === 1 && !count.more ? "s" : ""} it`;
    $("#b-delete-title").textContent = `Delete “${b.name}”?`;
    $("#b-delete-msg").textContent = txText
      ? `Deleting the balance also deletes ${txText}, and undoes their effect on your other balances (for example, a transfer from another account is put back there). This cannot be undone.`
      : "No transactions use this balance. Deleting it cannot be undone.";
    $("#b-delete-confirm").textContent = txText ? "Delete balance and its transactions" : "Delete balance";
    // Hiding is the alternative that keeps the transactions.
    $("#b-delete-hide").hidden = b.hidden;
    $("#b-delete-hide-note").hidden = b.hidden;
    return new Promise((resolve) => {
      deleteDialog.returnValue = "cancel";
      deleteDialog.addEventListener("close", () => resolve(deleteDialog.returnValue || "cancel"), { once: true });
      deleteDialog.showModal();
    });
  }

  // Delete (or hide) the balance being edited. A change made after the page
  // was opened (409) shows the conflict box and nothing is changed.
  async function deleteFromForm() {
    const b = editing;
    if (!b) return;
    const btn = $("#b-delete-btn");
    btn.disabled = true;
    App.hideConflict(conflictBox);
    setStatus("");
    try {
      const choice = await askDelete(b, await transactionCount(b));
      if (editing !== b || (choice !== "hide" && choice !== "delete")) return;
      const hide = choice === "hide";
      const res = await fetch(hide ? `/api/balances/${b.uid}/hide` : `/api/balances/${b.uid}`,
        { method: hide ? "POST" : "DELETE", headers: App.ifMatch(b.version) });
      const body = await res.json().catch(() => ({}));
      if (res.status === 409) {
        App.showConflict(conflictBox,
          `“${b.name}” was changed after you opened this page (for example by a transaction or in another tab), so it was not ${hide ? "hidden" : "deleted"}. ` +
          `Reload to see the latest values, then try again if you still want to.`,
          () => enterForm({ uid: b.uid }));
        setStatus(`Not ${hide ? "hidden" : "deleted"}: changed elsewhere.`, "bad");
        return;
      }
      if (!res.ok && res.status !== 404) {
        setStatus(body.error || `${hide ? "Hide" : "Delete"} failed (${res.status})`, "bad");
        return;
      }
      editing = null;
      if (hide) {
        App.setFlash("balances", `Hid “${b.name}”. Its transactions are kept; tick “Show hidden” to see it again.`, b.uid);
        App.returnToList(LIST, App.BALANCE_PAGE_RE);
        return;
      }
      const n = body.deleted_transactions || 0;
      App.setFlash("balances", `Deleted “${b.name}”` + (n ? ` and ${n} transaction${n === 1 ? "" : "s"}.` : "."));
      // Not back to the balance's own transactions page: it is gone.
      App.returnToList(LIST);
    } catch (err) {
      setStatus("Network error: " + err.message, "bad");
    } finally {
      btn.disabled = false;
    }
  }

  // ---- List page: /balances ---------------------------------------------------

  const showHidden = $("#b-show-hidden");

  // A 409 on a list action: the balance changed after the list was loaded.
  function staleAlert(b, what) {
    alert(`“${b.name}” changed after this list was loaded (for example by a transaction), so it was not ${what}. The list has been refreshed; check it and try again if you still want to.`);
  }

  // Hide (or show again) a balance; its transactions are kept.
  async function setHidden(b, hidden) {
    const res = await fetch(`/api/balances/${b.uid}/${hidden ? "hide" : "unhide"}`, { method: "POST", headers: App.ifMatch(b.version) });
    if (res.status === 409) staleAlert(b, hidden ? "hidden" : "shown");
    else if (!res.ok && res.status !== 404) alert(`${hidden ? "Hide" : "Show"} failed (${res.status})`);
    else if (res.ok) {
      App.setFlash("balances", hidden
        ? `Hid “${b.name}”. Its transactions are kept${showHidden.checked ? "" : "; tick “Show hidden” to see it again"}.`
        : `“${b.name}” is shown again.`, b.uid);
    }
    $("#flash-balances").hidden = true;
    await loadBalances(App.takeFlash("balances"));
  }

  // An amount cell (issue #188): negatives in brackets, never red; a
  // liability's debt (debt = true) in the debt colour, owed amounts positive.
  function moneyCell(value, debt) {
    return cell(App.fmtBalance(value ?? ""), debt ? "num debt-text" : "num");
  }

  function renderGroups(items, highlightUID) {
    groupsEl.replaceChildren();
    for (const t of TYPES) {
      const rows = items.filter((b) => b.type === t.id);
      const h = document.createElement("h3");
      h.textContent = `${t.label} (${rows.length})`;
      groupsEl.append(h);
      if (rows.length === 0) {
        const p = document.createElement("p");
        p.className = "group-empty";
        p.textContent = "None yet.";
        groupsEl.append(p);
        continue;
      }
      const table = document.createElement("table");
      table.className = "balance-table";
      table.dataset.type = t.id;
      const head = t.kind === "asset"
        ? ["Name", "Currency", "Balance", "Description", ""]
        : ["Name", "Currency", "Debt", "Limit", "Available", "Description", ""];
      const thead = document.createElement("thead");
      const htr = document.createElement("tr");
      head.forEach((label, i) => {
        const th = document.createElement("th");
        th.textContent = label;
        if (["Balance", "Debt", "Limit", "Available"].includes(label)) th.className = "num";
        if (label === "Name") th.className = "col-name";
        if (label === "Currency") th.className = "col-cur";
        if (label === "Description") th.className = "col-desc";
        if (i === head.length - 1) { th.className = "col-actions"; th.setAttribute("aria-label", "Actions"); }
        htr.append(th);
      });
      thead.append(htr);
      const tbody = document.createElement("tbody");
      for (const b of rows) {
        const tr = document.createElement("tr");
        tr.dataset.uid = String(b.uid);
        if (highlightUID && b.uid === highlightUID) tr.classList.add("just-saved");
        if (b.hidden) tr.classList.add("is-hidden");
        // The name opens the balance's transactions (issue #175).
        const nameTd = document.createElement("td");
        nameTd.append(link(b.name, `/balances/${encodeURIComponent(b.uid)}`, "balance-link"));
        if (b.hidden) {
          const badge = document.createElement("span");
          badge.className = "badge hidden-badge";
          badge.textContent = "hidden";
          badge.title = "Hidden: kept with its transactions, but left out of the list and the transaction form";
          nameTd.append(badge);
        }
        if (b.over_limit) {
          const badge = document.createElement("span");
          badge.className = "badge over";
          badge.textContent = "over limit";
          badge.title = "Debt is higher than the limit";
          nameTd.append(badge);
        }
        tr.append(nameTd, cell(App.currencyText(b.currency)));
        if (t.kind === "asset") {
          tr.append(moneyCell(b.balance));
        } else {
          tr.append(moneyCell(b.debt, true), moneyCell(b.limit), moneyCell(b.available));
        }
        tr.append(cell(b.description || "", "note"));
        const actions = document.createElement("td");
        actions.className = "actions-cell";
        actions.append(
          link("Edit", `/balances/${encodeURIComponent(b.uid)}/edit`, "button edit"),
          // Delete (and Hide, offered from its dialog) is on the edit page
          // (issue #226); only a hidden balance (listed with "Show hidden")
          // gets a button here, to show it again.
          ...(b.hidden ? [button("Show", "edit", () => setHidden(b, false))] : []),
        );
        tr.append(actions);
        // Column names for the narrow-screen card layout (style.css).
        [...tr.children].forEach((td, i) => { if (head[i]) td.dataset.label = head[i]; });
        tbody.append(tr);
      }
      table.append(thead, tbody);
      const wrap = document.createElement("div");
      wrap.className = "table-wrap";
      wrap.append(table);
      groupsEl.append(wrap);
    }
  }

  function renderTotals(totals) {
    totalsBody.replaceChildren();
    if (!totals.length) {
      const tr = document.createElement("tr");
      const td = cell("No balances yet.", "muted");
      td.colSpan = 6;
      tr.append(td);
      totalsBody.append(tr);
      return;
    }
    for (const t of totals) {
      const tr = document.createElement("tr");
      tr.append(cell(App.currencyText(t.currency)), moneyCell(t.assets), moneyCell(t.liabilities, true), moneyCell(t.net),
        moneyCell(t.credit_limit), moneyCell(t.available_credit));
      totalsBody.append(tr);
    }
    App.labelCells($("#b-totals"));
  }

  async function loadBalances(highlightUID) {
    const my = ++gen;
    listStatus.textContent = "Loading…";
    try {
      // Currencies (cached) first, so the list can show flags.
      const url = "/api/balances" + (showHidden.checked ? "?include_hidden=true" : "");
      const [res] = await Promise.all([fetch(url), App.loadCurrencies()]);
      const body = await res.json();
      if (my !== gen) return;
      if (!res.ok) {
        listStatus.textContent = body.error || `Failed (${res.status})`;
        return;
      }
      renderGroups(body.balances, highlightUID);
      renderTotals(body.totals);
      const hiddenN = body.balances.filter((b) => b.hidden).length;
      listStatus.textContent = body.count === 0
        ? (showHidden.checked ? "No balances yet." : "No balances shown (hidden ones are not listed).")
        : `${body.count} balance(s)` + (hiddenN ? `, ${hiddenN} hidden.` : ".");
    } catch (err) {
      if (my === gen) listStatus.textContent = "Failed to load balances: " + err.message;
    }
  }

  // ---- Transactions page: /balances/<uid> (issue #175) ------------------------
  // Every transaction that moved the balance (GET /api/reports/balance-
  // transactions), newest first, with money in / out and the amount after
  // each one. The only filter is a date range, kept in the URL
  // (?from=YYYY-MM-DD&to=YYYY-MM-DD); without one it defaults to the shared
  // remembered range or the current month (App.dateFilter).

  const bdBody = $("#bd-body");
  const bdStatus = $("#bd-status");
  const TYPE_NAME = { payment_account: "Payment account", credit_card: "Credit card", other_asset: "Other asset", other_liability: "Other liability" };
  const TX_TYPE_LABEL = {
    expense: "Expense", income: "Income", transfer: "Transfer", asset_purchase: "Asset Purchase",
    return_on_investment: "Return on Investment", asset_sale: "Asset Sale", repay_debt: "Repay Debt",
    balance_adjustment: "Balance Adjustment", beginning_balance: "Beginning Balance",
  };
  const pad2 = (n) => String(n).padStart(2, "0");
  // A local date ("YYYY-MM-DD") at h:m:s as RFC 3339 with the device's offset
  // for that date, so the range covers whole days in the viewer's time zone.
  function localRFC3339(ymd, h, m, sec) {
    const [y, mo, d] = ymd.split("-").map(Number);
    const t = new Date(y, mo - 1, d, h, m, sec);
    const off = -t.getTimezoneOffset();
    const sign = off >= 0 ? "+" : "-";
    return `${t.getFullYear()}-${pad2(t.getMonth() + 1)}-${pad2(t.getDate())}T${pad2(h)}:${pad2(m)}:${pad2(sec)}` +
      `${sign}${pad2(Math.floor(Math.abs(off) / 60))}:${pad2(Math.abs(off) % 60)}`;
  }
  const negText = App.fmtBalance;
  function rangeText(from, to) {
    // "06 Oct 2026": the date part of the app's DD MMM YYYY HH:mm format.
    const day = (v) => { const [y, m, d] = v.split("-").map(Number); return App.fmtDateTime(new Date(y, m - 1, d)).slice(0, 11); };
    if (from && to) return `${day(from)} – ${day(to)}`;
    if (from) return `from ${day(from)}`;
    if (to) return `until ${day(to)}`;
    return "all time";
  }

  let detailUID = null;

  async function enterDetail(params, query) {
    detailUID = params.uid;
    // URL range, else the range last applied on a transaction list (24 h),
    // else the current month (issue #183).
    const range = App.dateFilter(query);
    $("#bd-from").value = range.from;
    $("#bd-to").value = range.to;
    $("#bd-title").textContent = "Balance";
    $("#bd-meta").textContent = "";
    $("#bd-edit").hidden = true;
    // Back from editing one of its transactions (or the balance itself).
    const highlight = App.takeFlash(["transactions", "balances"], "balance-detail");
    return loadDetail(highlight);
  }

  async function loadDetail(highlightUID) {
    const my = ++gen;
    const uid = detailUID;
    const from = $("#bd-from").value, to = $("#bd-to").value;
    bdBody.replaceChildren();
    if (from && to && from > to) {
      bdStatus.textContent = "“To” must not be earlier than “From”.";
      return;
    }
    bdStatus.textContent = "Loading…";
    const q = new URLSearchParams({ balance_uid: uid });
    if (from) q.set("from", localRFC3339(from, 0, 0, 0));
    if (to) q.set("to", localRFC3339(to, 23, 59, 59));
    let res, body;
    try {
      [res] = await Promise.all([fetch("/api/reports/balance-transactions?" + q), App.loadCurrencies()]);
      body = await res.json().catch(() => ({}));
    } catch (err) {
      if (my === gen) bdStatus.textContent = "Network error: " + err.message;
      return;
    }
    if (my !== gen) return;
    if (!res.ok) {
      bdStatus.textContent = res.status === 404 ? "This balance does not exist (it may have been deleted)."
        : (body.fields && Object.values(body.fields).join("; ")) || body.error || `Could not load transactions (${res.status})`;
      return;
    }
    renderDetail(body, from, to, highlightUID);
  }

  function renderDetail(body, from, to, highlightUID) {
    const b = body.balance;
    const liability = b.kind === "liability";
    const what = liability ? "Debt" : "Balance";
    document.title = `${b.name} · Expense Log`;
    $("#bd-title").textContent = b.name;
    $("#bd-meta").textContent = `${TYPE_NAME[b.type] || b.type}${b.hidden ? " (hidden)" : ""} · ${App.currencyText(b.currency)} · ` +
      `${what} now ${negText(b.amount)}`;
    const edit = $("#bd-edit");
    edit.href = `/balances/${encodeURIComponent(b.uid)}/edit`;
    edit.hidden = false;

    const summary = document.createElement("dl");
    summary.className = "report-summary";
    const item = (label, value, cls) => {
      const d = document.createElement("div");
      const dt = document.createElement("dt");
      dt.className = "muted";
      dt.textContent = label;
      const dd = document.createElement("dd");
      if (cls) dd.className = cls;
      dd.textContent = value;
      d.append(dt, dd);
      summary.append(d);
    };
    item("Period", rangeText(from, to));
    const amtCls = liability ? "debt-text" : "";
    item(`Opening ${what.toLowerCase()}`, negText(body.opening_amount), amtCls);
    item(liability ? "In (payments)" : "Money in", App.fmtAmount(body.money_in), "pos-text");
    item(liability ? "Out (charges)" : "Money out", App.fmtAmount(body.money_out), "neg-text");
    item(`Closing ${what.toLowerCase()}`, negText(body.closing_amount), amtCls);
    bdBody.append(summary);

    const list = (body.transactions || []).slice().reverse(); // newest first
    bdStatus.textContent = list.length === 0
      ? (from || to ? "No transactions in this period." : "No transactions use this balance yet.")
      : `${list.length} transaction${list.length === 1 ? "" : "s"}, newest first.`;
    if (!list.length) return;

    const table = document.createElement("table");
    table.className = "statement stack-table";
    table.id = "bd-table";
    const thead = document.createElement("thead");
    const hr = document.createElement("tr");
    for (const [t, cls] of [["Date", ""], ["Type", ""], ["Details", ""], ["In", "num"], ["Out", "num"], [what, "num"], ["", "col-actions"]]) {
      const th = document.createElement("th");
      th.textContent = t;
      if (cls) th.className = cls;
      if (!t) th.setAttribute("aria-label", "Actions");
      hr.append(th);
    }
    thead.append(hr);
    const tbody = document.createElement("tbody");
    for (const e of list) {
      const tr = document.createElement("tr");
      tr.dataset.uid = String(e.uid);
      if (highlightUID && e.uid === highlightUID) tr.classList.add("just-saved");
      tr.append(cell(App.fmtDateTime(e.spent_at), "time"), cell(TX_TYPE_LABEL[e.type] || e.type, "type"));
      const det = document.createElement("td");
      det.className = "details";
      const span = (text, cls) => {
        const el = document.createElement("span");
        if (cls) el.className = cls;
        el.textContent = text;
        det.append(el);
      };
      const parts = [];
      if (e.category_name || e.category_uid) parts.push(e.category_name || "(unknown category)");
      if (e.counterparty) parts.push((e.money_in ? "from " : "to ") + e.counterparty);
      if (parts.length) span(parts.join(" · "));
      if (e.currency !== b.currency) span(`${App.fmtAmount(e.amount)} ${e.currency}`, "muted foreign-amt");
      if (e.note) span(e.note, "muted note-line");
      tr.append(det);
      tr.append(cell(e.money_in ? App.fmtAmount(e.money_in) : "", "num pos-text"));
      tr.append(cell(e.money_out ? App.fmtAmount(e.money_out) : "", "num neg-text"));
      tr.append(cell(negText(e.amount_after), liability ? "num debt-text" : "num"));
      const actions = document.createElement("td");
      actions.className = "actions-cell";
      actions.append(link("Edit", `/transactions/${encodeURIComponent(e.uid)}/edit`, "button edit"));
      // Delete here too (balance adjustments included, issue #194); a
      // beginning balance goes only together with its balance.
      if (e.type !== "beginning_balance") actions.append(button("Delete", "danger", () => deleteEntry(e, b)));
      tr.append(actions);
      tbody.append(tr);
    }
    table.append(thead, tbody);
    App.labelCells(table);
    const wrap = document.createElement("div");
    wrap.className = "table-wrap";
    wrap.append(table);
    bdBody.append(wrap);
  }

  // Deletes one of the balance's transactions and reloads the page. The
  // statement rows carry no version, so this DELETE is sent without If-Match.
  async function deleteEntry(e, b) {
    const adj = e.type === "balance_adjustment";
    const revert = adj
      ? `\n\nThis balance adjustment ${e.money_in ? (b.kind === "liability" ? "lowered the debt of" : "raised") : (b.kind === "liability" ? "raised the debt of" : "lowered")} ${b.name}; deleting it reverts that change.`
      : "";
    const what = adj ? "balance adjustment" : (TX_TYPE_LABEL[e.type] || e.type).toLowerCase();
    if (!confirm(`Delete this ${what} of ${e.amount} ${e.currency} at ${App.fmtDateTime(e.spent_at)}?${revert}`)) return;
    let res;
    try {
      res = await fetch(`/api/transactions/${encodeURIComponent(e.uid)}`, { method: "DELETE" });
    } catch (err) {
      alert("Network error: " + err.message);
      return;
    }
    if (!res.ok && res.status !== 404) {
      alert("Delete failed (" + res.status + ")");
      return;
    }
    App.setFlash("balances", `Deleted ${e.amount} ${e.currency} (${TX_TYPE_LABEL[e.type] || e.type}).`);
    await loadDetail(App.takeFlash(["balances"], "balance-detail"));
  }

  function applyDetailFilter() {
    App.rememberDateFilter($("#bd-from").value, $("#bd-to").value);
    const q = new URLSearchParams();
    if ($("#bd-from").value) q.set("from", $("#bd-from").value);
    if ($("#bd-to").value) q.set("to", $("#bd-to").value);
    App.replaceQuery(q.toString());
    $("#flash-balance-detail").hidden = true;
    loadDetail();
  }

  // ---- Init ----------------------------------------------------------------

  typeSel.addEventListener("change", syncTypeFields);
  Combobox.enhance($("#b-currency")); // issue #25: searchable currency
  form.addEventListener("submit", submit);
  $("#b-cancel-btn").addEventListener("click", cancelForm);
  $("#b-delete-btn").addEventListener("click", deleteFromForm);
  syncTypeFields();

  // "Show hidden" lives in the URL (?hidden=1) so it survives edits and reloads.
  showHidden.addEventListener("change", () => {
    App.replaceQuery(showHidden.checked ? "hidden=1" : "");
    $("#flash-balances").hidden = true;
    loadBalances();
  });
  App.route("/balances", {
    view: "#view-balances", section: "balances", title: "Balances",
    enter: (_params, query) => {
      showHidden.checked = query.get("hidden") === "1";
      return loadBalances(App.takeFlash("balances"));
    },
  });
  $("#bd-filter-form").addEventListener("submit", (ev) => { ev.preventDefault(); applyDetailFilter(); });
  $("#bd-clear").addEventListener("click", () => {
    $("#bd-from").value = "";
    $("#bd-to").value = "";
    applyDetailFilter();
  });
  // Registered after /balances/new so "new" is never taken for a uid.
  const formRoute = { view: "#view-balance-form", section: "balances", enter: enterForm, onEscape: cancelForm };
  App.route("/balances/new", { ...formRoute, title: "Add balance" });
  App.route("/balances/:uid/edit", { ...formRoute, title: "Edit balance" });
  App.route("/balances/:uid", {
    view: "#view-balance-detail", section: "balances", title: "Balance", enter: enterDetail,
    onEscape: () => App.returnToList(LIST),
  });
})();
