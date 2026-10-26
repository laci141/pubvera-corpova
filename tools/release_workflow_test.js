// CalVer release workflow test.
//
// Run: node tools/release_workflow_test.js
//
// Every non-pull_request run on main gets a version YYYY.MM.DD.N, stamped into
// the binary, an image tag, the OCI label, a git tag and a GitHub Release. This
// static check reads .github/workflows/deploy.yml and the Dockerfile and
// asserts the pieces are wired together.
//
// The workflow is checked line by line, never with a regex that spans lines:
// one such regex hung with catastrophic backtracking in the Retractis pilot.
"use strict";
const fs = require("fs");
const path = require("path");

const root = path.join(__dirname, "..");
const lines = fs.readFileSync(path.join(root, ".github", "workflows", "deploy.yml"), "utf8").split(/\r?\n/);
const docker = fs.readFileSync(path.join(root, "Dockerfile"), "utf8").split(/\r?\n/);

let failed = 0;
function check(label, ok) {
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label);
}

// The lines of one step, from its "- name:" to the next step, the next job or
// end of file. start is the index of the "- name:" line, -1 when missing.
function stepAt(start) {
  if (start < 0) return { start: -1, lines: [] };
  const out = [lines[start]];
  for (let j = start + 1; j < lines.length && !/^\s*- name:/.test(lines[j]) && !/^  [\w-]+:\s*$/.test(lines[j]); j++) out.push(lines[j]);
  return { start, lines: out };
}
const step = (name) => stepAt(lines.findIndex((l) => l.trim() === "- name: " + name));
const has = (s, text) => s.lines.some((l) => l.includes(text));
const ifLine = (s) => s.lines.find((l) => /^\s*if:/.test(l)) || "";

// Jobs: a line with exactly two spaces of indent and a key, inside "jobs:".
const jobsAt = lines.findIndex((l) => l === "jobs:");
const jobs = [];
for (let j = jobsAt + 1; jobsAt >= 0 && j < lines.length; j++) {
  const m = lines[j].match(/^  ([\w-]+):\s*$/);
  if (m) jobs.push({ name: m[1], start: j });
}
jobs.forEach((jb, i) => { jb.end = i + 1 < jobs.length ? jobs[i + 1].start : lines.length; });
const jobOf = (idx) => jobs.find((jb) => idx >= jb.start && idx < jb.end) || { name: "" };
// The permission lines of a job: the indented block under its "permissions:".
function perms(jb) {
  const p = lines.slice(jb.start, jb.end).findIndex((l) => l.trim() === "permissions:");
  if (p < 0) return [];
  const out = [];
  for (let j = jb.start + p + 1; j < jb.end && /^      \S/.test(lines[j]); j++) out.push(lines[j].trim());
  return out;
}

// Every non-pull_request event on main (push and workflow_dispatch) is
// versioned: a workflow_dispatch run also pushes :latest, so it must not ship
// as "dev".
const NONPR_MAIN = /github\.event_name != 'pull_request' && github\.ref == 'refs\/heads\/main'/;
const nonPRMain = (cond) => cond !== "" && NONPR_MAIN.test(cond) && !/event_name\s*==\s*'push'/.test(cond);

const push = step("Build and push");
check("Build and push step exists", push.start >= 0);
const pubJob = jobOf(push.start);

const ver = step("Compute version");
check("version step exists", ver.start >= 0);
check("version step is in the publishing job (" + pubJob.name + ")", ver.start >= 0 && jobOf(ver.start).name === pubJob.name);
check("version step comes after Checkout", ver.start >= 0 && lines.slice(pubJob.start, ver.start).some((l) => l.trim() === "- name: Checkout"));
check("version step has id: version", has(ver, "id: version"));
check("version step if: is non-PR main and not event_name == 'push'", nonPRMain(ifLine(ver)));
check("version step uses date -u +%Y.%m.%d", has(ver, "date -u +%Y.%m.%d"));
check("version step uses git ls-remote --tags", has(ver, "git ls-remote --tags"));
check("version step writes GITHUB_OUTPUT", has(ver, "GITHUB_OUTPUT"));

const argIdx = push.lines.findIndex((l) => /^\s*build-args:/.test(l));
check("pushing build has build-args with APP_VERSION",
  argIdx >= 0 && push.lines.slice(argIdx).some((l) => l.includes("APP_VERSION=")));
const tagLine = push.lines.find((l) => l.includes("steps.version.outputs.version") && l.includes("format(")) || "";
check("version image tag only on non-PR main and not event_name == 'push'", nonPRMain(tagLine));
check("pushing build keeps :latest and :sha tags",
  has(push, "${{ env.IMAGE_NAME }}:latest") && has(push, "${{ env.IMAGE_NAME }}:${{ github.sha }}"));
check("pushing build keeps the revision label", has(push, "org.opencontainers.image.revision=${{ github.sha }}"));

