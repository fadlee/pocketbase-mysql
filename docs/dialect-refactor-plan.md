# Dialect Abstraction Refactor Plan

## Goal

Replace the ~53 scattered `if isMySQLDataDB(app)` branches with a single `Dialect` interface, eliminating dialect-specific conditionals from business logic. This makes the codebase simpler and prepares for adding PostgreSQL as a third dialect.

## Current State

- `core/db_dialect.go` defines `isMySQLDataDB(app App)` and `IsMySQLDataDB(app App)` — checks env var `PB_DATABASE_DRIVER` **first**, then falls back to runtime `DriverName()`
- ~53 call sites across ~15 files branch on this function (treat 53 as the current count, not an exhaustive guarantee — re-grep in Patch 7)
- 3 duplicate copies of the detection logic (in `migrations/1640988000_init.go`, `core/migrations_runner.go`, `tools/search/filter.go`)
- `tools/search` and `tools/dbutils` read the env var directly (no access to `App`)

### Important constraints discovered during review

- **Detection is currently lazy + env-var-first.** `isMySQLDataDB` is evaluated at each call site (e.g. inside `Field.ColumnType(app)`), and it returns `true` whenever `PB_DATABASE_DRIVER=mysql` *regardless of the actual opened driver*. Several tests depend on this: e.g. `TestSelectFieldMySQLColumnType` / `TestRelationFieldMySQLColumnType` call `tests.NewTestApp()` (which opens **SQLite**), then `t.Setenv("PB_DATABASE_DRIVER", "mysql")` **after** boot, and expect MySQL column types. A naive "compute dialect once at boot from `DriverName()`" approach **breaks these tests**. See the detection design below for the resolution.
- **The aux DB is always SQLite**, even when the data DB is MySQL. `app.Dialect()` describes the **data** DB only and must never be applied to aux-DB operations (`AuxDB`, `AuxConcurrentDB`, `AuxNonconcurrentDB`).
- **`tools/search` and `tools/dbutils` cannot import `core`** (would create an import cycle). This is the real reason those packages use anonymous interface assertions on the resolver and receive dialect info as primitives (strings/bools) rather than a `Dialect` value. This pattern must be preserved.

## Design

### Dialect interface

New file `core/db_dialect.go` (replaces current content):

```go
type Dialect interface {
    Name() string  // "sqlite", "mysql"

    // Column type primitives — called by Field.ColumnType(app)
    VarCharColumnType(max int) string          // VARCHAR(max) DEFAULT '' / TEXT DEFAULT ''
    PrimaryKeyColumnType() string              // VARCHAR(15) PRIMARY KEY / TEXT PRIMARY KEY
    EditorColumnType() string                  // LONGTEXT / TEXT DEFAULT ''
    JSONArrayColumnType() string               // JSON NOT NULL / JSON DEFAULT '[]'
    JSONValueColumnType(defaultValue string) string // JSON NOT NULL / JSON DEFAULT '{...}'

    // Introspection queries (return SQL templates with {:tableName} param)
    TableColumnsQuery() string
    TableInfoQuery() string
    TableIndexesQuery() string
    HasTableQuery() string
    ViewsQuery() string

    // Index DDL
    DropIndexSQL(name, table string) string
    SupportsPartialIndexes() bool

    // Filter / query generation
    LikeEscapeClause() string
    RelationEqualityExpr(left, right string) string
    JSONArrayContainsExpr(jsonCol, idCol string) string
    JSONEachExpr(column string) string
    NotEqualValueOperator() string  // "<>" for MySQL, "IS NOT" for SQLite

    // Schema sync
    AddColumnDirectly() bool  // true for MySQL (ALTER TABLE ADD), false for SQLite (table recreation)
    RenameColumnSQL(table, old, new, colType string) string
    SingleToMultiConversionSQL(table, col, temp string) string
    MultiToSingleConversionSQL(table, col, temp string) string

    // Maintenance — two distinct concerns, kept as separate methods:
    //   PostSchemaSyncOptimize: run after a schema sync (collection_record_table_sync.go).
    //   PeriodicMaintenance:    run by the daily cron against the *data* DB only
    //                           (main-DB wal_checkpoint + optimize for SQLite; no-op for MySQL).
    // Both are no-ops for MySQL. The aux DB checkpoint is NOT covered here — it is always SQLite.
    PostSchemaSyncOptimize(db *dbx.DB, logger Logger)
    PeriodicMaintenance(db *dbx.DB, logger Logger)

    // Sort / count
    DefaultCollectionSort() string  // "rowid ASC" / "id ASC"
    CountColumn(isView bool) string // "_rowid_" / "id" / ""

    // Views
    RequiresSubqueryAlias() bool
    IDCastType() string  // "CHAR(255)" / "TEXT"

    // Migration metadata
    MigrationAppliedColumnType() string  // "BIGINT" / "INTEGER"

    // System table DDL (for init migration)
    CollectionsTableDDL() string
    ParamsTableDDL() string
}
```

