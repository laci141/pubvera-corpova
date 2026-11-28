// The BibTeX file starts with ONE comment line describing the export.
//
// Run: node tools/bibtex_header_test.js
//
// The claim comes from a textarea, so it can contain line breaks, and
// exportHeader() prints it inside the header. Written after "% ", a second line
// no longer starts with "%": BibTeX ignores text outside entries, but a claim
// line such as "@article{...}" would be read as a real entry. Runs the real
// downloadBibTeX and exportHeader in a vm:
//   a  a multi-line claim -> every line before the first entry starts with "%";
//   b  a claim that contains an "@article{" line adds no entry to the file;
//   c  a single-line claim keeps its text in the header unchanged;
//   d  the entries after the header are untouched.
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
for (const n of ["exportHeader", "downloadBibTeX"]) {
  const s = extractFunction(n);
  if (!s) { console.error("FAIL: " + n + " not found in index.html - refusing to pass silently"); process.exit(1); }
  srcs.push(s);
}

function run(query) {
  let written = null;
  const ctx = vm.createContext({
    lastData: { consensus_verdict: [{}] },
    ANALYSIS_LABEL: { consensus_verdict: "Consensus analysis" },
    queryFor: () => query,
    plural: (n, w) => n + " " + w + (n === 1 ? "" : "s"),
    corpusRetrieved: () => undefined,
    rowsFromLastData: () => [{ title: "T" }],
    exportRows: () => ({ rows: [{ title: "T" }], scope: "" }),
    toBibTeX: () => "@article{k,\n  title = {T}\n}\n",
    analysisBibTeX: () => "",
    Blob: class { constructor(parts) { this.text = parts.join(""); } },
    triggerDownload: blob => { written = blob.text; },
    alert: () => { throw new Error("alert called"); }
  });
  vm.runInContext(srcs.join("\n") + "\nthis.downloadBibTeX = downloadBibTeX;", ctx);
  ctx.downloadBibTeX("consensus_verdict", "x.bib");
  return written;
}
const beforeFirstEntry = text => text.split("\n@article{k,")[0];
const entriesIn = text => (text.match(/^@\w+\{/gm) || []).length;

const multi = run("first line\nsecond line");
check("a  multi-line claim: every header line starts with %",
  beforeFirstEntry(multi).split("\n").filter(l => l.trim()).every(l => l.startsWith("%")), beforeFirstEntry(multi));

const evil = run("topic\n@article{evil, title={x}}");
check("b  an @article line in the claim adds no entry", entriesIn(evil) === 1, evil);

const single = run("vitamin D reduces fractures");
check("c  single-line claim kept as typed", single.indexOf('query: "vitamin D reduces fractures"') > 0, single);

check("d  the entry after the header is untouched", /\n\n@article\{k,\n  title = \{T\}\n\}\n$/.test(multi), multi);

console.log("\n" + (ran - failed) + "/" + ran + " passed");
process.exit(failed ? 1 : 0);
