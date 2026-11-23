// Model defaults and suggestion lists test.
//
// Run: node tools/model_defaults_test.js
//
// The server registry in providers.go decides which model runs when the Model
// field is empty; index.html shows its own copy of that default as a hint and
// offers a datalist of suggested models. If the two drift apart, the page
// recommends one model while the server runs another. This static check reads
// both files and asserts:
//   1. every provider's default in index.html (providerDefaults) equals the
//      DefaultModel in providers.go. openrouter is skipped: the page prefills
//      'openrouter/free' on purpose (OpenRouter needs a model id), which is
//      existing behaviour this test does not change;
//   2. the first suggested model of every provider in providerModelChoices is
//      that provider's default;
//   3. the gemini list is exactly the models measured on 2026-11-23, most
//      reliable first, with no deprecated or unanswering ids;
//   4. the openai list is exactly the current Luna/Sol/Terra/Astra set.
"use strict";
const fs = require("fs");
const path = require("path");

const root = path.join(__dirname, "..");
const html = fs.readFileSync(path.join(root, "index.html"), "utf8").replace(/\r/g, "");
const goSrc = fs.readFileSync(path.join(root, "providers.go"), "utf8").replace(/\r/g, "");

let failed = 0;
function check(label, ok) {
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label);
}

// providers.go: "name": {"https://base", "default-model", style, json},
const serverDefaults = {};
for (const m of goSrc.matchAll(/^\s*"(\w+)":\s*\{"https:\/\/[^"]+",\s*"([^"]+)"/gm)) {
  serverDefaults[m[1]] = m[2];
}
check("0 providers.go registry parsed (found " + Object.keys(serverDefaults).length + " providers)", Object.keys(serverDefaults).length >= 10);

const defBlock = html.match(/const providerDefaults = \{([\s\S]*?)\};/);
const pageDefaults = {};
if (defBlock) {
  for (const m of defBlock[1].matchAll(/(\w+):\s*'([^']+)'/g)) pageDefaults[m[1]] = m[2];
}
check("0 index.html providerDefaults parsed", !!defBlock && Object.keys(pageDefaults).length >= 10);

for (const [name, model] of Object.entries(serverDefaults)) {
  if (name === "openrouter") continue;
  check("1 " + name + " page default '" + pageDefaults[name] + "' == server default '" + model + "'", pageDefaults[name] === model);
}

const choiceBlock = html.match(/const providerModelChoices = \{([\s\S]*?)\n {4}\};/);
const choices = {};
if (choiceBlock) {
  for (const m of choiceBlock[1].matchAll(/(\w+):\s*\[([\s\S]*?)\]/g)) {
    choices[m[1]] = [...m[2].matchAll(/id:\s*'([^']+)'/g)].map((x) => x[1]);
  }
}
check("0 index.html providerModelChoices parsed", !!choiceBlock && Object.keys(choices).length >= 3);

for (const [name, ids] of Object.entries(choices)) {
  check("2 " + name + " first suggestion '" + ids[0] + "' is the default '" + serverDefaults[name] + "'", ids[0] === serverDefaults[name]);
}

const wantGemini = ["gemini-3.5-flash-lite", "gemini-3.8-flash", "gemini-3.6-flash"];
check("3 gemini list is " + wantGemini.join(", ") + " (found " + (choices.gemini || []).join(", ") + ")",
  JSON.stringify(choices.gemini) === JSON.stringify(wantGemini));

const wantOpenAI = ["gpt-6-luna", "gpt-6.2-sol", "gpt-6.1-terra", "gpt-6.2-astra"];
check("4 openai list is " + wantOpenAI.join(", ") + " (found " + (choices.openai || []).join(", ") + ")",
  JSON.stringify(choices.openai) === JSON.stringify(wantOpenAI));

if (failed) {
  console.log("\n" + failed + " check(s) failed");
  process.exit(1);
}
console.log("\nall checks passed");
