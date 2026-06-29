# Dialect Refactor Execution Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace scattered MySQL conditionals with a data-DB dialect abstraction while preserving current SQLite/MySQL behavior and keeping aux DB operations SQLite-only.

**Architecture:** Keep exported `core.Dialect` minimal with `Name()` only, then add narrow capability interfaces as each call-site category is migrated. Runtime-sensitive SQL behavior is resolved by explicit MySQL spike tasks before dependent migrations. `tools/search` and `tools/dbutils` continue to avoid importing `core`; they receive primitive dialect behavior through resolver interface assertions.

**Tech Stack:** Go, PocketBase core, `github.com/pocketbase/dbx`, `tools/search`, Node.js runtime QA scripts, MySQL 8.x, SQLite.

---

## Source Of Truth

- Design/spec reference: `docs/dialect-refactor-plan.md`
- This execution plan is the implementation checklist.
- If this plan and the spec conflict, stop and resolve the conflict in this execution plan before editing production code.
- Do not expand exported `core.Dialect` beyond `Name()` unless a task explicitly changes this rule.

## Global Rules

- Do not add `SetDialect` or mutable public dialect injection.
- Do not route aux DB SQL through `app.Dialect()`.
- Do not use `t.Parallel()` in tests that mutate `PB_DATABASE_DRIVER`.
- Do not migrate call sites that depend on runtime-sensitive SQL until the matching spike task has passed.
- Do not edit `tools/dbutils.JSONEach` or `tools/dbutils.JSONExtract` until `rg -n "dbutils\.JSONEach\(|dbutils\.JSONExtract\(" core/` confirms dependent `core/` callers are migrated.
- Commit after each task if implementation is performed by a human or agent in a normal development branch. Use concise messages matching the task title.

## Phase 0: Inventory And Spike Baseline

### Task 0.1: Capture Current Dialect Call-Site Inventory

**Files:**
- Read: `core/`, `tools/`, `migrations/`, `apis/`, `forms/`, `cmd/`, `examples/`, `plugins/`, `pocketbase.go`
- Optional notes: `docs/dialect-refactor-plan.md`

- [ ] **Step 1: Run production inventory grep**

Run:
```sh
rg -n "isMySQLDataDB|IsMySQLDataDB|PB_DATABASE_DRIVER|dbutils\.JSONEach\(|dbutils\.JSONExtract\(|JSONArrayLength\(|_rowid_|strftime\(" core tools migrations apis forms cmd examples plugins pocketbase.go
```

Expected: output includes the known current call sites in `core/db_dialect.go`, field files, `record_field_resolver*`, `collection_record_table_sync.go`, migrations, `tools/search`, `tools/dbutils`, and CLI help text.

- [ ] **Step 2: Run test env mutation grep**

Run:
```sh
rg -n "t\.Setenv\(\"PB_DATABASE_DRIVER\"|os\.Setenv\(\"PB_DATABASE_DRIVER\"|t\.Parallel\(\)" -- '*_test.go'
```

Expected: known current env behavior tests include `core/field_select_test.go` and `core/field_relation_test.go`. Any env-dialect test using `t.Parallel()` must be treated as a blocker before dialect caching work.

- [ ] **Step 3: Commit inventory notes only if files changed**

If no files were changed, do not commit.

If notes were added, run:
```sh
git add docs/dialect-refactor-plan.md
git commit -m "docs: capture dialect refactor inventory"
```

Expected: commit succeeds only if documentation changed.

### Task 0.2: Prove Env-Forced SQLite-Backed Bootstrap

**Files:**
- Create or modify: `core/db_dialect_test.go`

- [ ] **Step 1: Add the current-state spike test**

Add this test before `core.App.Dialect()` exists:
```go
package core_test

import (
    "testing"

    "github.com/pocketbase/dbx"
    "github.com/pocketbase/pocketbase/core"
    "github.com/pocketbase/pocketbase/tests"
    "github.com/stretchr/testify/require"
)

func TestEnvForcedMySQLBootstrapWithSQLiteTestApp(t *testing.T) {
    t.Setenv("PB_DATABASE_DRIVER", "mysql")

    app, err := tests.NewTestAppWithConfig(core.BaseAppConfig{
        DBConnect: func(dbPath string) (*dbx.DB, error) {
            pragmas := "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
            return dbx.Open("sqlite", dbPath+pragmas)
        },
    })
    require.NoError(t, err)
    defer app.Cleanup()

    require.True(t, core.IsMySQLDataDB(app))
}
```

- [ ] **Step 2: Run the focused test**

Run:
```sh
go test ./core -run TestEnvForcedMySQLBootstrapWithSQLiteTestApp -count=1
```

Expected: PASS. If it fails, stop and do not implement cached dialect semantics until the test strategy is redesigned.

- [ ] **Step 3: Commit the spike test**

Run:
```sh
git add core/db_dialect_test.go
git commit -m "test: prove env-forced dialect bootstrap"
```

Expected: commit succeeds.

## Phase 1: Minimal Dialect Wiring

### Task 1.1: Add Minimal Dialect API And Cached Data Dialect

**Files:**
- Modify: `core/db_dialect.go`
- Modify: `core/base.go`
- Modify: `core/app.go`
- Modify: `core/db_tx.go` only if clone behavior needs explicit adjustment
- Test: `core/db_dialect_test.go`, `core/base_test.go`, `core/db_tx_test.go`

