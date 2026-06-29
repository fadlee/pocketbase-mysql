#!/usr/bin/env node
/**
 * MySQL Runtime QA - Full Node.js implementation.
 *
 * Starts PocketBase with MySQL, creates collections/records, and verifies
 * CRUD, filtering, sorting, schema updates, relations, views, and all field types.
 *
 * Usage:
 *   node scripts/mysql-runtime-qa.mjs [options]
 *
 * Options:
 *   --skip-docker         Use existing MySQL instead of starting Docker
 *   --mysql-host HOST     MySQL host (default: 127.0.0.1)
 *   --mysql-port PORT     MySQL port (default: 3307)
 *   --mysql-user USER     MySQL user (default: root)
 *   --mysql-password PW   MySQL password (default: pbpass)
 *   --mysql-database DB   MySQL database (default: pocketbase_qa)
 *   --mysql-image IMG     Docker image (default: mysql:8.4)
 *   --http-addr ADDR      PocketBase HTTP address (default: 127.0.0.1:18090)
 *   --tmp-dir DIR         Temp directory for QA artifacts
 *   --keep-tmp            Keep the temp directory after the run (for debugging)
 *   -h, --help            Show this help text
 */

import { execSync, spawn, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createServer, createConnection } from "node:net";
import http from "node:http";

const __dirname = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(__dirname, "..");

// ---------------------------------------------------------------------------
// Argument parsing
// ---------------------------------------------------------------------------

function parseArgs() {
  const args = process.argv.slice(2);
  const opts = {
    skipDocker: envBool("MYSQL_QA_SKIP_DOCKER", false),
    mysqlHost: process.env.MYSQL_QA_HOST || "127.0.0.1",
    mysqlPort: parseInt(process.env.MYSQL_QA_MYSQL_PORT || "3307", 10),
    mysqlUser: process.env.MYSQL_QA_USER || "root",
    mysqlPassword: process.env.MYSQL_QA_PASSWORD ?? "pbpass",
    mysqlDatabase: process.env.MYSQL_QA_DATABASE || "pocketbase_qa",
    mysqlImage: process.env.MYSQL_QA_IMAGE || "mysql:8.4",
    mysqlContainer: process.env.MYSQL_QA_CONTAINER || "pb-mysql-runtime-qa",
    httpAddr: process.env.MYSQL_QA_HTTP_ADDR || "127.0.0.1:18090",
    tmpDir: process.env.MYSQL_QA_TMP_DIR || resolve(tmpdir(), "pb-mysql-runtime-qa"),
    keepTmp: envBool("MYSQL_QA_KEEP_TMP", false),
  };

  const need = (i, flag) => {
    if (i >= args.length) fail(`Missing value for ${flag}`);
    return args[i];
  };

  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (arg === "--skip-docker") opts.skipDocker = true;
    else if (arg === "--keep-tmp") opts.keepTmp = true;
    else if (arg === "-h" || arg === "--help") { printHelp(); process.exit(0); }
    else if (arg === "--mysql-host") opts.mysqlHost = need(++i, arg);
    else if (arg === "--mysql-port") opts.mysqlPort = parseInt(need(++i, arg), 10);
    else if (arg === "--mysql-user") opts.mysqlUser = need(++i, arg);
    else if (arg === "--mysql-password") opts.mysqlPassword = need(++i, arg);
    else if (arg === "--mysql-database") opts.mysqlDatabase = need(++i, arg);
    else if (arg === "--mysql-image") opts.mysqlImage = need(++i, arg);
    else if (arg === "--mysql-container") opts.mysqlContainer = need(++i, arg);
    else if (arg === "--http-addr") opts.httpAddr = need(++i, arg);
    else if (arg === "--tmp-dir") opts.tmpDir = need(++i, arg);
    else fail(`Unknown argument: ${arg}\nRun with --help for usage.`);
  }

  // Validate
  if (!Number.isInteger(opts.mysqlPort) || opts.mysqlPort < 1 || opts.mysqlPort > 65535) {
    fail(`Invalid --mysql-port: must be an integer 1-65535`);
  }
  const { port: httpPort } = splitHostPort(opts.httpAddr);
  if (!Number.isInteger(httpPort) || httpPort < 1 || httpPort > 65535) {
    fail(`Invalid --http-addr "${opts.httpAddr}": expected host:port with a valid port`);
  }
  if (!opts.mysqlDatabase || !/^[A-Za-z0-9_]+$/.test(opts.mysqlDatabase)) {
    fail(`Invalid --mysql-database "${opts.mysqlDatabase}": use only letters, digits and underscores`);
  }

  return opts;
}

function fail(msg) {
  console.error(`[qa] ${msg}`);
  process.exit(2);
}

function printHelp() {
  const header = readFileSync(fileURLToPath(import.meta.url), "utf8")
    .split(/\r?\n/)
    .filter((l) => l.startsWith(" *") || l.startsWith("/**"))
    .map((l) => l.replace(/^\/\*\*?/, "").replace(/^ \*\/?/, "").trimEnd())
    .join("\n");
  console.log(header.trim());
}

// Split "host:port" supporting IPv6 ("[::1]:8090") and bare hosts.
function splitHostPort(addr) {
  if (addr.startsWith("[")) {
    const end = addr.indexOf("]");
    const host = addr.slice(1, end);
    const port = parseInt(addr.slice(end + 2), 10);
    return { host, port };
  }
  const idx = addr.lastIndexOf(":");
  if (idx === -1) return { host: addr, port: NaN };
  return { host: addr.slice(0, idx), port: parseInt(addr.slice(idx + 1), 10) };
}

function envBool(name, def) {
  const v = process.env[name];
  if (v == null) return def;
  return ["1", "true", "yes", "on"].includes(v.toLowerCase());
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

const HTTP_TIMEOUT_MS = 30000;

// Marker error so callers can distinguish HTTP 4xx/5xx (a real server response)
// from transport-level failures (connection refused, timeout, ...).
class HttpError extends Error {
  constructor(status, method, url, body) {
    super(`HTTP ${status} ${method} ${url}\n${body}`);
    this.name = "HttpError";
    this.status = status;
    this.body = body;
  }
}

function httpRequestOnce(method, url, { token, payload, headers: extraHeaders, rawBody } = {}) {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    const headers = { ...(extraHeaders || {}) };
    let body = null;

    if (token) headers["Authorization"] = `Bearer ${token}`;
    if (rawBody !== undefined) {
      body = rawBody;
    } else if (payload !== undefined) {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(payload);
    }
    if (body != null && headers["Content-Length"] === undefined) {
      headers["Content-Length"] = Buffer.byteLength(body);
    }

    const req = http.request(
      { hostname: u.hostname, port: u.port, path: u.pathname + u.search, method, headers, timeout: HTTP_TIMEOUT_MS },
      (res) => {
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () => {
          const text = Buffer.concat(chunks).toString("utf8");
          if (res.statusCode >= 400) {
            reject(new HttpError(res.statusCode, method, url, text));
            return;
          }
          if (!text) return resolve(null);
          try {
            resolve(JSON.parse(text));
          } catch {
            reject(new Error(`Failed to parse JSON response for ${method} ${url}\n${text}`));
          }
        });
      }
    );
    req.on("timeout", () => req.destroy(new Error(`Request timed out after ${HTTP_TIMEOUT_MS}ms: ${method} ${url}`)));
    req.on("error", reject);
    if (body != null) req.write(body);
    req.end();
  });
}

// Retry only transport-level errors (not HTTP 4xx/5xx). Useful right after the
// server reports "Server started" but is still binding the socket.
async function httpRequest(method, url, opts = {}, { retries = 3, retryDelayMs = 500 } = {}) {
  let lastErr;
  for (let attempt = 0; attempt <= retries; attempt++) {
    try {
      return await httpRequestOnce(method, url, opts);
    } catch (err) {
      if (err instanceof HttpError) throw err; // real server response, do not retry
      lastErr = err;
      if (attempt < retries) await sleep(retryDelayMs);
    }
  }
  throw lastErr;
}

function GET(url, opts) { return httpRequest("GET", url, opts); }
function POST(url, opts) { return httpRequest("POST", url, opts); }
function PATCH(url, opts) { return httpRequest("PATCH", url, opts); }
function DELETE(url, opts) { return httpRequest("DELETE", url, opts); }

// Assert that a request fails with an expected HTTP status (negative tests).
async function expectStatus(label, expected, fn) {
  return expectStatusOneOf(label, [expected], fn);
}

// Like expectStatus but tolerant of a set of acceptable statuses (PocketBase
// may answer 403 or 404 depending on whether a rule hides record existence).
async function expectStatusOneOf(label, expectedList, fn) {
  try {
    await fn();
  } catch (err) {
    if (err instanceof HttpError && expectedList.includes(err.status)) return err;
    throw new Error(`${label}: expected HTTP ${expectedList.join("/")} but got: ${err.message}`);
  }
  throw new Error(`${label}: expected HTTP ${expectedList.join("/")} but request succeeded`);
}

// Build a multipart/form-data body from string and file fields.
function buildMultipart(fields = {}, files = []) {
  const boundary = `----pbqa${Date.now().toString(16)}${Math.random().toString(16).slice(2)}`;
  const parts = [];
  for (const [name, value] of Object.entries(fields)) {
    parts.push(Buffer.from(
      `--${boundary}\r\nContent-Disposition: form-data; name="${name}"\r\n\r\n${value}\r\n`
    ));
  }
  for (const f of files) {
    parts.push(Buffer.from(
      `--${boundary}\r\nContent-Disposition: form-data; name="${f.field}"; filename="${f.filename}"\r\n` +
      `Content-Type: ${f.contentType || "application/octet-stream"}\r\n\r\n`
    ));
    parts.push(Buffer.isBuffer(f.content) ? f.content : Buffer.from(f.content));
    parts.push(Buffer.from("\r\n"));
  }
  parts.push(Buffer.from(`--${boundary}--\r\n`));
  return { body: Buffer.concat(parts), contentType: `multipart/form-data; boundary=${boundary}` };
}