Two implementations: `SQLiteDialect{}` and `MySQLDialect{}`.

### App wiring & dialect detection

- `BaseApp` gets a `dialect Dialect` field
- `App` interface gets a `Dialect() Dialect` method
- `App` interface also gets a `SetDialect(Dialect)` (or the field is set internally) so tests can override it explicitly — see below.

**Detection priority must match the current `isMySQLDataDB` semantics: env var first, then driver name.** The factory is:

```go
func DialectForDriver(driverName string) Dialect {
    if strings.EqualFold(os.Getenv(envDatabaseDriver), "mysql") ||
        strings.EqualFold(driverName, "mysql") {
        return MySQLDialect{}
    }
    return SQLiteDialect{}
}
```

- The dialect is set in `initDataDB()` right after `DBConnect` returns, via `app.dialect = DialectForDriver(concurrentDB.DriverName())`. The env-var-first check inside the factory preserves the existing behavior where setting `PB_DATABASE_DRIVER=mysql` forces MySQL semantics even on a SQLite test connection.
- **Do not** switch to "driver name only" — that was the original (rejected) idea and it breaks the env-var-only column-type tests described in Current State.
- Because the affected tests call `t.Setenv(...)` **after** `NewTestApp()` (i.e. after `initDataDB` already ran), Patch 7 must update those tests to either (a) set `PB_DATABASE_DRIVER=mysql` *before* `NewTestApp()`, or (b) inject the dialect directly with `app.SetDialect(core.MySQLDialect{})`. Option (b) is preferred (no env-var coupling, no boot-order trap). This test migration is an explicit deliverable, not incidental.
- `IsMySQLDataDB` kept as thin wrapper `return app.Dialect().Name() == "mysql"` during transition, removed in Patch 7.

### tools/search wiring (no new package needed)

The existing interface assertion pattern in `tools/search` is extended. `tools/search` cannot import `core`, so it only ever receives **primitive** dialect info via assertions on the resolver — never a `Dialect` value or a generic `IsMySQL` boolean (a bare boolean would just recreate the dialect conditional we are removing and would not extend to a third dialect).

- `filter.go`: `buildResolversExpr` already receives `likeEscape string` as a parameter. Add a `notEqualOp string` parameter the same way, computed from the resolver in `resolveExpr` via `interface{ NotEqualValueOperator() string }` assertion (defaulting to the SQLite `IS NOT` when the assertion fails). This also removes the direct `PB_DATABASE_DRIVER` read at `filter.go:347`.
- `sort.go`: `SortField.BuildExpr(fieldResolver)` already receives the resolver. Instead of an `IsMySQL() bool` flag, assert a method that returns the concrete rowid sort expression, e.g. `interface{ RowidSortIdentifier() string }` — SQLite returns `[[_rowid_]]`, MySQL returns the resolved `id` identifier, a future dialect returns its own. `BuildExpr` then just formats `"<identifier> <direction>"`. This removes the direct `PB_DATABASE_DRIVER` read at `sort.go:35` and keeps the dialect as the single source of truth.
- `tools/dbutils/json.go`: `JSONEach` callers in `core` are replaced with `app.Dialect().JSONEachExpr(column)`. `dbutils.JSONEach` kept as SQLite-only fallback.

### Aux DB caveat

`app.Dialect()` is the **data** dialect only; the aux DB is always SQLite. The periodic cron in `base.go` currently runs `PRAGMA wal_checkpoint(TRUNCATE)` against **both** the main and aux DBs unconditionally, plus `PRAGMA optimize` against the main DB (guarded today by `!isMySQLDataDB`):