- [ ] **Step 1: Write failing dialect factory and app wiring tests**

Add focused tests covering:
- `DialectForDriver("").Name() == core.DialectSQLiteName`
- `DialectForDriver("sqlite").Name() == core.DialectSQLiteName`
- `DialectForDriver("mysql").Name() == core.DialectMySQLName`
- env var `PB_DATABASE_DRIVER=mysql` overrides driver name
- unbootstrapped `app.Dialect()` is nil-safe and SQLite by default
- env-forced SQLite-backed bootstrap returns MySQL data dialect
- data transaction inherits parent data dialect
- aux transaction preserves parent data dialect
- `UnsafeWithoutHooks().Dialect()` preserves parent data dialect
- `ResetBootstrapState()` clears cached dialect and fallback observes env again

- [ ] **Step 2: Run tests and verify failure**

Run:
```sh
go test ./core -run 'Test.*Dialect|Test.*Bootstrap|Test.*Transaction|Test.*UnsafeWithoutHooks|Test.*ResetBootstrapState' -count=1
```

Expected: FAIL because `Dialect`, `DialectForDriver`, or `App.Dialect()` does not exist yet.

- [ ] **Step 3: Implement minimal dialect types**

In `core/db_dialect.go`, implement only:
```go
const (
    DialectSQLiteName = "sqlite"
    DialectMySQLName  = "mysql"
)

type Dialect interface {
    Name() string
}

type SQLiteDialect struct{}

func (SQLiteDialect) Name() string {
    return DialectSQLiteName
}

type MySQLDialect struct{}

func (MySQLDialect) Name() string {
    return DialectMySQLName
}

func DialectForDriver(driverName string) Dialect {
    if strings.EqualFold(os.Getenv(envDatabaseDriver), DialectMySQLName) || strings.EqualFold(driverName, DialectMySQLName) {
        return MySQLDialect{}
    }

    return SQLiteDialect{}
}
```

Keep `isMySQLDataDB` and `IsMySQLDataDB` transitional wrappers behavior-compatible until behavior tests are migrated.

- [ ] **Step 4: Add `Dialect() Dialect` to `core.App` and `BaseApp`**

Add `dialect Dialect` to `BaseApp`.

Set it in `initDataDB()` after the concurrent data handle opens and driver validation passes.

`Dialect()` must return cached `app.dialect` when available, otherwise fallback to `DialectForDriver(driverNameFrom(app.ConcurrentDB()))` with nil-safe guards.

- [ ] **Step 5: Add data handle driver consistency validation and cleanup**

Validate concurrent and nonconcurrent data handles only when both expose `DriverName() string`. If both expose names and names differ, bootstrap must fail with a clear error.

On any error after either data handle opens, close every successfully opened data handle before returning.

- [ ] **Step 6: Add bootstrap partial-resource cleanup**

Ensure `Bootstrap()` cleans up partially opened DB resources if any later step fails after `initDataDB()` succeeds, including `initAuxDB()`, logger init, migrations, collection reload, or settings reload.

- [ ] **Step 7: Run focused tests**

Run:
```sh
go test ./core -run 'Test.*Dialect|Test.*Bootstrap|Test.*Transaction|Test.*UnsafeWithoutHooks|Test.*ResetBootstrapState' -count=1
```

Expected: PASS.

- [ ] **Step 8: Run compile check**

Run:
```sh
go build ./...
```

Expected: PASS.

- [ ] **Step 9: Commit**

Run:
```sh
git add core/db_dialect.go core/base.go core/app.go core/db_tx.go core/db_dialect_test.go core/base_test.go core/db_tx_test.go
git commit -m "feat: add data dialect wiring"
```

Expected: commit succeeds.

## Phase 2: Column Types And Env-Test Migration

### Task 2.1: Add Column Dialect Capability And Migrate Field Column Types

**Files:**
- Modify: `core/db_dialect.go`
- Modify: `core/field_text.go`
- Modify: `core/field_email.go`
- Modify: `core/field_url.go`
- Modify: `core/field_password.go`
- Modify: `core/field_date.go`
- Modify: `core/field_autodate.go`
- Modify: `core/field_file.go`
- Modify: `core/field_relation.go`
- Modify: `core/field_select.go`
- Modify: `core/field_editor.go`
- Modify: `core/field_geo_point.go`
- Modify tests: `core/field_select_test.go`, `core/field_relation_test.go`, any other field tests found by grep

- [ ] **Step 1: Run field inventory**

Run:
```sh
rg -n "jsonArrayColumnType|JSON DEFAULT '\\[\\]'|JSON NOT NULL|ColumnType\(app|isMySQLDataDB" core/field_*.go core/db_dialect.go
```

Expected: current field MySQL branches are visible.

- [ ] **Step 2: Write or update field tests to avoid post-boot env mutation**

Update field tests that currently call `t.Setenv("PB_DATABASE_DRIVER", "mysql")` after `tests.NewTestApp()` so env is set before app bootstrap with a SQLite-backed custom `DBConnect`, or replace them with focused dialect method tests.

- [ ] **Step 3: Add narrow column capability methods**