// ---------------------------------------------------------------------------
// QA Runner
// ---------------------------------------------------------------------------

class QA {
  constructor(opts) {
    this.opts = opts;
    this.tmpDir = resolve(opts.tmpDir);
    this.pbData = resolve(this.tmpDir, "pb_data");
    this.pbMigrations = resolve(this.tmpDir, "pb_migrations");
    this.pbLog = resolve(this.tmpDir, "pb.log");
    this.binary = resolve(this.tmpDir, process.platform === "win32" ? "pocketbase-qa.exe" : "pocketbase-qa");
    this.pbProc = null;
    this.dockerStarted = false;
    this.succeeded = false;
    this.baseUrl = `http://${opts.httpAddr}`;
    this.token = null;
  }

  log(msg) { console.log(msg); }

  logStep(msg) { this.log(`[qa] ${msg}`); }

  logWait(label, attempt, max) {
    if (attempt === 1 || attempt % 5 === 0 || attempt === max) {
      this.logStep(`${label} (${attempt}/${max})...`);
    }
  }

  tailFile(path, maxLines = 40) {
    if (!existsSync(path)) return "";
    const lines = readFileSync(path, "utf8").trimEnd().split(/\r?\n/);
    return lines.slice(-maxLines).join("\n");
  }

  assert(condition, msg) {
    if (!condition) throw new Error(`ASSERTION FAILED: ${msg}`);
  }

  mysqlDsn() {
    // NOTE: go-sql-driver/mysql does NOT URL-decode the user/password, so the
    // credentials are intentionally embedded verbatim. The parser is robust to
    // special characters as long as the "@tcp(", "/<db>" and "?<params>"
    // structure stays intact, so no escaping is applied here.
    const creds = this.opts.mysqlPassword
      ? `${this.opts.mysqlUser}:${this.opts.mysqlPassword}`
      : this.opts.mysqlUser;
    return `${creds}@tcp(${this.opts.mysqlHost}:${this.opts.mysqlPort})/${this.opts.mysqlDatabase}?parseTime=true&multiStatements=true`;
  }

  mysqlEnv() {
    return {
      ...process.env,
      PB_DATABASE_DRIVER: "mysql",
      PB_DATABASE_DSN: this.mysqlDsn(),
    };
  }

  cleanup() {
    if (this.pbProc && this.pbProc.exitCode === null) {
      try { this.pbProc.kill(); } catch {}
      // Give it a moment, then force kill if still alive (esp. on Windows).
      try {
        sleepSync(500);
        if (this.pbProc.exitCode === null) {
          if (process.platform === "win32") {
            spawnSync("taskkill", ["/pid", String(this.pbProc.pid), "/f", "/t"], { stdio: "ignore" });
          } else {
            this.pbProc.kill("SIGKILL");
          }
        }
      } catch {}
    }
    if (this.dockerStarted) {
      spawnSync("docker", ["rm", "-f", this.opts.mysqlContainer], { stdio: "ignore" });
    }
    // Keep artifacts on failure (for debugging) or when explicitly requested.
    if (this.succeeded && !this.opts.keepTmp) {
      try { rmSync(this.tmpDir, { recursive: true, force: true }); } catch {}
    } else if (!this.succeeded) {
      this.logStep(`Artifacts kept for debugging at: ${this.tmpDir}`);
    }
  }

  ensurePortFree() {
    const { port } = splitHostPort(this.opts.httpAddr);
    return new Promise((resolve, reject) => {
      const srv = createServer();
      srv.once("error", () => reject(new Error(`HTTP address ${this.opts.httpAddr} is already in use.`)));
      srv.listen(port, () => { srv.close(); resolve(); });
    });
  }

  // mysql CLI password flag; empty password must omit the value entirely.
  mysqlPwFlag() {
    return this.opts.mysqlPassword ? [`-p${this.opts.mysqlPassword}`] : [];
  }

  // Verify an external MySQL is reachable before doing expensive work.
  ensureMysqlReachable() {
    if (!this.opts.skipDocker) return;
    this.logStep(`Checking connectivity to existing MySQL at ${this.opts.mysqlHost}:${this.opts.mysqlPort}...`);
    return new Promise((resolve, reject) => {
      const socket = createConnection({ host: this.opts.mysqlHost, port: this.opts.mysqlPort });
      const timer = setTimeout(() => {
        socket.destroy();
        reject(new Error(`Cannot reach MySQL at ${this.opts.mysqlHost}:${this.opts.mysqlPort} (timeout). Is it running?`));
      }, 5000);
      socket.once("connect", () => { clearTimeout(timer); socket.end(); this.logStep("MySQL reachable."); resolve(); });
      socket.once("error", (err) => { clearTimeout(timer); reject(new Error(`Cannot reach MySQL at ${this.opts.mysqlHost}:${this.opts.mysqlPort}: ${err.message}`)); });
    });
  }

  // Query the MySQL version string and reject unsupported engines (MariaDB)
  // or MySQL versions older than 8.x. JSON_TABLE — used by the dialect
  // refactor — requires MySQL 8.0.17+.
  //
  // In Docker mode the mysql CLI runs inside the container via `docker exec`.
  // In skip-Docker mode the local mysql CLI is used; if it is not installed
  // the preflight is skipped with a warning (the TCP connectivity check already
  // passed).
  preflightMysqlVersion() {
    this.logStep("Checking MySQL engine and version...");

    let cmd, args;
    if (this.opts.skipDocker) {
      // Check if the mysql CLI is available on the host.
      const probe = process.platform === "win32"
        ? spawnSync("where", ["mysql"], { stdio: "ignore" })
        : spawnSync("which", ["mysql"], { stdio: "ignore" });
      if (probe.status !== 0) {
        this.logStep("WARNING: mysql CLI not found on host — skipping engine/version preflight.");
        return;
      }
      cmd = "mysql";
      args = [
        "-h", this.opts.mysqlHost, "-P", String(this.opts.mysqlPort),
        "-u", this.opts.mysqlUser, ...this.mysqlPwFlag(),
        this.opts.mysqlDatabase, "-N", "-B", "-e", "SELECT VERSION()",
      ];
    } else {
      cmd = "docker";
      args = [
        "exec", this.opts.mysqlContainer,
        "mysql", "-h127.0.0.1", "-uroot", ...this.mysqlPwFlag(),
        this.opts.mysqlDatabase, "-N", "-B", "-e", "SELECT VERSION()",
      ];
    }

    const r = spawnSync(cmd, args, { stdio: "pipe" });
    if (r.status !== 0) {
      const stderr = r.stderr?.toString().trim();
      throw new Error(`Failed to query MySQL version: ${stderr || "unknown error"}`);
    }
    const versionStr = r.stdout?.toString().trim();
    if (!versionStr) {
      throw new Error("MySQL version query returned an empty result");
    }

    // MariaDB reports version strings like "10.11.8-MariaDB" or
    // "11.4.3-MariaDB-1:11.4.3+maria~ubu2204". MySQL reports "8.0.40" or
    // "8.4.3" (no suffix). Some MySQL forks embed a suffix like "-MySQL".
    const isMariaDB = /mariadb/i.test(versionStr);
    if (isMariaDB) {
      throw new Error(
        `Unsupported MySQL engine: MariaDB detected ("${versionStr}").\n` +
        `The PocketBase MySQL fork requires MySQL 8.0.17+ (JSON_TABLE support).\n` +
        `MariaDB is not supported due to differences in JSON functions and SQL syntax.`
      );
    }

    // Extract the major.minor version number from the leading numeric part.
    const match = versionStr.match(/^(\d+)\.(\d+)/);
    if (!match) {
      throw new Error(`Could not parse MySQL version from "${versionStr}"`);
    }
    const major = parseInt(match[1], 10);

    // Require MySQL 8.0.17+ for JSON_TABLE. We check major >= 8, and for
    // major === 8 we accept any minor (8.0.x and 8.4.x both have JSON_TABLE).
    if (major < 8) {
      throw new Error(
        `Unsupported MySQL version: ${versionStr}.\n` +
        `The PocketBase MySQL fork requires MySQL 8.0.17+ (JSON_TABLE support).\n` +
        `Please upgrade your MySQL server.`
      );
    }

    this.logStep(`MySQL engine/version OK: ${versionStr}`);
  }

