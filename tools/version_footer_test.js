// Footer version test.
//
// Run: node tools/version_footer_test.js
//
// The footer shows "Corpova v<version>" from GET /version. This static check
// reads index.html and asserts:
//   1. exactly one element has id="app-version", inside the <footer>;
//   2. the page fetches '/version' with cache 'no-store';
//   3. the text is written with textContent, never innerHTML;
//   4. a failed fetch leaves the element empty (no error text is shown);
//   5. the fetch lives in its own small classic script (no attributes at all,
//      an IIFE, no named functions, no inline handlers), so the first-<script>
//      extraction in the other tools/*_test.js never picks it up and a CSP
//      nonce can later be added to its one tag.
"use strict";
const fs = require("fs");
const path = require("path");

const html = fs.readFileSync(path.join(__dirname, "..", "index.html"), "utf8").replace(/\r/g, "");

let failed = 0;
function check(label, ok) {
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label);
}

const ids = html.match(/\bid=["']app-version["']/g) || [];
check("1 exactly one id=\"app-version\" (found " + ids.length + ")", ids.length === 1);
const footer = html.match(/<footer\b[^>]*>([\s\S]*?)<\/footer>/i);
check("1 id=\"app-version\" is inside the <footer>", !!footer && /\bid=["']app-version["']/.test(footer[1]));

// The script block that does the fetch.
const scripts = [...html.matchAll(/<script\b(?: nonce="__CSP_NONCE__")?([^>]*)>([\s\S]*?)<\/script>/gi)].map((m) => ({ attrs: m[1], body: m[2] }));
const found = scripts.filter((s) => /fetch\(\s*['"]\/version['"]/.test(s.body));
const block = found.length === 1 ? found[0].body : "";
check("2 exactly one script fetches '/version' (found " + found.length + ")", found.length === 1);
check("2 fetch uses cache: 'no-store'", /fetch\(\s*['"]\/version['"]\s*,\s*\{[^}]*cache:\s*['"]no-store['"]/.test(block));

check("3 writes with textContent", /\.textContent\s*=\s*['"]Corpova v['"]\s*\+/.test(block));
check("3 never uses innerHTML", block !== "" && !/innerHTML/.test(block));

// 4: the catch handler must not write anything.
const c = block.match(/\.catch\(\s*\(\)\s*=>\s*\{([^}]*)\}\s*\)/);
check("4 has a .catch handler", !!c);
check("4 catch handler writes nothing", !!c && !/textContent|innerHTML|innerText|append/.test(c[1]));

check("5 classic script (no type or other attribute)", found.length === 1 && found[0].attrs.trim() === "");
const firstClassic = scripts.find((s) => s.attrs.trim() === "");
check("5 not the first classic script", !!firstClassic && !/fetch\(\s*['"]\/version['"]/.test(firstClassic.body));
check("5 body is one IIFE", /^\s*\(function \(\) \{/.test(block) && /\}\)\(\);\s*$/.test(block));
check("5 no named function", block !== "" && !/function\s+[A-Za-z_$]/.test(block));
check("5 no inline event handler attribute", block !== "" && !/\son[a-z]+\s*=/i.test(block));
check("5 small (under 20 lines)", block !== "" && block.split("\n").length < 20);

if (failed) {
  console.error(failed + " check(s) failed");
  process.exit(1);
}
console.log("all footer version checks pass");