Add methods to concrete dialect types, not to a broad public `Dialect` interface unless a local capability interface requires them:
```go
type columnDialect interface {
    VarCharColumnType(max int) string
    PrimaryKeyColumnType() string
    EditorColumnType() string
    JSONArrayColumnType() string
    JSONValueColumnType(defaultValue string) string
}
```

SQLite outputs must match existing SQLite strings. MySQL outputs must match existing MySQL branch strings.

- [ ] **Step 4: Migrate field call sites**

Replace field `isMySQLDataDB(app)` column branches with calls through the column dialect capability.

- [ ] **Step 5: Run field tests**

Run:
```sh
go test ./core -run 'Test.*Field.*ColumnType|TestSelectFieldMySQLColumnType|TestRelationFieldMySQLColumnType' -count=1
```

Expected: PASS.

- [ ] **Step 6: Run full checks**

Run:
```sh
go build ./...
go test ./...
```

Expected: PASS.

- [ ] **Step 7: Commit**

Run:
```sh
git add core/db_dialect.go core/field_*.go core/*field*_test.go
git commit -m "refactor: route field column types through dialect"
```

Expected: commit succeeds.

## Phase 3: Introspection

### Task 3.1: Add Introspection Capability And Migrate Table Metadata Queries

**Files:**
- Modify: `core/db_dialect.go`
- Modify: `core/db_table.go`
- Modify: `core/collection_record_table_sync.go`
- Modify: `core/collection_validate.go`
- Test: relevant `core/*table*_test.go`, `core/collection_validate_test.go`, or focused tests added during implementation

- [ ] **Step 1: Add focused tests for introspection SQL selection**

Cover SQLite and MySQL SQL strings for table columns, table info, table indexes, table existence, views, and duplicate index owner lookup.

- [ ] **Step 2: Add narrow introspection capability**

Implement:
```go
type introspectionDialect interface {
    TableColumnsQuery() string
    TableInfoQuery() string
    TableIndexesQuery() string
    HasTableQuery() string
    ViewsQuery() string
    IndexOwnerQuery() string
}
```

- [ ] **Step 3: Migrate call sites**

Migrate:
- `core/db_table.go`
- transaction-sensitive existence checks in `core/collection_record_table_sync.go`
- view lookup in `core/collection_record_table_sync.go`
- duplicate index owner validation in `core/collection_validate.go`

- [ ] **Step 4: Run focused tests**

Run:
```sh
go test ./core -run 'Test.*Table|Test.*Index|Test.*Collection.*Validate' -count=1
```

Expected: PASS.

- [ ] **Step 5: Run full checks and commit**

Run:
```sh
go build ./...
go test ./...
git add core/db_dialect.go core/db_table.go core/collection_record_table_sync.go core/collection_validate.go core/*test.go
git commit -m "refactor: route introspection queries through dialect"
```

Expected: checks pass and commit succeeds.

## Phase 4: Search Primitive Refactors

### Task 4.1: Equality And LIKE Search Primitives

**Files:**
- Create: `tools/search/dialect_primitives.go`
- Modify: `tools/search/filter.go`
- Modify: `core/db_dialect.go`
- Modify: `core/record_field_resolver.go`
- Test: `tools/search/filter_test.go`

- [ ] **Step 1: Add resolver-backed tests**

Add tests for SQLite/default and MySQL primitive resolvers covering:
- `=` null fallback matrix
- `!=` null fallback matrix
- column-operand LIKE using SQLite `||`
- column-operand LIKE using MySQL `CONCAT`
- same-process cache safety by building default then MySQL and MySQL then default

- [ ] **Step 2: Add primitive types in `tools/search/dialect_primitives.go`**

Add:
```go
type EqualityOperatorSet struct {
    EqualOp     string
    NullEqualOp string
    NullConcat  string
    NullExpr    string
}

type EqualityOperators struct {
    Equal    EqualityOperatorSet
    NotEqual EqualityOperatorSet
}
```

- [ ] **Step 3: Implement resolver primitive threading**

In `tools/search/filter.go`, compute `likeEscape`, `EqualityOperators`, and `LikeColumnContainsExpr` from resolver assertions before calling `buildResolversExpr(...)`.

- [ ] **Step 4: Implement `RecordFieldResolver` primitive methods**

Add methods that delegate to dialect capability methods on `r.app.Dialect()`.

- [ ] **Step 5: Run focused tests and commit**

Run:
```sh
go test ./tools/search -run 'Test.*Filter|Test.*Like|Test.*Equal' -count=1
go build ./...
git add tools/search/dialect_primitives.go tools/search/filter.go tools/search/filter_test.go core/db_dialect.go core/record_field_resolver.go
git commit -m "refactor: add dialect search equality primitives"
```

Expected: tests and build pass, commit succeeds.

### Task 4.2: Rowid Sort And Count Override

**Files:**
- Modify: `tools/search/sort.go`
- Modify: `tools/search/provider.go`
- Modify: `core/db_dialect.go`
- Modify: `core/record_field_resolver.go`
- Modify: `apis/record_crud.go`
- Test: `tools/search/sort_test.go`, `tools/search/provider_test.go`, optionally `apis/record_crud_test.go`

- [ ] **Step 1: Add tests for `@rowid` exact output**

