// "abstract shortened" chip on the study cards.
//
// Run: node tools/abstract_chip_test.js
//
// The CLI caps abstracts at 1500 characters and sets abstract_truncated. The
// exports carry the field, but the page shows no abstract, so a reader could
// not tell a cut abstract from a whole one. renderStudyCard now adds a chip.
// Runs the real renderStudyCard inside a node:vm context (no DOM):
//   a  abstract_truncated === true  -> exactly one chip, with the 1500 tooltip;
//   b  false, missing (older CLI), null, "true" (string) -> no chip;
//   c  the chip does not touch the other chips or the retraction note;
//   d  it also shows on an AI-excluded card (second argument set);
//   e  the markup is escaped like the other chips (a title with < and ").
// A piece that cannot be found is a FAIL, never a silent pass.
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");
const html = fs.readFileSync(path.join(__dirname, "..", "index.html"), "utf8").replace(/\r\n/g, "\n");

let ran = 0, failed = 0;
function check(label, ok, extra) { ran++; if (!ok) failed++; console.log((ok ? "PASS" : "FAIL") + "  " + label + (ok || extra === undefined ? "" : "  " + JSON.stringify(extra))); }

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
const src = extractFunction("renderStudyCard");
check("renderStudyCard found", !!src);
if (!src) { console.log("\n0/1 passed"); process.exit(1); }
const ctx = {};
vm.createContext(ctx);
vm.runInContext(
  "const esc = s => String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/\"/g,'&quot;');" +
  "const num = n => String(n || 0); const fx = n => Number(n).toFixed(2);" + src + ";this.render = renderStudyCard;", ctx);
const render = ctx.render;
const base = { title: "T", year: 2020, cited_by_count: 3, design: "meta-analysis", stance: "supporting", stance_confidence: 0.9, doi: "10.1/x" };
const chipCount = h => (h.match(/>abstract shortened</g) || []).length;

const yes = render(Object.assign({}, base, { abstract_truncated: true }));
check("a  true -> one chip", chipCount(yes) === 1, chipCount(yes));
check("a  chip carries the 1500-character tooltip", yes.includes('title="The abstract was cut at 1500 characters; the export holds the shortened text."'));
for (const [label, v] of [["false", false], ["missing", undefined], ["null", null], ["string 'true'", "true"], ["1", 1]]) {
  const w = Object.assign({}, base); if (v !== undefined) w.abstract_truncated = v;
  const h = render(w);
  check("b  " + label + " -> no chip", chipCount(h) === 0, chipCount(h));
}
const plain = render(base);
check("c  other chips unchanged when the chip is added",
  yes.replace(/<span class="study-chip" title="[^"]*">abstract shortened<\/span>/, "") === plain);
const retr = render(Object.assign({}, base, { abstract_truncated: true, retraction: "retracted-declared" }));
check("c  retraction chip still first, note kept, chip present",
  retr.indexOf("retracted-chip") < retr.indexOf("abstract shortened") && retr.includes("This paper has been retracted") && chipCount(retr) === 1);
const ex = render(Object.assign({}, base, { abstract_truncated: true }), { reason: "off-topic" });
check("d  AI-excluded card shows it too, reason kept", chipCount(ex) === 1 && ex.includes("off-topic"));
const esc = render(Object.assign({}, base, { title: 'A <b>"x"</b>', abstract_truncated: true }));
check("e  title escaped, chip intact", esc.includes("A &lt;b&gt;&quot;x&quot;&lt;/b&gt;") && !esc.includes("<b>") && chipCount(esc) === 1);

console.log("\n" + (ran - failed) + "/" + ran + " passed");
process.exit(failed ? 1 : 0);
