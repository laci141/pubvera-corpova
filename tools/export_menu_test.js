// Export menu test (E1-E13): every result toolbar has ONE "Export" menu button
// (WAI-ARIA APG menu button pattern) instead of four separate download buttons.
//
// Run: node tools/export_menu_test.js
//
// Behaviour test without a browser. metaRow() (plus anything between
// triggerDownload() and it) is cut out of index.html by a brace-balanced scan, the block from
// `const clickActions = {` to the page-load hook (the delegated data-action
// listener plus the export-menu functions and listeners) is cut out as a slice,
// and both run inside node:vm against a tiny fake DOM. The real listeners are
// then driven with fake click and keydown events. The download functions are
// stubs that click a synthetic anchor, like triggerDownload() does.
// Layout (343/360/375 px, the open panel inside the viewport) is NOT checked
// here: that is the Playwright script's job.
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const html = fs.readFileSync(path.join(__dirname, "..", "index.html"), "utf8").replace(/\r\n/g, "\n");

// extractFunction returns { start, end } of "function <name>(...) { ... }".
// The functions read here hold no unbalanced braces in strings, so a plain
// depth count is enough.
function extractFunction(name) {
  const start = html.indexOf("function " + name + "(");
  if (start < 0) return null;
  const open = html.indexOf("{", start);
  if (open < 0) return null;
  let depth = 0;
  for (let i = open; i < html.length; i++) {
    if (html[i] === "{") depth++;
    else if (html[i] === "}") {
      depth--;
      if (depth === 0) return { start: start, end: i + 1 };
    }
  }
  return null;
}

let ran = 0;
let failed = 0;
function check(label, ok) {
  ran++;
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label);
}
function refuse(what) {
  console.error("FAIL: " + what + " not found in index.html - refusing to pass silently");
  process.exit(1);
}

