// Every form control has an accessible name.
//
// Run: node tools/form_labels_test.js [html-file]   (the optional file is for mutation runs)
//
// Measured 2026-11-28 with the page open in Chromium: 27 of the 32 form controls had no
// accessible name. The five modals had a visible <label> above each control but no
// for= on it, so the label was only text; the hero textarea had a placeholder and no
// label at all. A placeholder is not a name, and a label without for= names nothing.
//   a  each modal control has exactly one <label for=ID> and its id appears once;
//   b  the hero textarea has an aria-label;
//   c  no <label> is without for= unless it wraps its control;
//   d  every input / select / textarea has a name: label for=, a wrapping label,
//      aria-label or aria-labelledby (hidden inputs excepted); this includes the
//      quick-filter box that the result toolbars add after an analysis runs.
// A piece that cannot be found is a FAIL, never a silent pass.
"use strict";
const fs = require("fs");
const path = require("path");

const file = process.argv[2] || path.join(__dirname, "..", "index.html");
const html = fs.readFileSync(file, "utf8").replace(/\r\n/g, "\n");

let ran = 0, failed = 0;
function check(label, ok, got) { ran++; if (!ok) failed++; console.log((ok ? "PASS" : "FAIL") + "  " + label + (ok || got === undefined ? "" : "  got " + JSON.stringify(got))); }
const count = re => (html.match(re) || []).length;

// a
const kinds = ["Claim", "Key", "Provider", "Model", "Limit"];
const modals = { consensus: kinds, evidence: kinds, gaps: kinds, controversies: kinds,
  compare: ["Claim1", "Claim2", "Key", "Provider", "Model", "Limit"] };
for (const [ep, list] of Object.entries(modals)) {
  for (const k of list) {
    const id = ep + k;
    const forCount = count(new RegExp("<label[^>]*[ ]for=\"" + id + "\"", "g"));
    const idCount = count(new RegExp("<(?:input|select|textarea)[^>]*[ ]id=\"" + id + "\"", "g"));
    check("a  " + id + ": one label for=, one control", forCount === 1 && idCount === 1, { forCount, idCount });
  }
}

// b
const hero = /<textarea[^>]*[ ]id="heroClaimInput"[^>]*>/.exec(html);
check("b  heroClaimInput has a non-empty aria-label", !!hero && /[ ]aria-label="[^"]+"/.test(hero[0]), hero && hero[0]);

// c
const labels = html.match(/<label[^>]*>[^]*?<[/]label>/g) || [];
const bad = labels.filter(l => !/^<label[^>]*[ ]for="/.test(l) && !/<(input|select|textarea)[ >]/.test(l));
check("c  no <label> without for= that does not wrap a control (" + labels.length + " labels, " + bad.length + " bad)", labels.length > 0 && bad.length === 0, bad);

// d
// A control inside a wrapping <label> is named by it; the rest must be named another way.
const unwrapped = html.replace(/<label[^>]*>[^]*?<[/]label>/g, m => (/[ ]for="/.test(m.split(">")[0]) ? m : ""));
const controls = (unwrapped.match(/<(?:input|select|textarea)\b[^>]*>/g) || []).filter(t => !/type="hidden"/.test(t));
const forIds = new Set((html.match(/<label[^>]*[ ]for="[^"]+"/g) || []).map(t => /for="([^"]+)"/.exec(t)[1]));
const wrapped = new Set();
for (const l of labels) { const m = /<(?:input|select|textarea)\b[^>]*[ ]id="([^"]+)"/.exec(l); if (m) wrapped.add(m[1]); }
const unnamed = controls.filter(t => {
  const id = (/[ ]id="([^"]+)"/.exec(t) || [])[1];
  return !(id && (forIds.has(id) || wrapped.has(id))) && !/[ ]aria-label="[^"]+"/.test(t) && !/[ ]aria-labelledby="[^"]+"/.test(t);
});
check("d  every control has a name (" + controls.length + " controls, " + unnamed.length + " unnamed)", controls.length >= 28 && unnamed.length === 0, unnamed.map(t => t.slice(0, 60)));

if (ran === 0) { console.error("FAIL: zero checks ran"); process.exit(1); }
console.log("\n" + (ran - failed) + "/" + ran + " passed");
process.exit(failed ? 1 : 0);
