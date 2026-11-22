// Settings page (/settings): the signed-in user's own preferences, read and
// written through GET/PUT /api/users/:uid/preferences. The uid comes from
// /api/auth/me and is only used in the request URL, never shown. Below it,
// the API tokens section manages personal API tokens.
(function () {
  "use strict";
  const { $, clearErrors, showFieldErrors } = App;
  const form = $("#settings-form");
  const sel = $("#s-base-currency");
  let path = null; // /api/users/<uid>/preferences of the signed-in user
  let gen = 0;

  function setStatus(text, kind) { App.setStatus($("#s-form-status"), text, kind); }

  async function enter() {
    const my = ++gen;
    clearErrors(form);
    setStatus("");
    form.hidden = true;
    try {
      const me = await fetch("/api/auth/me").then((r) => (r.ok ? r.json() : Promise.reject(new Error(`status ${r.status}`))));
      path = `/api/users/${encodeURIComponent(me.uid)}/preferences`;
      const [res] = await Promise.all([fetch(path), App.fillCurrencySelect(sel)]);
      const prefs = await res.json().catch(() => ({}));
      if (my !== gen) return;
      if (!res.ok) {
        setStatus(prefs.error || `Could not load settings (${res.status})`, "bad");
        return;
      }
      App.setCurrency(sel, prefs.base_currency);
      form.hidden = false;
    } catch (err) {
      if (my === gen) setStatus("Could not load settings: " + err.message, "bad");
    }
  }

  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    clearErrors(form);
    if (!path) return;
    const btn = $("#s-submit-btn");
    btn.disabled = true;
    setStatus("Saving…");
    try {
      const res = await fetch(path, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ base_currency: sel.value }),
      });
      const body = await res.json().catch(() => ({}));
      if (res.ok) {
        App.setCurrency(sel, body.base_currency);
        setStatus("Settings saved.", "ok");
      } else {
        showFieldErrors(form, body.fields);
        setStatus(body.error || `Could not save (${res.status})`, "bad");
      }
    } catch (err) {
      setStatus("Network error: " + err.message, "bad");
    } finally {
      btn.disabled = false;
    }
  });

  Combobox.enhance(sel);

  // ---- API tokens (GET/POST /api/tokens, DELETE /api/tokens/:uid) ----------
  // Token uids are only used in request URLs, never shown. The plaintext is
  // shown once, right after creation. Dates are device local time.
  const tForm = $("#token-form");
  const tBody = $("#token-table tbody");
  const scopeLabels = { read: "Read", write: "Write" };

  function setTokenStatus(text, kind) { App.setStatus($("#t-form-status"), text, kind); }

  function shownDate(s) {
    if (!s) return "";
    return App.fmtDateTime(s);
  }

  function renderTokens(list) {
    tBody.replaceChildren();
    $("#t-empty").hidden = list.length > 0;
    $("#token-table").hidden = list.length === 0;
    for (const t of list) {
      const tr = document.createElement("tr");
      const actions = App.cell("", "actions-cell");
      actions.append(App.button("Revoke", "danger", () => revoke(t)));
      tr.append(
        App.cell(t.name),
        App.cell(t.prefix + "…", "mono"),
        App.cell(t.scopes.map((s) => scopeLabels[s] || s).join(", ")),
        App.cell(shownDate(t.created_at), "time"),
        App.cell(t.last_used_at ? shownDate(t.last_used_at) : "Never", "time"),
        App.cell(t.expires_at ? shownDate(t.expires_at) : "Never", "time"),
        actions,
      );
      tBody.append(tr);
    }
  }

  async function loadTokens() {
    try {
      const res = await fetch("/api/tokens");
      const body = await res.json().catch(() => ({}));
      if (!res.ok) {
        setTokenStatus(body.error || `Could not load tokens (${res.status})`, "bad");
        return;
      }
      renderTokens(body.tokens || []);
    } catch (err) {
      setTokenStatus("Could not load tokens: " + err.message, "bad");
    }
  }

  async function revoke(t) {
    if (!confirm(`Revoke token “${t.name}”? Scripts using it will stop working.`)) return;
    try {
      const res = await fetch(`/api/tokens/${encodeURIComponent(t.uid)}`, { method: "DELETE" });
      if (res.ok || res.status === 404) {
        setTokenStatus(`Token “${t.name}” revoked.`, "ok");
      } else {
        const body = await res.json().catch(() => ({}));
        setTokenStatus(body.error || `Could not revoke (${res.status})`, "bad");
      }
    } catch (err) {
      setTokenStatus("Network error: " + err.message, "bad");
    }
    loadTokens();
  }

  tForm.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    clearErrors(tForm);
    const btn = $("#t-submit-btn");
    const scope = $("#t-scopes").value;
    const days = $("#t-expiry").value;
    const payload = { name: $("#t-name").value.trim(), scopes: scope === "write" ? ["read", "write"] : ["read"] };
    if (days) payload.expires_in_days = Number(days);
    btn.disabled = true;
    setTokenStatus("Creating…");
    try {
      const res = await fetch("/api/tokens", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      const body = await res.json().catch(() => ({}));
      if (res.ok) {
        $("#t-new-value").value = body.token;
        $("#t-copy-status").textContent = "";
        $("#t-new").hidden = false;
        $("#t-name").value = "";
        setTokenStatus(`Token “${body.name}” created.`, "ok");
        loadTokens();
      } else {
        showFieldErrors(tForm, body.fields);
        setTokenStatus(body.error || `Could not create (${res.status})`, "bad");
      }
    } catch (err) {
      setTokenStatus("Network error: " + err.message, "bad");
    } finally {
      btn.disabled = false;
    }
  });

  $("#t-copy-btn").addEventListener("click", async () => {
    const input = $("#t-new-value");
    try {
      await navigator.clipboard.writeText(input.value);
      $("#t-copy-status").textContent = "Copied.";
    } catch {
      input.select();
      $("#t-copy-status").textContent = "Press Ctrl+C (⌘C) to copy.";
    }
  });

  Combobox.enhance($("#t-scopes"));
  Combobox.enhance($("#t-expiry"));

  async function enterSettings() {
    $("#t-new").hidden = true; // the secret is shown only once
    $("#t-new-value").value = "";
    clearErrors(tForm);
    setTokenStatus("");
    await Promise.all([enter(), loadTokens()]);
  }

  App.route("/settings", { view: "#view-settings", section: "settings", title: "Settings", enter: enterSettings });
})();
