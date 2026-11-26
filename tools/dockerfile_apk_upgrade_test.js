// Runtime apk upgrade test (OpenSSL CVEs fixed in 3.5.9-r1, plus zlib).
//
// Run: node tools/dockerfile_apk_upgrade_test.js [path/to/Dockerfile]
//
// grype (--only-fixed --fail-on high) reported libcrypto3 and libssl3
// 3.5.9-r0 in the alpine:3.24 runtime image; the fix is 3.5.9-r1. This static
// check reads the Dockerfile and asserts, for the final stage (after the last
// FROM):
//   (a) exactly one non-comment line contains "apk upgrade";
//   (b) that line contains --no-cache, zlib, libcrypto3 and libssl3;
//   (c) no "apk upgrade" anywhere is without a package name.
"use strict";
const fs = require("fs");
const path = require("path");

const file = process.argv[2] || path.join(__dirname, "..", "Dockerfile");

// Normalise so a CRLF worktree and the LF CI checkout read the same text.
let raw = fs.readFileSync(file, "utf8");
if (raw.charCodeAt(0) === 0xfeff) raw = raw.slice(1);
const src = raw
    .split("\r\n").join("\n")
  .split("\r").join("\n");
const lines = src.split("\n");

let failed = 0;
function check(label, ok) {
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label);
}

let lastFrom = -1;
lines.forEach((l, i) => {
  if (l.startsWith("FROM ")) lastFrom = i;
});
if (lastFrom < 0) {
  console.error("FAIL: no FROM line in " + file + " - refusing to pass silently");
  process.exit(1);
}

const isCode = (l) => !l.trim().startsWith("#");
const finalUpgrades = lines.slice(lastFrom).filter((l) => isCode(l) && l.includes("apk upgrade"));
check("(a) exactly one apk upgrade line in the final stage (found " + finalUpgrades.length + ")", finalUpgrades.length === 1);

const line = finalUpgrades[0] || "";
for (const want of ["--no-cache", "zlib", "libcrypto3", "libssl3"]) {
  const words = line.split(" ").filter((w) => w !== "");
  check("(b) apk upgrade line contains " + want, words.includes(want));
}

// Each apk upgrade segment (up to && or ;) must name at least one package.
let bare = 0;
for (const l of lines.filter(isCode)) {
  const parts = l.split("apk upgrade").slice(1);
  for (const p of parts) {
    const seg = p.split("&&")[0].split(";")[0];
    const pkgs = seg.split(" ").filter((w) => w !== "" && w !== "\\" && !w.startsWith("-"));
    if (pkgs.length === 0) bare++;
  }
}
check("(c) no apk upgrade without a package name (found " + bare + ")", bare === 0);

if (failed) {
  console.error(failed + " check(s) failed");
  process.exit(1);
}
console.log("all apk upgrade checks pass");
