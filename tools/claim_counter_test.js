// Claim character counter test.
//
// Run: node tools/claim_counter_test.js
//
// The server rejects claims over 500 characters (main.go maxClaimRunes), so
// each claim textarea shows a live "N / 500" counter that warns at 450:
//   a. exactly 6 .claim-counter elements, one data-for per claim textarea,
//      each placed directly after its textarea;
//   b. the script holds CLAIM_MAX = 500 and CLAIM_WARN = 450;
//   c. .claim-counter, .is-warn and .is-over are defined in the style block;
//   d. every counter's textarea gets an 'input' listener that updates it;
//   e. updateClaimCounters, run against a stub DOM, counts characters (not
//      UTF-16 units) and toggles is-warn at >= 450 and is-over at > 500.
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const html = fs.readFileSync(path.join(__dirname, "..", "index.html"), "utf8").replace(/\r\n/g, "\n");

let failed = 0;
function check(label, ok) {
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label);
}

const css = (html.match(/<style\b[^>]*>([\s\S]*?)<\/style>/) || [, ""])[1];
// The classic (non-module) inline script blocks, joined.
const script = [...html.matchAll(/<script\b(?![^>]*\bsrc=)(?![^>]*type="module")[^>]*>([\s\S]*?)<\/script>/g)]
  .map(m => m[1]).join("\n");
if (!css || !script) {
  console.error("FAIL: no <style> or inline <script> found in index.html - refusing to pass silently");
  process.exit(1);
}

// ── a: markup ──
const ids = ["consensusClaim", "evidenceClaim", "compareClaim1", "compareClaim2", "gapsClaim", "controversiesClaim"];
const counters = [...html.matchAll(/<div\b[^>]*\bclass="claim-counter"[^>]*>/g)].map(m => m[0]);
check("exactly 6 class=\"claim-counter\" elements (found " + counters.length + ")", counters.length === 6);
const forIds = counters.map(c => (/\bdata-for="([^"]*)"/.exec(c) || [, null])[1]);
check("every claim-counter has a data-for", forIds.every(Boolean));
for (const id of ids) {
  check('one claim-counter has data-for="' + id + '"', forIds.filter(f => f === id).length === 1);
  const after = new RegExp('<textarea\\b[^>]*\\bid="' + id + '"[^>]*></textarea>\\s*<div class="claim-counter" data-for="' + id + '"');
  check('claim-counter for ' + id + ' directly follows its textarea', after.test(html));
}

// ── b: limits ──
check("script defines const CLAIM_MAX = 500", /\bconst CLAIM_MAX = 500;/.test(script));
check("script defines const CLAIM_WARN = 450", /\bconst CLAIM_WARN = 450;/.test(script));

// ── c: CSS ──
check(".claim-counter is defined in the style block", /(^|[\s,}])\.claim-counter\s*\{/m.test(css));
check(".claim-counter.is-warn is defined in the style block", /\.claim-counter\.is-warn\s*\{/.test(css));
check(".claim-counter.is-over is defined in the style block", /\.claim-counter\.is-over\s*\{/.test(css));

// ── d: input listener ──
check("textareas get addEventListener('input', updateClaimCounters)",
  /addEventListener\('input', updateClaimCounters\)/.test(script));

// ── e: behaviour against a stub DOM ──
const fnSrc = (script.match(/function updateClaimCounters\(\) \{[\s\S]*?\n    \}/) || [""])[0];
check("updateClaimCounters function found", !!fnSrc);
if (fnSrc) {
  function stubCounter(forId) {
    const set = new Set();
    return {
      dataset: { for: forId }, textContent: "",
      classList: { toggle: (c, on) => (on ? set.add(c) : set.delete(c)), has: c => set.has(c) },
    };
  }
  const ta = { value: "" };
  const counter = stubCounter("t");
  const document = {
    querySelectorAll: sel => (sel === ".claim-counter" ? [counter] : []),
    getElementById: id => (id === "t" ? ta : null),
  };
  const ctx = vm.createContext({ document });
  vm.runInContext("const CLAIM_MAX = 500; const CLAIM_WARN = 450;\n" + fnSrc + "\nthis.run = updateClaimCounters;", ctx);
  const cases = [
    ["", "0 / 500", false, false],
    ["a".repeat(449), "449 / 500", false, false],
    ["a".repeat(450), "450 / 500", true, false],
    ["\u{1F600}".repeat(450), "450 / 500", true, false], // 900 UTF-16 units, 450 characters
    ["a".repeat(500), "500 / 500", true, false],
    ["a".repeat(501), "501 / 500", true, true],
  ];
  for (const [value, text, warn, over] of cases) {
    ta.value = value;
    ctx.run();
    const label = value.length + " UTF-16 units";
    check(label + ' -> text "' + text + '" (got "' + counter.textContent + '")', counter.textContent === text);
    check(label + " -> is-warn " + warn, counter.classList.has("is-warn") === warn);
    check(label + " -> is-over " + over, counter.classList.has("is-over") === over);
  }
}

if (failed) {
  console.log(failed + " check(s) failed");
  process.exit(1);
}
console.log("all checks passed");