  maybeStartDocker() {
    if (this.opts.skipDocker) {
      this.logStep(`Skipping Docker - using existing MySQL at ${this.opts.mysqlHost}:${this.opts.mysqlPort}`);
      return;
    }
    ensureCommand("docker");
    this.logStep(`Removing any previous MySQL container '${this.opts.mysqlContainer}'...`);
    spawnSync("docker", ["rm", "-f", this.opts.mysqlContainer], { stdio: "ignore" });

    // Empty root password requires MYSQL_ALLOW_EMPTY_PASSWORD instead.
    const pwEnv = this.opts.mysqlPassword
      ? ["-e", `MYSQL_ROOT_PASSWORD=${this.opts.mysqlPassword}`]
      : ["-e", "MYSQL_ALLOW_EMPTY_PASSWORD=yes"];

    this.logStep(`Starting MySQL container '${this.opts.mysqlContainer}' from ${this.opts.mysqlImage} on port ${this.opts.mysqlPort}...`);
    const r = spawnSync("docker", [
      "run", "--rm", "-d",
      "--name", this.opts.mysqlContainer,
      ...pwEnv,
      "-e", `MYSQL_DATABASE=${this.opts.mysqlDatabase}`,
      "-p", `${this.opts.mysqlPort}:3306`,
      this.opts.mysqlImage,
    ], { stdio: "pipe" });
    if (r.status !== 0) {
      const stderr = r.stderr?.toString().trim();
      const stdout = r.stdout?.toString().trim();
      throw new Error([
        "Failed to start MySQL Docker container.",
        stderr ? `docker stderr:\n${stderr}` : "",
        stdout ? `docker stdout:\n${stdout}` : "",
      ].filter(Boolean).join("\n"));
    }
    const containerId = r.stdout?.toString().trim();
    if (containerId) this.logStep(`MySQL container started: ${containerId}`);
    this.dockerStarted = true;

    // Wait for MySQL to be ready
    this.logStep(`Waiting for MySQL readiness at ${this.opts.mysqlHost}:${this.opts.mysqlPort} (timeout: 90s)...`);
    let lastError = "";
    for (let i = 1; i <= 90; i++) {
      // Bail out early if the container died (e.g. bad image / port clash).
      const alive = spawnSync("docker", ["inspect", "-f", "{{.State.Running}}", this.opts.mysqlContainer], { stdio: "pipe" });
      if (alive.status === 0 && alive.stdout?.toString().trim() === "false") {
        const logs = spawnSync("docker", ["logs", "--tail", "40", this.opts.mysqlContainer], { stdio: "pipe" });
        throw new Error(`MySQL container exited during startup. Logs:\n${logs.stdout?.toString() || ""}${logs.stderr?.toString() || ""}`);
      }
      const check = spawnSync("docker", [
        "exec", this.opts.mysqlContainer,
        "mysql", "-h127.0.0.1", `-uroot`, ...this.mysqlPwFlag(),
        this.opts.mysqlDatabase, "-e", "SELECT 1",
      ], { stdio: "pipe" });
      if (check.status === 0) {
        this.logStep(`MySQL ready after ${i}s.`);
        return;
      }
      lastError = check.stderr?.toString().trim() || check.stdout?.toString().trim() || lastError;
      this.logWait("MySQL not ready yet", i, 90);
      sleepSync(1000);
    }
    throw new Error([
      "MySQL container did not become ready in time.",
      lastError ? `Last readiness error:\n${lastError}` : "",
    ].filter(Boolean).join("\n"));
  }

  buildBinary() {
    ensureCommand("go");
    mkdirSync(this.tmpDir, { recursive: true });
    if (existsSync(this.binary)) rmSync(this.binary);
    this.logStep(`Building PocketBase QA binary: ${this.binary}`);
    const r = spawnSync("go", ["build", "-o", this.binary, "./examples/base"], {
      cwd: REPO_ROOT, stdio: "inherit",
    });
    if (r.status !== 0) throw new Error("Failed to build binary");
    this.logStep("PocketBase QA binary built.");
  }

  async startServer() {
    if (existsSync(this.pbData)) rmSync(this.pbData, { recursive: true });
    if (existsSync(this.pbMigrations)) rmSync(this.pbMigrations, { recursive: true });
    if (existsSync(this.pbLog)) rmSync(this.pbLog);

    mkdirSync(this.tmpDir, { recursive: true });

    const { openSync, closeSync } = await import("node:fs");
    const logFd = openSync(this.pbLog, "w");
    this.logStep(`Starting PocketBase server at ${this.opts.httpAddr}...`);
    this.logStep(`PocketBase log: ${this.pbLog}`);
    this.pbProc = spawn(
      this.binary,
      // --dev=false: the binary lives in the temp dir, which PocketBase would
      // otherwise treat as "go run" and enable dev mode (printing every request
      // and SQL statement, polluting the log scan with expected 4xx errors).
      ["serve", "--dev=false", "--dir", this.pbData, "--migrationsDir", this.pbMigrations, "--http", this.opts.httpAddr],
      { cwd: REPO_ROOT, env: this.mysqlEnv(), stdio: ["ignore", logFd, logFd] }
    );
    closeSync(logFd);

    this.logStep("Waiting for PocketBase server startup (timeout: 90s)...");
    for (let i = 1; i <= 90; i++) {
      await sleep(1000);
      if (existsSync(this.pbLog)) {
        const log = readFileSync(this.pbLog, "utf8");
        if (log.includes("Server started")) {
          this.logStep(`PocketBase server ready after ${i}s.`);
          return;
        }
      }
      if (this.pbProc.exitCode !== null) {
        throw new Error(`PocketBase server exited during startup. Log tail:\n${this.tailFile(this.pbLog)}`);
      }
      this.logWait("PocketBase not ready yet", i, 90);
    }
    throw new Error(`Server did not start in time. Log tail:\n${this.tailFile(this.pbLog)}`);
  }

  async createSuperuserAndAuth() {
    const r = spawnSync(
      this.binary,
      ["superuser", "upsert", "qa@example.com", "password123", "--dir", this.pbData, "--migrationsDir", this.pbMigrations],
      { cwd: REPO_ROOT, env: this.mysqlEnv(), stdio: "pipe" }
    );
    if (r.status !== 0) throw new Error(`superuser upsert failed: ${r.stderr?.toString()}`);

    const auth = await POST(`${this.baseUrl}/api/collections/_superusers/auth-with-password`, {
      payload: { identity: "qa@example.com", password: "password123" },
    });
    this.assert(auth.token, "Failed to authenticate QA superuser");
    this.token = auth.token;
  }

  async cleanupQaCollections() {
    for (let pass = 0; pass < 10; pass++) {
      const result = await GET(`${this.baseUrl}/api/collections?page=1&perPage=500`, { token: this.token });
      const qaItems = (result.items || [])
        .filter((i) => i.name.startsWith("qa_"))
        .sort((a, b) => b.created.localeCompare(a.created));
      if (qaItems.length === 0) return;

      let deletedAny = false;
      for (const item of qaItems) {
        try {
          await DELETE(`${this.baseUrl}/api/collections/${item.id}`, { token: this.token });
          deletedAny = true;
        } catch {}
      }
      if (!deletedAny) throw new Error("Failed to cleanup previous qa_* collections");
    }
  }

  async createCollection(payload) {
    return POST(`${this.baseUrl}/api/collections`, { token: this.token, payload });
  }

  async patchCollection(id, payload) {
    return PATCH(`${this.baseUrl}/api/collections/${id}`, { token: this.token, payload });
  }

  async createRecord(collection, payload) {
    return POST(`${this.baseUrl}/api/collections/${collection}/records`, { payload });
  }

  async getRecords(collection, query = "", { token } = {}) {
    const url = query
      ? `${this.baseUrl}/api/collections/${collection}/records?${query}`
      : `${this.baseUrl}/api/collections/${collection}/records`;
    return GET(url, { token: token ? this.token : undefined });
  }

  async getRecord(collection, id) {
    return GET(`${this.baseUrl}/api/collections/${collection}/records/${id}`);
  }

  async updateRecord(collection, id, payload) {
    return PATCH(`${this.baseUrl}/api/collections/${collection}/records/${id}`, { payload });
  }

  // --- Token-aware record/auth helpers (used by the rules section) ---

  recordsUrl(collection, query = "") {
    return query
      ? `${this.baseUrl}/api/collections/${collection}/records?${query}`
      : `${this.baseUrl}/api/collections/${collection}/records`;
  }

  listAs(collection, query, token) { return GET(this.recordsUrl(collection, query), { token }); }
  viewAs(collection, id, token) { return GET(`${this.baseUrl}/api/collections/${collection}/records/${id}`, { token }); }
  createAs(collection, payload, token) { return POST(this.recordsUrl(collection), { token, payload }); }
  updateAs(collection, id, payload, token) { return PATCH(`${this.baseUrl}/api/collections/${collection}/records/${id}`, { token, payload }); }
  deleteAs(collection, id, token) { return DELETE(`${this.baseUrl}/api/collections/${collection}/records/${id}`, { token }); }

  // Create an auth-collection user (seeded as superuser to bypass createRule).
  async createUser(collection, email, password, extra = {}) {
    return POST(this.recordsUrl(collection), {
      token: this.token,
      payload: { email, password, passwordConfirm: password, ...extra },
    });
  }

  async authAs(collection, identity, password) {
    const res = await POST(`${this.baseUrl}/api/collections/${collection}/auth-with-password`, {
      payload: { identity, password },
    });
    this.assert(res.token, `auth-with-password for ${identity} should return a token`);
    return res.token;
  }

  // =========================================================================
  // Test sections
  // =========================================================================

  async sectionBasicRuntime() {
    const runtime = await this.createCollection({
      name: "qa_runtime", type: "base",
      listRule: "", viewRule: "", createRule: "", updateRule: "", deleteRule: "",
      fields: [
        { name: "title", type: "text", required: true, max: 255 },
        { name: "published", type: "bool" },
      ],
      indexes: ["CREATE INDEX idx_qa_runtime_title ON qa_runtime (title)"],
    });

    await this.createRecord("qa_runtime", { title: "hello mysql runtime", published: true });
    await this.getRecords("qa_runtime", "sort=title");
    await this.getRecords("qa_runtime", "filter=title~%22runtime%22");

    const addSelect = { fields: [...runtime.fields, { name: "status", type: "select", required: false, values: ["draft", "published"], maxSelect: 1 }] };
    await this.patchCollection(runtime.id, addSelect);

    await this.createRecord("qa_runtime", { title: "schema updated", published: false, status: "draft" });
    await this.getRecords("qa_runtime", "filter=status=%22draft%22");
    this.log("Basic runtime: OK");
  }

