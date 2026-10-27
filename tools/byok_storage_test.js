// BYOK key storage test (COR-003).
//
// Run: node tools/byok_storage_test.js
//
// The provider key typed into an analysis modal must live only in page memory
// unless the user ticks "Remember this key on this browser". Runs the first
// inline <script> of index.html inside a node:vm sandbox with a minimal fake
// DOM and a Map-backed localStorage, the same approach as
// export_wysiwyg_test.mjs (copied here, not imported).
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

const ENDPOINTS = ["consensus", "evidence", "compare", "gaps", "controversies"];
const KEY = "sk-test-COR003-secret-0123456789";
const LABEL = "Remember this key on this browser";
const HELP = "Stored in this browser's local storage. Anyone with access to this browser profile, or any script on this page, could read it. Use a key you can revoke.";
const CLEAR = "Clear key";

let ran = 0;
let failed = 0;
function check(label, ok) {
  ran++;
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label);
}

// ── fake DOM ──
function makeEl(id) {
  const listeners = {};
  return {
    id, value: "", checked: false, style: {}, innerHTML: "", textContent: "",
    listeners,
    addEventListener(type, fn) { (listeners[type] = listeners[type] || []).push(fn); },
    querySelectorAll: () => [],
    querySelector: () => null,
  };
}
function fire(el, type) {
  if (!el) return;
  (el.listeners[type] || []).forEach(fn => fn({ target: el, type }));
}

// boot loads the page script into a fresh sandbox with the given localStorage
// contents and fires DOMContentLoaded and window load, as a browser would.
function boot(preset) {
  const store = new Map(Object.entries(preset || {}));
  const localStorage = {
    getItem: k => (store.has(k) ? store.get(k) : null),
    setItem: (k, v) => { store.set(k, String(v)); },
    removeItem: k => { store.delete(k); },
  };
  const els = {};
  for (const ep of ENDPOINTS) {
    for (const f of ["Key", "KeyRemember", "KeyClear", "Provider", "Model", "ModelHint", "Form", "Results", "Claim"]) {
      els[ep + f] = makeEl(ep + f);
    }
    els[ep + "Limit"] = makeEl(ep + "Limit");
    els[ep + "Limit"].value = "25";
  }
  els.compareClaim1 = makeEl("compareClaim1");
  els.compareClaim2 = makeEl("compareClaim2");
  const docListeners = {};
  const winListeners = {};
  const logs = [];
  const capture = (...a) => { logs.push(a.map(x => (typeof x === "string" ? x : JSON.stringify(x))).join(" ")); };
  const sandbox = {
    console: { log: capture, warn: capture, error: capture, info: capture, debug: capture },
    alert: () => {},
    setTimeout: () => 0,
    requestAnimationFrame: () => 0,
    localStorage,
    window: {
      location: { origin: "http://localhost" },
      addEventListener: (t, fn) => { (winListeners[t] = winListeners[t] || []).push(fn); },
    },
    document: {
      addEventListener: (t, fn) => { (docListeners[t] = docListeners[t] || []).push(fn); },
      querySelectorAll: () => [],
      querySelector: () => null,
      getElementById: id => els[id] || null,
      createElement: () => makeEl(""),
    },
  };
  vm.createContext(sandbox);
  vm.runInContext(script, sandbox);
  (docListeners.DOMContentLoaded || []).forEach(fn => fn());
  (winListeners.load || []).forEach(fn => fn());
  return { store, els, sandbox, logs };
}

function typeKey(page, ep, value) {
  const el = page.els[ep + "Key"];
  el.value = value;
  fire(el, "input");
}
function setRemember(page, ep, on) {
  const cb = page.els[ep + "KeyRemember"];
  cb.checked = on;
  fire(cb, "change");
}
const allKeys = page => ENDPOINTS.map(ep => page.els[ep + "Key"].value);
const allBoxes = page => ENDPOINTS.map(ep => page.els[ep + "KeyRemember"].checked);

