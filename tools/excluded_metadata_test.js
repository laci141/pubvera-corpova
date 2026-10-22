// Excluded-export metadata test (X-10 / X-14).
//
// Run: node tools/excluded_metadata_test.js
//
// An excluded_studies entry that matches no rendered card used to export as
// title + reason only, although the same work sits in all_studies with full
// metadata (and, on compare, its claim). excludedRows must now look the entry
// up in that pool. Extracts the needed function sources from index.html by a
// brace-balanced scan and evaluates only those inside a node:vm context, so no
// DOM is needed.
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
const required = ["cleanTitle", "splitClaimLabel", "studyRow", "normTitle", "findExclusion", "excludedRows"];
// Added by the change: missing ones are reported as FAIL checks (RED), not a crash.
const added = ["titleMatches", "comparePool"];
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
const ctx = vm.createContext({ aiExcluded: [] });
try {
  vm.runInContext(srcs.join("\n") + "\nthis.excludedRows = excludedRows; this.findExclusion = findExclusion;" +
    "\nthis.normTitle = normTitle;" +
    "\nif (typeof comparePool === 'function') this.comparePool = comparePool;", ctx);
} catch (e) {
  console.error("FAIL: could not load functions: " + (e && e.stack || e));
  process.exit(1);
}
const { excludedRows, findExclusion, normTitle, comparePool } = ctx;
if (typeof excludedRows !== "function" || typeof findExclusion !== "function") {
  console.error("FAIL: excludedRows/findExclusion did not load as functions - refusing to pass silently");
  process.exit(1);
}

const plain = o => JSON.parse(JSON.stringify(o));
const entry = (title, reason) => ({ key: normTitle(title), title, reason });
const T1 = "Administration of Intravenous Amino Acids and Postoperative Hypothermia";
const W1 = { title: T1, authors: ["Smith, A.", "Doe, B."], year: 2020, doi: "10.5152/tjar.2020.29", journal: "TJAR" };
const T2 = "Phytotherapy of acute upper respiratory tract infections in children";
const W2 = { title: T2, authors: ["Kim, C."], year: 2022, doi: "10.5937/arhfarm72-37803" };
const oldShape = (title, reason) => ({ title, group: "excluded", exclusion_reason: reason, matched: false });

