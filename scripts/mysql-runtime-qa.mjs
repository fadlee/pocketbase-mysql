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
 */

import { execSync, spawn, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "node:net";
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
  };

  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (arg === "--skip-docker") opts.skipDocker = true;
    else if (arg === "--mysql-host") opts.mysqlHost = args[++i];
    else if (arg === "--mysql-port") opts.mysqlPort = parseInt(args[++i], 10);
    else if (arg === "--mysql-user") opts.mysqlUser = args[++i];
    else if (arg === "--mysql-password") opts.mysqlPassword = args[++i];
    else if (arg === "--mysql-database") opts.mysqlDatabase = args[++i];
    else if (arg === "--mysql-image") opts.mysqlImage = args[++i];
    else if (arg === "--mysql-container") opts.mysqlContainer = args[++i];
    else if (arg === "--http-addr") opts.httpAddr = args[++i];
    else if (arg === "--tmp-dir") opts.tmpDir = args[++i];
  }
  return opts;
}

function envBool(name, def) {
  const v = process.env[name];
  if (v == null) return def;
  return ["1", "true", "yes", "on"].includes(v.toLowerCase());
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

function httpRequest(method, url, { token, payload } = {}) {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    const headers = {};
    let body = null;

    if (token) headers["Authorization"] = `Bearer ${token}`;
    if (payload !== undefined) {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(payload);
    }

    const req = http.request(
      { hostname: u.hostname, port: u.port, path: u.pathname + u.search, method, headers },
      (res) => {
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () => {
          const text = Buffer.concat(chunks).toString("utf8");
          if (res.statusCode >= 400) {
            reject(new Error(`HTTP ${res.statusCode} ${method} ${url}\n${text}`));
            return;
          }
          resolve(text ? JSON.parse(text) : null);
        });
      }
    );
    req.on("error", reject);
    if (body) req.write(body);
    req.end();
  });
}

function GET(url, opts) { return httpRequest("GET", url, opts); }
function POST(url, opts) { return httpRequest("POST", url, opts); }
function PATCH(url, opts) { return httpRequest("PATCH", url, opts); }
function DELETE(url, opts) { return httpRequest("DELETE", url, opts); }

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
    }
    if (this.dockerStarted) {
      spawnSync("docker", ["rm", "-f", this.opts.mysqlContainer], { stdio: "ignore" });
    }
  }

  ensurePortFree() {
    const [, portStr] = this.opts.httpAddr.split(":");
    const port = parseInt(portStr, 10);
    return new Promise((resolve, reject) => {
      const srv = createServer();
      srv.once("error", () => reject(new Error(`HTTP address ${this.opts.httpAddr} is already in use.`)));
      srv.listen(port, () => { srv.close(); resolve(); });
    });
  }

  maybeStartDocker() {
    if (this.opts.skipDocker) {
      this.logStep(`Skipping Docker - using existing MySQL at ${this.opts.mysqlHost}:${this.opts.mysqlPort}`);
      return;
    }
    this.logStep(`Removing any previous MySQL container '${this.opts.mysqlContainer}'...`);
    spawnSync("docker", ["rm", "-f", this.opts.mysqlContainer], { stdio: "ignore" });

    this.logStep(`Starting MySQL container '${this.opts.mysqlContainer}' from ${this.opts.mysqlImage} on port ${this.opts.mysqlPort}...`);
    const r = spawnSync("docker", [
      "run", "--rm", "-d",
      "--name", this.opts.mysqlContainer,
      "-e", `MYSQL_ROOT_PASSWORD=${this.opts.mysqlPassword}`,
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
      const check = spawnSync("docker", [
        "exec", this.opts.mysqlContainer,
        "mysql", "-h127.0.0.1", `-uroot`, `-p${this.opts.mysqlPassword}`,
        this.opts.mysqlDatabase, "-e", "SELECT 1",
      ], { stdio: "pipe" });
      if (check.status === 0) {
        this.logStep(`MySQL ready after ${i}s.`);
        return;
      }
      lastError = check.stderr?.toString().trim() || check.stdout?.toString().trim() || lastError;
      this.logWait("MySQL not ready yet", i, 90);
      spawnSync("sleep", ["1"]);
    }
    throw new Error([
      "MySQL container did not become ready in time.",
      lastError ? `Last readiness error:\n${lastError}` : "",
    ].filter(Boolean).join("\n"));
  }

  buildBinary() {
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
      ["serve", "--dir", this.pbData, "--migrationsDir", this.pbMigrations, "--http", this.opts.httpAddr],
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

  // =========================================================================
  // Main
  // =========================================================================

  async run() {
    this.maybeStartDocker();
    this.buildBinary();
    await this.startServer();
    await this.createSuperuserAndAuth();
    await this.cleanupQaCollections();
    await this.sectionBasicRuntime();
    await this.sectionSelectAndMatrix();
    await this.sectionRelationsAndView();
    await this.sectionAllFields();

    // Check server log for errors
    const log = readFileSync(this.pbLog, "utf8");
    if (log.includes("ERROR")) {
      throw new Error(`Runtime QA completed but server log contains ERROR entries:\n${log}`);
    }
    this.log(`\nMySQL runtime QA passed. Logs: ${this.pbLog}`);
  }
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

const opts = parseArgs();
const qa = new QA(opts);

try {
  await qa.run();
} catch (err) {
  console.error(err.message || err);
  process.exit(1);
} finally {
  qa.cleanup();
}