Test expected primitive behavior:
- SQLite/default `@rowid` expression uses `[[_rowid_]]`
- MySQL resolver-backed `@rowid` expression uses `[[id]]`
- provider prefixing produces valid first-table-qualified output for both
- env vars are ignored by generic resolvers

- [ ] **Step 2: Add safe count override contract**

Use `(column string, ok bool)` semantics for count override capability. SQLite non-view returns `"_rowid_", true`; SQLite view returns `"", false`; MySQL returns `"", false`.

- [ ] **Step 3: Migrate sort and count call sites**

Migrate `tools/search/sort.go` to resolver primitive assertion and `apis/record_crud.go` to call `CountCol` only when override `ok` is true.

- [ ] **Step 4: Run focused tests and commit**

Run:
```sh
go test ./tools/search -run 'Test.*Sort|Test.*Provider|Test.*Count' -count=1
go test ./apis -run 'Test.*Record.*List|Test.*Count' -count=1
go build ./...
git add tools/search/sort.go tools/search/provider.go tools/search/*test.go core/db_dialect.go core/record_field_resolver.go apis/record_crud.go apis/*test.go
git commit -m "refactor: dialectize rowid sort and count override"
```

Expected: tests and build pass, commit succeeds.

## Phase 5: Raw Expression Plumbing

### Task 5.1: Raw Table Joins And Expression-Valued Multi-Match Identifiers

**Files:**
- Modify: `tools/search/multi_match_subquery.go`
- Modify: `core/record_field_resolver.go`
- Test: `tools/search/multi_match_subquery_test.go`, `core/record_field_resolver_test.go`

- [ ] **Step 1: Add failing raw join tests**

Cover:
- regular join table names are still quoted
- `Join{RawTableExpr: true}` renders `LEFT JOIN json_each(...) {{alias}}` without quoting table expression
- `Join{RawTableExpr: true}` renders `LEFT JOIN JSON_TABLE(...) {{alias}}` without quoting table expression
- expression-valued `ValueIdentifier` renders raw
- regular `ValueIdentifier` remains quoted
- main-query raw joins are not quoted
- list-rule cloned resolver raw joins are not quoted

- [ ] **Step 2: Implement raw table expression fields**

Add fields such as:
```go
type Join struct {
    TableName    string
    TableAlias   string
    On           dbx.Expression
    RawTableExpr bool
}
```

Add an explicit raw/expression path for `MultiMatchSubquery.ValueIdentifier`, such as `ValueIdentifierRaw bool`.

- [ ] **Step 3: Implement raw join registration in `RecordFieldResolver`**

Add `registerJoinExpr(tableExpr, tableAlias string, on dbx.Expression)` or equivalent. When raw, skip collection/list-rule lookup. Preserve legacy behavior where regular non-collection expression strings can still be registered when hidden-field checks do not apply.

- [ ] **Step 4: Run focused tests and commit**

Run:
```sh
go test ./tools/search -run Test.*MultiMatch -count=1
go test ./core -run 'Test.*RecordFieldResolver.*Join|Test.*Raw.*Join' -count=1
go build ./...
git add tools/search/multi_match_subquery.go tools/search/multi_match_subquery_test.go core/record_field_resolver.go core/record_field_resolver_test.go
git commit -m "refactor: support raw dialect join expressions"
```

Expected: tests and build pass, commit succeeds.

## Phase 6: Runtime SQL Spikes

### Task 6.1: Add MySQL Engine Preflight To Runtime QA

**Files:**
- Modify: `scripts/mysql-runtime-qa.mjs`

- [ ] **Step 1: Add engine/version preflight**

Before JSON_TABLE-dependent checks, query MySQL version using the existing QA connection. Reject MariaDB and unsupported MySQL versions with a clear message.

- [ ] **Step 2: Run QA preflight against configured MySQL**

Run:
```sh
node scripts/mysql-runtime-qa.mjs --skip-docker --mysql-port 3306 --mysql-password "" --mysql-database pocketbase_qa
```

Expected: PASS against supported MySQL 8.x, or clear unsupported-engine failure against MariaDB/old MySQL.

- [ ] **Step 3: Commit**

Run:
```sh
git add scripts/mysql-runtime-qa.mjs
git commit -m "test: add MySQL runtime engine preflight"
```

Expected: commit succeeds.

### Task 6.2: Spike JSON_TABLE Scalar And Request-Body Binding

**Files:**
- Modify: `scripts/mysql-runtime-qa.mjs` or create focused temporary QA cases committed as permanent runtime coverage

- [ ] **Step 1: Add runtime cases**

Cover `JSON_TABLE` with:
- Go-equivalent JSON `[]byte` payload binding used by request-body `:each`
- string array values longer than 255 characters
- relation-like 15-character IDs
- select values
- file-like strings
- numeric scalars `2` and `10`
- boolean scalar
- JSON null

- [ ] **Step 2: Decide `JSON_TABLE` value column type**

Record the chosen column definition in code comments or QA assertion names. Do not use `VARCHAR(255)` unless runtime coverage proves truncation is acceptable or explicitly documented.

- [ ] **Step 3: Run runtime QA**

Run:
```sh
node scripts/mysql-runtime-qa.mjs --skip-docker --mysql-port 3306 --mysql-password "" --mysql-database pocketbase_qa
```

Expected: PASS and chosen `JSON_TABLE` value contract is documented by tests.

- [ ] **Step 4: Commit**

