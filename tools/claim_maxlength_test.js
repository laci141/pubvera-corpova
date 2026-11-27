// Claim textarea maxlength test.
//
// Run: node tools/claim_maxlength_test.js
//
// The server rejects claims over 500 characters (main.go maxClaimRunes), so
// every claim textarea must stop the user at the same limit: maxlength="500"
// on exactly these 6 textareas and nowhere else.
"use strict";
const fs = require("fs");
const path = require("path");

const html = fs.readFileSync(path.join(__dirname, "..", "index.html"), "utf8");

let failed = 0;
function check(label, ok) {
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label);
}

const ids = ["consensusClaim", "evidenceClaim", "compareClaim1", "compareClaim2", "gapsClaim", "controversiesClaim"];
for (const id of ids) {
  const m = new RegExp('<textarea\\b[^>]*\\bid="' + id + '"[^>]*>').exec(html);
  check('<textarea id="' + id + '"> exists', !!m);
  check('<textarea id="' + id + '"> has maxlength="500"', !!m && /\bmaxlength="500"/.test(m[0]));
}

const count = (html.match(/maxlength="500"/g) || []).length;
check('index.html has exactly 6 maxlength="500" (found ' + count + ")", count === 6);

if (failed) {
  console.log(failed + " check(s) failed");
  process.exit(1);
}
console.log("all checks passed");
