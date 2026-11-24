// AI per-study stance (llm_synthesis.study_stances) on cards and in exports.
//
// Run: node tools/ai_stance_test.js
//
// The server now returns one AI stance per kept study. The page shows it as a
// "🤖 AI: <stance>" chip with the short reason under it, next to the tool's
// heuristic chip, and the exports gain ai_stance / ai_stance_reason columns.
// Runs the real functions from index.html inside a node:vm context (no DOM):
//   a  buildStanceIndex reads the list, normalizes DOI and claim;
//   b  findAIStance matches by DOI, then by title; none -> null;
//   c  compare: side narrows to one claim; disagreeing matches without a side -> null;
//   d  renderStudyCard: chip + reason with the right colour class, escaped;
//      no chip without ai, none on an excluded card, other chips unchanged;
//   e  studyRow: ai_stance / ai_stance_reason filled, empty strings when absent,
//      never on an excluded row; compare rows use their claim side;
//   f  the response handler rebuilds the index per query.
// A piece that cannot be found is a FAIL, never a silent pass.
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");
const html = fs.readFileSync(path.join(__dirname, "..", "index.html"), "utf8").replace(/\r\n/g, "\n");

let ran = 0, failed = 0;
function check(label, ok, extra) { ran++; if (!ok) failed++; console.log((ok ? "PASS" : "FAIL") + "  " + label + (ok || extra === undefined ? "" : "  " + JSON.stringify(extra))); }

function extractFunction(name) {
  const start = html.indexOf("function " + name + "(");
  if (start < 0) return null;
  const open = html.indexOf("{", start);
  let depth = 0;
  for (let i = open; i < html.length; i++) {
    if (html[i] === "{") depth++;
    else if (html[i] === "}") { depth--; if (depth === 0) return html.slice(start, i + 1); }
  }
  return null;
}

const names = ["normTitle", "titleMatches", "normDoi", "cleanTitle", "splitClaimLabel", "studyRow",
  "buildStanceIndex", "findAIStance", "renderStudyCard"];
const srcs = [];
for (const n of names) {
  const s = extractFunction(n);
  check("0 " + n + " defined in index.html", !!s);
  if (s) srcs.push(s);
}
if (srcs.length !== names.length) { console.log("\n" + (ran - failed) + "/" + ran + " passed"); process.exit(1); }

const ctx = { aiStances: [] };
vm.createContext(ctx);
vm.runInContext(
  "const esc = s => String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/\"/g,'&quot;');" +
  "const num = n => String(n || 0); const fx = n => Number(n).toFixed(2);" +
  srcs.join("\n") + "\n" + names.map(n => "this." + n + " = " + n + ";").join("\n"), ctx);
// aiStances is a context global; assign through the context so the functions see it.
const useIndex = list => { ctx.aiStances = ctx.buildStanceIndex({ study_stances: list }); };

// a
useIndex([
  { title: "Inspiratory muscle training in patients with chronic obstructive pulmonary disease.", doi: "https://doi.org/10.1164/ARRD.1985.131.2.251", stance: "refutes", reason: "exercise performance remained unchanged" },
  { title: "Impact of inspiratory muscle training in patients with COPD: what is the evidence?", doi: "", stance: "supports", reason: "6MWD +32 m, endurance +261 s" },
  { title: "Shared study title used under both claims", doi: "10.9/shared", claim: "A", stance: "supports", reason: "a" },
  { title: "Shared study title used under both claims", doi: "10.9/shared", claim: "B", stance: "refutes", reason: "b" },
  { title: "", stance: "supports" }
]);
check("a  empty title dropped, 4 entries", ctx.aiStances.length === 4, ctx.aiStances.length);
check("a  DOI normalized (prefix stripped, lowercase)", ctx.aiStances[0].doi === "10.1164/arrd.1985.131.2.251", ctx.aiStances[0].doi);
check("a  claim kept as A/B", ctx.aiStances[2].claim === "A" && ctx.aiStances[1].claim === "", [ctx.aiStances[2].claim, ctx.aiStances[1].claim]);
check("a  null synthesis -> []", ctx.buildStanceIndex(null).length === 0 && ctx.buildStanceIndex({}).length === 0);