// :latest is what Watchtower deploys. workflow_dispatch lets the caller pick
// the branch, so the login and the push need the main ref too, not only the
// pull_request exclusion.
const login = step("Log in to GHCR");
check("Log in to GHCR if: is non-PR main", nonPRMain(ifLine(login)));
const pushFlag = push.lines.find((l) => /^\s*push:/.test(l)) || "";
check("Build and push push: is non-PR main", nonPRMain(pushFlag));
check("login comes before Build and push", login.start >= 0 && login.start < push.start);

// Every other step that builds an image: it gets APP_VERSION too (so it reuses
// the pushing build's layers from the gha cache and checks the shipped image),
// but never the version tag and never a push.
const others = lines
  .map((l, i) => (/^\s*- name:/.test(l) ? stepAt(i) : null))
  .filter((s) => s && s.start !== push.start && has(s, "docker/build-push-action"));
check("at least one other image build exists (found " + others.length + ")", others.length >= 1);
for (const s of others) {
  const name = s.lines[0].trim().replace(/^- name:\s*/, "");
  check(name + ": passes APP_VERSION build-arg", has(s, "APP_VERSION="));
  check(name + ": carries no version image tag", !has(s, "format(") && !has(s, "steps.version.outputs.version }}:"));
  check(name + ": never pushes", s.lines.some((l) => l.trim() === "push: false"));
  check(name + ": comes before Build and push", s.start < push.start);
}
const labelCheck = step("Build image for the label check");
check("load: true label-check build still exists", has(labelCheck, "load: true"));

const rel = step("Create GitHub release");
check("release step exists", rel.start >= 0);
check("release step is in the publishing job", rel.start >= 0 && jobOf(rel.start).name === pubJob.name);
check("release step if: is non-PR main and not event_name == 'push'", nonPRMain(ifLine(rel)));
check("release step runs gh release create --target",
  rel.lines.some((l) => l.includes("gh release create") && l.includes("--target")));
check("release step has baseline notes and --generate-notes",
  rel.lines.some((l) => l.includes("gh release create") && l.includes("--notes ")) &&
  rel.lines.some((l) => l.includes("gh release create") && l.includes("--generate-notes")));
check("release step comes after Build and push", push.start >= 0 && rel.start > push.start);
check("release step uses GH_TOKEN: ${{ github.token }}", has(rel, "GH_TOKEN: ${{ github.token }}"));

for (const jb of jobs) {
  const p = perms(jb);
  if (jb.name === pubJob.name) {
    check("job " + jb.name + " has contents: write", p.includes("contents: write"));
    check("job " + jb.name + " keeps packages: write", p.includes("packages: write"));
  } else {
    check("job " + jb.name + " has no contents: write", !p.includes("contents: write"));
  }
}
check("no workflow-level contents: write", !lines.some((l) => /^  contents:\s*write/.test(l)));
const conc = lines.findIndex((l) => l.trim() === "concurrency:");
check("concurrency group exists", conc >= 0 && (lines[conc + 1] || "").trim().startsWith("group:"));
check("cancel-in-progress is not true", !lines.some((l) => /^\s*cancel-in-progress:\s*true/.test(l)));

// Dockerfile: one ARG APP_VERSION=dev in the stage that runs go build (before
// that RUN), one in the final stage (before the LABEL).
const froms = docker.map((l, i) => (/^FROM\s/.test(l) ? i : -1)).filter((i) => i >= 0);
const lastFrom = froms.length ? froms[froms.length - 1] : -1;
const argLines = docker.map((l, i) => (l.trim() === "ARG APP_VERSION=dev" ? i : -1)).filter((i) => i >= 0);
const buildRun = docker.findIndex((l) => /^RUN .*go build/.test(l));
const ldflag = docker.findIndex((l) => l.includes("-X ") && l.includes(".appVersion="));
const label = docker.findIndex((l) => l.trim().startsWith("LABEL org.opencontainers.image.version="));
check("Dockerfile has ARG APP_VERSION=dev twice", argLines.length === 2);
check("Dockerfile ldflags set -X ....appVersion=", ldflag >= 0);
check("Dockerfile -X flag is on the go build line and keeps -trimpath",
  ldflag >= 0 && ldflag === buildRun && docker[buildRun].includes("-trimpath"));
check("Dockerfile build-stage ARG APP_VERSION=dev is before go build in the same stage",
  argLines.length === 2 && argLines[0] < buildRun && !froms.some((f) => f > argLines[0] && f < buildRun));
check("Dockerfile has org.opencontainers.image.version", label >= 0);
check("Dockerfile final-stage ARG APP_VERSION=dev is before the LABEL",
  argLines.length === 2 && argLines[1] > lastFrom && argLines[1] < label);
check("Dockerfile keeps the global ARG PP_LIBRARY_COMMIT before the first FROM",
  froms.length > 0 && docker.slice(0, froms[0]).some((l) => /^ARG PP_LIBRARY_COMMIT=[0-9a-f]{40}$/.test(l)));

if (failed) {
  console.error(failed + " check(s) failed");
  process.exit(1);
}
console.log("all release workflow checks pass");
