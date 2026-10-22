// Export schema test (X-07 / X-08 / X-09 / X-13).
//
// Run: node tools/export_schema_test.js
//
// X-08: the consensus verdict row keeps relevant_count as null when the CLI
// omits it, so JSON, CSV and XLSX carry the same columns.
// X-13: the three compare exports write the plain claim text in `claim` and
// A / B in `claim_side`; non-compare rows carry neither column.
// X-07: stance_confidence is rounded to the digits the UI's fx() shows.
// X-09: the verdict BibTeX note counts analysed studies, not export rows.
//
// Extracts the needed function sources from index.html by a brace-balanced
// scan and evaluates only those inside a node:vm context, with stubs for the
// DOM-facing helpers. Expressions that live inside renderResult (which needs
// the DOM) are pulled out of the source by regex and evaluated on their own.
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
const required = ["cleanTitle", "studyRow", "keptRows", "normTitle", "titleMatches", "excludedRows",
  "comparePool", "renderAllStudiesSection", "plural", "keyWords", "asciiSlug", "hash3", "uniqueKey",
  "bibEsc", "queryFor", "analysisBibTeX", "fx"];
// Added by the change: missing ones are reported as FAIL checks (RED), not a crash.
const added = ["splitClaimLabel"];
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
const ctx = vm.createContext({
  lastData: {}, lastQueries: { consensus: "c", compare: "c" }, aiExcluded: [],
  ANALYSIS_LABEL: {}, ANALYSIS_SLUG: {}, KEY_STOPWORDS: new Set(),
  window: { location: { origin: "http://localhost" } },
  esc: s => String(s == null ? "" : s), metaRow: () => "", renderStudyCard: () => ""
});
const exported = required.concat(added.filter(n => extractFunction(n)));
try {
  vm.runInContext(srcs.join("\n") + "\n" + exported.map(n => "this." + n + " = " + n + ";").join("\n"), ctx);
} catch (e) {
  console.error("FAIL: could not load functions: " + (e && e.stack || e));
  process.exit(1);
}
const { studyRow, keptRows, excludedRows, comparePool, renderAllStudiesSection, analysisBibTeX, normTitle, splitClaimLabel } = ctx;
if (typeof studyRow !== "function" || typeof excludedRows !== "function" || typeof analysisBibTeX !== "function") {
  console.error("FAIL: studyRow/excludedRows/analysisBibTeX did not load as functions - refusing to pass silently");
  process.exit(1);
}

// evalExpr evaluates a source expression with the given variables in scope.
function evalExpr(expr, vars) {
  const c = vm.createContext(Object.assign({}, vars));
  return vm.runInContext("(" + expr + ")", c);
}

const plain = o => JSON.parse(JSON.stringify(o));
const W = { title: "Warm fluids and hypothermia", authors: ["Smith, A."], year: 2020, doi: "10.1/w", stance: "supports" };