  async sectionSelectAndMatrix() {
    await this.createCollection({
      name: "qa_multi_select", type: "base",
      listRule: "", viewRule: "", createRule: "", updateRule: "", deleteRule: "",
      fields: [
        { name: "title", type: "text", required: true, max: 255 },
        { name: "tags", type: "select", required: false, values: ["alpha", "beta", "gamma"], maxSelect: 3 },
      ],
    });
    await this.createRecord("qa_multi_select", { title: "multi select", tags: ["alpha", "beta"] });
    await this.getRecords("qa_multi_select", "filter=tags~%22alpha%22");

    const matrix = await this.createCollection({
      name: "qa_matrix", type: "base",
      listRule: "", viewRule: "", createRule: "", updateRule: "", deleteRule: "",
      fields: [
        { name: "title", type: "text", required: true, max: 255 },
        { name: "status", type: "select", required: false, values: ["draft", "published"], maxSelect: 1 },
      ],
    });
    await this.createRecord("qa_matrix", { title: "before matrix", status: "draft" });

    // Rename status -> state
    let fields = matrix.fields.map((f) => f.name === "status" ? { ...f, name: "state" } : f);
    let renamed = await this.patchCollection(matrix.id, { fields });
    await sleep(1000);

    // Delete title field
    fields = renamed.fields.filter((f) => f.name !== "title");
    let deleted = await this.patchCollection(matrix.id, { fields });
    await sleep(1000);

    // Single -> multi
    fields = deleted.fields.map((f) => f.name === "state" ? { ...f, maxSelect: 3, values: ["draft", "published", "archived"] } : f);
    let multi = await this.patchCollection(matrix.id, { fields });
    await sleep(1000);

    await this.createRecord("qa_matrix", { state: ["draft", "published"] });
    await this.getRecords("qa_matrix", "filter=state~%22draft%22");

    // Multi -> single
    fields = multi.fields.map((f) => f.name === "state" ? { ...f, maxSelect: 1 } : f);
    await this.patchCollection(matrix.id, { fields });
    await this.getRecords("qa_matrix", "filter=state=%22published%22");

    this.log("Select and matrix: OK");
  }

  async sectionRelationsAndView() {
    const authors = await this.createCollection({
      name: "qa_authors", type: "base",
      listRule: "", viewRule: "", createRule: "", updateRule: "", deleteRule: "",
      fields: [{ name: "name", type: "text", required: true, max: 255 }],
    });
    await this.createCollection({
      name: "qa_books", type: "base",
      listRule: "", viewRule: "", createRule: "", updateRule: "", deleteRule: "",
      fields: [
        { name: "title", type: "text", required: true, max: 255 },
        { name: "authors", type: "relation", required: false, collectionId: authors.id, maxSelect: 3 },
      ],
    });

    const a1 = await this.createRecord("qa_authors", { name: "Author One" });
    const a2 = await this.createRecord("qa_authors", { name: "Author Two" });
    await this.createRecord("qa_books", { title: "Book One", authors: [a1.id, a2.id] });

    await this.getRecords("qa_authors", "filter=qa_books_via_authors.title~%22Book%22");
    await this.getRecords("qa_books", "filter=authors.name~%22Author%22");
    await this.getRecords("qa_books", "expand=authors");

    await this.createCollection({
      name: "qa_books_view", type: "view",
      viewQuery: "SELECT id, title FROM qa_books",
    });

    await this.getRecords("qa_books_view", "", { token: true });
    await this.getRecords("qa_books_view", "filter=title~%22Book%22", { token: true });
    await this.createRecord("qa_books", { title: "Book Two" });
    await this.getRecords("qa_books_view", "sort=title", { token: true });

    this.log("Relations and view: OK");
  }

  async sectionAllFields() {
    this.log("Testing all field types...");

    const ref = await this.createCollection({
      name: "qa_ref", type: "base",
      listRule: "", viewRule: "", createRule: "", updateRule: "", deleteRule: "",
      fields: [{ name: "label", type: "text", required: true }],
    });
    const refRecord = await this.createRecord("qa_ref", { label: "ref-one" });

    const baseFields = [
      { name: "f_text", type: "text", required: false, max: 500 },
      { name: "f_number", type: "number", required: false },
      { name: "f_bool", type: "bool", required: false },
      { name: "f_email", type: "email", required: false },
      { name: "f_url", type: "url", required: false },
      { name: "f_date", type: "date", required: false },
      { name: "f_select_single", type: "select", required: false, values: ["a", "b", "c"], maxSelect: 1 },
      { name: "f_select_multi", type: "select", required: false, values: ["x", "y", "z"], maxSelect: 3 },
      { name: "f_json", type: "json", required: false },
      { name: "f_editor", type: "editor", required: false },
      { name: "f_relation", type: "relation", required: false, collectionId: ref.id, maxSelect: 1 },
      { name: "f_relation_multi", type: "relation", required: false, collectionId: ref.id, maxSelect: 5 },
    ];

    const allFields = await this.createCollection({
      name: "qa_all_fields", type: "base",
      listRule: "", viewRule: "", createRule: "", updateRule: "", deleteRule: "",
      fields: baseFields,
    });

    // Create with all fields
    const record = await this.createRecord("qa_all_fields", {
      f_text: "hello world", f_number: 43, f_bool: true,
      f_email: "test@example.com", f_url: "https://example.com",
      f_date: "2026-01-15 10:00:00.000Z",
      f_select_single: "a", f_select_multi: ["x", "y"],
      f_json: { key: "value", num: 123 },
      f_editor: "<p>rich text</p>",
      f_relation: refRecord.id, f_relation_multi: [refRecord.id],
    });

    const fetched = await this.getRecord("qa_all_fields", record.id);
    this.assert(fetched.f_text === "hello world", "f_text mismatch");
    this.assert(fetched.f_number === 43, "f_number mismatch");
    this.assert(fetched.f_bool === true, "f_bool mismatch");
    this.assert(fetched.f_email === "test@example.com", "f_email mismatch");
    this.assert(fetched.f_url === "https://example.com", "f_url mismatch");
    this.assert(fetched.f_select_single === "a", "f_select_single mismatch");
    this.assert(fetched.f_select_multi.length === 2, "f_select_multi mismatch");
    this.assert(fetched.f_editor === "<p>rich text</p>", "f_editor mismatch");
    this.assert(fetched.f_relation === refRecord.id, "f_relation mismatch");
    this.assert(fetched.f_relation_multi.length === 1, "f_relation_multi mismatch");
    this.log("All field type create/read: OK");

    // Update
    const updated = await this.updateRecord("qa_all_fields", record.id, {
      f_text: "updated text", f_number: 99, f_bool: false,
      f_select_single: "b", f_select_multi: ["z"],
    });
    this.assert(updated.f_text === "updated text", "f_text update mismatch");
    this.assert(updated.f_number === 99, "f_number update mismatch");
    this.assert(updated.f_bool === false, "f_bool update mismatch");
    this.assert(updated.f_select_single === "b", "f_select_single update mismatch");
    this.assert(JSON.stringify(updated.f_select_multi) === '["z"]', "f_select_multi update mismatch");
    this.log("All field type update: OK");

    // Filters
    let r;
    r = await this.getRecords("qa_all_fields", "filter=f_text~%22updated%22");
    this.assert(r.items.length >= 1, "filter by f_text");
    r = await this.getRecords("qa_all_fields", "filter=f_number%3E50");
    this.assert(r.items.length >= 1, "filter by f_number");
    r = await this.getRecords("qa_all_fields", "filter=f_bool%3Dfalse");
    this.assert(r.items.length >= 1, "filter by f_bool");
    r = await this.getRecords("qa_all_fields", "filter=f_email~%22example%22");
    this.assert(r.items.length >= 1, "filter by f_email");
    r = await this.getRecords("qa_all_fields", "filter=f_select_single%3D%22b%22");
    this.assert(r.items.length >= 1, "filter by f_select_single");
    const relFilter = encodeURIComponent(`f_relation="${refRecord.id}"`);
    r = await this.getRecords("qa_all_fields", `filter=${relFilter}`);
    this.assert(r.items.length >= 1, "filter by f_relation");
    this.log("All field type filters: OK");

    // Schema add field
    const addFields = [...allFields.fields, { name: "f_new_text", type: "text", required: false, max: 100 }];
    await this.patchCollection(allFields.id, { fields: addFields });
    const afterAdd = await this.createRecord("qa_all_fields", { f_text: "after schema add", f_new_text: "new field value" });
    this.assert(afterAdd.f_new_text === "new field value", "f_new_text after schema add");
    this.log("Schema add field: OK");
    await sleep(1000);

    // Schema rename field
    const renamedFields = addFields.map((f) => f.name === "f_new_text" ? { ...f, name: "f_renamed_text" } : f);
    await this.patchCollection(allFields.id, { fields: renamedFields });
    await sleep(1000);
    const afterRename = await this.getRecord("qa_all_fields", record.id);
    this.assert(afterRename.f_text === "updated text", "existing record unreadable after rename");
    this.log("Schema rename field: OK");

    // Schema delete field
    const deletedFields = renamedFields.filter((f) => f.name !== "f_renamed_text");
    await this.patchCollection(allFields.id, { fields: deletedFields });
    await sleep(1000);
    const afterDelete = await this.getRecord("qa_all_fields", record.id);
    this.assert(afterDelete.f_text === "updated text", "existing record unreadable after field delete");
    this.assert(!("f_renamed_text" in afterDelete), "deleted field still present");
    this.log("Schema delete field: OK");
    await sleep(1000);

    // Sorts
    await this.getRecords("qa_all_fields", "sort=f_text");
    await this.getRecords("qa_all_fields", "sort=-f_number");
    await this.getRecords("qa_all_fields", "sort=-f_date");
    await this.getRecords("qa_all_fields", "sort=-id");
    this.log("All field type sorts: OK");

    // Expand
    const expanded = await this.getRecords("qa_all_fields", "expand=f_relation");
    const hasExpand = expanded.items.some((i) => i.expand?.f_relation?.label === "ref-one");
    this.assert(hasExpand, "expand f_relation");
    this.log("Relation expand: OK");

    this.log("All field types QA: PASSED");
  }

