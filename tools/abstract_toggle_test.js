// Include abstracts toggle test (option D).
//
// Run: node tools/abstract_toggle_test.js
//
// One global "Include abstracts" checkbox. When it is off, the JSON, CSV and
// XLSX exports drop the abstract and abstract_truncated columns; on (the
// default) keeps the current output. All of them read exportRows(), so the
// stripping happens there, in both of its return branches.
//   a  stripAbstracts(rows, true) returns the rows unchanged;
//   b  stripAbstracts(rows, false) drops both keys, keeps the rest in order;
//   c  the input rows are not mutated;
//   d  rows without the keys pass through; null / undefined are tolerated;
//   e  both exportRows return branches call stripAbstracts;
//   f  toggle off: no abstract column in CSV, JSON row keys == CSV header;
//   g  the toggle markup sits on the first dl-line of study-list keys only;
//   h  the toggle persists and syncs, and a storage failure never throws.
//
// Same approach as export_schema_test.js: function sources are extracted from
// index.html by a brace-balanced scan and evaluated inside a node:vm context.
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const html = fs.readFileSync(path.join(__dirname, "..", "index.html"), "utf8");

// extractFunction returns the full source of "function <name>(...) { ... }".
// Braces inside strings/regexes in these small functions are balanced, so a
// plain depth count is enough.
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
      if (depth === 0) return html.slice(start, i + 1);
    }
  }
  return null;
}

let ran = 0;
let failed = 0;
function check(label, ok, detail) {
  ran++;
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label + (ok || detail === undefined ? "" : "  got " + JSON.stringify(detail)));
}

// Present before and after the change: a missing one means the scan broke.
const required = ["rowsFromLastData", "exportRows", "boolCell", "toCSV", "metaRow"];
// Added by the change: missing ones are reported as FAIL checks (RED), not a crash.
const added = ["stripAbstracts", "setIncludeAbstracts"];
const srcs = [];
for (const name of required) {
  const s = extractFunction(name);
  if (!s) {
    console.error("FAIL: " + name + " not found in index.html - refusing to pass silently");
    process.exit(1);
  }
  srcs.push(s);
}
for (const name of added) {
  const s = extractFunction(name);
  check("0 " + name + " defined in index.html", !!s);
  if (s) srcs.push(s);
}

const store = {};
const boxes = [];
const ctx = vm.createContext({
  lastData: {}, viewProviders: {}, includeAbstracts: true,
  storageKeys: { abstracts: "sc_export_abstracts" },
  localStorage: { getItem: k => (k in store ? store[k] : null), setItem: (k, v) => { store[k] = String(v); } },
  document: { querySelectorAll: sel => (sel === '[data-action="toggleAbstracts"]' ? boxes : []) }
});
const exported = required.concat(added.filter(n => extractFunction(n)));
try {
  vm.runInContext(srcs.join("\n") + "\n" + exported.map(n => "this." + n + " = " + n + ";").join("\n"), ctx);
} catch (e) {
  console.error("FAIL: could not load functions: " + (e && e.stack || e));
  process.exit(1);
}
const { exportRows, toCSV, metaRow, stripAbstracts, setIncludeAbstracts } = ctx;
if (typeof exportRows !== "function" || typeof toCSV !== "function" || typeof metaRow !== "function") {
  console.error("FAIL: exportRows/toCSV/metaRow did not load as functions - refusing to pass silently");
  process.exit(1);
}
const strip = (rows, include) => typeof stripAbstracts === "function" ? stripAbstracts(rows, include) : undefined;

const plain = o => JSON.parse(JSON.stringify(o));
const row = () => ({ title: "T", year: 2020, doi: "10.1/x", abstract: "Some abstract", abstract_truncated: true, group: "supporting", stance: "supports" });
const KEPT = ["title", "year", "doi", "group", "stance"];

