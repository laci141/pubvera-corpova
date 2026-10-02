// Subresource Integrity test.
//
// Run: node tools/sri_test.js
//
// index.html holds the user's BYOK LLM key, so every third-party script it runs
// must be hash-checked by the browser (audit BUG-05):
//   1. every external <script src="http..."> carries integrity= and crossorigin=;
//   2. every script injected from JS (el.src = 'http...') sets .integrity and
//      .crossOrigin next to it;
//   3. no dynamic import() of a remote URL and no esm.sh at all: import()
//      cannot carry an integrity hash, and the esm.sh entry is a stub that
//      pulls further unversioned modules.
"use strict";
const fs = require("fs");
const path = require("path");

const html = fs.readFileSync(path.join(__dirname, "..", "index.html"), "utf8");

let failed = 0;
function check(label, ok) {
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label);
}
const SRI = /^sha384-[A-Za-z0-9+/]{64}$/;

// ── 1: static <script src> tags ──
const tags = [...html.matchAll(/<script\b[^>]*\bsrc=["'](https?:\/\/[^"']+)["'][^>]*>/gi)];
if (tags.length === 0) {
  console.error("FAIL: no external <script src> found in index.html - refusing to pass silently");
  process.exit(1);
}
for (const t of tags) {
  const integ = /\bintegrity=["']([^"']*)["']/i.exec(t[0]);
  check("<script src=" + t[1] + "> has a sha384 integrity", !!integ && SRI.test(integ[1]));
  check("<script src=" + t[1] + "> has crossorigin=\"anonymous\"", /\bcrossorigin=["']anonymous["']/i.test(t[0]));
}

// ── 2: scripts injected from JS ──
for (const m of html.matchAll(/(\w+)\.src\s*=\s*['"](https?:\/\/[^'"]+)['"]/g)) {
  const v = m[1];
  const near = html.slice(m.index, m.index + 600);
  const integ = new RegExp("\\b" + v + "\\.integrity\\s*=\\s*['\"]([^'\"]*)['\"]").exec(near);
  check("injected " + m[2] + " sets .integrity", !!integ && SRI.test(integ[1]));
  check("injected " + m[2] + " sets .crossOrigin = 'anonymous'",
    new RegExp("\\b" + v + "\\.crossOrigin\\s*=\\s*['\"]anonymous['\"]").test(near));
}

// ── 3: no remote import(), no esm.sh ──
check("no import() of a remote URL", !/import\(\s*['"`]https?:/.test(html));
check("no esm.sh reference", !/esm\.sh/.test(html));

if (failed) {
  console.error(failed + " check(s) failed");
  process.exit(1);
}
console.log("all SRI checks passed");
