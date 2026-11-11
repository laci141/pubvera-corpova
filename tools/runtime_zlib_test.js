// Runtime zlib upgrade test (CVE-2026-4738).
//
// Run: node tools/runtime_zlib_test.js
//
// zlib 1.3.2-r1 was measured in the live alpine:3.24 runtime container; the
// fix is 1.4-r0 (same fix as Recallis PR #39). This static check reads the
// Dockerfile and asserts:
//   (a) the final stage (after the last FROM) is alpine:3.24, optionally
//       pinned with an @sha256 digest;
//   (b) the final stage has a RUN that contains "apk upgrade --no-cache zlib";
//   (c) that RUN comes before the USER line (root is needed for apk);
//   (d) no "apk upgrade" anywhere without a package name;
//   (e) the build stages (everything before the last FROM) are unchanged.
"use strict";
const fs = require("fs");
const path = require("path");
const crypto = require("crypto");

// Normalise so a CRLF worktree and the LF CI checkout read the same text:
// strip a BOM, CRLF/CR -> LF, drop trailing newlines.
const src = fs
  .readFileSync(path.join(__dirname, "..", "Dockerfile"), "utf8")
  .replace(/^﻿/, "")
  .replace(/\r\n?/g, "\n")
  .replace(/\n+$/, "");
const lines = src.split("\n");

let failed = 0;
function check(label, ok) {
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label);
}

let lastFrom = -1;
lines.forEach((l, i) => {
  if (/^FROM\s/.test(l)) lastFrom = i;
});
if (lastFrom < 0) {
  console.error("FAIL: no FROM line in Dockerfile - refusing to pass silently");
  process.exit(1);
}

check(
  "(a) final stage is alpine:3.24 (found: " + lines[lastFrom] + ")",
  /^FROM alpine:3\.24(@sha256:[0-9a-f]{64})?\s*$/.test(lines[lastFrom])
);

// Join backslash continuations so a multi-line RUN is one instruction.
const finalStage = lines.slice(lastFrom).join("\n").replace(/\\\n/g, " ").split("\n");
const zlibUpgrade = /\bapk\s+upgrade\s+--no-cache\s+zlib\b/;
const runIdx = finalStage.findIndex((l) => /^RUN\s/.test(l) && zlibUpgrade.test(l));
const userIdx = finalStage.findIndex((l) => /^USER\s/.test(l));
check("(b) final stage has a RUN containing apk upgrade --no-cache zlib", runIdx >= 0);
check(
  "(c) zlib upgrade RUN is before USER (RUN at " + runIdx + ", USER at " + userIdx + ")",
  runIdx >= 0 && userIdx > runIdx
);

const bareUpgrade = src
  .replace(/\\\n/g, " ")
  .split("\n")
  .filter((l) => /\bapk\s+upgrade(\s+--?[\w-]+)*\s*(&&|;|$)/.test(l));
check("(d) no apk upgrade without a package name (found " + bareUpgrade.length + ")", bareUpgrade.length === 0);

// sha256 of the normalised text before the last FROM, measured on main
// 1d919eb (Go 1.27.4 builders). CI runs the PR merge commit, so this pin must
// match origin/main's build stages; re-pin it when main changes them.
const buildStages = lines.slice(0, lastFrom).join("\n") + "\n";
const hash = crypto.createHash("sha256").update(buildStages).digest("hex");
check(
  "(e) build stages unchanged (sha256 " + hash.slice(0, 12) + ")",
  hash === "b85bc72b0f1f321c087e66e780c5859d80e4b1bff1e12289cadb68c6ab048541"
);

if (failed) {
  console.error(failed + " check(s) failed");
  process.exit(1);
}
console.log("all runtime zlib checks pass");
