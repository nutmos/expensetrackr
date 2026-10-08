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
    syncTypeFields();
    App.clearErrors(form);
    setStatus("");
  }

  async function enterForm(params) {
    const my = ++gen;
    editing = null;
    resetForm();
    form.hidden = false;
    if (!params.uid) {
      $("#b-form-title").textContent = "Add a balance";
      $("#b-submit-btn").textContent = "Save balance";
      $("#b-name").focus();
      return;
    }
    $("#b-form-title").textContent = "Edit balance";
    $("#b-submit-btn").textContent = "Save changes";
    form.hidden = true; // until loaded
    try {
      const res = await fetch(`/api/balances/${params.uid}`);
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
    $("#b-currency").value = b.currency;
    $("#b-description").value = b.description || "";
    $("#b-balance").value = b.balance ?? "";
    $("#b-debt").value = b.debt ?? "";
    $("#b-limit").value = b.limit ?? "";
    syncTypeFields();
    $("#b-name").focus();
  }

  function payload() {
    const p = {
      name: $("#b-name").value.trim(),
      // The type is fixed after creation; an edit always sends the stored one.
      type: editing ? editing.type : typeSel.value,
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
    App.clearErrors(form);
    setStatus("");
    const isEdit = !!editing;
    const btn = $("#b-submit-btn");
    btn.disabled = true;
    try {
      const res = await fetch(isEdit ? `/api/balances/${editing.uid}` : "/api/balances", {
        method: isEdit ? "PUT" : "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload()),
      });
      const body = await res.json().catch(() => ({}));
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

  async function remove(b) {
    if (!confirm(`Delete balance “${b.name}” (${b.currency})?`)) return;
    const res = await fetch(`/api/balances/${b.uid}`, { method: "DELETE" });
    if (!res.ok && res.status !== 404) alert("Delete failed (" + res.status + ")");
    $("#flash-balances").hidden = true;
    await loadBalances();
  }

  function moneyCell(value) {
    const td = cell(value ?? "", "num");
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
        tr.dataset.uid = String(b.uid);
        if (highlightUID && b.uid === highlightUID) tr.classList.add("just-saved");
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
        actions.append(
          link("Edit", `/balances/${encodeURIComponent(b.uid)}/edit`, "button edit"),
          button("Delete", "danger", () => remove(b)),
        );
        tr.append(actions);
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
      tr.append(cell(t.currency), moneyCell(t.assets), moneyCell(t.liabilities), moneyCell(t.net),
        moneyCell(t.credit_limit), moneyCell(t.available_credit));
      totalsBody.append(tr);
    }
  }

  async function loadBalances(highlightUID) {
    const my = ++gen;
    listStatus.textContent = "Loading…";
    try {
      const res = await fetch("/api/balances");
      const body = await res.json();
      if (my !== gen) return;
      if (!res.ok) {
        listStatus.textContent = body.error || `Failed (${res.status})`;
        return;
      }
      renderGroups(body.balances, highlightUID);
      renderTotals(body.totals);
      listStatus.textContent = body.count === 0 ? "No balances yet." : `${body.count} balance(s).`;
    } catch (err) {
      if (my === gen) listStatus.textContent = "Failed to load balances: " + err.message;
    }
  }

  // ---- Init ----------------------------------------------------------------

  typeSel.addEventListener("change", syncTypeFields);
  form.addEventListener("submit", submit);
  $("#b-cancel-btn").addEventListener("click", cancelForm);
  syncTypeFields();

  App.route("/balances", { view: "#view-balances", section: "balances", title: "Balances", enter: () => loadBalances(App.takeFlash("balances")) });
  const formRoute = { view: "#view-balance-form", section: "balances", enter: enterForm, onEscape: cancelForm };
  App.route("/balances/new", { ...formRoute, title: "Add balance" });
  App.route("/balances/:uid/edit", { ...formRoute, title: "Edit balance" });
})();
