// Categories: list page (/categories) and add/edit page (/categories/new,
// /categories/<uid>/edit). Plain vanilla JS, no build step.
// All user data is rendered with textContent / input .value (never innerHTML).
(function () {
  "use strict";

  const { $, cell, button, link } = App;
  const form = $("#category-form");
  const statusEl = $("#c-form-status");
  const listStatus = $("#c-list-status");
  const groupsEl = $("#c-groups");
  const LIST = "/categories";
  const TYPES = [
    { id: "expense", label: "Expense categories" },
    { id: "income", label: "Income categories" },
  ];

  let editing = null; // category being edited (edit page), or null
  let gen = 0; // ignore responses for pages we already left

  const setStatus = (text, kind) => App.setStatus(statusEl, text, kind);

  // ---- Form page: /categories/new and /categories/<uid>/edit -----------------

  async function enterForm(params, query) {
    const my = ++gen;
    editing = null;
    form.reset();
    App.clearErrors(form);
    setStatus("");
    form.hidden = false;
    if (!params.uid) {
      $("#c-form-title").textContent = "Add a category";
      $("#c-submit-btn").textContent = "Save category";
      if (query.get("type") === "income") $("#c-type").value = "income";
      $("#c-name").focus();
      return;
    }
    $("#c-form-title").textContent = "Edit category";
    $("#c-submit-btn").textContent = "Save changes";
    form.hidden = true; // until loaded
    let c;
    try {
      const res = await fetch(`/api/categories/${params.uid}`);
      c = await res.json().catch(() => ({}));
      if (my !== gen) return;
      if (!res.ok) {
        setStatus(res.status === 404 ? "This category does not exist (it may have been deleted)." : c.error || `Could not load category (${res.status})`, "bad");
        return;
      }
    } catch (err) {
      if (my === gen) setStatus("Network error: " + err.message, "bad");
      return;
    }
    editing = c;
    form.hidden = false;
    $("#c-form-title").textContent = `Edit category “${c.name}”`;
    $("#c-name").value = c.name;
    $("#c-type").value = c.type;
    $("#c-description").value = c.description || "";
    $("#c-name").focus();
  }

  async function submit(ev) {
    ev.preventDefault();
    App.clearErrors(form);
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
        App.showFieldErrors(form, body.fields);
        let msg = body.error || `Request failed (${res.status})`;
        if (isEdit && res.status === 404) msg = "This category no longer exists (it may have been deleted).";
        setStatus(msg, "bad");
        return;
      }
      App.setFlash("categories", `${isEdit ? "Updated" : "Saved"} “${body.name}”.`, body.uid);
      editing = null;
      App.returnToList(LIST);
    } catch (err) {
      setStatus("Network error: " + err.message, "bad");
    } finally {
      btn.disabled = false;
    }
  }

  function cancelForm() { App.returnToList(LIST); }

  // ---- List page: /categories -------------------------------------------------

  async function remove(c) {
    if (!confirm(`Delete ${c.type} category “${c.name}”?`)) return;
    const flashEl = $("#flash-categories");
    const res = await fetch(`/api/categories/${c.uid}`, { method: "DELETE" });
    if (!res.ok && res.status !== 404) {
      // e.g. 409: still used by transactions.
      const body = await res.json().catch(() => ({}));
      flashEl.textContent = body.error || `Delete failed (${res.status})`;
      flashEl.className = "flash bad";
      flashEl.hidden = false;
      return;
    }
    flashEl.textContent = `Deleted “${c.name}”.`;
    flashEl.className = "flash";
    flashEl.hidden = false;
    await loadCategories();
  }

  function render(items, highlightUID) {
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
        if (highlightUID && c.uid === highlightUID) tr.classList.add("just-saved");
        const nameTd = cell(c.name);
        const uidEl = document.createElement("div");
        uidEl.className = "uid muted";
        uidEl.textContent = c.uid;
        nameTd.append(uidEl);
        const actions = document.createElement("td");
        actions.className = "actions-cell";
        actions.append(
          link("Edit", `/categories/${encodeURIComponent(c.uid)}/edit`, "button edit"),
          button("Delete", "danger", () => remove(c)),
        );
        tr.append(nameTd, cell(c.description || "", "note"), actions);
        tbody.append(tr);
      }
      table.append(thead, tbody);
      const wrap = document.createElement("div");
      wrap.className = "table-wrap";
      wrap.append(table);
      groupsEl.append(wrap);
    }
  }

  async function loadCategories(highlightUID) {
    const my = ++gen;
    listStatus.textContent = "Loading…";
    try {
      const res = await fetch("/api/categories");
      const body = await res.json();
      if (my !== gen) return;
      if (!res.ok) {
        listStatus.textContent = body.error || `Failed (${res.status})`;
        return;
      }
      render(body.categories, highlightUID);
      listStatus.textContent = body.count === 0 ? "No categories yet." : `${body.count} categor${body.count === 1 ? "y" : "ies"}.`;
    } catch (err) {
      if (my === gen) listStatus.textContent = "Failed to load categories: " + err.message;
    }
  }

  // ---- Init ----------------------------------------------------------------

  form.addEventListener("submit", submit);
  $("#c-cancel-btn").addEventListener("click", cancelForm);

  App.route("/categories", { view: "#view-categories", section: "categories", title: "Categories", enter: () => loadCategories(App.takeFlash("categories")) });
  const formRoute = { view: "#view-category-form", section: "categories", enter: enterForm, onEscape: cancelForm };
  App.route("/categories/new", { ...formRoute, title: "Add category" });
  App.route("/categories/:uid/edit", { ...formRoute, title: "Edit category" });
})();