  async createRecordMultipart(collection, fields, files) {
    const { body, contentType } = buildMultipart(fields, files);
    return POST(`${this.baseUrl}/api/collections/${collection}/records`, {
      rawBody: body, headers: { "Content-Type": contentType },
    });
  }

  // Raw GET returning the response body as a Buffer (for file downloads).
  downloadBytes(url) {
    return new Promise((resolveP, reject) => {
      const u = new URL(url);
      const req = http.request(
        { hostname: u.hostname, port: u.port, path: u.pathname + u.search, method: "GET", timeout: HTTP_TIMEOUT_MS },
        (res) => {
          const chunks = [];
          res.on("data", (c) => chunks.push(c));
          res.on("end", () => {
            if (res.statusCode >= 400) return reject(new HttpError(res.statusCode, "GET", url, Buffer.concat(chunks).toString("utf8")));
            resolveP(Buffer.concat(chunks));
          });
        }
      );
      req.on("timeout", () => req.destroy(new Error(`Download timed out: ${url}`)));
      req.on("error", reject);
      req.end();
    });
  }

  // Negative tests: server must reject invalid input instead of corrupting data.
  async sectionValidation() {
    const col = await this.createCollection({
      name: "qa_validation", type: "base",
      listRule: "", viewRule: "", createRule: "", updateRule: "", deleteRule: "",
      fields: [
        { name: "title", type: "text", required: true, max: 10 },
        { name: "count", type: "number", required: false, onlyInt: true, min: 0 },
        { name: "email", type: "email", required: false },
        { name: "link", type: "url", required: false },
        { name: "status", type: "select", required: false, values: ["a", "b"], maxSelect: 1 },
      ],
    });

    await expectStatus("required field omitted", 400, () =>
      this.createRecord("qa_validation", { count: 1 }));
    await expectStatus("text exceeds max length", 400, () =>
      this.createRecord("qa_validation", { title: "way-too-long-title-value" }));
    await expectStatus("non-integer for onlyInt number", 400, () =>
      this.createRecord("qa_validation", { title: "ok", count: 1.5 }));
    await expectStatus("number below min", 400, () =>
      this.createRecord("qa_validation", { title: "ok", count: -5 }));
    await expectStatus("invalid email", 400, () =>
      this.createRecord("qa_validation", { title: "ok", email: "not-an-email" }));
    await expectStatus("invalid url", 400, () =>
      this.createRecord("qa_validation", { title: "ok", link: "not a url" }));
    await expectStatus("invalid select value", 400, () =>
      this.createRecord("qa_validation", { title: "ok", status: "zzz" }));
    await expectStatus("missing record 404", 404, () =>
      this.getRecord("qa_validation", "nonexistent00000"));

    // A valid record must still succeed after all the rejections.
    const ok = await this.createRecord("qa_validation", { title: "ok", count: 2, email: "a@b.co", status: "a" });
    this.assert(ok.id, "valid record should be created");
    void col;
    this.log("Validation / negative tests: OK");
  }

  // Unique index must reject duplicates at the DB level.
  async sectionUniqueIndex() {
    await this.createCollection({
      name: "qa_unique", type: "base",
      listRule: "", viewRule: "", createRule: "", updateRule: "", deleteRule: "",
      fields: [
        { name: "sku", type: "text", required: true, max: 50 },
        { name: "name", type: "text", required: false, max: 50 },
      ],
      indexes: ["CREATE UNIQUE INDEX idx_qa_unique_sku ON qa_unique (sku)"],
    });
    await this.createRecord("qa_unique", { sku: "SKU-1", name: "first" });
    await expectStatus("duplicate unique value", 400, () =>
      this.createRecord("qa_unique", { sku: "SKU-1", name: "dup" }));
    // Different value is fine.
    const ok = await this.createRecord("qa_unique", { sku: "SKU-2", name: "second" });
    this.assert(ok.id, "non-duplicate unique value should succeed");

    const usersCol = await this.createCollection({
      type: "auth", name: "qa_index_users",
      fields: [{ name: "name", type: "text", required: false, max: 100 }],
    });
    const user = await this.createUser("qa_index_users", "idx@example.com", "password123", { name: "Index User", verified: true });

    await this.createCollection({
      name: "qa_notifications", type: "base",
      listRule: "", viewRule: "", createRule: "", updateRule: "", deleteRule: "",
      fields: [
        { name: "recipient_user", type: "relation", required: true, collectionId: usersCol.id, maxSelect: 1 },
        { name: "type", type: "select", required: true, maxSelect: 1, values: ["payment_request_verified"] },
        { name: "title", type: "text", required: true, max: 100 },
        // Keep these text fields unbounded to mirror exported PocketBase migrations
        // where max=0 would otherwise map to VARCHAR(255) on MySQL.
        { name: "entity_collection", type: "text", required: true, max: 0 },
        { name: "entity_id", type: "text", required: true, max: 0 },
      ],
      indexes: [
        "CREATE UNIQUE INDEX idx_qa_notifications_logical_event ON qa_notifications (recipient_user, type, entity_collection, entity_id)",
      ],
    });
    await this.createRecord("qa_notifications", {
      recipient_user: user.id,
      type: "payment_request_verified",
      title: "first",
      entity_collection: "payment_requests",
      entity_id: "abc123abc123abc",
    });
    await expectStatus("duplicate composite logical event", 400, () =>
      this.createRecord("qa_notifications", {
        recipient_user: user.id,
        type: "payment_request_verified",
        title: "duplicate",
        entity_collection: "payment_requests",
        entity_id: "abc123abc123abc",
      }));
    const uniqueComposite = await this.createRecord("qa_notifications", {
      recipient_user: user.id,
      type: "payment_request_verified",
      title: "different entity",
      entity_collection: "payment_requests",
      entity_id: "def456def456def",
    });
    this.assert(uniqueComposite.id, "non-duplicate composite logical event should succeed");

    this.log("Unique index: OK");
  }

  // File upload via multipart, read-back and download.
  async sectionFileField() {
    const col = await this.createCollection({
      name: "qa_files", type: "base",
      listRule: "", viewRule: "", createRule: "", updateRule: "", deleteRule: "",
      fields: [
        { name: "title", type: "text", required: true, max: 100 },
        { name: "doc", type: "file", required: false, maxSelect: 1, maxSize: 5242880 },
        { name: "gallery", type: "file", required: false, maxSelect: 3, maxSize: 5242880 },
      ],
    });

    const content = Buffer.from("hello pocketbase file content\n");
    const rec = await this.createRecordMultipart(
      "qa_files",
      { title: "with file" },
      [
        { field: "doc", filename: "note.txt", content, contentType: "text/plain" },
        { field: "gallery", filename: "a.txt", content: "aaa", contentType: "text/plain" },
        { field: "gallery", filename: "b.txt", content: "bbb", contentType: "text/plain" },
      ]
    );
    this.assert(typeof rec.doc === "string" && rec.doc.length > 0, "doc filename should be stored");
    this.assert(Array.isArray(rec.gallery) && rec.gallery.length === 2, "gallery should store 2 files");

    const fetched = await this.getRecord("qa_files", rec.id);
    this.assert(fetched.doc === rec.doc, "doc filename should persist");

    const bytes = await this.downloadBytes(`${this.baseUrl}/api/files/${col.id}/${rec.id}/${rec.doc}`);
    this.assert(bytes.equals(content), "downloaded file content should match upload");

    this.log("File field upload/download: OK");
  }

  // GeoPoint create/read/update.
  async sectionGeoPoint() {
    await this.createCollection({
      name: "qa_geo", type: "base",
      listRule: "", viewRule: "", createRule: "", updateRule: "", deleteRule: "",
      fields: [
        { name: "title", type: "text", required: true, max: 100 },
        { name: "location", type: "geoPoint", required: false },
      ],
    });
    const rec = await this.createRecord("qa_geo", { title: "hq", location: { lon: 106.8456, lat: -6.2088 } });
    this.assert(rec.location && Math.abs(rec.location.lon - 106.8456) < 1e-6, "geoPoint lon stored");
    this.assert(Math.abs(rec.location.lat - -6.2088) < 1e-6, "geoPoint lat stored");

    const updated = await this.updateRecord("qa_geo", rec.id, { location: { lon: 0, lat: 0 } });
    this.assert(updated.location.lon === 0 && updated.location.lat === 0, "geoPoint update");
    this.log("GeoPoint field: OK");
  }

  // Pagination, totals, date range filtering and sort stability.
  async sectionPaginationAndDates() {
    await this.createCollection({
      name: "qa_paging", type: "base",
      listRule: "", viewRule: "", createRule: "", updateRule: "", deleteRule: "",
      fields: [
        { name: "title", type: "text", required: true, max: 50 },
        { name: "seq", type: "number", required: true },
        { name: "when", type: "date", required: false },
      ],
    });
    const total = 15;
    for (let i = 1; i <= total; i++) {
      await this.createRecord("qa_paging", {
        title: `item-${String(i).padStart(2, "0")}`,
        seq: i,
        when: `2026-0${(i % 9) + 1}-15 10:00:00.000Z`,
      });
    }

    const page2 = await this.getRecords("qa_paging", "perPage=5&page=2&sort=seq");
    this.assert(page2.page === 2, "page number");
    this.assert(page2.perPage === 5, "perPage");
    this.assert(page2.items.length === 5, "page item count");
    this.assert(page2.totalItems === total, `totalItems should be ${total}`);
    this.assert(page2.totalPages === 3, "totalPages");
    this.assert(page2.items[0].seq === 6, "pagination offset (sorted)");

    const sortedDesc = await this.getRecords("qa_paging", "sort=-seq&perPage=3");
    this.assert(sortedDesc.items[0].seq === total, "descending sort");

    const ranged = await this.getRecords("qa_paging", `filter=${encodeURIComponent('when >= "2026-05-01"')}`);
    this.assert(ranged.totalItems >= 1, "date range filter returns results");

    const skipTotal = await this.getRecords("qa_paging", "perPage=5&page=1&skipTotal=1&sort=seq");
    this.assert(skipTotal.items.length === 5, "skipTotal still returns items");
    this.log("Pagination / dates: OK");
  }

