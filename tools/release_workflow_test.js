// CalVer release workflow test.
//
// Run: node tools/release_workflow_test.js
//
// Every non-pull_request run on main gets a version YYYY.MM.DD.N, stamped into
// the binary, an image tag, the OCI label, a git tag and a GitHub Release. This
// static check reads .github/workflows/deploy.yml and the Dockerfile and
// asserts the pieces are wired together, and that only the main-only publish
// job can write (T1-T6 below).
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
const noJob = { name: "", start: -1, end: -1 };
const jobOf = (idx) => jobs.find((jb) => idx >= jb.start && idx < jb.end) || noJob;
const job = (name) => jobs.find((jb) => jb.name === name) || noJob;
const jobLines = (jb) => (jb.start < 0 ? [] : lines.slice(jb.start, jb.end));
// The permission lines of a job: the indented block under its "permissions:".
function perms(jb) {
  const p = jobLines(jb).findIndex((l) => l.trim() === "permissions:");
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

const buildJob = job("build");
const publishJob = job("publish");
check("build job exists", buildJob.start >= 0);
check("publish job exists", publishJob.start >= 0);

const exp = step("Build and export");
check("Build and export step exists", exp.start >= 0);
check("Build and export is in the build job", exp.start >= 0 && jobOf(exp.start).name === "build");

const ver = step("Compute version");
check("version step exists", ver.start >= 0);
check("version step is in the build job", ver.start >= 0 && jobOf(ver.start).name === "build");
check("version step comes after Checkout", ver.start >= 0 && lines.slice(buildJob.start, ver.start).some((l) => l.trim() === "- name: Checkout"));
check("version step if: is non-PR main and not event_name == 'push'", nonPRMain(ifLine(ver)));
check("version step uses date -u +%Y.%m.%d", has(ver, "date -u +%Y.%m.%d"));
check("version step uses git ls-remote --tags", has(ver, "git ls-remote --tags"));
check("version step writes GITHUB_OUTPUT", has(ver, "GITHUB_OUTPUT"));
check("build job outputs version from the version step",
  jobLines(buildJob).some((l) => l.trim() === "version: ${{ steps.version.outputs.version }}"));

const argIdx = exp.lines.findIndex((l) => /^\s*build-args:/.test(l));
check("exported build has build-args with APP_VERSION",
  argIdx >= 0 && exp.lines.slice(argIdx).some((l) => l.includes("APP_VERSION=")));
const tagLine = exp.lines.find((l) => l.includes("steps.version.outputs.version") && l.includes("format(")) || "";
check("version image tag only on non-PR main and not event_name == 'push'", nonPRMain(tagLine));
check("exported build keeps :latest and :sha tags",
  has(exp, "${{ env.IMAGE_NAME }}:latest") && has(exp, "${{ env.IMAGE_NAME }}:${{ github.sha }}"));
check("Build and export has push: false", exp.lines.some((l) => l.trim() === "push: false"));
check("Build and export writes the docker tar to runner.temp",
  exp.lines.some((l) => l.trim() === "outputs: type=docker,dest=${{ runner.temp }}/corpova-image.tar"));

const iid = step("Write image ID");
check("Write image ID comes after Build and export", exp.start >= 0 && iid.start > exp.start);
const up = step("Upload image artifact");
check("Upload image artifact is in the build job, after Write image ID",
  up.start > iid.start && iid.start >= 0 && jobOf(up.start).name === "build");
check("upload artifact name is corpova-image", up.lines.some((l) => l.trim() === "name: corpova-image"));
check("upload fails when a file is missing", up.lines.some((l) => l.trim() === "if-no-files-found: error"));

// Corpova's load: true build is the image the CLI commit label check runs on.
const label = step("Build image for the label check");
check("load: true label-check build still exists", has(label, "load: true"));
check("label-check build passes APP_VERSION build-arg", has(label, "APP_VERSION="));
check("label-check build carries no version image tag", !has(label, "format(") && !has(label, "steps.version.outputs.version }}:"));
check("label-check build comes before Build and export", label.start >= 0 && label.start < exp.start);

// The workflow is split into a read-only build job and a publish job that is
// the only one able to push or release.
const topAt = lines.findIndex((l) => l === "permissions:");
const topPerms = [];
for (let j = topAt + 1; topAt >= 0 && j < lines.length; j++) {
  const l = lines[j];
  if (l.trim() === "" || l.trim().startsWith("#")) continue;
  if (!/^ /.test(l)) break;
  topPerms.push(l.trim());
}
check("T1 top-level permissions is contents: read (found " + JSON.stringify(topPerms) + ")",
  topAt >= 0 && topAt < jobsAt && JSON.stringify(topPerms) === JSON.stringify(["contents: read"]));

const buildPerms = perms(buildJob);
check("T2 build job permissions are exactly contents: read (found " + JSON.stringify(buildPerms) + ")",
  JSON.stringify(buildPerms) === JSON.stringify(["contents: read"]));

const publishIf = jobLines(publishJob).find((l) => /^    if:/.test(l)) || "";
check("T3 publish job if: requires refs/heads/main", publishIf.includes("github.ref == 'refs/heads/main'"));

function writeLinesOutsidePublish(re) {
  const hits = lines.map((l, i) => (re.test(l) ? i : -1)).filter((i) => i >= 0);
  const outside = hits.filter((i) => i < publishJob.start || i >= publishJob.end);
  return { hits: hits.length, outside: outside.map((i) => i + 1) };
}
const pkg = writeLinesOutsidePublish(/^\s*packages:\s*write\b/);
const cnt = writeLinesOutsidePublish(/^\s*contents:\s*write\b/);
check("T4 packages: write only in the publish job (outside at lines " + JSON.stringify(pkg.outside) + ")",
  publishJob.start >= 0 && pkg.hits > 0 && pkg.outside.length === 0);
check("T4 contents: write only in the publish job (outside at lines " + JSON.stringify(cnt.outside) + ")",
  publishJob.start >= 0 && cnt.hits > 0 && cnt.outside.length === 0);

check("T5 build job has no docker/login-action",
  buildJob.start >= 0 && !jobLines(buildJob).some((l) => l.includes("docker/login-action")));

// Two jobs check out the repository (test and build); T6 is about the build one.
const checkoutAt = lines.findIndex((l, i) => l.trim() === "- name: Checkout" && jobOf(i).name === "build");
const checkout = stepAt(checkoutAt);
check("T6 build Checkout has persist-credentials: false",
  checkout.start >= 0 && jobOf(checkout.start).name === "build" && checkout.lines.some((l) => l.trim() === "persist-credentials: false"));

for (const jb of jobs) {
  const p = perms(jb);
  if (jb.name === "publish") {
    check("job " + jb.name + " has contents: write", p.includes("contents: write"));
    check("job " + jb.name + " keeps packages: write", p.includes("packages: write"));
  } else {
    check("job " + jb.name + " has no contents: write", !p.includes("contents: write"));
  }
}
check("no workflow-level contents: write", !lines.some((l) => /^  contents:\s*write/.test(l)));

check("publish job needs build", jobLines(publishJob).some((l) => /^    needs:\s*build\s*$/.test(l)));
check("publish job if: allows push and workflow_dispatch",
  publishIf.includes("github.event_name == 'push'") && publishIf.includes("github.event_name == 'workflow_dispatch'"));

const login = step("Log in to GHCR");
check("Log in to GHCR is in the publish job", login.start >= 0 && jobOf(login.start).name === "publish");

const load = step("Load and push");
check("Load and push is in the publish job", load.start >= 0 && jobOf(load.start).name === "publish");
check("Load and push reads VERSION from the build job", has(load, "VERSION: ${{ needs.build.outputs.version }}"));
check("Load and push refuses an empty VERSION", has(load, 'test -n "$VERSION"'));
check("Load and push pushes :latest, :sha and :VERSION",
  has(load, 'docker push "$REGISTRY/$IMAGE_NAME:latest"') &&
  has(load, 'docker push "$REGISTRY/$IMAGE_NAME:${GITHUB_SHA}"') &&
  has(load, 'docker push "$REGISTRY/$IMAGE_NAME:$VERSION"'));

const rel = step("Create GitHub release");
check("release step exists", rel.start >= 0);
check("release step is in the publish job", rel.start >= 0 && jobOf(rel.start).name === "publish");
check("release step runs gh release create --target",
  rel.lines.some((l) => l.includes("gh release create") && l.includes("--target")));
const ghCalls = rel.lines.filter((l) => /\bgh release\b/.test(l));
check("every gh call in the release step names --repo (" + ghCalls.length + " calls)",
  ghCalls.length > 0 && ghCalls.every((l) => (l.match(/gh release/g) || []).length === (l.match(/--repo "\$GITHUB_REPOSITORY"/g) || []).length));
check("release step comes after Load and push", load.start >= 0 && rel.start > load.start);
check("release step uses GH_TOKEN: ${{ github.token }}", has(rel, "GH_TOKEN: ${{ github.token }}"));
check("release step reads VERSION from the build job", has(rel, "VERSION: ${{ needs.build.outputs.version }}"));
check("release title is Corpova $VERSION", rel.lines.some((l) => l.includes('--title "Corpova $VERSION"')));

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
const dockerLabel = docker.findIndex((l) => l.trim().startsWith("LABEL org.opencontainers.image.version="));
check("Dockerfile has ARG APP_VERSION=dev twice", argLines.length === 2);
check("Dockerfile ldflags set -X ....appVersion=", ldflag >= 0);
check("Dockerfile -X flag is on the go build line and keeps -trimpath",
  ldflag >= 0 && ldflag === buildRun && docker[buildRun].includes("-trimpath"));
check("Dockerfile build-stage ARG APP_VERSION=dev is before go build in the same stage",
  argLines.length === 2 && argLines[0] < buildRun && !froms.some((f) => f > argLines[0] && f < buildRun));
check("Dockerfile has org.opencontainers.image.version", dockerLabel >= 0);
check("Dockerfile final-stage ARG APP_VERSION=dev is before the LABEL",
  argLines.length === 2 && argLines[1] > lastFrom && argLines[1] < dockerLabel);
check("Dockerfile keeps the global ARG PP_LIBRARY_COMMIT before the first FROM",
  froms.length > 0 && docker.slice(0, froms[0]).some((l) => /^ARG PP_LIBRARY_COMMIT=[0-9a-f]{40}$/.test(l)));

if (failed) {
  console.error(failed + " check(s) failed");
  process.exit(1);
}
console.log("all release workflow checks pass");