try {
  // 1 X-08: relevant_count expression of the verdict row (source regex: the row
  // is built inside renderResult, which needs the DOM).
  {
    const m = html.match(/lastData\.consensus_verdict = \{[\s\S]*?relevant_count: ([^\r\n]+?),\r?\n/);
    check("1 verdict row relevant_count expression found", !!m);
    if (m) {
      const undef = evalExpr("{ relevant_count: " + m[1] + " }", { result: {} });
      check("1a relevant_count undefined -> key present with null",
        JSON.stringify(undef).includes('"relevant_count":null'), JSON.stringify(undef));
      const twelve = evalExpr("{ relevant_count: " + m[1] + " }", { result: { relevant_count: 12 } });
      check("1b relevant_count 12 -> 12", twelve.relevant_count === 12, twelve.relevant_count);
      const zero = evalExpr("{ relevant_count: " + m[1] + " }", { result: { relevant_count: 0 } });
      check("1c relevant_count 0 -> 0", zero.relevant_count === 0, zero.relevant_count);
    }
  }

  // 2 X-13: splitClaimLabel
  {
    const ok = typeof splitClaimLabel === "function";
    const sp = l => ok ? plain(splitClaimLabel(l)) : {};
    const a = sp("Claim A: warming fluids");
    check("2a Claim A label -> claim/side", a.claim === "warming fluids" && a.side === "A", a);
    const b = sp("Claim B: cold fluids");
    check("2b Claim B label -> claim/side", b.claim === "cold fluids" && b.side === "B", b);
    const n = sp("warming fluids");
    check("2c no prefix -> claim unchanged, side ''", n.claim === "warming fluids" && n.side === "", n);
    const inner = sp("Claim B: is Claim A: wrong");
    check("2d later 'Claim A: ' inside the claim is kept", inner.claim === "is Claim A: wrong" && inner.side === "B", inner);
    const mid = sp("Note Claim A: x");
    check("2e prefix not at start -> unchanged", mid.claim === "Note Claim A: x" && mid.side === "", mid);
  }

  // 3 X-13: excluded row from the compare pool
  {
    const T = "Phytotherapy of acute upper respiratory tract infections in children";
    const pool = comparePool([], "Claim A: y", [{ title: T, doi: "10.5937/x" }], "Claim B: x");
    const r = plain(excludedRows([], [{ key: normTitle(T), title: T, reason: "r" }], pool))[0] || {};
    check("3a pool B item -> claim \"x\"", r.claim === "x", r.claim);
    check("3b pool B item -> claim_side \"B\"", r.claim_side === "B", r.claim_side);
    const m = plain(excludedRows([{ work: W, ex: { reason: "dup" }, claimLabel: "Claim A: y" }], []))[0] || {};
    check("3c rendered item -> claim \"y\", claim_side \"A\"", m.claim === "y" && m.claim_side === "A", m);
  }

  // 4 X-13: compare_all row and compare_studies row for the same claim agree.
  // The compare card's keptRows claim argument and labelA live inside
  // renderResult, so they are read from the source by regex.
  {
    const keptArg = (html.match(/const rows = keptRows\(x\.top_supporting, x\.top_refuting, sink, ([^\r\n]+?)\);/) || [])[1];
    const labelExpr = (html.match(/const labelA = (`Claim A: \$\{a\.claim \|\| ''\}`)/) || [])[1];
    check("4 compare keptRows claim argument and labelA found", !!keptArg && !!labelExpr, { keptArg, labelExpr });
    for (const claimText of ["warming fluids", ""]) {
      if (!keptArg || !labelExpr) break;
      const x = { claim: claimText, top_supporting: [W], top_refuting: [] };
      const studies = plain(keptRows(x.top_supporting, x.top_refuting, [],
        evalExpr(keptArg, { x, fallbackLabel: "Claim A" })))[0] || {};
      const labelA = evalExpr(labelExpr, { a: x });
      ctx.lastData = {};
      renderAllStudiesSection([W], "compare_all", "f.json", [labelA]);
      const all = plain(ctx.lastData.compare_all || [])[0] || {};
      check("4 claim " + JSON.stringify(claimText) + ": studies and all rows share claim + claim_side",
        studies.claim === all.claim && studies.claim_side === all.claim_side && "claim_side" in studies,
        { studies: [studies.claim, studies.claim_side], all: [all.claim, all.claim_side] });
      check("4 claim " + JSON.stringify(claimText) + ": claim is plain text, side A",
        all.claim === claimText && all.claim_side === "A", [all.claim, all.claim_side]);
    }
  }

  // 5 X-07: stance_confidence rounded like fx() (2 decimals)
  {
    const sc = v => plain(studyRow(Object.assign({}, W, { stance_confidence: v }), "supporting")).stance_confidence;
    check("5a 0.8999999999999999 -> 0.9", sc(0.8999999999999999) === 0.9, sc(0.8999999999999999));
    check("5b 0.8166666666666667 -> 0.82", sc(0.8166666666666667) === 0.82, sc(0.8166666666666667));
    check("5c 1 -> 1", sc(1) === 1, sc(1));
    check("5d '' -> ''", sc("") === "", sc(""));
    check("5e null -> ''", sc(null) === "", sc(null));
    check("5f undefined -> ''", sc(undefined) === "", sc(undefined));
    check("5g string '0.85' unchanged", sc("0.85") === "0.85", sc("0.85"));
  }

  // 6 X-09: verdict BibTeX note uses the analysed study count
  {
    const v = analysisBibTeX("consensus_verdict", [{ claim: "c", studies_analyzed: 18 }], new Set());
    check("6a verdict studies_analyzed 18 -> note '18 studies'", /note\s*=\s*\{Evidence analysis of 18 studies;/.test(v), v);
    const o = analysisBibTeX("gaps_findings", [{}, {}, {}, {}, {}], new Set());
    check("6b non-verdict 5 rows -> note '5 records'", /note\s*=\s*\{Evidence analysis of 5 records;/.test(o), o);
    const n = analysisBibTeX("consensus_verdict", [{ claim: "c" }], new Set());
    check("6c verdict without studies_analyzed -> '1 record' as before", /note\s*=\s*\{Evidence analysis of 1 record;/.test(n), n);
    const one = analysisBibTeX("consensus_verdict", [{ claim: "c", studies_analyzed: 1 }], new Set());
    check("6d verdict studies_analyzed 1 -> '1 study'", /note\s*=\s*\{Evidence analysis of 1 study;/.test(one), one);
  }

  // 7 non-compare rows carry no claim / claim_side
  {
    const r = plain(studyRow(W, "supporting"));
    check("7a studyRow without claim -> no claim key", !("claim" in r), r);
    check("7b studyRow without claim -> no claim_side key", !("claim_side" in r), r);
    const e = plain(excludedRows([], [{ key: normTitle(W.title), title: W.title, reason: "r" }], [{ work: W }]))[0] || {};
    check("7c consensus excluded pool row -> no claim / claim_side", !("claim" in e) && !("claim_side" in e), e);
    ctx.lastData = {};
    renderAllStudiesSection([W], "consensus_all", "f.json");
    const a = plain(ctx.lastData.consensus_all || [])[0] || {};
    check("7d consensus all-studies row -> no claim / claim_side", !("claim" in a) && !("claim_side" in a), a);
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
