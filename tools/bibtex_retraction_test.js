// Retraction flag in the BibTeX note.
//
// Run: node tools/bibtex_retraction_test.js
//
// The study cards mark a paper "Retracted" or "Retracted?" and the export rows
// carry `retraction`, but toBibTeX wrote only the generic provenance note, so
// someone citing from the .bib could not see that a paper was retracted (seen
// in the live export of 2026-11-11, "vitamin D reduces fractures": two
// retracted-flagged rows, plain notes). Runs the real toBibTeX in a vm:
//   a  retracted-declared -> note says RETRACTED and "excluded from the Corpova score";
//   b  retracted-flagged  -> note says flagged, not confirmed, verify before citing;
//   c  empty, missing, null and unknown values -> the note is the old one, byte for byte;
//   d  the note stays one balanced {...} field (no stray brace or newline), and the
//      exclusion reason still follows it;
//   e  the other fields of the entry are untouched.
// A piece that cannot be found is a FAIL, never a silent pass.
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");
const html = fs.readFileSync(path.join(__dirname, "..", "index.html"), "utf8").replace(/\r\n/g, "\n");

let ran = 0, failed = 0;
function check(label, ok, got) { ran++; if (!ok) failed++; console.log((ok ? "PASS" : "FAIL") + "  " + label + (ok || got === undefined ? "" : "  got " + JSON.stringify(got))); }

function extractFunction(name) {
  const start = html.indexOf("function " + name + "(");
  if (start < 0) return null;
  const open = html.indexOf("{", start);
  let depth = 0;
  for (let i = open; i < html.length; i++) {
    if (html[i] === "{") depth++;
    else if (html[i] === "}") { depth--; if (depth === 0) return html.slice(start, i + 1); }
  }
  return null;
}
const srcs = [];
for (const n of ["bibEsc", "normDoi", "bibKey", "asciiSlug", "hash3", "uniqueKey", "keyWords"]) {
  const s = extractFunction(n);
  if (!s) { console.error("FAIL: " + n + " not found in index.html - refusing to pass silently"); process.exit(1); }
  srcs.push(s);
}
// toBibTeX has "@article{" in string literals, so it is cut at its closing return.
const start = html.indexOf("function toBibTeX(");
const ret = start < 0 ? -1 : html.indexOf("return out.join('\\n\\n') + '\\n';", start);
const close = ret < 0 ? -1 : html.indexOf("}", ret);
if (close < 0) { console.error("FAIL: toBibTeX not found - refusing to pass silently"); process.exit(1); }
srcs.push(html.slice(start, close + 1));
const ctx = vm.createContext({ KEY_STOPWORDS: new Set(), window: { location: { origin: "http://localhost" } } });
vm.runInContext(srcs.join("\n") + "\nthis.toBibTeX = toBibTeX;", ctx);
const toBibTeX = ctx.toBibTeX;

const base = { title: "A paper", authors: "Ann Lee; Bo Kim", journal: "J Test", year: 2005, doi: "10.1/x" };
const OLD = "  note    = {Exported from Corpova; metadata via OpenAlex (CC0)}";
const bib = (extra) => toBibTeX([Object.assign({}, base, extra)], new Set());
const noteOf = b => (b.match(/  note    = \{[^\n]*\}\n/) || [""])[0];

const dec = bib({ retraction: "retracted-declared" });
check("a  declared: RETRACTED + excluded from the score", /note    = \{[^\n]*RETRACTED: this paper has been retracted[^\n]*excluded from the Corpova score/.test(dec), noteOf(dec));
const flg = bib({ retraction: "retracted-flagged" });
check("b  flagged: not confirmed + verify before citing",
  /Flagged as retracted in the source index, not confirmed from the title/.test(flg) && /verify before citing/.test(flg) && !/RETRACTED: this paper/.test(flg), noteOf(flg));
for (const [label, v] of [["empty string", ""], ["missing", undefined], ["null", null], ["unknown value", "retracted-maybe"], ["a different case", "RETRACTED-DECLARED"]]) {
  const e = v === undefined ? {} : { retraction: v };
  const b = bib(e);
  check("c  " + label + " -> old note unchanged", b.includes(OLD + "\n") && !/RETRACTED|Flagged/.test(b), noteOf(b));
}
for (const [label, e] of [["declared", { retraction: "retracted-declared" }], ["flagged", { retraction: "retracted-flagged" }]]) {
  const b = bib(Object.assign({ exclusion_reason: "off-topic" }, e));
  const n = noteOf(b);
  const inner = n.replace(/^  note    = \{/, "").replace(/\}\n$/, "");
  check("d  " + label + ": one balanced note field, reason follows",
    n !== "" && !/[{}]/.test(inner) && /Exclusion reason: off-topic$/.test(inner) && b.trim().endsWith("}"), n);
}
const plainB = bib({}), decB = bib({ retraction: "retracted-declared" });
check("e  all other lines identical to the plain entry",
  plainB.split("\n").filter(l => !l.startsWith("  note")).join("\n") === decB.split("\n").filter(l => !l.startsWith("  note")).join("\n"));
const mixed = toBibTeX([Object.assign({}, base, { title: "One" }), Object.assign({}, base, { title: "Two", doi: "10.1/y", retraction: "retracted-flagged" })], new Set());
check("e  only the flagged entry of two gets the flag", (mixed.match(/Flagged as retracted/g) || []).length === 1 && mixed.indexOf("Flagged as retracted") > mixed.indexOf("Two"));

console.log("\n" + (ran - failed) + "/" + ran + " passed");
process.exit(failed ? 1 : 0);
