#!/usr/bin/env node
/**
 * MySQL runtime QA - Node.js wrapper.
 *
 * Delegates to the Python implementation (scripts/mysql_runtime_qa.py) which
 * handles the full QA suite including Docker lifecycle, PocketBase build,
 * server management, and API verification.
 *
 * Usage:
 *   node scripts/mysql-runtime-qa.mjs [options]
 *
 * All arguments and environment variables are forwarded to the Python script.
 *
 * Options (forwarded):
 *   --skip-docker         Use existing MySQL instead of starting Docker
 *   --mysql-host HOST     MySQL host (default: 127.0.0.1)
 *   --mysql-port PORT     MySQL port (default: 3307)
 *   --mysql-user USER     MySQL user (default: root)
 *   --mysql-password PW   MySQL password (default: pbpass)
 *   --mysql-database DB   MySQL database (default: pocketbase_qa)
 *   --mysql-image IMG     Docker image (default: mysql:8.4)
 *   --http-addr ADDR      PocketBase HTTP address (default: 127.0.0.1:18090)
 *   --tmp-dir DIR         Temp directory for QA artifacts
 */

import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const scriptPath = resolve(__dirname, "mysql_runtime_qa.py");

if (!existsSync(scriptPath)) {
  console.error(`Python QA script not found: ${scriptPath}`);
  console.error("Make sure scripts/mysql_runtime_qa.py exists on this branch.");
  process.exit(1);
}

// Detect python executable
const pythonCandidates = ["python3", "python"];
let pythonBin = null;

for (const candidate of pythonCandidates) {
  const check = spawnSync(candidate, ["--version"], {
    encoding: "utf8",
    stdio: "pipe",
  });
  if (check.status === 0) {
    pythonBin = candidate;
    break;
  }
}

if (!pythonBin) {
  console.error("Python 3 is required but not found in PATH.");
  process.exit(1);
}

// Forward all CLI args to the Python script
const forwardedArgs = process.argv.slice(2);
const result = spawnSync(pythonBin, [scriptPath, ...forwardedArgs], {
  stdio: "inherit",
  env: process.env,
});

process.exit(result.status ?? 1);
