// Balances view + tab switching. Plain vanilla JS, no build step.
// All user data is rendered with textContent / input .value (never innerHTML).
(function () {
  "use strict";

  const $ = (sel) => document.querySelector(sel);
  const form = $("#balance-form");
  const formCard = form.closest(".card");
  const typeSel = $("#b-type");
  const statusEl = $("#b-form-status");
  const listStatus = $("#b-list-status");
  const groupsEl = $("#b-groups");
  const totalsBody = $("#b-totals tbody");

  const TYPES = [
    { id: "payment_account", label: "Payment accounts", kind: "asset" },
    { id: "credit_card", label: "Credit cards", kind: "liability" },
    { id: "other_asset", label: "Other assets", kind: "asset" },
    { id: "other_liability", label: "Other liabilities", kind: "liability" },
  ];
  const kindOf = (type) => (TYPES.find((t) => t.id === type) || {}).kind;

  let editing = null; // balance being edited, or null
  let loaded = false;

  // ---- Tabs ----------------------------------------------------------------

  function showView() {
    const view = location.hash === "#balances" ? "balances" : "expenses";
    $("#view-expenses").hidden = view !== "expenses";
    $("#view-balances").hidden = view !== "balances";
    $("#tab-expenses").classList.toggle("active", view === "expenses");
    $("#tab-balances").classList.toggle("active", view === "balances");
    $("#tab-expenses").setAttribute("aria-selected", String(view === "expenses"));
    $("#tab-balances").setAttribute("aria-selected", String(view === "balances"));
    document.title = view === "balances" ? "Balances · Expense Log" : "Expense Log";
    if (view === "balances" && !loaded) loadBalances();
  }
  window.addEventListener("hashchange", showView);

  // ---- Form ------------------------------------------------------------------

  // Show the amount fields for the selected type; hidden fields are not sent.
  function syncTypeFields() {
    const kind = kindOf(typeSel.value);
    form.querySelectorAll(".asset-only").forEach((el) => (el.hidden = kind !== "asset"));
    form.querySelectorAll(".liability-only").forEach((el) => (el.hidden = kind !== "liability"));
  }

  function clearErrors() {
    form.querySelectorAll(".err").forEach((el) => (el.textContent = ""));
    form.querySelectorAll("input, select").forEach((el) => el.classList.remove("invalid"));
  }

  function showFieldErrors(fields) {
    for (const [name, msg] of Object.entries(fields || {})) {
      const errEl = form.querySelector(`.err[data-for="${name}"]`);
      if (errEl) errEl.textContent = msg;
      const input = form.querySelector(`[name="${name}"]`);
      if (input) input.classList.add("invalid");
    }
  }

  function setStatus(text, kind) {
    statusEl.textContent = text;
    statusEl.className = kind || "";
  }

  function resetForm() {
    form.reset();
    typeSel.value = "payment_account";
    syncTypeFields();
    clearErrors();
  }

  function payload() {
    const p = {
      name: $("#b-name").value.trim(),
      type: typeSel.value,
      currency: $("#b-currency").value.trim().toUpperCase(),
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
    clearErrors();
    setStatus("");
    const isEdit = !!editing;
    const btn = $("#b-submit-btn");
    btn.disabled = true;
    try {
      const res = await fetch(isEdit ? `/api/balances/${editing.id}` : "/api/balances", {
        method: isEdit ? "PUT" : "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload()),
      });
      const body = await res.json().catch(() => ({}));
      if (!res.ok) {
        showFieldErrors(body.fields);
        let msg = body.error || `Request failed (${res.status})`;
        if (isEdit && res.status === 404) msg = "This balance no longer exists (it may have been deleted).";
        setStatus(msg, "bad");
        return;
      }
      exitEdit();
      resetForm();
      setStatus(`${isEdit ? "Updated" : "Saved"} “${body.name}”.`, "ok");
      $("#b-name").focus();
      await loadBalances();
    } catch (err) {
      setStatus("Network error: " + err.message, "bad");
    } finally {
      btn.disabled = false;
    }
  }

  async function startEdit(id) {
    clearErrors();
    setStatus("");
    const res = await fetch(`/api/balances/${id}`);
    const b = await res.json().catch(() => ({}));
    if (!res.ok) {
      setStatus(b.error || `Could not load balance (${res.status})`, "bad");
      if (res.status === 404) await loadBalances();
      return;
    }
    editing = b;
    $("#b-name").value = b.name;
    typeSel.value = b.type;
    $("#b-currency").value = b.currency;
    $("#b-description").value = b.description || "";
    $("#b-balance").value = b.balance ?? "";
    $("#b-debt").value = b.debt ?? "";
    $("#b-limit").value = b.limit ?? "";
    syncTypeFields();
    $("#b-form-title").textContent = `Edit balance “${b.name}”`;
    $("#b-submit-btn").textContent = "Save changes";
    $("#b-cancel-btn").hidden = false;
    formCard.classList.add("editing");
    highlightRow();
    formCard.scrollIntoView({ behavior: "smooth", block: "start" });
    $("#b-name").focus();
  }

  function exitEdit() {
    editing = null;
    $("#b-form-title").textContent = "Add a balance";
    $("#b-submit-btn").textContent = "Save balance";
    $("#b-cancel-btn").hidden = true;
    formCard.classList.remove("editing");
    highlightRow();
  }

  function cancelEdit() {
    if (!editing) return;
    exitEdit();
    resetForm();
    setStatus("Edit cancelled.");
  }

  async function remove(b) {
    if (!confirm(`Delete balance “${b.name}” (${b.currency})?`)) return;
    const res = await fetch(`/api/balances/${b.id}`, { method: "DELETE" });
    if (!res.ok && res.status !== 404) alert("Delete failed (" + res.status + ")");
    if (editing && editing.id === b.id) {
      exitEdit();
      resetForm();
      setStatus("The balance you were editing was deleted.");
    }
    await loadBalances();
  }

  // ---- List ----------------------------------------------------------------

  function cell(text, cls) {
    const td = document.createElement("td");
    td.textContent = text;
    if (cls) td.className = cls;
    return td;
  }

  function moneyCell(value) {
    const td = cell(value ?? "", "num");
    if (value && value.startsWith("-")) td.classList.add("neg");
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

  function highlightRow() {
    groupsEl.querySelectorAll("tr[data-id]").forEach((tr) => {
      tr.classList.toggle("editing", !!editing && tr.dataset.id === String(editing.id));
    });
  }

  function renderGroups(items) {
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
        if (i === head.length - 1) th.setAttribute("aria-label", "Actions");
        htr.append(th);
      });
      thead.append(htr);
      const tbody = document.createElement("tbody");
      for (const b of rows) {
        const tr = document.createElement("tr");
        tr.dataset.id = String(b.id);
        const nameTd = cell(b.name);
        if (b.over_limit) {
          const badge = document.createElement("span");
          badge.className = "badge over";
          badge.textContent = "over limit";
          badge.title = "Debt is higher than the limit";
          nameTd.append(badge);
        }
        if (b.uid) {
          const uidEl = document.createElement("div");
          uidEl.className = "uid muted";
          uidEl.textContent = b.uid;
          uidEl.title = "Stable id (uid)";
          nameTd.append(uidEl);
        }
        tr.append(nameTd, cell(b.currency));
        if (t.kind === "asset") {
          tr.append(moneyCell(b.balance));
        } else {
          tr.append(moneyCell(b.debt), moneyCell(b.limit), moneyCell(b.available));
        }
        tr.append(cell(b.description || "", "note"));
        const actions = document.createElement("td");
        actions.className = "actions-cell";
        actions.append(button("Edit", "edit", () => startEdit(b.id)), button("Delete", "danger", () => remove(b)));
        tr.append(actions);
        tbody.append(tr);
      }
      table.append(thead, tbody);
      const wrap = document.createElement("div");
      wrap.className = "table-wrap";
      wrap.append(table);
      groupsEl.append(wrap);
    }
    highlightRow();
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
      tr.append(cell(t.currency), moneyCell(t.assets), moneyCell(t.liabilities), moneyCell(t.net),
        moneyCell(t.credit_limit), moneyCell(t.available_credit));
      totalsBody.append(tr);
    }
  }

  async function loadBalances() {
    listStatus.textContent = "Loading…";
    try {
      const res = await fetch("/api/balances");
      const body = await res.json();
      if (!res.ok) {
        listStatus.textContent = body.error || `Failed (${res.status})`;
        return;
      }
      loaded = true;
      renderGroups(body.balances);
      renderTotals(body.totals);
      listStatus.textContent = body.count === 0 ? "No balances yet." : `${body.count} balance(s).`;
    } catch (err) {
      listStatus.textContent = "Failed to load balances: " + err.message;
    }
  }

  // ---- Init ----------------------------------------------------------------

  typeSel.addEventListener("change", syncTypeFields);
  form.addEventListener("submit", submit);
  $("#b-cancel-btn").addEventListener("click", cancelEdit);
  document.addEventListener("keydown", (ev) => {
    if (ev.key === "Escape" && !$("#view-balances").hidden) cancelEdit();
  });
  syncTypeFields();
  showView();
})();