try {
  // 1 unmatched title present in pool -> full metadata
  {
    const rows = plain(excludedRows([], [entry(T1, "off-topic")], [{ work: W1 }]));
    const r = rows[0] || {};
    check("1a pool hit -> doi from pool work", r.doi === W1.doi, r);
    check("1b pool hit -> year from pool work", r.year === 2020, r.year);
    check("1c pool hit -> authors from pool work", r.authors === "Smith, A.; Doe, B.", r.authors);
    check("1d pool hit -> exclusion_reason kept", r.exclusion_reason === "off-topic", r.exclusion_reason);
    check("1e pool hit -> matched === false", r.matched === false, r.matched);
    check("1f consensus pool (no claimLabel) -> no claim column", !("claim" in r), r.claim);
  }

  // 2 compare: claim label carried from the pool
  {
    const pool = typeof comparePool === "function" ? comparePool([], "Claim A: y", [W2], "Claim B: x") : [];
    const r = plain(excludedRows([], [entry(T2, "animal study")], pool))[0] || {};
    check("2 compare pool B item -> claim \"x\", claim_side \"B\"", r.claim === "x" && r.claim_side === "B", r.claim);
  }

  // 3 title in both A and B -> A wins
  {
    const wA = Object.assign({}, W2, { doi: "10.1/a" });
    const wB = Object.assign({}, W2, { doi: "10.1/b" });
    const pool = typeof comparePool === "function" ? comparePool([wA], "Claim A: y", [wB], "Claim B: x") : [];
    const r = plain(excludedRows([], [entry(T2, "r")], pool))[0] || {};
    check("3a title in A and B -> claim is the A claim", r.claim === "y" && r.claim_side === "A", r.claim);
    check("3b title in A and B -> A work's doi", r.doi === "10.1/a", r.doi);
  }

  // 4 case / punctuation / accent differences still match
  {
    const r = plain(excludedRows([], [entry("ADMINISTRATION of intravenous amino-acids, and postoperative hypothérmia!", "r")], [{ work: W1 }]))[0] || {};
    check("4 case/punctuation/accent variant -> matched to pool work", r.doi === W1.doi, r);
  }

  // 5 only a 20-char shared prefix -> no match
  {
    const a = "abcdefghij klmnopqrs one completely different ending here";
    const b = "abcdefghij klmnopqrs two another unrelated tail text";
    const r = plain(excludedRows([], [entry(a, "r")], [{ work: { title: b, doi: "10.9/x" } }]))[0];
    check("5 20-char prefix only -> old shape", JSON.stringify(r) === JSON.stringify(oldShape(a, "r")), r);
  }

  // 6 absent from pool -> old shape exactly
  {
    const r = plain(excludedRows([], [entry("Not in the pool at all, a unique title", "why")], [{ work: W1 }]))[0];
    check("6 absent from pool -> old shape", JSON.stringify(r) === JSON.stringify(oldShape("Not in the pool at all, a unique title", "why")), r);
  }

  // 7 pool undefined / null / not array / null works -> no throw, old shape
  for (const [label, pool] of [["undefined", undefined], ["null", null], ["object", { work: W1 }],
    ["null items", [null, { work: null }, { work: { title: null } }]]]) {
    let r, err = null;
    try { r = plain(excludedRows([], [entry(T1, "r")], pool))[0]; } catch (e) { err = String(e); }
    check("7 pool " + label + " -> no throw, old shape", !err && JSON.stringify(r) === JSON.stringify(oldShape(T1, "r")), err || r);
  }

  // 8 rendered items unchanged
  {
    const items = [{ work: W1, ex: { reason: "dup" }, claimLabel: "Claim A: y" }];
    const withPool = plain(excludedRows(items, [], [{ work: W2 }]));
    const want = plain(excludedRows(items, []));
    const r = withPool[0] || {};
    check("8a rendered item -> matched === true", r.matched === true, r.matched);
    check("8b rendered item -> fields unchanged by pool", JSON.stringify(withPool) === JSON.stringify(want), withPool);
    check("8c rendered item -> metadata + claim + reason", r.doi === W1.doi && r.claim === "y" && r.claim_side === "A" && r.exclusion_reason === "dup", r);
  }

  // 9 findExclusion behaviour unchanged
  {
    const long = "randomized controlled trial of vitamin d supplementation in older adults";
    const e1 = entry(long, "a");
    const e2 = entry("short twenty char ttl", "b");
    ctx.aiExcluded = [e1, e2];
    check("9a exact title -> entry", findExclusion(long) === e1);
    check("9b 25+ prefix title (30-char truncation) -> entry", findExclusion(long.slice(0, 30)) === e1);
    check("9c 20-char prefix -> null", findExclusion("randomized controlled trial of something else") === null &&
      findExclusion(long.slice(0, 20)) === null);
    check("9d empty title -> null", findExclusion("") === null);
    ctx.aiExcluded = [];
  }

  // 10 call sites hand the pool through
  {
    const res = extractFunction("renderExcludedSection") || "";
    check("10a renderExcludedSection passes pool to excludedRows", /excludedRows\(items, unmatched, pool\)/.test(res));
    check("10b consensus passes result.all_studies as pool", /renderExcludedSection\(sink, 'consensus_excluded', '[^']+',\s*[^)]*result\.all_studies/.test(html));
    check("10c compare passes comparePool(allA, labelA, allB, labelB)", /renderExcludedSection\(labeled, 'compare_excluded', '[^']+',\s*comparePool\(allA, labelA, allB, labelB\)\)/.test(html));
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
