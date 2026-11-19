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
  ];

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

  function setRangeQuery(from, to) {
    const q = new URLSearchParams();
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
