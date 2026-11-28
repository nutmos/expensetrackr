// Calculator amount fields (issue #206). Inputs marked data-calc accept
// arithmetic such as "120+35.50*2" or "(450-30)/3" as well as plain numbers.
// The expression is evaluated exactly (BigInt fractions, no floating point)
// and replaced by its result, rounded half up to the currency's decimals, when
// the field loses focus, on Enter or "=", and before the form is submitted.
// While an expression is typed, "= result" is previewed under the field.
// Inputs marked data-calc="keypad" also get a calculator button that opens an
// on-screen keypad, since phone decimal keypads have no + − × ÷ keys.
// Plain vanilla JS, no build step.
(function () {
  "use strict";

  // ---- Exact evaluation -------------------------------------------------
  // A value is a fraction {n, d} of BigInts with d > 0.
  const pow10 = (k) => 10n ** BigInt(k);
  const gcd = (a, b) => { a = a < 0n ? -a : a; while (b) [a, b] = [b, a % b]; return a; };
  function frac(n, d) {
    if (d < 0n) { n = -n; d = -d; }
    const g = gcd(n, d) || 1n;
    return { n: n / g, d: d / g };
  }

  // Tokens: numbers, + - * / ( ). Thousands separators (",") and spaces are
  // ignored; ×, ÷, x and the typographic minus/dash are accepted as operators.
  function tokenize(src) {
    const s = src.replace(/[,\s_]/g, "").replace(/[×xX]/g, "*").replace(/÷/g, "/").replace(/[−–]/g, "-");
    const out = [];
    let i = 0;
    while (i < s.length) {
      const m = /^(\d+\.?\d*|\.\d+)/.exec(s.slice(i));
      if (m) {
        const [ip, fp = ""] = m[1].split(".");
        out.push({ num: frac(BigInt((ip || "0") + fp), pow10(fp.length)), dp: fp.length });
        i += m[1].length;
      } else if ("+-*/()".includes(s[i])) {
        out.push({ op: s[i++] });
      } else {
        return null;
      }
    }
    return out;
  }

  // Recursive descent: expr = term (("+"|"-") term)*,
  // term = factor (("*"|"/") factor)*, factor = ("+"|"-") factor | num | "(" expr ")".
  // Returns {value, dp} (dp: the most decimals typed in any number) or null.
  function evaluate(src) {
    const toks = tokenize(String(src));
    if (!toks || !toks.length) return null;
    let pos = 0, dp = 0;
    const peek = () => toks[pos] && toks[pos].op;
    function factor() {
      const t = toks[pos];
      if (!t) throw 0;
      if (t.op === "-" || t.op === "+") { pos++; const v = factor(); return t.op === "-" ? { n: -v.n, d: v.d } : v; }
      if (t.op === "(") {
        pos++;
        const v = expr();
        if (peek() !== ")") throw 0;
        pos++;
        return v;
      }
      if (!t.num) throw 0;
      pos++;
      dp = Math.max(dp, t.dp);
      return t.num;
    }
    function term() {
      let v = factor();
      while (peek() === "*" || peek() === "/") {
        const op = toks[pos++].op, r = factor();
        if (op === "*") v = frac(v.n * r.n, v.d * r.d);
        else if (r.n === 0n) throw 0;
        else v = frac(v.n * r.d, v.d * r.n);
      }
      return v;
    }
    function expr() {
      let v = term();
      while (peek() === "+" || peek() === "-") {
        const op = toks[pos++].op, r = term();
        v = frac(op === "+" ? v.n * r.d + r.n * v.d : v.n * r.d - r.n * v.d, v.d * r.d);
      }
      return v;
    }
    try {
      const value = expr();
      return pos === toks.length ? { value, dp } : null;
    } catch (e) {
      return null;
    }
  }

  // The fraction rounded half up (away from zero) to `scale` decimals, as a
  // plain decimal string without trailing zeros ("150", "12.5", "-3.33").
  function format(v, scale) {
    const neg = v.n < 0n, n = neg ? -v.n : v.n, m = pow10(scale);
    const r = (n * m * 2n + v.d) / (v.d * 2n);
    let s = (r / m).toString();
    if (scale > 0) {
      const f = (r % m).toString().padStart(scale, "0").replace(/0+$/, "");
      if (f) s += "." + f;
    }
    return neg && r !== 0n ? "-" + s : s;
  }

  // True when the text is more than a plain (optionally signed) number.
  function isExpression(v) {
    const s = String(v).trim();
    return s !== "" && !/^[-+−]?\s*[\d,]*\.?\d*$/.test(s);
  }

  // Decimals to round a field's result to: App.calcScale(input) when the page
  // provides it (the currency's scale), else 2.
  function scaleFor(input) {
    const k = window.App && typeof App.calcScale === "function" ? App.calcScale(input) : null;
    return Number.isInteger(k) && k >= 0 ? k : 2;
  }

  // Result text for an input's current expression, or null if it is not one
  // (undefined if it is one that cannot be calculated).
  function resultFor(input) {
    if (!isExpression(input.value)) return null;
    const r = evaluate(input.value);
    return r ? format(r.value, scaleFor(input)) : undefined;
  }

  if (typeof document === "undefined") {
    // Loaded outside a browser (tests): expose the pure helpers only.
    if (typeof module !== "undefined") module.exports = { evaluate, format, isExpression };
    return;
  }

  // ---- Fields -----------------------------------------------------------
  function errEl(input) {
    const form = input.form;
    return form && input.name ? form.querySelector(`.err[data-for="${input.name}"]`) : null;
  }

  // Replace an expression with its result. Returns false if it could not be
  // calculated (the field is then marked with an error).
  function commit(input) {
    const res = resultFor(input);
    const preview = input._calcPreview;
    if (res === null) { if (preview) preview.textContent = ""; return true; }
    if (res === undefined) {
      input.classList.add("invalid");
      const e = errEl(input);
      if (e) e.textContent = "Can’t calculate this; use numbers with + − × ÷ and ( ).";
      return false;
    }
    input.value = res;
    if (preview) preview.textContent = "";
    input.classList.remove("invalid");
    const e = errEl(input);
    if (e && e.textContent.startsWith("Can’t calculate")) e.textContent = "";
    // Live previews and conversions listen for "input".
    input.dispatchEvent(new Event("input", { bubbles: true }));
    return true;
  }

  function updatePreview(input) {
    const p = input._calcPreview;
    if (!p) return;
    const res = resultFor(input);
    p.textContent = res === null ? "" : res === undefined ? "= …" : "= " + res;
  }

  // On-screen keypad: buttons append to the field's text (no focus change, so
  // the phone's own keyboard stays closed).
  const KEYS = [
    ["7", "8", "9", "÷"],
    ["4", "5", "6", "×"],
    ["1", "2", "3", "−"],
    ["0", ".", "⌫", "+"],
    ["C", "(", ")", "="],
  ];
  const KEY_LABEL = { "⌫": "Backspace", "C": "Clear", "÷": "Divide", "×": "Multiply", "−": "Minus", "+": "Plus", "=": "Calculate" };

  function buildKeypad(input) {
    const pad = document.createElement("div");
    pad.className = "calc-keypad";
    pad.hidden = true;
    pad.setAttribute("role", "group");
    pad.setAttribute("aria-label", "Calculator keypad");
    KEYS.flat().forEach((k) => {
      const b = document.createElement("button");
      b.type = "button";
      b.textContent = k;
      b.className = /[0-9.]/.test(k) ? "secondary" : k === "=" ? "calc-eq" : "secondary calc-op";
      if (KEY_LABEL[k]) b.setAttribute("aria-label", KEY_LABEL[k]);
      b.addEventListener("click", () => {
        if (k === "=") { commit(input); return; }
        if (k === "C") input.value = "";
        else if (k === "⌫") input.value = input.value.slice(0, -1);
        else input.value += k;
        input.classList.remove("invalid");
        input.dispatchEvent(new Event("input", { bubbles: true }));
      });
      pad.appendChild(b);
    });
    return pad;
  }

  function setup(input) {
    const preview = document.createElement("small");
    preview.className = "calc-preview muted";
    preview.setAttribute("aria-live", "polite");
    input._calcPreview = preview;
    if (!input.title) input.title = "You can type a calculation, e.g. 120+35.50*2";

    // Inputs that share a row (± toggle, interest + fees, amount + currency)
    // get their preview under the row.
    let anchor = input.closest(".signed-row, .charges-row, .foreign-row") || input;
    if (input.dataset.calc === "keypad") {
      const row = document.createElement("span");
      row.className = "calc-row";
      input.parentNode.insertBefore(row, input);
      row.appendChild(input);
      const pad = buildKeypad(input);
      const toggle = document.createElement("button");
      toggle.type = "button";
      toggle.className = "secondary calc-toggle";
      toggle.textContent = "+−×÷";
      toggle.title = "Calculator";
      toggle.setAttribute("aria-label", "Calculator");
      toggle.setAttribute("aria-expanded", "false");
      toggle.addEventListener("click", () => {
        pad.hidden = !pad.hidden;
        toggle.setAttribute("aria-expanded", String(!pad.hidden));
        toggle.classList.toggle("active", !pad.hidden);
        if (pad.hidden) commit(input);
      });
      row.appendChild(toggle);
      row.after(pad);
      anchor = pad;
      input._calcPad = pad;
      input._calcToggle = toggle;
    }
    anchor.after(preview);

    input.addEventListener("input", () => updatePreview(input));
    input.addEventListener("blur", () => commit(input));
    input.addEventListener("keydown", (ev) => {
      if ((ev.key === "Enter" || ev.key === "=") && isExpression(input.value)) {
        ev.preventDefault();
        commit(input);
      } else if (ev.key === "=") {
        ev.preventDefault();
      }
    });
  }

  // Close the keypads and clear previews, e.g. when a form is reset/refilled.
  function reset(root) {
    (root || document).querySelectorAll("input[data-calc]").forEach((input) => {
      if (input._calcPreview) input._calcPreview.textContent = "";
      if (input._calcPad) {
        input._calcPad.hidden = true;
        input._calcToggle.setAttribute("aria-expanded", "false");
        input._calcToggle.classList.remove("active");
      }
    });
  }

  // Before any submit handler (capture phase on document): calculate every
  // expression in the form; stop the submit if one cannot be calculated.
  document.addEventListener("submit", (ev) => {
    let bad = null;
    ev.target.querySelectorAll("input[data-calc]").forEach((input) => {
      if (!commit(input) && !bad) bad = input;
    });
    if (bad) {
      ev.preventDefault();
      ev.stopImmediatePropagation();
      bad.focus();
    }
  }, true);

  document.addEventListener("DOMContentLoaded", () => {
    document.querySelectorAll("input[data-calc]").forEach(setup);
  });

  window.Calc = { evaluate, format, isExpression, commit, reset };
})();