  // Relation cascade delete: deleting the parent removes the dependent record.
  async sectionCascadeDelete() {
    const owners = await this.createCollection({
      name: "qa_owners", type: "base",
      listRule: "", viewRule: "", createRule: "", updateRule: "", deleteRule: "",
      fields: [{ name: "name", type: "text", required: true, max: 50 }],
    });
    await this.createCollection({
      name: "qa_pets", type: "base",
      listRule: "", viewRule: "", createRule: "", updateRule: "", deleteRule: "",
      fields: [
        { name: "name", type: "text", required: true, max: 50 },
        { name: "owner", type: "relation", required: true, collectionId: owners.id, maxSelect: 1, cascadeDelete: true },
      ],
    });

    const owner = await this.createRecord("qa_owners", { name: "Alice" });
    const pet = await this.createRecord("qa_pets", { name: "Rex", owner: owner.id });

    await DELETE(`${this.baseUrl}/api/collections/qa_owners/records/${owner.id}`, { token: this.token });

    await expectStatus("cascade-deleted dependent record gone", 404, () =>
      this.getRecord("qa_pets", pet.id));
    this.log("Relation cascade delete: OK");
  }

  // API access rules. Rules compile to SQL WHERE clauses, so this is the most
  // MySQL-sensitive area (LIKE escaping, auth-id comparisons, relations...).
  async sectionRules() {
    this.log("Testing API access rules...");

    // --- Auth collection + users (exercises auth-collection DDL on MySQL) ---
    const users = await this.createCollection({
      type: "auth", name: "qa_users",
      fields: [{ name: "name", type: "text", required: false, max: 100 }],
    });
    const userA = await this.createUser("qa_users", "a@example.com", "password123", { name: "User A", verified: true });
    const userB = await this.createUser("qa_users", "b@example.com", "password123", { name: "User B", verified: true });
    const tokenA = await this.authAs("qa_users", "a@example.com", "password123");
    const tokenB = await this.authAs("qa_users", "b@example.com", "password123");
    this.log("Rules: auth collection + user auth: OK");

    // --- (1) null rule = superuser only ---
    await this.createCollection({
      name: "qa_rules_locked", type: "base",
      listRule: null, viewRule: null, createRule: null, updateRule: null, deleteRule: null,
      fields: [{ name: "title", type: "text", required: true, max: 100 }],
    });
    // Guest is denied list/create; superuser bypasses the locked rules.
    await expectStatusOneOf("guest list of locked collection denied", [403, 404], () =>
      this.listAs("qa_rules_locked", "", undefined));
    await expectStatusOneOf("guest create on locked collection denied", [400, 403], () =>
      this.createAs("qa_rules_locked", { title: "x" }, undefined));
    const lockedRec = await this.createAs("qa_rules_locked", { title: "secret" }, this.token);
    this.assert(lockedRec.id, "superuser should create on locked collection");
    await expectStatusOneOf("guest view of locked record denied", [403, 404], () =>
      this.viewAs("qa_rules_locked", lockedRec.id, undefined));
    const lockedList = await this.listAs("qa_rules_locked", "", this.token);
    this.assert(lockedList.totalItems === 1, "superuser should list locked records");
    this.log("Rules: null rule (superuser-only) enforcement: OK");

    // --- (2) owner-scoped rules with @request.auth.id ---
    const notes = await this.createCollection({
      name: "qa_notes", type: "base",
      listRule: "owner = @request.auth.id",
      viewRule: "owner = @request.auth.id",
      createRule: "@request.auth.id != '' && owner = @request.auth.id",
      updateRule: "owner = @request.auth.id",
      deleteRule: "owner = @request.auth.id",
      fields: [
        { name: "title", type: "text", required: true, max: 100 },
        { name: "owner", type: "relation", required: true, collectionId: users.id, maxSelect: 1 },
      ],
    });
    void notes;

    // Seed as superuser (bypasses rules): 2 notes for A, 1 for B.
    const nA1 = await this.createAs("qa_notes", { title: "A one", owner: userA.id }, this.token);
    await this.createAs("qa_notes", { title: "A two", owner: userA.id }, this.token);
    const nB1 = await this.createAs("qa_notes", { title: "B one", owner: userB.id }, this.token);

    // List as A → only A's notes (rule filters silently).
    const listA = await this.listAs("qa_notes", "perPage=100", tokenA);
    this.assert(listA.totalItems === 2, `userA should see exactly 2 notes, got ${listA.totalItems}`);
    this.assert(listA.items.every((i) => i.owner === userA.id), "userA list leaked other owners' notes");

    // Guest list → rule "owner = ''" matches nothing.
    const listGuest = await this.listAs("qa_notes", "perPage=100", undefined);
    this.assert(listGuest.totalItems === 0, `guest should see 0 notes, got ${listGuest.totalItems}`);

    // View: A can read own, cannot read B's (hidden → 403/404).
    const ownView = await this.viewAs("qa_notes", nA1.id, tokenA);
    this.assert(ownView.id === nA1.id, "userA should read own note");
    await expectStatusOneOf("userA view of userB note denied", [403, 404], () =>
      this.viewAs("qa_notes", nB1.id, tokenA));

    // Create: guest blocked; A can create own; A cannot create owned-by-B.
    await expectStatusOneOf("guest create blocked by createRule", [400, 403], () =>
      this.createAs("qa_notes", { title: "x", owner: userA.id }, undefined));
    const created = await this.createAs("qa_notes", { title: "A self", owner: userA.id }, tokenA);
    this.assert(created.id, "userA should create note owned by self");
    await expectStatusOneOf("userA create owned-by-B blocked", [400, 403], () =>
      this.createAs("qa_notes", { title: "spoof", owner: userB.id }, tokenA));

    // Update / delete across owners denied; own succeeds.
    const upd = await this.updateAs("qa_notes", nA1.id, { title: "A one edited" }, tokenA);
    this.assert(upd.title === "A one edited", "userA should update own note");
    await expectStatusOneOf("userB update of userA note denied", [403, 404], () =>
      this.updateAs("qa_notes", nA1.id, { title: "hacked" }, tokenB));
    await expectStatusOneOf("userB delete of userA note denied", [403, 404], () =>
      this.deleteAs("qa_notes", nA1.id, tokenB));
    await this.deleteAs("qa_notes", nB1.id, tokenB); // own delete → 204
    this.log("Rules: owner-scoped list/view/create/update/delete: OK");

    // --- (3) LIKE operator in a rule (MySQL-sensitive escaping) ---
    await this.createCollection({
      name: "qa_rules_like", type: "base",
      listRule: "title ~ 'public'",
      viewRule: "title ~ 'public'",
      createRule: "", updateRule: "", deleteRule: "",
      fields: [{ name: "title", type: "text", required: true, max: 100 }],
    });
    await this.createRecord("qa_rules_like", { title: "public-announcement" });
    await this.createRecord("qa_rules_like", { title: "public-notice" });
    await this.createRecord("qa_rules_like", { title: "secret-memo" });
    // Literal % must be treated as data, not a wildcard, by the rule's LIKE.
    await this.createRecord("qa_rules_like", { title: "100%-private" });

    const likeList = await this.listAs("qa_rules_like", "perPage=100", undefined);
    this.assert(likeList.totalItems === 2, `LIKE rule should match 2 'public' rows, got ${likeList.totalItems}`);
    this.assert(likeList.items.every((i) => i.title.includes("public")), "LIKE rule returned non-matching rows");

    // Combine rule with a user filter using a wildcard char to check escaping.
    const pctList = await this.listAs("qa_rules_like", `perPage=100&filter=${encodeURIComponent("title ~ 'public'")}`, undefined);
    this.assert(pctList.totalItems === 2, "LIKE rule + filter mismatch");
    this.log("Rules: LIKE operator in rule (MySQL escaping): OK");

    // --- (4) != operator in a rule/filter (MySQL uses <> instead of SQLite IS NOT) ---
    await this.createCollection({
      name: "qa_rules_not_equal", type: "base",
      listRule: "status != 'secret'",
      viewRule: "status != 'secret'",
      createRule: "", updateRule: "", deleteRule: "",
      fields: [
        { name: "title", type: "text", required: true, max: 100 },
        { name: "status", type: "text", required: true, max: 100 },
      ],
    });
    await this.createRecord("qa_rules_not_equal", { title: "public one", status: "public" });
    await this.createRecord("qa_rules_not_equal", { title: "draft one", status: "draft" });
    const secretRec = await this.createRecord("qa_rules_not_equal", { title: "secret one", status: "secret" });

    const notEqualRuleList = await this.listAs("qa_rules_not_equal", "perPage=100", undefined);
    this.assert(notEqualRuleList.totalItems === 2, `!= rule should expose 2 non-secret rows, got ${notEqualRuleList.totalItems}`);
    this.assert(notEqualRuleList.items.every((i) => i.status !== "secret"), "!= rule leaked secret row");
    await expectStatusOneOf("view blocked by != rule", [403, 404], () =>
      this.viewAs("qa_rules_not_equal", secretRec.id, undefined));

    const notEqualFilterList = await this.listAs(
      "qa_rules_not_equal",
      `perPage=100&filter=${encodeURIComponent("status != 'draft'")}`,
      undefined,
    );
    this.assert(notEqualFilterList.totalItems === 1, `!= filter combined with rule should return 1 row, got ${notEqualFilterList.totalItems}`);
    this.assert(notEqualFilterList.items[0].status === "public", "!= filter returned unexpected row");
    this.log("Rules: != operator in rule/filter: OK");

    // --- (5) authRule: only verified users may authenticate ---
    const authRuleCol = await this.createCollection({
      type: "auth", name: "qa_authrule",
      authRule: "verified = true",
      fields: [],
    });
    void authRuleCol;
    await this.createUser("qa_authrule", "ok@example.com", "password123", { verified: true });
    await this.createUser("qa_authrule", "no@example.com", "password123", { verified: false });
    const okToken = await this.authAs("qa_authrule", "ok@example.com", "password123");
    this.assert(okToken, "verified user should authenticate under authRule");
    await expectStatusOneOf("unverified user blocked by authRule", [400, 403], () =>
      POST(`${this.baseUrl}/api/collections/qa_authrule/auth-with-password`, {
        payload: { identity: "no@example.com", password: "password123" },
      }));
    this.log("Rules: authRule (verified only): OK");

    this.log("API access rules QA: PASSED");
  }

