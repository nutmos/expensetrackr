// Categories view. Plain vanilla JS, no build step.
// All user data is rendered with textContent / input .value (never innerHTML).
(function () {
  "use strict";

  const $ = (sel) => document.querySelector(sel);
  const form = $("#category-form");
  const formCard = form.closest(".card");
  const statusEl = $("#c-form-status");
  const listStatus = $("#c-list-status");
  const groupsEl = $("#c-groups");
  const TYPES = [
    { id: "expense", label: "Expense categories" },
    { id: "income", label: "Income categories" },
  ];

  let editing = null; // category being edited, or null

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

  function notifyTransactions() {
    if (typeof window.loadTxCategories === "function") window.loadTxCategories();
  }

  function exitEdit() {
    editing = null;
    $("#c-form-title").textContent = "Add a category";
    $("#c-submit-btn").textContent = "Save category";
    $("#c-cancel-btn").hidden = true;
    formCard.classList.remove("editing");
    form.reset();
    clearErrors();
    highlightRow();
  }

  async function submit(ev) {
    ev.preventDefault();
    clearErrors();
    setStatus("");
    const isEdit = !!editing;
    const payload = {
      name: $("#c-name").value.trim(),
      type: $("#c-type").value,
      description: $("#c-description").value.trim(),
    };
    const btn = $("#c-submit-btn");
    btn.disabled = true;
    try {
      const res = await fetch(isEdit ? `/api/categories/${editing.uid}` : "/api/categories", {
        method: isEdit ? "PUT" : "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      const body = await res.json().catch(() => ({}));
      if (!res.ok) {
        showFieldErrors(body.fields);
        let msg = body.error || `Request failed (${res.status})`;
        if (isEdit && res.status === 404) msg = "This category no longer exists (it may have been deleted).";
        setStatus(msg, "bad");
        return;
      }
      exitEdit();
      setStatus(`${isEdit ? "Updated" : "Saved"} “${body.name}”.`, "ok");
      $("#c-name").focus();
      await loadCategories();
      notifyTransactions();
    } catch (err) {
      setStatus("Network error: " + err.message, "bad");
    } finally {
      btn.disabled = false;
    }
  }

  async function startEdit(uid) {
    clearErrors();
    setStatus("");
    const res = await fetch(`/api/categories/${uid}`);
    const c = await res.json().catch(() => ({}));
    if (!res.ok) {
      setStatus(c.error || `Could not load category (${res.status})`, "bad");
      if (res.status === 404) await loadCategories();
      return;
    }
    editing = c;
    $("#c-name").value = c.name;
    $("#c-type").value = c.type;
    $("#c-description").value = c.description || "";
    $("#c-form-title").textContent = `Edit category “${c.name}”`;
    $("#c-submit-btn").textContent = "Save changes";
    $("#c-cancel-btn").hidden = false;
    formCard.classList.add("editing");
    highlightRow();
    formCard.scrollIntoView({ behavior: "smooth", block: "start" });
    $("#c-name").focus();
  }

  function cancelEdit() {
    if (!editing) return;
    exitEdit();
    setStatus("Edit cancelled.");
  }

  async function remove(c) {
    if (!confirm(`Delete ${c.type} category “${c.name}”?`)) return;
    setStatus("");
    const res = await fetch(`/api/categories/${c.uid}`, { method: "DELETE" });
    if (!res.ok && res.status !== 404) {
      const body = await res.json().catch(() => ({}));
      setStatus(body.error || `Delete failed (${res.status})`, "bad");
      return;
    }
    if (editing && editing.uid === c.uid) exitEdit();
    setStatus(`Deleted “${c.name}”.`, "ok");
    await loadCategories();
    notifyTransactions();
  }

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

  function highlightRow() {
    groupsEl.querySelectorAll("tr[data-uid]").forEach((tr) => {
      tr.classList.toggle("editing", !!editing && tr.dataset.uid === editing.uid);
    });
  }

  function render(items) {
    groupsEl.replaceChildren();
    for (const t of TYPES) {
      const rows = items.filter((c) => c.type === t.id);
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
      const thead = document.createElement("thead");
      const htr = document.createElement("tr");
      for (const label of ["Name", "Description", ""]) {
        const th = document.createElement("th");
        th.textContent = label;
        htr.append(th);
      }
      thead.append(htr);
      const tbody = document.createElement("tbody");
      for (const c of rows) {
        const tr = document.createElement("tr");
        tr.dataset.uid = c.uid;
        const nameTd = cell(c.name);
        const uidEl = document.createElement("div");
        uidEl.className = "uid muted";
        uidEl.textContent = c.uid;
        nameTd.append(uidEl);
        const actions = document.createElement("td");
        actions.className = "actions-cell";
        actions.append(button("Edit", "edit", () => startEdit(c.uid)), button("Delete", "danger", () => remove(c)));
        tr.append(nameTd, cell(c.description || "", "note"), actions);
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

  async function loadCategories() {
    listStatus.textContent = "Loading…";
    try {
      const res = await fetch("/api/categories");
      const body = await res.json();
      if (!res.ok) {
        listStatus.textContent = body.error || `Failed (${res.status})`;
        return;
      }
      render(body.categories);
      listStatus.textContent = body.count === 0 ? "No categories yet." : `${body.count} categor${body.count === 1 ? "y" : "ies"}.`;
    } catch (err) {
      listStatus.textContent = "Failed to load categories: " + err.message;
    }
  }

  window.loadCategoriesView = loadCategories;
  form.addEventListener("submit", submit);
  $("#c-cancel-btn").addEventListener("click", cancelEdit);
  document.addEventListener("keydown", (ev) => {
    if (ev.key === "Escape" && !$("#view-categories").hidden) cancelEdit();
  });
})();