- The aux-DB `wal_checkpoint` must **always** use SQLite behavior — do not route it through `app.Dialect()`.
- The main-DB `wal_checkpoint` is also SQLite-specific and currently errors (swallowed as a warning) when the data DB is MySQL. Fold both the main-DB `wal_checkpoint` and `PRAGMA optimize` into a single data-dialect maintenance call (see `RunPostSyncOptimize` discussion) so MySQL becomes a clean no-op instead of a logged error.

## Patch Breakdown

### Patch 1: Add Dialect interface + implementations + App.Dialect() wiring

**Files:**
- `core/db_dialect.go` — rewrite: add `Dialect` interface, `SQLiteDialect`, `MySQLDialect`, `DialectForDriver(name string) Dialect` factory
- `core/base.go` — add `dialect Dialect` field to `BaseApp` struct, set in `initDataDB()`, add `Dialect()` method
- `core/app.go` — add `Dialect() Dialect` to `App` interface
- Keep `isMySQLDataDB` and `IsMySQLDataDB` as thin wrappers: `return app.Dialect().Name() == "mysql"`

**Verification:**
- `go build ./...` passes
- `go test ./...` passes
- No behavior change — all call sites still use `isMySQLDataDB` wrappers

### Patch 2: Migrate ColumnType call sites (15 sites)

**Files & changes:**
- `core/field_text.go:163` — `app.Dialect().VarCharColumnType(max)` / `app.Dialect().PrimaryKeyColumnType()`
- `core/field_email.go:113` — `app.Dialect().VarCharColumnType(255)`
- `core/field_url.go:113` — `app.Dialect().VarCharColumnType(255)`
- `core/field_password.go:147` — `app.Dialect().VarCharColumnType(255)`
- `core/field_date.go:110` — `app.Dialect().VarCharColumnType(255)`
- `core/field_autodate.go:109` — `app.Dialect().VarCharColumnType(255)`
- `core/field_file.go:198` — `app.Dialect().VarCharColumnType(255)`
- `core/field_relation.go:161` — `app.Dialect().VarCharColumnType(15)`
- `core/field_select.go:149` — `app.Dialect().VarCharColumnType(max)` (max calculated from values)
- `core/field_editor.go:117` — `app.Dialect().EditorColumnType()`
- `core/field_geo_point.go:110` — `app.Dialect().JSONValueColumnType('{"lon":0,"lat":0}')`
- `core/db_dialect.go:27` `jsonArrayColumnType()` — `app.Dialect().JSONArrayColumnType()`

**Verification:** `go build ./...` + `go test ./...`

### Patch 3: Migrate introspection call sites (7 sites)

**Files & changes:**
- `core/db_table.go:13` `TableColumns` — `app.Dialect().TableColumnsQuery()`
- `core/db_table.go:46` `TableInfo` — `app.Dialect().TableInfoQuery()`
- `core/db_table.go:93` `TableIndexes` — `app.Dialect().TableIndexesQuery()`
- `core/db_table.go:151` `HasTable` — `app.Dialect().HasTableQuery()`
- `core/collection_record_table_sync.go:181` `recordTableExistsForSchemaSync` — `txApp.Dialect().HasTableQuery()`
- `core/collection_record_table_sync.go:236` view lookup — `txApp.Dialect().ViewsQuery()`
- `core/collection_validate.go:565` index name validation — `cv.app.Dialect().TableIndexesQuery()` (cross-table index check)

**Verification:** `go build ./...` + `go test ./...`

### Patch 4: Migrate filter/search/query call sites (10 sites)

**Files & changes:**
- `core/record_field_resolver.go:85` `LikeEscapeClause` — `r.app.Dialect().LikeEscapeClause()`
- `core/record_field_resolver_runner.go:62` `relationValueEquals` — `resolver.app.Dialect().RelationEqualityExpr(left, right)`
- `core/record_field_resolver_runner.go:70` `relationArrayContainsIdentifier` — `resolver.app.Dialect().JSONArrayContainsExpr(jsonCol, idCol)`
- `core/record_field_resolver_runner.go:584,636,699,751` — 4 JOIN sites use `JSONArrayContainsExpr` via dialect
- `core/collection_query.go:42` sort — `app.Dialect().DefaultCollectionSort()`
- `core/collection_query.go:322,341,351` — `app.Dialect().IDCastType()`, `app.Dialect().RequiresSubqueryAlias()`
- `apis/record_crud.go:83` count — `e.App.Dialect().CountColumn(collection.IsView())`
- `core/view.go:56,169` — `app.Dialect().RequiresSubqueryAlias()`
- `tools/search/filter.go:347` `resolveEqualExpr` — add `notEqualOp string` param, computed from resolver via `interface{ NotEqualValueOperator() string }` assertion (removes the direct env-var read)
- `tools/search/sort.go:35` — replace the direct env-var read with `interface{ RowidSortIdentifier() string }` assertion on the resolver (returns `[[_rowid_]]` for SQLite, resolved `id` identifier for MySQL); no `IsMySQL` boolean
- `tools/dbutils/json.go:12` `JSONEach` — callers in `core` use `app.Dialect().JSONEachExpr(column)` instead

