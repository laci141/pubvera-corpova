// CSV formula-injection guard test (X-19).
//
// Run: node tools/csv_injection_test.js
//
// A string cell starting with = + - @ TAB or CR must be prefixed with a single
// quote so spreadsheet apps do not evaluate it as a formula (OWASP CSV
// Injection). Extracts the boolCell and toCSV function sources from index.html
// by a brace-balanced scan and evaluates only those two inside a node:vm
// context, so no DOM is needed.
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const html = fs.readFileSync(path.join(__dirname, "..", "index.html"), "utf8");

// extractFunction returns the full source of "function <name>(...) { ... }".
// Braces inside strings/regexes in these two small functions are balanced, so a
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

const boolSrc = extractFunction("boolCell");
const csvSrc = extractFunction("toCSV");
if (!boolSrc || !csvSrc) {
  console.error("FAIL: boolCell or toCSV not found in index.html - refusing to pass silently");
  process.exit(1);
}
const ctx = vm.createContext({});
try {
  vm.runInContext(boolSrc + "\n" + csvSrc + "\nthis.toCSV = toCSV;", ctx);
} catch (e) {
  console.error("FAIL: could not load toCSV: " + (e && e.stack || e));
  process.exit(1);
}
const toCSV = ctx.toCSV;
if (typeof toCSV !== "function") {
  console.error("FAIL: toCSV did not load as a function - refusing to pass silently");
  process.exit(1);
}

let ran = 0;
let failed = 0;
function check(label, ok, detail) {
  ran++;
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label + (ok || detail === undefined ? "" : "  got " + JSON.stringify(detail)));
}

const BOM = String.fromCharCode(0xFEFF);
// dataCell returns the single data cell produced for value v under column "a".
function dataCell(v) {
  const out = toCSV([{ a: v }]);
  const lines = out.slice(BOM.length).split("\r\n");
  // lines: "sep=,", "a", <cell> (a CR/LF inside a quoted cell keeps the rest joined)
  return lines.slice(2).join("\r\n");
}

try {
  check("1 \"=1+1\" -> '=1+1", dataCell("=1+1") === "'=1+1", dataCell("=1+1"));
  check("2 \"+1\" -> '+1", dataCell("+1") === "'+1", dataCell("+1"));
  check("3 \"-1\" (strictly numeric string) unchanged", dataCell("-1") === "-1", dataCell("-1"));
  check("4 \"@SUM(A1)\" -> '@SUM(A1)", dataCell("@SUM(A1)") === "'@SUM(A1)", dataCell("@SUM(A1)"));
  check("5 \"\\tx\" -> '\\tx", dataCell("\tx") === "'\tx", dataCell("\tx"));
  check("6 \"\\rx\" -> guarded and quoted", dataCell("\rx") === "\"'\rx\"", dataCell("\rx"));
  check("7 \"=a,b\" -> \"'=a,b\"", dataCell("=a,b") === "\"'=a,b\"", dataCell("=a,b"));
  check("8 number -0.5 unchanged", dataCell(-0.5) === "-0.5", dataCell(-0.5));
  check("9 number 0.9 unchanged", dataCell(0.9) === "0.9", dataCell(0.9));
  check("10 \"normal text\" unchanged", dataCell("normal text") === "normal text", dataCell("normal text"));

  check("14a \" =1+1\" (leading space) -> ' =1+1", dataCell(" =1+1") === "' =1+1", dataCell(" =1+1"));
  check("14b \"\\n=1+1\" (leading LF) -> guarded and quoted", dataCell("\n=1+1") === "\"'\n=1+1\"", dataCell("\n=1+1"));
  check("15a full-width \\uFF1D -> prefixed", dataCell("\uFF1D1+1") === "'\uFF1D1+1", dataCell("\uFF1D1+1"));
  check("15b full-width \\uFF0B -> prefixed", dataCell("\uFF0B1") === "'\uFF0B1", dataCell("\uFF0B1"));
  check("15c full-width \\uFF0D -> prefixed", dataCell("\uFF0D1") === "'\uFF0D1", dataCell("\uFF0D1"));
  check("15d full-width \\uFF20 -> prefixed", dataCell("\uFF20SUM(A1)") === "'\uFF20SUM(A1)", dataCell("\uFF20SUM(A1)"));
  check("16a number -5 unchanged", dataCell(-5) === "-5", dataCell(-5));
  check("16b \"-5\" unchanged", dataCell("-5") === "-5", dataCell("-5"));
  check("16c \"3.14\" unchanged", dataCell("3.14") === "3.14", dataCell("3.14"));
  check("16d \"-0.5\" unchanged", dataCell("-0.5") === "-0.5", dataCell("-0.5"));
  check("16e \"-1+2\" (not strictly numeric) -> '-1+2", dataCell("-1+2") === "'-1+2", dataCell("-1+2"));

  const h1 = toCSV([{ a: 1 }], "Query: =1+1").slice(BOM.length).split("\r\n")[1];
  check("11a headerLine \"Query: =1+1\" unchanged", h1 === "Query: =1+1", h1);
  const h2 = toCSV([{ a: 1 }], "=evil").slice(BOM.length).split("\r\n")[1];
  check("11b headerLine \"=evil\" -> '=evil", h2 === "'=evil", h2);

  check("12a boolean true -> Yes", dataCell(true) === "Yes", dataCell(true));
  check("12b boolean false -> No", dataCell(false) === "No", dataCell(false));

  const full = toCSV([{ a: "x" }, { a: "y" }]);
  check("13a output starts with U+FEFF + sep=,", full.startsWith(BOM + "sep=,\r\n"), full.slice(0, 8));
  check("13b lines joined with CRLF", full === BOM + "sep=,\r\na\r\nx\r\ny", full);
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