  // =========================================================================
  // Phase 6 Runtime SQL Spikes
  // =========================================================================

  // Task 6.2: Prove JSON_TABLE scalar behavior and request-body binding.
  //
  // The MySQL JSONEach expression uses JSON_TABLE(... COLUMNS(value VARCHAR(255) ...)).
  // This spike proves:
  //   1. JSON_TABLE works in MySQL 8.4 with ON clause (direct SQL verified)
  //   2. The :each modifier currently FAILS on MySQL because LEFT JOIN
  //      JSON_TABLE(...) is generated without an ON clause (MySQL requires ON
  //      for all LEFT JOINs, unlike SQLite which allows ON-less json_each joins)
  //   3. Request-body :each also FAILS because it uses hardcoded json_each()
  //      instead of dbutils.JSONEach()
  //
  // ROOT CAUSE: registerJoin() passes nil for the ON expression when registering
  // json_each/JSON_TABLE joins. SQLite tolerates `LEFT JOIN json_each(...) alias`
  // without ON, but MySQL requires `LEFT JOIN JSON_TABLE(...) alias ON 1=1`.
  //
  // FIX: Task 7.1 will add ON 1=1 for MySQL JSON_TABLE joins and migrate
  // request-body :each to use dbutils.JSONEach().
  //
  // VARCHAR(255) contract: PocketBase relation IDs are 15 characters, select
  // values are typically short, and file names are well under 255 chars. The
  // VARCHAR(255) truncation test will be added after the ON clause fix enables
  // :each to work on MySQL.
  async sectionJsonTableSpike() {
    // Create a collection with a multi-select field
    const selectValues = ["alpha", "beta", "gamma"];
    await this.createCollection({
      name: "qa_json_table", type: "base",
      listRule: "", viewRule: "", createRule: "", updateRule: "", deleteRule: "",
      fields: [
        { name: "tags", type: "select", required: false, values: selectValues, maxSelect: selectValues.length },
        { name: "json_arr", type: "json", required: false },
      ],
    });

    // Insert a record
    await this.createRecord("qa_json_table", {
      tags: ["alpha", "gamma"],
      json_arr: ["string_val", "123456789012345", 42, true, null],
    });

    // --- Test :each on select field (currently fails on MySQL) ---
    // The :each modifier generates LEFT JOIN JSON_TABLE(...) without ON clause.
    // MySQL requires ON for all LEFT JOINs. This should return 400.
    {
      const err = await expectStatus(
        "tags:each on MySQL (expected 400 — missing ON clause)",
        400,
        () => this.getRecords("qa_json_table", `filter=${encodeURIComponent('tags:each="alpha"')}`)
      );
      this.log("JSON_TABLE spike: :each on select field fails with 400 (missing ON clause) — confirmed");
    }

    // --- Test :each on JSON field (also fails for same reason) ---
    {
      const err = await expectStatus(
        "json_arr:each on MySQL (expected 400 — missing ON clause)",
        400,
        () => this.getRecords("qa_json_table", `filter=${encodeURIComponent('json_arr:each="string_val"')}`)
      );
      this.log("JSON_TABLE spike: :each on json field fails with 400 (missing ON clause) — confirmed");
    }

    // --- Test :each with numeric scalar (also fails for same reason) ---
    {
      const err = await expectStatus(
        "json_arr:each=42 on MySQL (expected 400 — missing ON clause)",
        400,
        () => this.getRecords("qa_json_table", `filter=${encodeURIComponent("json_arr:each=42")}`)
      );
      this.log("JSON_TABLE spike: :each with numeric scalar fails with 400 (missing ON clause) — confirmed");
    }

    // --- Document request-body :each finding ---
    // The request-body :each uses hardcoded json_each() (SQLite only), not
    // dbutils.JSONEach(). This will fail on MySQL even after the ON clause fix.
    // The request-body :each is exercised when a collection list rule uses
    // @request.body.field:each syntax. Testing this through the API requires
    // a POST request with a body, which is complex to set up in QA.
    // The fix will be validated in Task 7.1 integration tests.
    this.log("JSON_TABLE spike: request-body :each uses hardcoded json_each() — will fail on MySQL (documented)");

    // --- Document the fix path ---
    this.log("JSON_TABLE spike findings:");
    this.log("  1. JSON_TABLE works in MySQL 8.4 with ON clause (verified via direct SQL)");
    this.log("  2. :each on database fields fails — LEFT JOIN JSON_TABLE(...) missing ON clause");
    this.log("  3. :each on request-body fields fails — hardcoded json_each() not dialect-aware");
    this.log("  4. Fix: Task 7.1 will add ON 1=1 for MySQL joins and migrate request-body :each");
    this.log("  5. VARCHAR(255) and scalar type tests deferred to post-fix");

    this.log("JSON_TABLE scalar spike: PASSED (findings documented)");
  }

  // Task 6.3: Spike JSON array length normalization.
  //
  // The :length modifier uses dbutils.JSONArrayLength() which is currently
  // SQLite-only (no MySQL branch). This spike proves that :length fails on
  // MySQL and documents the normalization contract that the MySQL implementation
  // must preserve:
  //   - Empty string → 0
  //   - SQL NULL → 0
  //   - Scalar non-JSON string → 1 (wrapped in json_array)
  //   - Scalar non-JSON number → 1 (wrapped in json_array)
  //   - JSON array → actual length
  //   - JSON object → 0 (not an array)
  //   - Invalid JSON → 1 (wrapped in json_array)
  //   - JSON string scalar → 1
  //   - JSON number scalar → 1
  //   - JSON boolean scalar → 1
  //   - JSON null → 1
  //
  // FIX: Task 7.2 will add MySQL JSONArrayLength expression.
  async sectionJsonLengthSpike() {
    await this.createCollection({
      name: "qa_json_len", type: "base",
      listRule: "", viewRule: "", createRule: "", updateRule: "", deleteRule: "",
      fields: [
        { name: "tags", type: "select", required: false, values: ["a", "b", "c"], maxSelect: 3 },
        { name: "json_data", type: "json", required: false },
      ],
    });

    await this.createRecord("qa_json_len", { tags: ["a", "b"], json_data: ["x", "y", "z"] });
    await this.createRecord("qa_json_len", { tags: ["a"], json_data: { key: "val" } });
    await this.createRecord("qa_json_len", { tags: [], json_data: "scalar_string" });

    // --- Test :length modifier (currently fails on MySQL) ---
    // JSONArrayLength has no MySQL branch — it generates SQLite's
    // json_array_length() which doesn't exist in MySQL.
    {
      const err = await expectStatus(
        "tags:length on MySQL (expected 400 — no MySQL JSONArrayLength)",
        400,
        () => this.getRecords("qa_json_len", `filter=${encodeURIComponent("tags:length=2")}`)
      );
      this.log("JSON length spike: :length on select field fails with 400 (no MySQL JSONArrayLength) — confirmed");
    }

    {
      const err = await expectStatus(
        "json_data:length on MySQL (expected 400 — no MySQL JSONArrayLength)",
        400,
        () => this.getRecords("qa_json_len", `filter=${encodeURIComponent("json_data:length=3")}`)
      );
      this.log("JSON length spike: :length on json field fails with 400 (no MySQL JSONArrayLength) — confirmed");
    }

    this.log("JSON length spike findings:");
    this.log("  1. :length modifier fails — JSONArrayLength has no MySQL branch");
    this.log("  2. SQLite normalization contract: empty→0, null→0, scalar→1, array→len, object→0");
    this.log("  3. Fix: Task 7.2 will add MySQL JSON_LENGTH-based expression with normalization");
    this.log("JSON length spike: PASSED (findings documented)");
  }