Run:
```sh
git add scripts/mysql-runtime-qa.mjs
git commit -m "test: prove MySQL JSON_TABLE scalar behavior"
```

Expected: commit succeeds.

### Task 6.3: Spike JSON Array Length Normalization

**Files:**
- Modify: `scripts/mysql-runtime-qa.mjs`

- [ ] **Step 1: Add runtime cases**

Cover empty string, SQL NULL, scalar non-JSON string, scalar non-JSON number, JSON array, JSON object, invalid JSON, JSON string scalar, JSON number scalar, JSON boolean scalar, and JSON null.

- [ ] **Step 2: Run runtime QA**

Run:
```sh
node scripts/mysql-runtime-qa.mjs --skip-docker --mysql-port 3306 --mysql-password "" --mysql-database pocketbase_qa
```

Expected: PASS with MySQL expression preserving the documented SQLite normalization contract or documenting intentional differences.

- [ ] **Step 3: Commit**

Run:
```sh
git add scripts/mysql-runtime-qa.mjs
git commit -m "test: prove MySQL JSON length normalization"
```

Expected: commit succeeds.

### Task 6.4: Spike JSON Extraction Contract

**Files:**
- Modify: `scripts/mysql-runtime-qa.mjs`
- Modify execution plan if the proposed contract must become context-aware

- [ ] **Step 1: Add runtime cases**

Cover root path, object path, scalar non-JSON fallback, string equality, numeric ordering, null fallback, object/array text behavior, SQL NULL, JSON null, and `LOWER(...)` composition for `:lower`.

- [ ] **Step 2: Decide contract shape**

Choose exactly one:
- comparison-agnostic `JSONExtractExpr(column, path)` is safe
- operator/context-aware `JSONExtractExpr(column, path, ctx)` is required
- JSON extraction migration is deferred out of this refactor

- [ ] **Step 3: Stop if contract changes**

If the contract is not comparison-agnostic, update this execution plan before implementing JSON extraction migration. Do not migrate call sites first.

- [ ] **Step 4: Run runtime QA and commit**

Run:
```sh
node scripts/mysql-runtime-qa.mjs --skip-docker --mysql-port 3306 --mysql-password "" --mysql-database pocketbase_qa
git add scripts/mysql-runtime-qa.mjs docs/superpowers/plans/2026-06-29-dialect-refactor-execution.md
git commit -m "test: decide MySQL JSON extraction contract"
```

Expected: QA passes and the contract decision is captured.

### Task 6.5: Spike Strftime Datetime Parsing

**Files:**
- Modify: `scripts/mysql-runtime-qa.mjs`

- [ ] **Step 1: Record SQLite baseline**

Capture current SQLite output for representative calls including:
- `strftime('%Y', '2026-01-02 03:04:05.123Z')`
- `strftime('%Y-%m-%d %H:%M:%fZ', '2026-01-02 03:04:05.123Z')`
- value without trailing `Z`
- `unixepoch` numeric input

- [ ] **Step 2: Add MySQL runtime cases**

Test `DATE_FORMAT`, fractional seconds, trailing `Z`, values without `Z`, UTC current time semantics, `unixepoch`, unsupported modifiers, and unsupported format failures.

- [ ] **Step 3: Decide MySQL translation contract**

Document whether MySQL uses plain `DATE_FORMAT`, `STR_TO_DATE`, `REPLACE(..., 'Z', '')`, `UTC_TIMESTAMP(3)`, and how `%f` precision is normalized.

- [ ] **Step 4: Run runtime QA and commit**

Run:
```sh
node scripts/mysql-runtime-qa.mjs --skip-docker --mysql-port 3306 --mysql-password "" --mysql-database pocketbase_qa
git add scripts/mysql-runtime-qa.mjs
git commit -m "test: prove MySQL strftime translation"
```

Expected: QA passes and translation contract is documented by tests.

## Phase 7: JSON Query Generation Migration

### Task 7.1: Migrate JSONEach Relation And Request-Body Joins

**Depends on:** Task 5.1 and Task 6.2

**Files:**
- Modify: `core/db_dialect.go`
- Modify: `core/record_field_resolver_runner.go`
- Modify: `core/view.go`
- Modify: `core/record_model.go`
- Modify: `core/record_query_expand.go`
- Test: `core/record_field_resolver_test.go`, `core/record_model_test.go`, `core/view_test.go`

- [ ] **Step 1: Add generated-SQL tests**

Cover relation-many joins, back-relation-many joins, request-body `@request.body.<field>:each`, view file joins, record model joins, and expand indirect relation joins for SQLite and MySQL dialect-backed output.

- [ ] **Step 2: Add `JSONEachColumnExpr` and `JSONEachParamExpr` capability**

Use the runtime-proven `JSON_TABLE` column type from Task 6.2.

- [ ] **Step 3: Migrate call sites using raw join paths**

Every table-valued expression returned by dialect methods must use raw join rendering.

- [ ] **Step 4: Run tests and QA**

