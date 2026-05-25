#!/usr/bin/env node
/**
 * Export the MySQL patch stack as individual .patch files.
 *
 * Usage:
 *   node scripts/export-mysql-patches.mjs <base-ref>
 *
 * Example:
 *   node scripts/export-mysql-patches.mjs v0.38.2
 *
 * Environment variables:
 *   MYSQL_PATCH_DIR  - output directory (default: patches/mysql-poc)
 */

import { execSync } from "node:child_process";
import { mkdirSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";

const baseRef = process.argv[2];
if (!baseRef) {
  console.error("Usage: node scripts/export-mysql-patches.mjs <base-ref>");
  process.exit(1);
}

const outDir = resolve(process.env.MYSQL_PATCH_DIR || "patches/mysql-poc");

// Resolve refs for README metadata
const baseHash = git(`rev-parse --short ${baseRef}`);
const headHash = git("rev-parse --short HEAD");
const headRef = git("rev-parse --abbrev-ref HEAD");

// Clean previous patches (keep directory)
mkdirSync(outDir, { recursive: true });
for (const f of readdirSync(outDir)) {
  if (f.endsWith(".patch")) rmSync(resolve(outDir, f));
}

// Generate patches
console.log(`Exporting patches: ${baseRef}..HEAD`);
const patchOutput = git(
  `format-patch --output-directory "${outDir}" ${baseRef}..HEAD`
);
const patches = patchOutput.split("\n").filter(Boolean);
console.log(`Exported ${patches.length} patch(es) to ${outDir}`);

// Write README
const readme = `# MySQL PoC Patch Stack

Generated from:

- Base ref: \`${baseRef}\` (${baseHash})
- Head ref: \`${headRef}\` (${headHash})
- Date: ${new Date().toISOString().slice(0, 10)}

Apply to a clean upstream worktree with:

\`\`\`sh
node scripts/apply-mysql-patches.mjs patches/mysql-poc
\`\`\`
`;

writeFileSync(resolve(outDir, "README.md"), readme);
console.log("Updated patches/mysql-poc/README.md");

function git(args) {
  return execSync(`git ${args}`, { encoding: "utf8" }).trim();
}