**Verification:** `go build ./...` + `go test ./...`

### Patch 5: Migrate schema sync call sites (12 sites)

**Files & changes:**
- `core/collection_record_table_sync.go:96` — `txApp.Dialect().AddColumnDirectly()`
- `core/collection_record_table_sync.go:104` — `txApp.Dialect().RenameColumnSQL(...)`
- `core/collection_record_table_sync.go:168` — `app.Dialect().PostSchemaSyncOptimize(db, logger)`
- `core/collection_record_table_sync.go:262` — `txApp.Dialect().RenameColumnSQL(...)`
- `core/collection_record_table_sync.go:287` — `txApp.Dialect().SingleToMultiConversionSQL(...)`
- `core/collection_record_table_sync.go:339` — `txApp.Dialect().MultiToSingleConversionSQL(...)`
- `core/collection_record_table_sync.go:426` — `txApp.Dialect().DropIndexSQL(...)`
- `core/collection_record_table_sync.go:457` — `txApp.Dialect().SupportsPartialIndexes()` (if false, strip WHERE)
- `core/base.go:1360-1377` periodic cron — route the **main** DB through `app.Dialect().PeriodicMaintenance(mainDB, logger)` (SQLite: `wal_checkpoint(TRUNCATE)` + `PRAGMA optimize`; MySQL: no-op). The **aux** DB `wal_checkpoint(TRUNCATE)` stays as an unconditional SQLite call (aux is always SQLite) — do NOT route it through the dialect.

**Verification:** `go build ./...` + `go test ./...`

### Patch 6: Migrate migration call sites (10 sites)

**Files & changes:**
- `migrations/1640988000_init.go:58,83,145` — `txApp.Dialect().CollectionsTableDDL()`, `ParamsTableDDL()`
- `migrations/1640988000_init.go:161` — remove local `isMySQLDataDB` duplicate
- `migrations/1717233556_v0.23_migrate.go:23` — `txApp.Dialect().Name() != "sqlite"` (skip for non-SQLite)
- `migrations/1717233557_v0.23_migrate2.go:11` — same
- `migrations/1717233558_v0.23_migrate3.go:24` — same
- `migrations/1717233559_v0.23_migrate4.go:12` — same
- `migrations/1778828400_normalize_indexes.go:18` — `txApp.Dialect().Name() != "sqlite"`
- `core/migrations_runner.go:267` `migrationAppliedColumnType` — `app.Dialect().MigrationAppliedColumnType()`

**Verification:** `go build ./...` + `go test ./...` + runtime QA against MySQL

### Patch 7: Remove isMySQLDataDB + cleanup

**Files & changes:**
- `core/db_dialect.go` — remove `isMySQLDataDB` and `IsMySQLDataDB` functions
- Remove `jsonArrayColumnType` helper (now in dialect)
- Grep for any remaining `isMySQLDataDB`, `IsMySQLDataDB`, `PB_DATABASE_DRIVER` references in Go source across **`core/`, `tools/`, `migrations/`, `apis/`, `cmd/`, and `examples/`**. The only allowed remaining references are:
  - the `envDatabaseDriver`/`envDatabaseDSN` consts and the `DialectForDriver` factory in `core/db_dialect.go` and `core/db_connect.go`
  - user-facing CLI help text (whitelist explicitly: `pocketbase.go`, `cmd/serve.go`)
- **Migrate the env-var-only tests** (the deliverable flagged in App wiring): `core/field_select_test.go`, `core/field_relation_test.go`, and any other test that does `t.Setenv("PB_DATABASE_DRIVER", "mysql")` after `NewTestApp()`. Switch them to inject the dialect directly via `app.SetDialect(core.MySQLDialect{})` (preferred) so they no longer depend on lazy env-var detection.

