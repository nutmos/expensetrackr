// Balances: list page (/balances) and add/edit page (/balances/new,
// /balances/<uid>/edit). Plain vanilla JS, no build step.
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
      App.returnToList(LIST);
    } catch (err) {
      setStatus("Network error: " + err.message, "bad");
    } finally {
      btn.disabled = false;
    }
  }

  function cancelForm() { App.returnToList(LIST); }

  // ---- List page: /balances ---------------------------------------------------

  const showHidden = $("#b-show-hidden");
  const deleteDialog = $("#b-delete-dialog");

  // A 409 on a list action: the balance changed after the list was loaded.
  function staleAlert(b, what) {
    alert(`“${b.name}” changed after this list was loaded (for example by a transaction), so it was not ${what}. The list has been refreshed; check it and try again if you still want to.`);
  }

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

  async function remove(b) {
    const choice = await askDelete(b, await transactionCount(b));
    if (choice === "hide") return setHidden(b, true);
    if (choice !== "delete") return;
    const res = await fetch(`/api/balances/${b.uid}`, { method: "DELETE", headers: App.ifMatch(b.version) });
    const body = await res.json().catch(() => ({}));
    if (res.status === 409) staleAlert(b, "deleted");
    else if (!res.ok && res.status !== 404) alert("Delete failed (" + res.status + ")");
    else if (res.ok) {
      const n = body.deleted_transactions || 0;
      App.setFlash("balances", `Deleted “${b.name}”` + (n ? ` and ${n} transaction${n === 1 ? "" : "s"}.` : "."));
    }
    $("#flash-balances").hidden = true;
    await loadBalances(App.takeFlash("balances"));
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

  function moneyCell(value) {
    const td = cell(App.fmtAmount(value ?? ""), "num");
    if (value && value.startsWith("-")) td.classList.add("neg");
    return td;
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
        const nameTd = cell(b.name);
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
          tr.append(moneyCell(b.debt), moneyCell(b.limit), moneyCell(b.available));
        }
        tr.append(cell(b.description || "", "note"));
        const actions = document.createElement("td");
        actions.className = "actions-cell";
        actions.append(
          link("Edit", `/balances/${encodeURIComponent(b.uid)}/edit`, "button edit"),
          b.hidden ? button("Show", "edit", () => setHidden(b, false)) : button("Hide", "edit", () => setHidden(b, true)),
          button("Delete", "danger", () => remove(b)),
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
      tr.append(cell(App.currencyText(t.currency)), moneyCell(t.assets), moneyCell(t.liabilities), moneyCell(t.net),
        moneyCell(t.credit_limit), moneyCell(t.available_credit));
      totalsBody.append(tr);
    }
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

  // ---- Init ----------------------------------------------------------------

  typeSel.addEventListener("change", syncTypeFields);
  Combobox.enhance($("#b-currency")); // issue #25: searchable currency
  form.addEventListener("submit", submit);
  $("#b-cancel-btn").addEventListener("click", cancelForm);
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
  const formRoute = { view: "#view-balance-form", section: "balances", enter: enterForm, onEscape: cancelForm };
  App.route("/balances/new", { ...formRoute, title: "Add balance" });
  App.route("/balances/:uid/edit", { ...formRoute, title: "Edit balance" });
})();