// ── Source blocks. Present before and after the change: missing = exit 1. ──
const trig = extractFunction("triggerDownload");
const meta = extractFunction("metaRow");
if (!trig) refuse("function triggerDownload");
if (!meta) refuse("function metaRow");
if (meta.start < trig.end) refuse("metaRow after triggerDownload");
// From the end of triggerDownload to the end of metaRow: the comment, any
// module-level panel-id counter, and metaRow itself.
const metaSrc = html.slice(trig.end, meta.end);
const a = html.indexOf("    const clickActions = {");
const b = html.indexOf("    // Load settings on page load");
if (a < 0 || b < a) refuse("the clickActions block");
const actionsSrc = html.slice(a, b);
const styleOpen = /<style\b[^>]*>/.exec(html);
const styleStart = styleOpen ? styleOpen.index : -1;
const styleEnd = html.indexOf("</style>");
if (styleStart < 0 || styleEnd < styleStart) refuse("the <style> block");
const style = html.slice(styleStart, styleEnd).replace(/\/\*[^]*?\*\//g, "");

// ── tiny DOM: enough for the selectors the toolbar and menu code use ──
class El {
  constructor(tag, attrs) { this.tag = tag; this.attrs = attrs || {}; this.children = []; this.parent = null; }
  getAttribute(n) { return n in this.attrs ? this.attrs[n] : null; }
  setAttribute(n, v) { this.attrs[n] = String(v); }
  removeAttribute(n) { delete this.attrs[n]; }
  hasAttribute(n) { return n in this.attrs; }
  get hidden() { return this.hasAttribute("hidden"); }
  set hidden(v) { if (v) this.attrs.hidden = ""; else delete this.attrs.hidden; }
  get id() { return this.attrs.id || ""; }
  get checked() { return this.hasAttribute("checked"); }
  get dataset() {
    const d = {};
    for (const k in this.attrs) if (k.startsWith("data-")) d[k.slice(5).replace(/-(\w)/g, (_, c) => c.toUpperCase())] = this.attrs[k];
    return d;
  }
  get classList() { const c = (this.attrs.class || "").split(/\s+/); return { contains: x => c.includes(x) }; }
  focus() { doc.activeElement = this; }
  contains(o) { for (; o; o = o.parent) if (o === this) return true; return false; }
  all() { const out = []; const walk = e => e.children.forEach(c => { out.push(c); walk(c); }); walk(this); return out; }
  matches(sel) { return sel.split(",").some(s => matchChain(this, s.trim().split(/\s+/).filter(x => x !== ">"))); }
  closest(sel) { for (let e = this; e; e = e.parent) if (e.matches && e.matches(sel)) return e; return null; }
  querySelectorAll(sel) { return this.all().filter(e => e.matches(sel)); }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
}
function matchSimple(e, s) {
  if (!e || !(e instanceof El)) return false;
  const parts = s.match(/:not\(\[[^\]]+\]\)|\[[^\]]+\]|\.[\w-]+|^[a-z]+/g) || [];
  return parts.length > 0 && parts.every(p => {
    if (p.startsWith(":not(")) return !matchSimple(e, p.slice(5, -1));
    if (p[0] === ".") return e.classList.contains(p.slice(1));
    if (p[0] === "[") {
      const m = p.match(/^\[([\w-]+)(?:="([^"]*)")?\]$/);
      return m[2] === undefined ? e.hasAttribute(m[1]) : e.getAttribute(m[1]) === m[2];
    }
    return e.tag === p;
  });
}
function matchChain(e, parts) {
  if (!matchSimple(e, parts[parts.length - 1])) return false;
  if (parts.length === 1) return true;
  for (let p = e.parent; p; p = p.parent) if (matchChain(p, parts.slice(0, -1))) return true;
  return false;
}
function parse(src, root) {
  let cur = root;
  for (const m of src.matchAll(/<(\/?)(\w+)([^>]*)>/g)) {
    if (m[1]) { cur = cur.parent; continue; }
    const attrs = {};
    for (const at of m[3].matchAll(/([\w-]+)(?:="([^"]*)")?/g)) attrs[at[1]] = at[2] === undefined ? "" : at[2];
    const el = new El(m[2], attrs);
    el.parent = cur;
    cur.children.push(el);
    if (!/^(input|br|img)$/.test(m[2])) cur = el;
  }
}

const body = new El("body");
const listeners = { click: [], keydown: [], input: [] };
const doc = {
  activeElement: body, body: body,
  querySelector: s => body.querySelector(s), querySelectorAll: s => body.querySelectorAll(s),
  getElementById: id => body.all().find(e => e.id === id) || null,
  addEventListener: (t, f) => { if (listeners[t]) listeners[t].push(f); },
};
function fire(type, target, trusted, key) {
  const e = { type: type, target: target, key: key, isTrusted: trusted !== false, defaultPrevented: false,
    preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, stopImmediatePropagation() {} };
  listeners[type].forEach(f => f(e));
  return e;
}
// The synthetic download anchor: triggerDownload() appends it to body and clicks it.
const anchor = new El("a");
anchor.parent = body;
body.children.push(anchor);
const calls = [];
const exporter = fmt => (k, f) => { calls.push(fmt + " " + k + " " + f); fire("click", anchor, false); };
const sandbox = {
  document: doc, console: console, includeAbstracts: false,
  downloadXLSX: exporter("xlsx"), downloadCSV: exporter("csv"), downloadJSON: exporter("json"), downloadBibTeX: exporter("bib"),
  setIncludeAbstracts: () => {},
};
vm.createContext(sandbox);
vm.runInContext(metaSrc, sandbox);
vm.runInContext(actionsSrc, sandbox);

// Four toolbars on one page: a study list (filter + abstracts toggle), compare's
// two claim cards that share one export key, and a filter-less summary row.
const specs = [
  ["'3 studies shown', 'consensus_studies', 'corpova-consensus-studies.json'", "consensus_studies", "corpova-consensus-studies"],
  ["'2 studies shown', 'compare_studies', 'corpova-compare-studies.json'", "compare_studies", "corpova-compare-studies"],
  ["'1 study shown', 'compare_studies', 'corpova-compare-studies.json'", "compare_studies", "corpova-compare-studies"],
  ["'Consensus analysis', 'consensus_verdict', 'corpova-consensus.json', { filter: false }", "consensus_verdict", "corpova-consensus"],
];
const bars = specs.map(([args, key, base]) => {
  const src = vm.runInContext("metaRow(" + args + ")", sandbox);
  const holder = new El("div");
  holder.parent = body;
  body.children.push(holder);
  parse(src, holder);
  const row = holder.querySelector(".meta-row");
  const trigger = holder.querySelector('[aria-haspopup="menu"]');
  const menu = trigger ? doc.getElementById(trigger.getAttribute("aria-controls")) : null;
  const items = menu ? menu.querySelectorAll('[role="menuitem"]') : [];
  return { src: src, key: key, base: base, holder: holder, row: row, trigger: trigger, menu: menu, items: items,
    triggers: holder.querySelectorAll('[aria-haspopup="menu"]') };
});
const A = bars[0];
const allSrc = bars.map(x => x.src).join("\n");
// A step that throws (old markup, missing function) is a FAIL, not a crash.
function attempt(f) { try { return !!f(); } catch (e) { console.log("      note: " + e.message); return false; } }
function isOpen(x) { return !!x.menu && !x.menu.hidden && x.trigger.getAttribute("aria-expanded") === "true"; }
function isClosed(x) { return !!x.menu && x.menu.hidden && x.trigger.getAttribute("aria-expanded") === "false"; }
function reset() { bars.forEach(x => { if (x.menu) x.menu.hidden = true; if (x.trigger) x.trigger.setAttribute("aria-expanded", "false"); }); doc.activeElement = body; }

// ── markup ──
check("E1 one trigger \"⬇ Export ▾\" with aria-haspopup=menu, aria-expanded=false, aria-controls -> hidden role=menu panel (" +
  bars.map(x => x.triggers.length).join(",") + " triggers per toolbar)",
  bars.every(x => x.triggers.length === 1 && x.trigger.tag === "button" &&
    x.trigger.getAttribute("aria-expanded") === "false" && />⬇ Export ▾<\/button>/.test(x.src) &&
    !!x.menu && x.holder.contains(x.menu) && x.menu.getAttribute("role") === "menu" && x.menu.hidden));

const want = { Excel: ["downloadXLSX", ".xlsx"], CSV: ["downloadCSV", ".csv"], JSON: ["downloadJSON", ".json"], BibTeX: ["downloadBibTeX", ".bib"] };
const outside = bars.map(x => x.holder.querySelectorAll("[data-action]")
  .filter(e => /^download/.test(e.getAttribute("data-action")) && !e.closest(".dl-menu")).length);
check("E2 the panel holds four role=menuitem buttons Excel, CSV, JSON, BibTeX with their data-action, data-key, data-filename; " +
  "no export button outside the panel (" + bars.map(x => x.items.length).join(",") + " items, " + outside.join(",") + " outside)",
  bars.every((x, n) => {
    const labels = (x.menu ? [...x.src.matchAll(/role="menuitem"[^>]*>([^<]*)</g)].map(m => m[1]) : []);
    return x.items.length === 4 && outside[n] === 0 && labels.join(",") === "Excel,CSV,JSON,BibTeX" &&
      x.items.every((it, i) => {
        const w = want[labels[i]];
        return it.tag === "button" && it.getAttribute("type") === "button" && it.getAttribute("data-action") === w[0] &&
          it.getAttribute("data-key") === x.key && it.getAttribute("data-filename") === x.base + w[1];
      });
  }) &&
  // The BibTeX title still depends on studyList.
  A.items.length === 4 && A.items[3].getAttribute("title") === "BibTeX citations for these papers" &&
  bars[3].items.length === 4 && bars[3].items[3].getAttribute("title") === "BibTeX citation for this analysis (@misc)");

const onAttr = (allSrc.match(/\son[a-z]+\s*=/gi) || []).length;
const styleAttr = (allSrc.match(/\sstyle\s*=/gi) || []).length;
check("E3 the toolbar markup has no on*= and no style= attribute (" + onAttr + " on*=, " + styleAttr + " style=)",
  onAttr === 0 && styleAttr === 0);

const ids = bars.map(x => x.menu ? x.menu.id : "");
check("E4 toolbars rendered one after another get different panel ids (" + ids.join(", ") + ")",
  ids.every(id => id !== "") && new Set(ids).size === ids.length &&
  bars.every(x => x.trigger.getAttribute("aria-controls") === x.menu.id));

// ── behaviour ──
reset();
check("E5 a trigger click opens: aria-expanded=true, hidden removed, first item focused", attempt(() => {
  fire("click", A.trigger);
  return isOpen(A) && doc.activeElement === A.items[0];
}));

check("E6 ArrowDown/ArrowUp cycle with wrap, Home/End jump to first/last", attempt(() => {
  reset();
  fire("click", A.trigger);
  const last = A.items[A.items.length - 1];
  fire("keydown", doc.activeElement, true, "ArrowDown");
  const d1 = doc.activeElement === A.items[1];
  fire("keydown", doc.activeElement, true, "End");
  const end = doc.activeElement === last;
  fire("keydown", doc.activeElement, true, "ArrowDown");
  const wrapDown = doc.activeElement === A.items[0];
  fire("keydown", doc.activeElement, true, "ArrowUp");
  const wrapUp = doc.activeElement === last;
  fire("keydown", doc.activeElement, true, "Home");
  return d1 && end && wrapDown && wrapUp && doc.activeElement === A.items[0] && isOpen(A);
}));

check("E7 Escape closes (aria-expanded=false, hidden back) and returns focus to the trigger", attempt(() => {
  reset();
  fire("click", A.trigger);
  const e = fire("keydown", doc.activeElement, true, "Escape");
  return isClosed(A) && doc.activeElement === A.trigger && e.defaultPrevented;
}));

check("E8 ArrowDown, Enter and Space on the trigger open and focus the first item; Tab closes without preventDefault", attempt(() => {
  const opened = ["ArrowDown", "Enter", " "].map(k => {
    reset();
    A.trigger.focus();
    const e = fire("keydown", A.trigger, true, k);
    const ok = isOpen(A) && doc.activeElement === A.items[0] && e.defaultPrevented;
    if (!ok) console.log("      note: key " + JSON.stringify(k) + " did not open the menu");
    return ok;
  });
  reset();
  fire("click", A.trigger);
  const tab = fire("keydown", doc.activeElement, true, "Tab");
  return opened.every(Boolean) && isClosed(A) && !tab.defaultPrevented;
}));

check("E9 an outside trusted click closes; an untrusted click (the download anchor) is ignored", attempt(() => {
  reset();
  fire("click", A.trigger);
  fire("click", anchor, false);
  const keptOpen = isOpen(A);
  fire("click", body, true);
  return keptOpen && isClosed(A);
}));

const got = [];
check("E10 an item click runs its download action once, closes the menu and returns focus to the trigger", attempt(() => {
  const ok = A.items.length === 4 && A.items.every(it => {
    reset();
    fire("click", A.trigger);
    doc.activeElement = body; // WebKit does not focus a button on mouse click
    calls.length = 0;
    fire("click", it, true);
    got.push(calls.join("+"));
    return calls.length === 1 && isClosed(A) && doc.activeElement === A.trigger;
  });
  const exp = ["xlsx", "csv", "json", "bib"].map((f, i) =>
    f + " consensus_studies corpova-consensus-studies." + ["xlsx", "csv", "json", "bib"][i]);
  console.log("      calls: " + (got.join(" | ") || "none"));
  return ok && exp.every(w => got.includes(w));
}));

check("E11 opening a second menu closes the first", attempt(() => {
  reset();
  const B = bars[1];
  fire("click", A.trigger);
  const first = isOpen(A);
  fire("click", B.trigger);
  return first && isClosed(A) && isOpen(B) && doc.activeElement === B.items[0];
}));
reset();

// ── CSS ──
const used = new Set([...allSrc.matchAll(/class="([^"]+)"/g)].flatMap(m => m[1].split(/\s+/)));
const missing = [...used].filter(c => !new RegExp("\\." + c + "(?![\\w-])").test(style));
check("E12 every class used by the toolbar and menu markup exists in the CSS (missing: " + (missing.join(",") || "none") + ")",
  used.size > 0 && missing.length === 0);

const rules = [...style.matchAll(/([^{}]+)\{([^{}]*)\}/g)]
  .map(m => ({ sel: m[1].split(",").map(s => s.trim()), body: m[2].replace(/\s+/g, "") }));
const declsOf = sel => rules.filter(r => r.sel.includes(sel)).map(r => r.body).join(";");
const panel = declsOf(".dl-menu");
const panelAll = rules.filter(r => r.sel.some(s => /^\.dl-menu(?![\w-])/.test(s))).map(r => r.body).join(";");
const bg = (panel.match(/(?:^|;)background(?:-color)?:[^;]+/g) || []).pop() || "";
const bgM = bg.match(/:rgba\(\d+,\d+,\d+,(0?\.\d+)\)$/);
const trigCss = declsOf(".dl-trigger");
const itemCss = declsOf('.dl-menu [role="menuitem"]');
check("E13 .dl-menu: one solid rgba background alpha 0.90, no gradient; absolute, right:0, z-index, " +
  "max-width:calc(100vw - 32px); trigger min-height/min-width 44px; items min-height 44px (background \"" + bg.replace(/^;/, "") + "\")",
  !!bgM && Math.abs(parseFloat(bgM[1]) - 0.9) < 1e-9 && !/gradient/.test(panelAll) &&
  /(^|;)position:absolute/.test(panel) && /(^|;)right:0/.test(panel) && /(^|;)z-index:\d+/.test(panel) &&
  /(^|;)max-width:calc\(100vw-32px\)/.test(panel) &&
  /(^|;)min-height:44px/.test(trigCss) && /(^|;)min-width:44px/.test(trigCss) && /(^|;)min-height:44px/.test(itemCss));

if (ran === 0) {
  console.error("FAIL: zero checks ran - refusing to pass silently");
  process.exit(1);
}
console.log((ran - failed) + "/" + ran + " passed, " + failed + " failed");
process.exit(failed ? 1 : 0);