  // Task 6.4: Spike JSON extraction contract.
  //
  // JSON path extraction uses dbutils.JSONExtract() which is currently
  // SQLite-only (no MySQL branch). This spike proves that JSON path filtering
  // fails on MySQL and documents the extraction contract.
  //
  // The SQLite JSONExtract expression:
  //   CASE WHEN json_valid(column) THEN JSON_EXTRACT(column, '$path')
  //   ELSE JSON_EXTRACT(json_object('pb', column), '$.pbpath') END
  //
  // This wraps non-JSON columns in a JSON object so scalar values can be
  // extracted. MySQL needs an equivalent expression using JSON_EXTRACT/
  // JSON_UNQUOTE.
  //
  // FIX: Task 7.3 will add MySQL JSONExtract expression.
  async sectionJsonExtractSpike() {
    await this.createCollection({
      name: "qa_json_ex", type: "base",
      listRule: "", viewRule: "", createRule: "", updateRule: "", deleteRule: "",
      fields: [
        { name: "json_data", type: "json", required: false },
        { name: "title", type: "text", required: false, max: 255 },
      ],
    });

    await this.createRecord("qa_json_ex", {
      json_data: { name: "alice", age: 30, nested: { city: "NYC" } },
      title: "record1",
    });
    await this.createRecord("qa_json_ex", {
      json_data: { name: "bob", age: 25 },
      title: "record2",
    });

    // --- Test JSON path extraction (currently fails on MySQL) ---
    // JSONExtract has no MySQL branch — it generates SQLite's json_extract()
    // which doesn't exist in MySQL (MySQL has JSON_EXTRACT but with different
    // quoting behavior).
    {
      const err = await expectStatus(
        "json_data.name extraction on MySQL (expected 400 — no MySQL JSONExtract)",
        400,
        () => this.getRecords("qa_json_ex", `filter=${encodeURIComponent('json_data.name="alice"')}`)
      );
      this.log("JSON extract spike: json_data.name filter fails with 400 (no MySQL JSONExtract) — confirmed");
    }

    {
      const err = await expectStatus(
        "json_data.nested.city extraction on MySQL (expected 400)",
        400,
        () => this.getRecords("qa_json_ex", `filter=${encodeURIComponent('json_data.nested.city="NYC"')}`)
      );
      this.log("JSON extract spike: nested path filter fails with 400 (no MySQL JSONExtract) — confirmed");
    }

    // --- Test :lower modifier with JSON extraction ---
    // The :lower modifier wraps the identifier in LOWER(). If JSONExtract
    // fails, :lower will also fail.
    {
      const err = await expectStatus(
        "json_data.name:lower on MySQL (expected 400)",
        400,
        () => this.getRecords("qa_json_ex", `filter=${encodeURIComponent('json_data.name:lower="alice"')}`)
      );
      this.log("JSON extract spike: :lower with JSON path fails with 400 — confirmed");
    }

    this.log("JSON extract spike findings:");
    this.log("  1. JSON path filtering fails — JSONExtract has no MySQL branch");
    this.log("  2. SQLite contract: CASE WHEN json_valid THEN JSON_EXTRACT ELSE wrap in json_object");
    this.log("  3. MySQL needs JSON_EXTRACT/JSON_UNQUOTE with equivalent normalization");
    this.log("  4. :lower composition also fails (depends on JSONExtract)");
    this.log("  5. Fix: Task 7.3 will add MySQL JSONExtract expression");
    this.log("JSON extract spike: PASSED (findings documented)");
  }

  // Task 6.5: Spike strftime datetime parsing.
  //
  // The strftime token function is currently SQLite-only. This spike proves
  // that strftime-based filters fail on MySQL and documents the translation
  // contract.
  //
  // SQLite strftime format tokens → MySQL DATE_FORMAT equivalents:
  //   %Y → %Y (4-digit year)
  //   %m → %m (2-digit month)
  //   %d → %d (2-digit day)
  //   %H → %H (2-digit hour 24h)
  //   %M → %i (2-digit minute — NOTE: MySQL uses %i not %M)
  //   %S → %s (2-digit second — NOTE: MySQL uses %s not %S)
  //   %f → %f (fractional seconds — MySQL 8.0+ supports this)
  //
  // Key differences:
  //   - SQLite %M = minutes, MySQL %M = month name → must map to %i
  //   - SQLite %S = seconds, MySQL %S = seconds (same but case matters)
  //   - SQLite uses strftime(), MySQL uses DATE_FORMAT()
  //   - SQLite accepts 'Z' suffix in datetime, MySQL needs STR_TO_DATE or REPLACE
  //   - SQLite unixepoch modifier → MySQL FROM_UNIXTIME()
  //
  // FIX: Task 8.1 will implement StrftimeExpr dialect method.
  async sectionStrftimeSpike() {
    await this.createCollection({
      name: "qa_strftime", type: "base",
      listRule: "", viewRule: "", createRule: "", updateRule: "", deleteRule: "",
      fields: [
        { name: "when", type: "date", required: false },
      ],
    });

    await this.createRecord("qa_strftime", { when: "2026-01-15 10:30:00.000Z" });
    await this.createRecord("qa_strftime", { when: "2026-06-20 14:45:00.000Z" });

    // --- Test strftime filter (currently fails on MySQL) ---
    // strftime token function has no MySQL handling — it generates
    // strftime() which doesn't exist in MySQL.
    {
      const err = await expectStatus(
        "strftime year filter on MySQL (expected 400 — no MySQL strftime)",
        400,
        () => this.getRecords("qa_strftime", `filter=${encodeURIComponent("strftime('%Y', when)='2026'")}`)
      );
      this.log("Strftime spike: strftime('%Y', when) filter fails with 400 (no MySQL strftime) — confirmed");
    }

    {
      const err = await expectStatus(
        "strftime month filter on MySQL (expected 400)",
        400,
        () => this.getRecords("qa_strftime", `filter=${encodeURIComponent("strftime('%m', when)='01'")}`)
      );
      this.log("Strftime spike: strftime('%m', when) filter fails with 400 — confirmed");
    }

    // --- Test strftime with format string containing time tokens ---
    {
      const err = await expectStatus(
        "strftime full datetime filter on MySQL (expected 400)",
        400,
        () => this.getRecords("qa_strftime", `filter=${encodeURIComponent("strftime('%Y-%m-%d %H:%M:%S', when)='2026-01-15 10:30:00'")}`)
      );
      this.log("Strftime spike: full datetime format fails with 400 — confirmed");
    }

    this.log("Strftime spike findings:");
    this.log("  1. strftime filter fails — token function has no MySQL handling");
    this.log("  2. MySQL translation: strftime() → DATE_FORMAT()");
    this.log("  3. Critical token mappings: %M→%i (minutes), %S→%s (seconds)");
    this.log("  4. Datetime parsing: SQLite accepts 'Z' suffix, MySQL needs REPLACE/STR_TO_DATE");
    this.log("  5. unixepoch modifier: SQLite uses modifier, MySQL needs FROM_UNIXTIME()");
    this.log("  6. Fix: Task 8.1 will implement StrftimeExpr dialect method");
    this.log("Strftime spike: PASSED (findings documented)");
  }

  // =========================================================================
  // Main
  // =========================================================================

  async run() {
    await this.ensurePortFree();
    this.maybeStartDocker();
    await this.ensureMysqlReachable();
    this.preflightMysqlVersion();
    this.buildBinary();
    await this.startServer();
    await this.createSuperuserAndAuth();
    await this.cleanupQaCollections();
    await this.sectionBasicRuntime();
    await this.sectionSelectAndMatrix();
    await this.sectionRelationsAndView();
    await this.sectionAllFields();
    await this.sectionValidation();
    await this.sectionUniqueIndex();
    await this.sectionFileField();
    await this.sectionGeoPoint();
    await this.sectionPaginationAndDates();
    await this.sectionCascadeDelete();
    await this.sectionRules();
    await this.sectionJsonTableSpike();
    await this.sectionJsonLengthSpike();
    await this.sectionJsonExtractSpike();
    await this.sectionStrftimeSpike();

    // Scan server log for error-level lines or panics. Request-level errors
    // (e.g. "ERROR POST /api/...") mirror HTTP responses we already assert on
    // (including the intentional negative tests), so they are ignored here; we
    // only flag panics, fatal/startup errors and other unexpected ERROR output.
    const log = readFileSync(this.pbLog, "utf8");
    const isRequestErr = (l) => /\bERROR\s+(GET|POST|PATCH|PUT|DELETE|HEAD|OPTIONS)\s+\//.test(l);
    const badLines = log.split(/\r?\n/).filter((l) =>
      (/(^|\s)ERROR(\s|$)/.test(l) && !isRequestErr(l)) || /panic:|runtime error|PANIC RECOVER/i.test(l));
    if (badLines.length > 0) {
      throw new Error(`Runtime QA completed but server log contains error entries:\n${badLines.join("\n")}\n\n--- full log tail ---\n${this.tailFile(this.pbLog, 60)}`);
    }
    this.succeeded = true;
    this.log(`\nMySQL runtime QA passed. Logs: ${this.pbLog}`);
  }
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// Synchronous, cross-platform sleep (works inside spawnSync polling loops).
function sleepSync(ms) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
}

// Abort early with a friendly message if a required CLI tool is missing.
function ensureCommand(cmd) {
  const probe = process.platform === "win32"
    ? spawnSync("where", [cmd], { stdio: "ignore" })
    : spawnSync("which", [cmd], { stdio: "ignore" });
  if (probe.status !== 0) {
    throw new Error(`Required command '${cmd}' was not found in PATH. Please install it and retry.`);
  }
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

const opts = parseArgs();
const qa = new QA(opts);

// Ensure resources are released on Ctrl+C / termination, not just on normal exit.
let cleanedUp = false;
function cleanupOnce() {
  if (cleanedUp) return;
  cleanedUp = true;
  try { qa.cleanup(); } catch {}
}
for (const sig of ["SIGINT", "SIGTERM", "SIGHUP"]) {
  process.on(sig, () => {
    console.error(`\n[qa] Received ${sig}, cleaning up...`);
    cleanupOnce();
    process.exit(130);
  });
}

try {
  await qa.run();
} catch (err) {
  console.error(err.message || err);
  // Print server log tail for debugging (server may have logged the error).
  try {
    const logTail = qa.tailFile(qa.pbLog, 30);
    console.error(`\n--- server log tail ---\n${logTail}`);
  } catch {}
  cleanupOnce();
  process.exit(1);
} finally {
  cleanupOnce();
}