(async () => {
  // T1 default: a typed key is never written to localStorage.
  {
    const p = boot();
    typeKey(p, "consensus", KEY);
    check("T1 default: typing a key writes no sc_llm_key", !p.store.has("sc_llm_key"));
    check("T1 default: typing a key writes no remember flag", !p.store.has("sc_llm_key_remember"));
    check("T1 default: the key is kept in sync across all modals in memory", allKeys(p).every(v => v === KEY));
  }

  // T2 opt-in: ticking the box stores the key and the flag.
  {
    const p = boot();
    typeKey(p, "consensus", KEY);
    setRemember(p, "consensus", true);
    check("T2 opt-in: sc_llm_key equals the typed key", p.store.get("sc_llm_key") === KEY);
    check("T2 opt-in: sc_llm_key_remember is 1", p.store.get("sc_llm_key_remember") === "1");
    typeKey(p, "evidence", KEY + "-edited");
    check("T2 opt-in: a key edit while checked updates sc_llm_key", p.store.get("sc_llm_key") === KEY + "-edited");
    check("T2 opt-in: every modal's checkbox shows checked", allBoxes(p).every(Boolean));
  }

  // T3 opt-out: unticking removes both, the input keeps the key.
  {
    const p = boot();
    typeKey(p, "consensus", KEY);
    setRemember(p, "consensus", true);
    setRemember(p, "consensus", false);
    check("T3 opt-out: sc_llm_key removed", !p.store.has("sc_llm_key"));
    check("T3 opt-out: sc_llm_key_remember removed", !p.store.has("sc_llm_key_remember"));
    check("T3 opt-out: the input still holds the key", p.els.consensusKey.value === KEY);
    typeKey(p, "consensus", KEY + "-after");
    check("T3 opt-out: a key edit after unchecking writes nothing", !p.store.has("sc_llm_key"));
  }

  // T4 Clear key: storage, inputs and checkboxes all cleared.
  {
    const p = boot();
    typeKey(p, "consensus", KEY);
    setRemember(p, "consensus", true);
    fire(p.els.gapsKeyClear, "click");
    check("T4 Clear key: sc_llm_key removed", !p.store.has("sc_llm_key"));
    check("T4 Clear key: sc_llm_key_remember removed", !p.store.has("sc_llm_key_remember"));
    check("T4 Clear key: every key input is empty", allKeys(p).every(v => v === ""));
    check("T4 Clear key: every checkbox is unchecked", allBoxes(p).every(b => b === false));
  }

  // T5 legacy: a key stored by the old auto-save, without the flag, is removed.
  {
    const p = boot({ sc_llm_key: KEY, sc_provider: "deepseek" });
    check("T5 legacy: sc_llm_key without the flag is removed on load", !p.store.has("sc_llm_key"));
    check("T5 legacy: every key input is empty", allKeys(p).every(v => v === ""));
    check("T5 legacy: the provider preference is untouched", p.store.get("sc_provider") === "deepseek");
  }

  // T6 remembered: key + flag load into every modal, box checked.
  {
    const p = boot({ sc_llm_key: KEY, sc_llm_key_remember: "1" });
    check("T6 remembered: every key input holds the stored key", allKeys(p).every(v => v === KEY));
    check("T6 remembered: every checkbox is checked", allBoxes(p).every(Boolean));
    check("T6 remembered: sc_llm_key is kept", p.store.get("sc_llm_key") === KEY);
  }

  // T7 transport: the key travels only in the X-LLM-Key request header.
  {
    const p = boot();
    typeKey(p, "consensus", KEY);
    p.els.consensusClaim.value = "coffee improves alertness";
    const calls = [];
    p.sandbox.fetch = async (url, opts) => {
      calls.push({ url: String(url), opts });
      return { ok: false, status: 500, json: async () => ({ error: "test" }) };
    };
    try { await vm.runInContext('runAnalysis("consensus")', p.sandbox); } catch (e) { /* rendering is not under test */ }
    const call = calls.find(c => c.url.includes("/api/consensus"));
    check("T7 transport: one analysis request was sent", !!call);
    const headers = (call && call.opts && call.opts.headers) || {};
    check("T7 transport: the key is in the X-LLM-Key header", headers["X-LLM-Key"] === KEY);
    check("T7 transport: the key is not in the URL", !!call && !call.url.includes(KEY));
    check("T7 transport: the key is not in the JSON body", !!call && !String(call.opts.body).includes(KEY));
    check("T7 transport: the key is not in any console output", !p.logs.some(l => l.includes(KEY)));
    check("T7 transport: no other header carries the key",
      Object.entries(headers).every(([k, v]) => k === "X-LLM-Key" || !String(v).includes(KEY)));
  }

  // T8 labels: exact UI strings, once per modal.
  {
    const count = s => html.split(s).length - 1;
    check("T8 labels: checkbox label in every modal", count("> " + LABEL + "</label>") === ENDPOINTS.length);
    check("T8 labels: help text in every modal", count(HELP) === ENDPOINTS.length);
    check("T8 labels: Clear key button in every modal", count(">" + CLEAR + "</button>") === ENDPOINTS.length);
    for (const ep of ENDPOINTS) {
      check("T8 labels: " + ep + " has a remember checkbox",
        new RegExp('<input type="checkbox" id="' + ep + 'KeyRemember"').test(html));
      check("T8 labels: " + ep + " has a Clear key button", html.includes('id="' + ep + 'KeyClear"'));
    }
  }

  if (ran === 0) {
    console.error("FAIL: zero checks ran - refusing to pass silently");
    process.exit(1);
  }
  console.log((ran - failed) + "/" + ran + " passed, " + failed + " failed");
  process.exit(failed ? 1 : 0);
})().catch(e => {
  console.error("FAIL: test crashed: " + (e && e.stack || e));
  process.exit(1);
});
