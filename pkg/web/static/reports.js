// Reports page (issue #136): /reports/<id>, read-only summaries drawn as
// horizontal bar charts (plain DOM; bar widths set through el.style, which
// the Content Security Policy allows). Each currency is its own section:
// amounts are never converted between currencies.
//
// To add a report: add it to REPORTS (id = the API's report id, the URL is
// /reports/<id>), add its #report-<id> block to index.html and its id to
// report.Catalog in pkg/report.
(function () {
  "use strict";

  const { $, fmtAmount } = App;

  const REPORTS = [
    { id: "income-expense", title: "Income & expenses", enter: enterIncomeExpense },
    { id: "assets-liabilities", title: "Assets & liabilities", enter: enterAssetsLiabilities },
    { id: "balance-transactions", title: "Balance transactions", enter: enterBalanceTransactions },
  ];

  const TX_TYPE_LABEL = {
    expense: "Expense", income: "Income", transfer: "Transfer", asset_purchase: "Asset purchase",
    return_on_investment: "Return on investment", repay_debt: "Repay debt", balance_adjustment: "Balance adjustment",
  };
  const TYPE_GROUP = {
    payment_account: "Payment accounts", credit_card: "Credit cards",
    other_asset: "Other assets", other_liability: "Other liabilities",
  };

  const TYPE_LABEL = {
    payment_account: "Payment account", credit_card: "Credit card",
    other_asset: "Other asset", other_liability: "Other liability",
  };

  const bodyEl = $("#report-body");
  const statusEl = $("#report-status");
  let gen = 0; // ignores responses of a report page we already left

  // ---- Small helpers ---------------------------------------------------------

  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  }

  const pad = (n) => String(n).padStart(2, "0");
  const ymd = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  const isYMD = (s) => /^\d{4}-\d{2}-\d{2}$/.test(s || "");

  // Local wall-clock time with the device's offset for that date (DST-aware),
  // e.g. 2026-11-01T00:00:00+07:00, like the transaction list's date filter.
  function rfc3339(d) {
    const mins = -d.getTimezoneOffset();
    const sign = mins >= 0 ? "+" : "-";
    const off = `${sign}${pad(Math.floor(Math.abs(mins) / 60))}:${pad(Math.abs(mins) % 60)}`;
    return `${ymd(d)}T${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}${off}`;
  }
  function dayStart(s) { const [y, m, d] = s.split("-").map(Number); return new Date(y, m - 1, d, 0, 0, 0); }
  function dayEnd(s) { const [y, m, d] = s.split("-").map(Number); return new Date(y, m - 1, d, 23, 59, 59); }

  // Amounts are exact decimal strings from the API; Number() is used only to
  // size the bars.
  const num = (s) => Math.abs(Number(s)) || 0;
  const isNeg = (s) => String(s).startsWith("-");
  const isZero = (s) => !/[1-9]/.test(String(s));
  const signed = (s) => (isNeg(s) ? "−" + fmtAmount(String(s).slice(1)) : isZero(s) ? fmtAmount(s) : "+" + fmtAmount(s));

  // A horizontal bar chart. rows: [{label, sub, amount, cls, note}]; bars are
  // sized against the largest |amount| (or opts.max). A negative amount
  // (overdrawn account, card in credit) is drawn hatched.
  function barChart(rows, opts = {}) {
    const chart = el("div", "bar-chart");
    if (opts.label) chart.setAttribute("aria-label", opts.label);
    chart.setAttribute("role", "list");
    const max = opts.max || Math.max(0, ...rows.map((r) => num(r.amount)));
    for (const r of rows) {
      const row = el("div", "bar-row");
      row.setAttribute("role", "listitem");
      const label = el("div", "bar-label");
      label.append(el("span", "bar-name", r.label));
      if (r.sub) label.append(el("span", "bar-sub muted", r.sub));
      const track = el("div", "bar-track");
      const fill = el("div", "bar-fill " + (r.cls || "") + (isNeg(r.amount) ? " bar-neg" : ""));
      const pct = max > 0 ? (num(r.amount) / max) * 100 : 0;
      fill.style.width = (pct > 0 && pct < 0.5 ? 0.5 : pct) + "%";
      track.append(fill);
      const value = el("div", "bar-value");
      value.append(el("span", "bar-amount", (isNeg(r.amount) ? "−" + fmtAmount(String(r.amount).slice(1)) : fmtAmount(r.amount))));
      if (r.note) value.append(el("span", "bar-note muted", r.note));
      row.title = `${r.label}${r.sub ? " (" + r.sub + ")" : ""}: ${r.amount}${r.note ? " · " + r.note : ""}`;
      row.append(label, track, value);
      chart.append(row);
    }
    return chart;
  }

  function currencySection(code) {
    const sec = el("section", "report-currency");
    sec.append(el("h3", "report-cur", App.currencyText(code)));
    return sec;
  }

  function netLine(label, amount) {
    const p = el("p", "report-net");
    p.append(el("span", "muted", label + " "));
    p.append(el("strong", isNeg(amount) ? "neg-text" : isZero(amount) ? "" : "pos-text", signed(amount)));
    return p;
  }

  async function fetchReport(url, myGen) {
    statusEl.textContent = "Loading…";
    bodyEl.replaceChildren();
    try {
      const res = await fetch(url);
      const body = await res.json().catch(() => ({}));
      if (myGen !== gen) return null;
      if (!res.ok) {
        statusEl.textContent = (body.fields && Object.values(body.fields).join("; ")) || body.error || `Error ${res.status}`;
        return null;
      }
      statusEl.textContent = "";
      return body;
    } catch (err) {
      if (myGen === gen) statusEl.textContent = "Network error: " + err.message;
      return null;
    }
  }

  // ---- Income & expenses -----------------------------------------------------

  // The range lives in the URL (?from=YYYY-MM-DD&to=YYYY-MM-DD, or ?all=1),
  // so it survives reloads and can be bookmarked. No query = this month.
  function presetRange(name) {
    const now = new Date();
    const y = now.getFullYear(), m = now.getMonth();
    switch (name) {
      case "this-month": return { from: ymd(new Date(y, m, 1)), to: ymd(new Date(y, m + 1, 0)) };
      case "last-month": return { from: ymd(new Date(y, m - 1, 1)), to: ymd(new Date(y, m, 0)) };
      case "this-year": return { from: ymd(new Date(y, 0, 1)), to: ymd(new Date(y, 11, 31)) };
      default: return { from: "", to: "" };
    }
  }

  function setRangeQuery(from, to, extra) {
    const q = new URLSearchParams(extra || {});
    if (from) q.set("from", from);
    if (to) q.set("to", to);
    if (!from && !to) q.set("all", "1");
    App.replaceQuery(q.toString());
  }

  async function enterIncomeExpense(query) {
    let from = isYMD(query.get("from")) ? query.get("from") : "";
    let to = isYMD(query.get("to")) ? query.get("to") : "";
    if (!from && !to && query.get("all") !== "1") {
      ({ from, to } = presetRange("this-month"));
      setRangeQuery(from, to);
    }
    $("#r-from").value = from;
    $("#r-to").value = to;
    await loadIncomeExpense();
  }

  function rangeText(from, to) {
    if (from && to) return from === to ? from : `${from} to ${to}`;
    if (from) return `since ${from}`;
    if (to) return `until ${to}`;
    return "all time";
  }

  async function loadIncomeExpense() {
    const myGen = ++gen;
    const from = $("#r-from").value, to = $("#r-to").value;
    const params = new URLSearchParams();
    if (from) params.set("from", rfc3339(dayStart(from)));
    if (to) params.set("to", rfc3339(dayEnd(to)));
    await App.loadCurrencies(); // currency flags
    const body = await fetchReport("/api/reports/income-expense" + (params.toString() ? "?" + params : ""), myGen);
    if (!body) return;
    const list = body.currencies || [];
    if (!list.length) {
      statusEl.textContent = `No income or expenses (${rangeText(from, to)}).`;
      return;
    }
    const heading = el("p", "report-range muted", `Period: ${rangeText(from, to)}`);
    bodyEl.append(heading);
    for (const c of list) {
      const sec = currencySection(c.currency);
      const count = (n, what) => `${n} ${what}${n === 1 ? "" : "s"}`;
      sec.append(barChart([
        { label: "Income", amount: c.income, cls: "bar-income", note: count(c.income_count, "transaction") },
        { label: "Expenses", amount: c.expense, cls: "bar-expense", note: count(c.expense_count, "transaction") },
      ], { label: `Income and expenses in ${c.currency}` }));
      sec.append(netLine("Net (income − expenses)", c.net));

      const cols = el("div", "report-cols");
      for (const [title, items, total, cls] of [
        ["Expenses by category", c.expense_by_category, c.expense, "bar-expense"],
        ["Income by category", c.income_by_category, c.income, "bar-income"],
      ]) {
        const col = el("div", "report-col");
        col.append(el("h4", "", title));
        if (!items || !items.length) {
          col.append(el("p", "group-empty", "None in this period."));
        } else {
          const t = num(total);
          col.append(barChart(items.map((it) => ({
            label: it.category_name || (it.category_uid ? "(unknown category)" : "(no category)"),
            amount: it.amount,
            cls,
            note: (t > 0 ? `${Math.round((num(it.amount) / t) * 1000) / 10}% · ` : "") + count(it.count, "transaction"),
          })), { label: `${title} in ${c.currency}` }));
        }
        cols.append(col);
      }
      sec.append(cols);
      bodyEl.append(sec);
    }
  }

  $("#r-filter-form").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const from = $("#r-from").value, to = $("#r-to").value;
    if (from && to && from > to) {
      statusEl.textContent = "“To” must not be earlier than “From”.";
      return;
    }
    setRangeQuery(from, to);
    loadIncomeExpense();
  });

  document.querySelectorAll("#r-filter-form [data-preset]").forEach((b) => {
    b.addEventListener("click", () => {
      const { from, to } = presetRange(b.dataset.preset);
      $("#r-from").value = from;
      $("#r-to").value = to;
      setRangeQuery(from, to);
      loadIncomeExpense();
    });
  });

  // ---- Assets & liabilities --------------------------------------------------

  async function enterAssetsLiabilities() {
    const myGen = ++gen;
    await App.loadCurrencies();
    const body = await fetchReport("/api/reports/assets-liabilities", myGen);
    if (!body) return;
    const list = body.currencies || [];
    if (!list.length) {
      statusEl.textContent = "No balances yet.";
      return;
    }
    for (const c of list) {
      const sec = currencySection(c.currency);
      sec.append(barChart([
        { label: "Assets", amount: c.assets, cls: "bar-asset" },
        { label: "Liabilities", amount: c.liabilities, cls: "bar-liability" },
      ], { label: `Assets and liabilities in ${c.currency}` }));
      sec.append(netLine("Net worth (assets − liabilities)", c.net));
      sec.append(el("h4", "", "By account"));
      sec.append(barChart((c.accounts || []).map((a) => ({
        label: a.name,
        sub: TYPE_LABEL[a.type] || a.type,
        amount: a.amount,
        cls: a.kind === "liability" ? "bar-liability" : "bar-asset",
        note: a.kind === "liability" ? (isNeg(a.amount) ? "in credit" : "debt") : (isNeg(a.amount) ? "overdrawn" : ""),
      })), { label: `Balances by account in ${c.currency}` }));
      bodyEl.append(sec);
    }
  }

  // ---- Balance transactions (issue #160) ---------------------------------------

  // ?balance=<uid>&from=YYYY-MM-DD&to=YYYY-MM-DD (or &all=1), like income &
  // expenses; no range = this month. Export PDF prints the statement (the
  // print stylesheet keeps only the report) so the browser saves it as PDF.

  const rbSel = $("#rb-balance");
  const rbExport = $("#rb-export");
  let rbReport = null; // last loaded report, for the PDF file name

  async function fillBalanceSelect(keep) {
    let list = [];
    try {
      const res = await fetch("/api/balances?include_hidden=true");
      const body = await res.json();
      if (res.ok) list = body.balances || [];
    } catch (_) { /* empty list */ }
    rbSel.replaceChildren();
    if (!list.length) {
      rbSel.append(Object.assign(document.createElement("option"), { value: "", textContent: "No balances yet" }));
      return;
    }
    for (const [type, heading] of Object.entries(TYPE_GROUP)) {
      const items = list.filter((b) => b.type === type);
      if (!items.length) continue;
      const g = document.createElement("optgroup");
      g.label = heading;
      for (const b of items) {
        g.append(Object.assign(document.createElement("option"), {
          value: b.uid, textContent: `${b.name} (${b.currency})${b.hidden ? " · hidden" : ""}`,
        }));
      }
      rbSel.append(g);
    }
    rbSel.value = list.some((b) => b.uid === keep) ? keep : list[0].uid;
  }

  async function enterBalanceTransactions(query) {
    let from = isYMD(query.get("from")) ? query.get("from") : "";
    let to = isYMD(query.get("to")) ? query.get("to") : "";
    if (!from && !to && query.get("all") !== "1") ({ from, to } = presetRange("this-month"));
    $("#rb-from").value = from;
    $("#rb-to").value = to;
    await fillBalanceSelect(query.get("balance") || "");
    rbQuery();
    await loadBalanceTransactions();
  }

  function rbQuery() {
    setRangeQuery($("#rb-from").value, $("#rb-to").value, rbSel.value ? { balance: rbSel.value } : null);
  }

  async function loadBalanceTransactions() {
    const myGen = ++gen;
    rbReport = null;
    rbExport.disabled = true;
    const uid = rbSel.value;
    if (!uid) {
      bodyEl.replaceChildren();
      statusEl.textContent = "Add a balance first.";
      return;
    }
    const from = $("#rb-from").value, to = $("#rb-to").value;
    const params = new URLSearchParams({ balance_uid: uid });
    if (from) params.set("from", rfc3339(dayStart(from)));
    if (to) params.set("to", rfc3339(dayEnd(to)));
    await App.loadCurrencies();
    const body = await fetchReport("/api/reports/balance-transactions?" + params, myGen);
    if (!body) return;
    rbReport = { body, from, to };
    rbExport.disabled = false;

    const b = body.balance;
    const liability = b.kind === "liability";
    const what = liability ? "Debt" : "Balance";
    const head = el("div", "report-statement-head");
    head.append(el("h3", "report-cur", `${b.name} · ${App.currencyText(b.currency)}`));
    head.append(el("p", "report-range muted",
      `${TYPE_LABEL[b.type] || b.type}${b.hidden ? " (hidden)" : ""} · Period: ${rangeText(from, to)}`));
    bodyEl.append(head);

    const summary = el("dl", "report-summary");
    const item = (label, value, cls) => {
      const d = el("div", "");
      d.append(el("dt", "muted", label), el("dd", cls || "", value));
      summary.append(d);
    };
    item(`Opening ${what.toLowerCase()}`, fmtNeg(body.opening_amount));
    item(liability ? "In (payments)" : "Money in", fmtAmount(body.money_in), "pos-text");
    item(liability ? "Out (charges)" : "Money out", fmtAmount(body.money_out), "neg-text");
    item(`Closing ${what.toLowerCase()}`, fmtNeg(body.closing_amount));
    item("Transactions", String(body.count));
    bodyEl.append(summary);

    const list = body.transactions || [];
    if (!list.length) {
      bodyEl.append(el("p", "group-empty", "No transactions in this period."));
      return;
    }
    const wrap = el("div", "table-wrap");
    const table = el("table", "statement");
    const thead = el("thead");
    const hr = el("tr");
    for (const [t, cls] of [["Date", ""], ["Type", ""], ["Details", ""], ["In", "num"], ["Out", "num"], [what, "num"]]) {
      hr.append(el("th", cls, t));
    }
    thead.append(hr);
    const tbody = el("tbody");
    for (const e of list) {
      const tr = el("tr");
      tr.append(el("td", "time", shownDate(e.spent_at)));
      tr.append(el("td", "type", TX_TYPE_LABEL[e.type] || e.type));
      const det = el("td", "details");
      const parts = [];
      if (e.category_name || e.category_uid) parts.push(e.category_name || "(unknown category)");
      if (e.counterparty) parts.push((e.money_in ? "from " : "to ") + e.counterparty);
      if (parts.length) det.append(el("span", "", parts.join(" · ")));
      if (e.currency !== b.currency) det.append(el("span", "muted foreign-amt", `${fmtAmount(e.amount)} ${e.currency}`));
      if (e.note) det.append(el("span", "muted note-line", e.note));
      tr.append(det);
      tr.append(el("td", "num pos-text", e.money_in ? fmtAmount(e.money_in) : ""));
      tr.append(el("td", "num neg-text", e.money_out ? fmtAmount(e.money_out) : ""));
      tr.append(el("td", "num", fmtNeg(e.amount_after)));
      tbody.append(tr);
    }
    const tfoot = el("tfoot");
    const fr = el("tr");
    fr.append(el("th", "", "Total"), el("th", ""), el("th", ""));
    fr.append(el("th", "num", fmtAmount(body.money_in)), el("th", "num", fmtAmount(body.money_out)), el("th", "num", fmtNeg(body.closing_amount)));
    tfoot.append(fr);
    table.append(thead, tbody, tfoot);
    wrap.append(table);
    bodyEl.append(wrap);
    bodyEl.append(el("p", "muted small report-printed", `Generated ${shownDate(new Date().toISOString(), true)}`));
  }

  // "−1,234.50" for a negative decimal string, else fmtAmount.
  function fmtNeg(s) { return isNeg(s) ? "−" + fmtAmount(String(s).slice(1)) : fmtAmount(s); }

  // "YYYY-MM-DD HH:MM" in the device's time zone (the raw value if unparsable).
  function shownDate(v, seconds) {
    const d = new Date(v);
    if (isNaN(d)) return v;
    const t = `${pad(d.getHours())}:${pad(d.getMinutes())}` + (seconds ? `:${pad(d.getSeconds())}` : "");
    return `${ymd(d)} ${t}`;
  }

  $("#rb-filter-form").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const from = $("#rb-from").value, to = $("#rb-to").value;
    if (from && to && from > to) {
      statusEl.textContent = "“To” must not be earlier than “From”.";
      return;
    }
    rbQuery();
    loadBalanceTransactions();
  });
  rbSel.addEventListener("change", () => { rbQuery(); loadBalanceTransactions(); });

  document.querySelectorAll("#rb-filter-form [data-preset]").forEach((btn) => {
    btn.addEventListener("click", () => {
      const { from, to } = presetRange(btn.dataset.preset);
      $("#rb-from").value = from;
      $("#rb-to").value = to;
      rbQuery();
      loadBalanceTransactions();
    });
  });

  // Export PDF: the browser's print dialog ("Save as PDF"); style.css's
  // print rules keep only the report. The document title is the suggested
  // file name while printing. A real PDF is made by the browser, so names and
  // notes in any script (e.g. Thai) come out right with no server-side fonts.
  rbExport.addEventListener("click", () => {
    if (!rbReport) return;
    const { body, from, to } = rbReport;
    const name = `${body.balance.name} transactions ${from || "start"} to ${to || ymd(new Date())}`
      .replace(/[\\/:*?"<>|]+/g, "-");
    const saved = document.title;
    document.title = name;
    const restore = () => {
      document.title = saved;
      window.removeEventListener("afterprint", restore);
    };
    window.addEventListener("afterprint", restore);
    window.print();
  });

  // ---- Routes ----------------------------------------------------------------

  function renderTabs(active) {
    const nav = $("#report-tabs");
    nav.replaceChildren();
    for (const r of REPORTS) {
      const a = App.link(r.title, `/reports/${r.id}`, "report-tab" + (r.id === active ? " active" : ""));
      if (r.id === active) a.setAttribute("aria-current", "page");
      nav.append(a);
    }
  }

  function showReport(r, query) {
    renderTabs(r.id);
    document.querySelectorAll("#view-reports .report").forEach((d) => (d.hidden = d.id !== `report-${r.id}`));
    statusEl.textContent = "";
    bodyEl.replaceChildren();
    return r.enter(query);
  }

  App.route("/reports", {
    view: "#view-reports", section: "reports", title: "Reports",
    enter: () => App.navigate(`/reports/${REPORTS[0].id}`, { replace: true }),
  });
  for (const r of REPORTS) {
    App.route(`/reports/${r.id}`, {
      view: "#view-reports", section: "reports", title: r.title,
      enter: (_params, query) => showReport(r, query),
    });
  }
})();
