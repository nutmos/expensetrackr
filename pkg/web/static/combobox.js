// Searchable dropdown (combobox) built on top of a native <select>.
// The <select> stays in the DOM (hidden) and remains the source of truth:
// its value is what the form code reads/sets and what gets submitted, and it
// still fires "change". The combobox only shows option labels, never values.
// Usage: Combobox.enhance(selectEl, { clearable: true })
// Plain vanilla JS, no dependencies.
(function () {
  "use strict";

  const valueDesc = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value");
  let seq = 0;

  function enhance(sel, opts = {}) {
    if (sel._combo) return sel._combo;
    const id = "cb-" + (sel.id || ++seq);
    const clearable = !!opts.clearable;

    const wrap = document.createElement("div");
    wrap.className = "combo";
    const input = document.createElement("input");
    input.type = "text";
    input.id = id + "-input";
    input.className = "combo-input";
    input.autocomplete = "off";
    input.spellcheck = false;
    input.setAttribute("role", "combobox");
    input.setAttribute("aria-autocomplete", "list");
    input.setAttribute("aria-expanded", "false");
    input.setAttribute("aria-controls", id + "-list");
    if (sel.required) input.setAttribute("aria-required", "true");
    const toggle = document.createElement("button");
    toggle.type = "button";
    toggle.className = "combo-toggle";
    toggle.tabIndex = -1;
    toggle.setAttribute("aria-label", "Show options");
    toggle.textContent = "▾";
    const clear = document.createElement("button");
    clear.type = "button";
    clear.className = "combo-clear";
    clear.tabIndex = -1;
    clear.setAttribute("aria-label", "Clear selection");
    clear.textContent = "×";
    clear.hidden = true;
    const list = document.createElement("ul");
    list.id = id + "-list";
    list.className = "combo-list";
    list.setAttribute("role", "listbox");
    list.hidden = true;
    wrap.append(input, clear, toggle, list);
    // Insert before the select so the wrapping <label> labels the input
    // (a label's control is its first labelable descendant).
    sel.before(wrap);
    sel.hidden = true;
    sel.tabIndex = -1;
    sel.setAttribute("aria-hidden", "true");

    let shown = []; // [{value, text, el}]
    let active = -1;

    const options = () => [...sel.options];
    const selected = () => sel.options[sel.selectedIndex] || null;
    const placeholderText = () => { const o = options().find((x) => x.value === ""); return o ? o.textContent : ""; };

    function syncInput() {
      const o = selected();
      input.value = o && o.value !== "" ? o.textContent : "";
      input.placeholder = placeholderText();
      clear.hidden = !clearable || !o || o.value === "" || sel.disabled;
      input.disabled = sel.disabled;
      input.classList.toggle("invalid", sel.classList.contains("invalid"));
    }

    function setActive(i) {
      shown.forEach((s, j) => s.el.setAttribute("aria-selected", j === i ? "true" : "false"));
      shown.forEach((s, j) => s.el.classList.toggle("active", j === i));
      active = i;
      if (i >= 0 && shown[i]) {
        input.setAttribute("aria-activedescendant", shown[i].el.id);
        shown[i].el.scrollIntoView({ block: "nearest" });
      } else input.removeAttribute("aria-activedescendant");
    }

    function render(filter) {
      const q = (filter || "").trim().toLowerCase();
      list.replaceChildren();
      shown = [];
      options().forEach((o, idx) => {
        if (o.value === "" && (!clearable || q)) return; // placeholder only as "clear" choice
        if (q && !o.textContent.toLowerCase().includes(q)) return;
        const li = document.createElement("li");
        li.id = `${id}-opt-${idx}`;
        li.className = "combo-option" + (o.value === "" ? " combo-none" : "");
        li.setAttribute("role", "option");
        li.textContent = o.textContent;
        li.addEventListener("mousedown", (ev) => { ev.preventDefault(); choose(o.value); });
        list.append(li);
        shown.push({ value: o.value, el: li });
      });
      if (!shown.length) {
        const li = document.createElement("li");
        li.className = "combo-empty";
        li.setAttribute("role", "option");
        li.setAttribute("aria-disabled", "true");
        li.textContent = "No matches";
        list.append(li);
      }
      const cur = shown.findIndex((s) => s.value === sel.value && s.value !== "");
      setActive(q ? (shown.length ? 0 : -1) : cur);
    }

    function open(filter) {
      if (sel.disabled) return;
      render(filter);
      list.hidden = false;
      input.setAttribute("aria-expanded", "true");
    }

    function close() {
      list.hidden = true;
      input.setAttribute("aria-expanded", "false");
      input.removeAttribute("aria-activedescendant");
      active = -1;
    }

    function choose(value) {
      const changed = sel.value !== value;
      valueDesc.set.call(sel, value);
      close();
      syncInput();
      if (changed) sel.dispatchEvent(new Event("change", { bubbles: true }));
    }

    input.addEventListener("input", () => open(input.value));
    input.addEventListener("click", () => { if (list.hidden) open(""); });
    input.addEventListener("focus", () => input.select());
    input.addEventListener("keydown", (ev) => {
      const isOpen = !list.hidden;
      switch (ev.key) {
        case "ArrowDown":
          ev.preventDefault();
          if (!isOpen) open("");
          else if (shown.length) setActive(Math.min(active + 1, shown.length - 1));
          break;
        case "ArrowUp":
          ev.preventDefault();
          if (!isOpen) open("");
          else if (shown.length) setActive(Math.max(active - 1, 0));
          break;
        case "Enter":
          if (isOpen) {
            ev.preventDefault();
            if (active >= 0 && shown[active]) choose(shown[active].value);
          }
          break;
        case "Escape":
          if (isOpen) {
            ev.preventDefault();
            ev.stopPropagation(); // don't trigger the page's Escape = Cancel
            close();
            syncInput();
          }
          break;
        case "Tab":
          if (isOpen) close(); // blur then commits or restores
          break;
      }
    });
    input.addEventListener("blur", () => {
      // Clearing the text of an optional field clears the selection;
      // otherwise unfinished typing is discarded.
      if (clearable && input.value.trim() === "" && sel.value !== "") choose("");
      else { close(); syncInput(); }
    });
    toggle.addEventListener("mousedown", (ev) => {
      ev.preventDefault();
      if (list.hidden) { input.focus(); open(""); } else { close(); syncInput(); }
    });
    clear.addEventListener("mousedown", (ev) => { ev.preventDefault(); choose(""); input.focus(); });

    // Keep in sync with script changes: sel.value = ..., replaced options,
    // error class, disabled state, form.reset().
    Object.defineProperty(sel, "value", {
      configurable: true,
      get() { return valueDesc.get.call(this); },
      set(v) { valueDesc.set.call(this, v); syncInput(); },
    });
    new MutationObserver(() => { syncInput(); if (!list.hidden) render(input.value === (selected() || {}).textContent ? "" : input.value); })
      .observe(sel, { childList: true, subtree: true, attributes: true, attributeFilter: ["class", "disabled"] });
    if (sel.form) sel.form.addEventListener("reset", () => setTimeout(syncInput));

    syncInput();
    sel._combo = { input, sync: syncInput, open, close };
    return sel._combo;
  }

  window.Combobox = { enhance };
})();
