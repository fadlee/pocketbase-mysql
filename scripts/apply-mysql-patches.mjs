#!/usr/bin/env node
/**
 * Apply the MySQL patch stack onto the current branch using git am.
 *
 * Usage:
 *   node scripts/apply-mysql-patches.mjs [patch-dir]
 *
 * Default patch-dir: patches/mysql-poc (or MYSQL_PATCH_DIR env)
 *
 * Options:
 *   --3way       Use 3-way merge (default: enabled)
 *   --no-3way    Disable 3-way merge
 *   --continue   Resume after resolving a conflict
 *   --abort      Abort the current git am session
 *   --skip       Skip the current patch
 */

import { execSync, spawnSync } from "node:child_process";
import { readdirSync } from "node:fs";
import { resolve } from "node:path";

const args = process.argv.slice(2);
const flags = args.filter((a) => a.startsWith("--"));
const positional = args.filter((a) => !a.startsWith("--"));

// Handle --continue, --abort, --skip
if (flags.includes("--continue")) {
  run("git am --continue");
  process.exit(0);
}
if (flags.includes("--abort")) {
  run("git am --abort");
  console.log("Aborted git am session.");
  process.exit(0);
}
if (flags.includes("--skip")) {
  run("git am --skip");
  process.exit(0);
}

// Refuse to apply on dirty worktree (match bash behavior)
const status = execSync("git status --porcelain", { encoding: "utf8" }).trim();
if (status) {
  console.error("Refusing to apply patches on a dirty worktree.");
  process.exit(1);
}

const patchDir = resolve(
  positional[0] || process.env.MYSQL_PATCH_DIR || "patches/mysql-poc"
);

// Collect .patch files sorted by name
const patches = readdirSync(patchDir)
  .filter((f) => f.endsWith(".patch"))
  .sort()
  .map((f) => resolve(patchDir, f));

if (patches.length === 0) {
  console.error(`No .patch files found in ${patchDir}`);
  process.exit(1);
}

console.log(`Applying ${patches.length} patch(es) from ${patchDir}...`);

const useThreeWay = !flags.includes("--no-3way");
const amArgs = ["am", ...(useThreeWay ? ["--3way"] : []), ...patches];

const result = spawnSync("git", amArgs, {
  stdio: "inherit",
  encoding: "utf8",
});

if (result.status === 0) {
  console.log(`\nAll ${patches.length} patches applied successfully.`);
} else {
  console.error(
    `\ngit am stopped. Resolve conflicts then run:\n` +
      `  node scripts/apply-mysql-patches.mjs --continue\n` +
      `\nOr skip this patch:\n` +
      `  node scripts/apply-mysql-patches.mjs --skip\n` +
      `\nOr abort:\n` +
      `  node scripts/apply-mysql-patches.mjs --abort`
  );
  process.exit(result.status ?? 1);
}

function run(cmd) {
  const r = spawnSync(cmd, { shell: true, stdio: "inherit" });
  if (r.status !== 0) process.exit(r.status ?? 1);
}
