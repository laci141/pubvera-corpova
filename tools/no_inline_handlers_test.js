// No inline event handlers test (COR-007a).
//
// Run: node tools/no_inline_handlers_test.js
//
// A strict Content-Security-Policy blocks inline event handler attributes
// (onclick="..."), so index.html must carry none, neither in markup nor in
// HTML built inside JS strings. Clicks go through one delegated listener on
// document that maps data-action="<name>" to a function in clickActions:
//   T1 no on*= attribute anywhere in the file;
//   T2 at least 21 data-action= occurrences (handlers converted, not deleted);
//   T3 every data-action value is a key of clickActions;
//   T4 every key of clickActions is used by a data-action;
//   T5 the delegated listener runs a mapped action and ignores unknown ones.
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const html = fs.readFileSync(path.join(__dirname, "..", "index.html"), "utf8");
const scriptMatch = html.match(/<script(?: nonce="__CSP_NONCE__")?>([\s\S]*?)<\/script>/);
if (!scriptMatch) {
  console.error("FAIL: no inline <script> found in index.html - refusing to pass silently");
  process.exit(1);
}
const script = scriptMatch[1];

let ran = 0;
let failed = 0;
function check(label, ok) {
  ran++;
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label);
}

// ── T1: no inline handler attribute ──
const inline = [...html.matchAll(/\son[a-z]+\s*=\s*["']/gi)];
check("T1 no on*= attribute in index.html (found " + inline.length + ")", inline.length === 0);

// ── T2: handlers were converted, not deleted ──
const actionUses = [...html.matchAll(/data-action="([^"]*)"/g)].map(m => m[1]);
check("T2 at least 21 data-action= occurrences (found " + actionUses.length + ")", actionUses.length >= 21);

// ── T3 / T4: data-action values and clickActions keys match ──
const mapMatch = script.match(/const clickActions = \{([\s\S]*?)\n\s*\};/);
const keys = mapMatch ? [...mapMatch[1].matchAll(/^\s*([A-Za-z_$][\w$]*)\s*:/gm)].map(m => m[1]) : [];
check("T3 clickActions map found with at least one key", keys.length > 0);
const used = [...new Set(actionUses)];
for (const a of used) {
  check("T3 data-action \"" + a + "\" is a clickActions key", keys.includes(a));
}
for (const k of keys) {
  check("T4 clickActions key \"" + k + "\" is used by a data-action", used.includes(k));
}

// ── T5: the delegated document click listener ──
{
  const docListeners = {};
  const signIns = [];
  const sandbox = {
    console: { log() {}, warn() {}, error() {}, info() {}, debug() {} },
    alert: () => {},
    setTimeout: () => 0,
    requestAnimationFrame: () => 0,
    localStorage: { getItem: () => null, setItem: () => {}, removeItem: () => {} },
    window: {
      location: { origin: "http://localhost" },
      addEventListener: () => {},
      pubveraAuth: { enabled: true, signIn: () => { signIns.push(1); } },
    },
    document: {
      addEventListener: (t, fn) => { (docListeners[t] = docListeners[t] || []).push(fn); },
      querySelectorAll: () => [],
      querySelector: () => null,
      getElementById: () => null,
      createElement: () => ({ style: {}, addEventListener() {} }),
    },
  };
  vm.createContext(sandbox);
  vm.runInContext(script, sandbox);
  vm.runInContext("closeModal = function (ep) { __closed.push(ep); };", Object.assign(sandbox, { __closed: [] }));

  // fakeEvent: target.closest('[data-action]') returns el; any other selector
  // (for example the table sort listener's 'th') finds nothing.
  const fakeEvent = el => ({ target: { closest: sel => (sel === "[data-action]" ? el : null) } });
  const fireClick = ev => (docListeners.click || []).forEach(fn => fn(ev));

  check("T5 a click listener is registered on document", (docListeners.click || []).length > 0);
  let threw = null;
  try { fireClick(fakeEvent({ dataset: { action: "pubveraSignIn" } })); } catch (e) { threw = e; }
  check("T5 data-action=\"pubveraSignIn\" calls window.pubveraAuth.signIn once", !threw && signIns.length === 1);
  try { fireClick(fakeEvent({ dataset: { action: "closeModal", endpoint: "gaps" } })); } catch (e) { threw = e; }
  check("T5 data-action=\"closeModal\" passes data-endpoint through", !threw && sandbox.__closed.join() === "gaps");
  threw = null;
  try { fireClick(fakeEvent({ dataset: { action: "noSuchAction" } })); } catch (e) { threw = e; }
  check("T5 an unknown action does not throw", !threw);
  check("T5 an unknown action calls nothing", signIns.length === 1 && sandbox.__closed.length === 1);
  threw = null;
  try { fireClick(fakeEvent(null)); } catch (e) { threw = e; }
  check("T5 a click outside any data-action element does not throw", !threw);
}

if (ran === 0) {
  console.error("FAIL: zero checks ran - refusing to pass silently");
  process.exit(1);
}
console.log((ran - failed) + "/" + ran + " passed, " + failed + " failed");
process.exit(failed ? 1 : 0);
