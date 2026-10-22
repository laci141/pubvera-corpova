// CI action pinning test.
//
// Run: node tools/ci_pin_test.js
//
// A tag like actions/checkout@v6 can be moved by whoever controls the action
// repo, so every action is pinned to a full commit SHA with the exact version
// in a trailing comment. This static check reads .github/workflows/deploy.yml
// and asserts:
//   (a) there are exactly 7 "uses:" lines;
//   (b) every "uses:" line ends in "@<40 hex> # v<major>.<minor>.<patch>";
//   (c) no "uses:" line pins a bare tag ("@v<digits>") instead of a SHA;
//   (d) all 5 pinned actions are present;
//   (e) .github/dependabot.yml keeps the github-actions ecosystem enabled.
"use strict";
const fs = require("fs");
const path = require("path");

const root = path.join(__dirname, "..", ".github");
const yml = fs.readFileSync(path.join(root, "workflows", "deploy.yml"), "utf8").replace(/\r/g, "");
const dependabot = fs.readFileSync(path.join(root, "dependabot.yml"), "utf8").replace(/\r/g, "");

let failed = 0;
function check(label, ok) {
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label);
}

const uses = yml.split("\n").filter((l) => /^\s*(-\s+)?uses:/.test(l)).map((l) => l.trim());
if (uses.length === 0) {
  console.error("FAIL: no uses: lines in deploy.yml - refusing to pass silently");
  process.exit(1);
}

check("(a) exactly 7 uses: lines (found " + uses.length + ")", uses.length === 7);

const pinned = /^(-\s+)?uses:\s+[\w.-]+\/[\w.-]+@[0-9a-f]{40} # v\d+\.\d+\.\d+$/;
for (const l of uses) {
  check("(b) SHA-pinned with version comment: " + l, pinned.test(l));
}
for (const l of uses) {
  check("(c) no bare tag pin: " + l, !/@v\d/.test(l));
}

const actions = [
  "actions/checkout",
  "actions/setup-go",
  "docker/build-push-action",
  "docker/login-action",
  "docker/setup-buildx-action",
];
const names = uses.map((l) => l.replace(/^(-\s+)?uses:\s+/, "").split("@")[0]);
for (const a of actions) {
  check("(d) " + a + " present", names.includes(a));
}

check("(e) dependabot.yml enables github-actions", /package-ecosystem:\s*"github-actions"/.test(dependabot));

if (failed) {
  console.error(failed + " check(s) failed");
  process.exit(1);
}
console.log("all CI action pin checks pass");