try {
  // a  include = true returns the rows as they are
  {
    const rows = [row(), row()];
    const out = strip(rows, true);
    check("a  include true -> same array returned", out === rows, out === undefined ? "undefined" : "different array");
    check("a  include true -> abstract keys kept",
      Array.isArray(out) && out.every(r => "abstract" in r && "abstract_truncated" in r), out);
  }

  // b  include = false drops both keys and keeps every other key in order
  {
    const out = strip([row(), row()], false);
    const keys = Array.isArray(out) && out[0] ? Object.keys(out[0]) : null;
    check("b  include false -> abstract removed", Array.isArray(out) && out.length === 2 && out.every(r => !("abstract" in r)), out);
    check("b  include false -> abstract_truncated removed",
      Array.isArray(out) && out.length === 2 && out.every(r => !("abstract_truncated" in r)), out);
    check("b  include false -> other keys kept in order", JSON.stringify(keys) === JSON.stringify(KEPT), keys);
    check("b  include false -> values kept", !!out && out[0] && out[0].doi === "10.1/x" && out[0].year === 2020, out);
  }

  // c  the input is not mutated, and the result is a new array of copies
  {
    const rows = [row()];
    const out = strip(rows, false);
    check("c  input row still has abstract + abstract_truncated",
      rows[0].abstract === "Some abstract" && rows[0].abstract_truncated === true, rows[0]);
    check("c  input row key order unchanged", Object.keys(rows[0]).join() === Object.keys(row()).join(), Object.keys(rows[0]));
    check("c  result is a new array of new objects",
      Array.isArray(out) && out !== rows && out[0] !== rows[0], out === rows ? "same array" : out);
  }

  // d  rows without the keys pass through; null / undefined do not throw
  {
    const bare = [{ claim: "c", verdict: "v", studies_analyzed: 3 }];
    const out = strip(bare, false);
    check("d  row without abstract keys passes through", !!out && JSON.stringify(out) === JSON.stringify(bare), out);
    let mixed, threw = null;
    try { mixed = strip([null, undefined, row()], false); } catch (e) { threw = String(e); }
    check("d  null / undefined rows tolerated", threw === null && Array.isArray(mixed) && mixed.length === 3 &&
      mixed[0] === null && mixed[1] === undefined && !("abstract" in mixed[2]), threw || mixed);
    let empty, threw2 = null;
    try { empty = [strip(null, false), strip(undefined, false)]; } catch (e) { threw2 = String(e); }
    check("d  null / undefined rows argument tolerated", threw2 === null && typeof stripAbstracts === "function", threw2 || empty);
  }

  // e  both exportRows return branches call stripAbstracts
  {
    const src = extractFunction("exportRows");
    const calls = (src.match(/stripAbstracts\(/g) || []).length;
    const returns = src.match(/return [^\r\n]*/g) || [];
    check("e  exportRows calls stripAbstracts twice (found " + calls + ")", calls === 2, calls);
    check("e  exportRows has two return branches (found " + returns.length + ")", returns.length === 2, returns);
    check("e  every exportRows return passes stripAbstracts(..., includeAbstracts)",
      returns.length === 2 && returns.every(r => /stripAbstracts\([\s\S]*,\s*includeAbstracts\)/.test(r)), returns);
  }

  // f  toggle off: both branches drop the columns, CSV and JSON agree
  {
    ctx.includeAbstracts = false;
    ctx.lastData = { consensus_studies: [row(), row()], compare_studies: [row()] };
    ctx.viewProviders = { compare_studies: () => ({ rows: ctx.lastData.compare_studies, filters: [] }) };
    for (const [key, branch] of [["consensus_studies", "fallback"], ["compare_studies", "view provider"]]) {
      const v = exportRows(key);
      const csv = toCSV(v.rows);
      const head = csv.split("\r\n")[1].split(",");
      const jsonKeys = Object.keys(plain(v.rows)[0] || {});
      check("f  " + branch + ": CSV header has no abstract / abstract_truncated",
        !head.includes("abstract") && !head.includes("abstract_truncated"), head);
      check("f  " + branch + ": JSON row keys == CSV header columns", JSON.stringify(jsonKeys) === JSON.stringify(head),
        { jsonKeys, head });
      check("f  " + branch + ": lastData rows keep their abstracts",
        ctx.lastData[key].every(r => r.abstract === "Some abstract"), ctx.lastData[key]);
    }
    ctx.includeAbstracts = true;
    const on = exportRows("consensus_studies");
    const headOn = toCSV(on.rows).split("\r\n")[1].split(",");
    check("f  toggle on (default): CSV still has abstract + abstract_truncated",
      headOn.includes("abstract") && headOn.includes("abstract_truncated"), headOn);
    ctx.viewProviders = {};
    ctx.lastData = {};
  }

  // g  toggle markup: first dl-line of study-list keys only, data-action, no inline handler / style
  {
    const toggleRe = /<label class="[^"]*"><input type="checkbox"[^>]*data-action="toggleAbstracts"[^>]*>\s*Include abstracts<\/label>/;
    for (const key of ["consensus_studies", "consensus_excluded", "consensus_all", "compare_studies", "compare_all",
      "compare_excluded", "controversies_studies", "controversies_excluded"]) {
      const out = metaRow("x", key, "f.json");
      const lines = out.split('<span class="dl-line">');
      check("g  " + key + ": toggle on the first dl-line", lines.length === 3 && toggleRe.test(lines[1]), lines[1]);
      check("g  " + key + ": toggle appears once, not on the second dl-line",
        (out.match(/data-action="toggleAbstracts"/g) || []).length === 1 && !/toggleAbstracts/.test(lines[2] || ""), out);
    }
    for (const key of ["consensus_verdict", "evidence_pyramid", "compare_summary", "gaps_findings", "controversies_summary"]) {
      const out = metaRow("x", key, "f.json", { filter: false });
      check("g  " + key + ": no toggle", !/toggleAbstracts|Include abstracts/.test(out), out);
    }
    ctx.includeAbstracts = true;
    const on = metaRow("x", "consensus_studies", "f.json");
    ctx.includeAbstracts = false;
    const off = metaRow("x", "consensus_studies", "f.json");
    ctx.includeAbstracts = true;
    const input = s => (s.match(/<input type="checkbox"[^>]*>/) || [""])[0];
    check("g  includeAbstracts true -> checked", / checked[ >]/.test(input(on)), input(on));
    check("g  includeAbstracts false -> not checked", input(off) !== "" && !/checked/.test(input(off)), input(off));
    const label = (on.match(/<label[\s\S]*?<\/label>/) || [""])[0];
    check("g  toggle markup has no on*= attribute", label !== "" && !/\son[a-z]+\s*=/i.test(label), label);
    check("g  toggle markup has no style= attribute", label !== "" && !/\sstyle\s*=/i.test(label), label);
    check("g  metaRow output has no style= / on*= attribute at all", !/\sstyle\s*=|\son[a-z]+\s*=/i.test(on), on);
    const cls = (label.match(/<label class="([^"]+)"/) || [])[1];
    check("g  label class is defined in the <style> block",
      !!cls && new RegExp("\\." + cls + "\\s*\\{").test((html.match(/<style>([\s\S]*?)<\/style>/) || [])[1] || ""), cls);
    const mapMatch = html.match(/const clickActions = \{([\s\S]*?)\n\s*\};/);
    check("g  clickActions has toggleAbstracts", !!mapMatch && /^\s*toggleAbstracts\s*:/m.test(mapMatch[1]));
  }

  // h  toggle: updates, persists, syncs; storage failures never throw
  {
    const ok = typeof setIncludeAbstracts === "function";
    boxes.length = 0;
    boxes.push({ checked: true }, { checked: true });
    ctx.includeAbstracts = true;
    if (ok) setIncludeAbstracts(false);
    check("h  off -> includeAbstracts false", ok && ctx.includeAbstracts === false, ctx.includeAbstracts);
    check("h  off -> stored '0'", store.sc_export_abstracts === "0", store.sc_export_abstracts);
    check("h  off -> every toggle unchecked", ok && boxes.every(b => b.checked === false), boxes);
    if (ok) setIncludeAbstracts(true);
    check("h  on -> includeAbstracts true, stored '1', every toggle checked",
      ok && ctx.includeAbstracts === true && store.sc_export_abstracts === "1" && boxes.every(b => b.checked === true),
      { inc: ctx.includeAbstracts, stored: store.sc_export_abstracts, boxes });
    const saved = ctx.localStorage;
    ctx.localStorage = { getItem() { throw new Error("blocked"); }, setItem() { throw new Error("blocked"); } };
    let threw = null;
    try { if (ok) setIncludeAbstracts(false); } catch (e) { threw = String(e); }
    check("h  setItem throws -> no throw, state still updated and synced",
      ok && threw === null && ctx.includeAbstracts === false && boxes.every(b => b.checked === false), threw);
    ctx.localStorage = saved;
    ctx.includeAbstracts = true;

    // The page-load read is a top-level statement, not a function: evaluate it alone.
    const load = (html.match(/let includeAbstracts = true;\r?\n[^\r\n]*try \{[^\r\n]*\}[^\r\n]*/) || [])[0];
    check("h  load statement found", !!load, load);
    if (load) {
      const read = getItem => {
        const c = vm.createContext({ storageKeys: { abstracts: "sc_export_abstracts" }, localStorage: { getItem } });
        return vm.runInContext(load + "\nincludeAbstracts;", c);
      };
      check("h  load '0' -> off", read(() => "0") === false);
      check("h  load '1' -> on", read(() => "1") === true);
      check("h  load null (never set) -> on", read(() => null) === true);
      let v, t = null;
      try { v = read(() => { throw new Error("blocked"); }); } catch (e) { t = String(e); }
      check("h  load getItem throws -> on, no throw", t === null && v === true, t || v);
    }
  }
} catch (e) {
  console.error("FAIL: test crashed: " + (e && e.stack || e));
  process.exit(1);
}

if (ran === 0) {
  console.error("FAIL: zero checks ran - refusing to pass silently");
  process.exit(1);
}
console.log((ran - failed) + "/" + ran + " passed, " + failed + " failed");
process.exit(failed ? 1 : 0);