// b
const chen = { title: "Inspiratory muscle training in patients with chronic obstructive pulmonary disease", doi: "10.1164/arrd.1985.131.2.251", stance: "supporting", stance_confidence: 0.95, year: 1985, cited_by_count: 90 };
const byDoi = ctx.findAIStance({ title: "a different title entirely here for test", doi: "10.1164/ARRD.1985.131.2.251" }, "");
check("b  DOI match (case-insensitive)", byDoi && byDoi.stance === "refutes", byDoi);
const byTitle = ctx.findAIStance({ title: "Impact of inspiratory muscle training in patients with COPD: what is the evidence?" }, "");
check("b  title match when no DOI", byTitle && byTitle.stance === "supports", byTitle);
check("b  no match -> null", ctx.findAIStance({ title: "Totally unrelated work about something else", doi: "10.0/none" }, "") === null);
check("b  empty index -> null", (() => { const keep = ctx.aiStances; ctx.aiStances = []; const r = ctx.findAIStance(chen, ""); ctx.aiStances = keep; return r === null; })());

// c
const shared = { title: "Shared study title used under both claims", doi: "10.9/shared" };
check("c  side A -> supports", (ctx.findAIStance(shared, "A") || {}).stance === "supports");
check("c  side B -> refutes", (ctx.findAIStance(shared, "B") || {}).stance === "refutes");
check("c  no side + disagreeing matches -> null", ctx.findAIStance(shared, "") === null);

// d
const ai = ctx.findAIStance(chen, "");
const card = ctx.renderStudyCard(chen, null, ai);
check("d  chip shows 🤖 AI: refutes", card.includes(">🤖 AI: refutes<"), card);
check("d  refutes chip uses the red stance class", /class="study-chip ai-stance-chip stance-refuting"/.test(card));
check("d  reason line rendered", card.includes('<div class="ai-stance-reason">🤖 exercise performance remained unchanged</div>'));
check("d  heuristic chip still there", card.includes(">supporting<"));
for (const [st, cls] of [["supports", "stance-supporting"], ["mixed", "ai-stance-mixed"], ["unclear", "stance-neutral"]]) {
  const h = ctx.renderStudyCard(chen, null, { stance: st, reason: "" });
  check("d  " + st + " -> " + cls + ", no empty reason line", h.includes("ai-stance-chip " + cls) && !h.includes("ai-stance-reason"));
}
const plain = ctx.renderStudyCard(chen, null);
check("d  no ai -> no chip, no reason", !plain.includes("ai-stance") && !plain.includes("🤖"));
const withAi = ctx.renderStudyCard(chen, null, ai);
check("d  other markup unchanged when ai is added",
  withAi.replace(/<span class="study-chip ai-stance-chip[^"]*" title="[^"]*">🤖 AI: [^<]*<\/span>/, "")
    .replace(/\s*<div class="ai-stance-reason">[^<]*<\/div>/, "").replace(/\s+/g, " ") === plain.replace(/\s+/g, " "));
const exCard = ctx.renderStudyCard(chen, { reason: "off-topic" }, ai);
check("d  excluded card never shows the AI chip", !exCard.includes("ai-stance"));
const evil = ctx.renderStudyCard(chen, null, { stance: "refutes", reason: '<img src=x onerror="a">' });
check("d  reason escaped", evil.includes("&lt;img src=x onerror=&quot;a&quot;&gt;") && !evil.includes("<img"));

// e
const row = ctx.studyRow(chen, "supporting");
check("e  row ai_stance / ai_stance_reason", row.ai_stance === "refutes" && row.ai_stance_reason === "exercise performance remained unchanged", row);
const keys = Object.keys(row);
check("e  columns sit right after stance_confidence",
  keys.indexOf("ai_stance") === keys.indexOf("stance_confidence") + 1 &&
  keys.indexOf("ai_stance_reason") === keys.indexOf("stance_confidence") + 2, keys);
const none = ctx.studyRow({ title: "Totally unrelated work about something else" }, "analyzed");
check("e  no match -> empty strings, keys present", none.ai_stance === "" && none.ai_stance_reason === "" && "ai_stance" in none, none);
check("e  excluded row never carries an AI stance", ctx.studyRow(chen, "excluded").ai_stance === "");
check("e  compare row uses its claim side",
  ctx.studyRow(shared, "analyzed", "Claim B: x").ai_stance === "refutes" &&
  ctx.studyRow(shared, "analyzed", "Claim A: x").ai_stance === "supports");

// f
check("f  response handler rebuilds aiStances per query",
  /aiStances = buildStanceIndex\(data\.llm_synthesis\);/.test(html));
check("f  compare card passes its side to renderStudyGroups",
  /renderStudyGroups\(x\.top_supporting, x\.top_refuting, sink, fallbackLabel === 'Claim B' \? 'B' : 'A'\)/.test(html));

console.log("\n" + (ran - failed) + "/" + ran + " passed");
process.exit(failed ? 1 : 0);