Run:
```sh
go test ./core -run 'Test.*RecordFieldResolver|Test.*RecordModel|Test.*View|Test.*Expand' -count=1
node scripts/mysql-runtime-qa.mjs --skip-docker --mysql-port 3306 --mysql-password "" --mysql-database pocketbase_qa
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:
```sh
git add core/db_dialect.go core/record_field_resolver_runner.go core/view.go core/record_model.go core/record_query_expand.go core/*test.go
git commit -m "refactor: route JSON each joins through dialect"
```

Expected: commit succeeds.

### Task 7.2: Migrate JSON Array Length

**Depends on:** Task 6.3

**Files:**
- Modify: `core/db_dialect.go`
- Modify: `core/record_field_resolver_runner.go`
- Test: `core/record_field_resolver_test.go`

- [ ] **Step 1: Add generated-SQL and runtime-backed tests**

Cover `:length` for relation/select/file multi-value fields and request-data paths where applicable.

- [ ] **Step 2: Add `JSONArrayLengthExpr(column)` capability**

Use the runtime-proven expression from Task 6.3.

- [ ] **Step 3: Migrate `dbutils.JSONArrayLength(...)` core call sites**

Replace only data DB query-generation call sites with dialect calls.

- [ ] **Step 4: Run checks and commit**

Run:
```sh
go test ./core -run 'Test.*RecordFieldResolver.*Length|Test.*RecordFieldResolver' -count=1
go build ./...
git add core/db_dialect.go core/record_field_resolver_runner.go core/record_field_resolver_test.go
git commit -m "refactor: route JSON array length through dialect"
```

Expected: tests and build pass, commit succeeds.

### Task 7.3: Migrate JSON Extraction

**Depends on:** Task 6.4

**Files:**
- Modify: `core/db_dialect.go`
- Modify: `core/record_field_resolver_runner.go`
- Test: `core/record_field_resolver_test.go`

- [ ] **Step 1: Confirm contract decision**

Read Task 6.4 result in this plan. If JSON extraction was deferred, skip this task and mark it cancelled. If context-aware contract was chosen, implement that exact contract.

- [ ] **Step 2: Add generated-SQL tests**

Cover root path, object path, scalar fallback, string equality, numeric comparison, null comparison, and `:lower` modifier composition.

- [ ] **Step 3: Add dialect capability and migrate call sites**

Replace data DB `dbutils.JSONExtract(...)` usages in `core/record_field_resolver_runner.go` with dialect calls.

- [ ] **Step 4: Run checks and commit**

Run:
```sh
go test ./core -run 'Test.*RecordFieldResolver.*JSON|Test.*RecordFieldResolver' -count=1
node scripts/mysql-runtime-qa.mjs --skip-docker --mysql-port 3306 --mysql-password "" --mysql-database pocketbase_qa
git add core/db_dialect.go core/record_field_resolver_runner.go core/record_field_resolver_test.go
git commit -m "refactor: route JSON extraction through dialect"
```

Expected: tests and QA pass, commit succeeds.

### Task 7.4: Clean `tools/dbutils` JSON Env Detection

**Depends on:** Tasks 7.1 and 7.3, or explicit cancellation of 7.3

**Files:**
- Modify: `tools/dbutils/json.go`
- Modify: `tools/dbutils/json_test.go`

- [ ] **Step 1: Confirm core callers are migrated**

Run:
```sh
rg -n "dbutils\.JSONEach\(|dbutils\.JSONExtract\(" core/
```

Expected: zero results, unless every remaining occurrence is explicitly documented as SQLite-only and safe.

- [ ] **Step 2: Remove env detection from `JSONEach`**

Make `JSONEach` SQLite/default-only and remove `os` import from `tools/dbutils/json.go`.

- [ ] **Step 3: Document helper behavior in tests**

Add tests proving `JSONEach` ignores `PB_DATABASE_DRIVER=mysql` and returns SQLite/default expression.

- [ ] **Step 4: Run checks and commit**

Run:
```sh
go test ./tools/dbutils -count=1
go build ./...
git add tools/dbutils/json.go tools/dbutils/json_test.go
git commit -m "refactor: make dbutils JSON helpers SQLite default"
```

Expected: tests and build pass, commit succeeds.

## Phase 8: Strftime Token Function Migration

### Task 8.1: Make Strftime Resolver-Dialect-Aware

**Depends on:** Task 5.1 and Task 6.5

**Files:**
- Modify: `tools/search/token_functions.go`
- Modify: `tools/search/dialect_primitives.go`
- Modify: `core/db_dialect.go`
- Modify: `core/record_field_resolver.go`
- Test: `tools/search/token_functions_test.go`, `core/record_field_resolver_test.go`

- [ ] **Step 1: Add token function tests**

Cover default SQLite behavior, MySQL resolver-backed behavior, multi-match `ValueIdentifier` propagation, clone safety, cache safety, supported formats, unsupported formats, supported `unixepoch`/`utc`, and unsupported modifiers.

- [ ] **Step 2: Change token function callable shape**

Introduce explicit `TokenFunction` type:
```go
type TokenFunction func(
    fieldResolver FieldResolver,
    argTokenResolverFunc func(fexpr.Token) (*ResolverResult, error),
    args ...fexpr.Token,
) (*ResolverResult, error)
```

Update `resolveToken()` to pass the resolver.

- [ ] **Step 3: Add `TokenFunctionArg` primitive type**

Add the type in `tools/search/dialect_primitives.go` with token type, literal, and resolved result fields.

- [ ] **Step 4: Implement default clone-safe SQLite fallback and MySQL delegation**

The default fallback must clone multi-match state before changing `ValueIdentifier`. `RecordFieldResolver` delegates to dialect capability using the runtime-proven MySQL expression from Task 6.5.

- [ ] **Step 5: Run tests and commit**

Run:
```sh
go test ./tools/search -run 'Test.*TokenFunction|Test.*Strftime' -count=1
go test ./core -run 'Test.*RecordFieldResolver.*Strftime|Test.*RecordFieldResolver' -count=1
go build ./...
git add tools/search/token_functions.go tools/search/dialect_primitives.go tools/search/token_functions_test.go core/db_dialect.go core/record_field_resolver.go core/record_field_resolver_test.go
git commit -m "refactor: make strftime filters dialect-aware"
```

Expected: tests and build pass, commit succeeds.

## Phase 9: Collection Queries, Views, Schema Sync, Maintenance

### Task 9.1: Migrate Collection Query And View Dialect Behavior

**Files:**
- Modify: `core/db_dialect.go`
- Modify: `core/collection_query.go`
- Modify: `core/view.go`
- Test: `core/view_test.go`, relevant collection query tests

- [ ] **Step 1: Add tests for default sort, ID cast, and subquery alias behavior**

Cover SQLite and MySQL expected SQL.

- [ ] **Step 2: Add narrow query/view capability methods**

Implement default sort, ID cast type, and subquery alias requirement.

- [ ] **Step 3: Migrate call sites**

Migrate `core/collection_query.go` and `core/view.go` branches.

- [ ] **Step 4: Run checks and commit**

Run:
```sh
go test ./core -run 'Test.*View|Test.*Collection.*Query' -count=1
go build ./...
git add core/db_dialect.go core/collection_query.go core/view.go core/*test.go
git commit -m "refactor: route collection query SQL through dialect"
```

Expected: tests and build pass, commit succeeds.

### Task 9.2: Migrate Schema Sync And Maintenance

**Files:**
- Modify: `core/db_dialect.go`
- Modify: `core/collection_record_table_sync.go`
- Modify: `core/base.go`
- Modify: `core/base_backup.go`
- Test: `core/collection_record_table_sync_test.go`, relevant base/backup tests

- [ ] **Step 1: Add tests for schema sync SQL choices and maintenance routing**

Cover add column, rename column, single-to-multi conversion, multi-to-single conversion, drop index, partial index behavior, post-sync optimize, periodic maintenance main DB only, backup checkpoint main DB through dialect, and aux checkpoint SQLite-only.

- [ ] **Step 2: Add schema sync and maintenance capability methods**

Implement narrow capabilities for schema sync and three separate maintenance methods: post-schema-sync optimize, periodic maintenance, and checkpoint.

- [ ] **Step 3: Migrate call sites**

Migrate `core/collection_record_table_sync.go`, `core/base.go`, and `core/base_backup.go`.

- [ ] **Step 4: Run checks and commit**

Run:
```sh
go test ./core -run 'Test.*RecordTable|Test.*Schema|Test.*Backup|Test.*Maintenance' -count=1
go build ./...
git add core/db_dialect.go core/collection_record_table_sync.go core/base.go core/base_backup.go core/*test.go
git commit -m "refactor: route schema sync and maintenance through dialect"
```

Expected: tests and build pass, commit succeeds.

## Phase 10: Migrations

### Task 10.1: Migrate System Migration Dialect Behavior

**Files:**
- Modify: `core/db_dialect.go`
- Modify: `migrations/1640988000_init.go`
- Modify: `migrations/1717233556_v0.23_migrate.go`
- Modify: `migrations/1717233557_v0.23_migrate2.go`
- Modify: `migrations/1717233558_v0.23_migrate3.go`
- Modify: `migrations/1717233559_v0.23_migrate4.go`
- Modify: `migrations/1778828400_normalize_indexes.go`
- Modify: `core/migrations_runner.go`
- Test: migration runner tests or focused bootstrap/migration tests

- [ ] **Step 1: Add migration tests**

Cover MySQL migration applied column type, legacy SQLite migration skips for non-SQLite dialects, nested aux/data transaction preserving data dialect for migration metadata, and fresh empty DB bootstrap creating system collections.

- [ ] **Step 2: Add migration capability methods**

Implement migration applied column type and system table DDL methods as narrow capabilities.

- [ ] **Step 3: Migrate system migration call sites**

Remove duplicate local `isMySQLDataDB` from `migrations/1640988000_init.go`. Replace direct env read in `migrations/1778828400_normalize_indexes.go`.

- [ ] **Step 4: Run migration grep**

Run:
```sh
rg -n "randomblob|strftime|json_each|PRAGMA|_rowid_" migrations/1640988000_init.go migrations/1640988000_aux_init.go
```

Expected: remaining SQLite-only SQL is classified as SQLite branch, aux-only migration, dialect implementation, or explicitly safe.

- [ ] **Step 5: Run checks and commit**

Run:
```sh
go test ./core ./migrations -count=1
go build ./...
git add core/db_dialect.go core/migrations_runner.go migrations/*.go core/*test.go migrations/*test.go
git commit -m "refactor: route migrations through dialect"
```

Expected: tests and build pass, commit succeeds.

## Phase 11: Cleanup, Docs, Patch Stack

### Task 11.1: Remove Transitional MySQL Helpers And Env Reads

**Files:**
- Modify: `core/db_dialect.go`
- Modify any remaining production files found by grep
- Modify tests that still use env mutation for behavior instead of focused dialect/connect tests

- [ ] **Step 1: Run cleanup greps**

Run:
```sh
rg -n "isMySQLDataDB|IsMySQLDataDB" core tools migrations apis forms cmd examples plugins pocketbase.go
rg -n "os\.Getenv\(.*PB_DATABASE_DRIVER|PB_DATABASE_DRIVER" core tools migrations apis forms cmd plugins pocketbase.go
rg -n "dbutils\.JSONEach\(|dbutils\.JSONExtract\(" core/
rg -n "json_each\(|json_array_length\(|JSON_EXTRACT\(|JSON_LENGTH\(|JSON_UNQUOTE\(|JSON_CONTAINS\(|JSON_TABLE\(|_rowid_|PRAGMA|strftime\(" core apis tools/search
```

Expected allowed production references only:
- env constants/connect/factory in `core/db_dialect.go` and `core/db_connect.go`
- CLI help text in `pocketbase.go` and `cmd/serve.go`
- aux-only log/checkpoint paths
- SQLite/default helper implementations
- dialect implementations
- tests
- `core/field.go` reserved `_rowid_` metadata

- [ ] **Step 2: Remove transitional helpers**

Remove `isMySQLDataDB`, `IsMySQLDataDB`, and `jsonArrayColumnType` after all callers are migrated.

- [ ] **Step 3: Remove unused imports**

Remove env-related `os` imports from `tools/search/filter.go`, `tools/search/sort.go`, `tools/dbutils/json.go`, and migrations where no longer needed.

- [ ] **Step 4: Run checks and commit**

Run:
```sh
go build ./...
go test ./...
git add core tools migrations apis forms cmd examples plugins pocketbase.go
git commit -m "refactor: remove transitional MySQL helpers"
```

Expected: tests and build pass, commit succeeds.

### Task 11.2: Update Public Docs And Release Notes

**Files:**
- Modify: `README.md` or the repo's chosen release notes location
- Modify: `docs/mysql-gap-analysis.md`
- Modify: `docs/mysql-upstream-workflow.md`
- Modify: `AGENTS.md` only if workflow guidance changed
- Modify: `ui/src/apiPreview/docsList.js` and `ui/dist/` if `@rowid` public docs changed

- [ ] **Step 1: Document public API and behavior breaks**

Document:
- `core.App` now requires `Dialect() core.Dialect`
- external fake/manual App implementations must add `Dialect()` or embed `core.App`/`*core.BaseApp`
- `tools/search.TokenFunctions` callable signature changed
- `tools/dbutils.JSONEach`, `JSONExtract`, and `JSONArrayLength` are SQLite/default helpers and do not infer data DB dialect
- MySQL JSON_TABLE runtime support requires Oracle MySQL 8.x; MariaDB is unsupported unless separately implemented and tested
- `@rowid` MySQL behavior if changed

- [ ] **Step 2: Run docs/source checks**

Run:
```sh
rg -n "@rowid|PB_DATABASE_DRIVER|JSON_TABLE|TokenFunctions|JSONEach|Dialect\(\)" README.md docs AGENTS.md ui/src/apiPreview/docsList.js
```

Expected: docs no longer contradict implemented behavior.

- [ ] **Step 3: Rebuild UI if API preview docs changed**

If `ui/src/apiPreview/docsList.js` changed, run:
```sh
npm --prefix ui install
npm --prefix ui run build
```

Expected: UI build succeeds and `ui/dist/` updates if committed in this repo.

- [ ] **Step 4: Commit docs**

Run:
```sh
git add README.md docs AGENTS.md ui/src/apiPreview/docsList.js ui/dist
git commit -m "docs: document dialect refactor migration notes"
```

Expected: commit succeeds if docs changed.

### Task 11.3: Final Verification And Patch Stack Export

**Files:**
- Modify: `patches/mysql-poc/` generated patch stack

- [ ] **Step 1: Run full verification**

Run:
```sh
go build ./...
go test ./...
node scripts/mysql-runtime-qa.mjs --skip-docker --mysql-port 3306 --mysql-password "" --mysql-database pocketbase_qa
```

Expected: all pass.

- [ ] **Step 2: Run final cleanup greps**

Run the cleanup greps from Task 11.1 again.

Expected: only allowed references remain.

- [ ] **Step 3: Export patch stack**

Confirm upstream baseline from `AGENTS.md` or current branch metadata, then run:
```sh
node scripts/export-mysql-patches.mjs v0.39.4
```

Expected: `patches/mysql-poc/` refreshes against the correct upstream baseline.

- [ ] **Step 4: Review patch diff**

Run:
```sh
git diff -- patches/mysql-poc/
```

Expected: patch stack reflects only the completed dialect refactor changes.

- [ ] **Step 5: Commit patch stack**

Run:
```sh
git add patches/mysql-poc/
git commit -m "chore: refresh MySQL dialect patch stack"
```

Expected: commit succeeds.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-06-29-dialect-refactor-execution.md`.

Recommended execution mode: Subagent-Driven. Dispatch one fresh subagent per task or small phase, review between tasks, and do not start a dependent task until its required spike has passed.