**Verification:**
- `go build ./...` passes
- `go test ./...` passes
- `grep -rn "isMySQLDataDB\|IsMySQLDataDB" core/ tools/ migrations/ apis/ cmd/ examples/` returns zero results
- `grep -rn "PB_DATABASE_DRIVER" core/ tools/ migrations/ apis/` returns only `core/db_dialect.go` + `core/db_connect.go` (the detection factory)
- Runtime QA: `node scripts/mysql-runtime-qa.mjs --skip-docker --mysql-port 3306 --mysql-password "" --mysql-database pocketbase_qa`
- Export patch stack

## Key Design Decisions

1. **Detection keeps env-var-first priority, evaluated at boot** — `DialectForDriver` checks `PB_DATABASE_DRIVER=mysql` first, then the opened driver name. This is set once in `initDataDB()` and exposed via `app.Dialect()`. We deliberately did **not** switch to "driver name only" because the env-var override (used on SQLite test connections) must be preserved; tests are migrated to inject the dialect directly instead.
2. **`app.Dialect()` is the data dialect only** — the aux DB is always SQLite and must never be routed through the dialect.
3. **No new package for tools/search** — `tools/search`/`tools/dbutils` cannot import `core` (cycle), so they extend the existing interface-assertion pattern and receive **primitives** (e.g. `notEqualOp string`, `RowidSortIdentifier() string`), never a `Dialect` value or an `IsMySQL` boolean.
4. **Maintenance split into two methods** — `PostSchemaSyncOptimize` (per-sync) and `PeriodicMaintenance` (daily cron, main DB only) are distinct concerns and kept separate rather than overloaded into one method.
5. **Migration skips use `dialect.Name() != "sqlite"`** — dialect-agnostic, PG will also skip legacy SQLite migrations.
6. **System table DDL in dialect** — `CollectionsTableDDL()` and `ParamsTableDDL()` move the hardcoded MySQL CREATE TABLE strings out of migration logic.
7. **`IsMySQLDataDB` kept as wrapper during transition** — avoids breaking all ~53 sites in one patch, removed cleanly at the end.

## Files Touched Summary

| File | Patches |
|---|---|
| `core/db_dialect.go` | 1, 2, 7 |
| `core/base.go` | 1, 5 |
| `core/app.go` | 1 |
| `core/field_text.go` | 2 |
| `core/field_email.go` | 2 |
| `core/field_url.go` | 2 |
| `core/field_password.go` | 2 |
| `core/field_date.go` | 2 |
| `core/field_autodate.go` | 2 |
| `core/field_file.go` | 2 |
| `core/field_relation.go` | 2 |
| `core/field_select.go` | 2 |
| `core/field_editor.go` | 2 |
| `core/field_geo_point.go` | 2 |
| `core/db_table.go` | 3 |
| `core/collection_validate.go` | 3 |
| `core/record_field_resolver.go` | 4 |
| `core/record_field_resolver_runner.go` | 4 |
| `core/collection_query.go` | 4 |
| `core/view.go` | 4 |
| `apis/record_crud.go` | 4 |
| `tools/search/filter.go` | 4 |
| `tools/search/sort.go` | 4 |
| `tools/dbutils/json.go` | 4 |
| `core/collection_record_table_sync.go` | 3, 5 |
| `migrations/1640988000_init.go` | 6 |
| `migrations/1717233556_v0.23_migrate.go` | 6 |
| `migrations/1717233557_v0.23_migrate2.go` | 6 |
| `migrations/1717233558_v0.23_migrate3.go` | 6 |
| `migrations/1717233559_v0.23_migrate4.go` | 6 |
| `migrations/1778828400_normalize_indexes.go` | 6 |
| `core/migrations_runner.go` | 6 |
| `core/field_select_test.go` | 7 (inject dialect, drop env var) |
| `core/field_relation_test.go` | 7 (inject dialect, drop env var) |

## Risk & Rollback

- Each patch is independently revertable via `git revert`
- No behavior change until call sites are migrated (Patch 1 is pure addition)
- If runtime QA fails after any patch, the issue is isolated to that patch's category
- Patch stack is re-exported only after Patch 7 passes all verification
