# Dialect Abstraction Refactor Plan

## Goal

Replace 50 scattered `if isMySQLDataDB(app)` branches with a single `Dialect` interface, eliminating dialect-specific conditionals from business logic. This makes the codebase simpler and prepares for adding PostgreSQL as a third dialect.

## Current State

- `core/db_dialect.go` defines `isMySQLDataDB(app App)` and `IsMySQLDataDB(app App)` — checks env var `PB_DATABASE_DRIVER` + runtime `DriverName()`
- 50 call sites across ~15 files branch on this function
- 3 duplicate copies of the detection logic (in `migrations/1640988000_init.go`, `core/migrations_runner.go`, `tools/search/filter.go`)
- `tools/search` and `tools/dbutils` read the env var directly (no access to `App`)

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
    RunPostSyncOptimize(db *dbx.DB, logger Logger)

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

### App wiring

- `BaseApp` gets a `dialect Dialect` field
- `App` interface gets a `Dialect() Dialect` method
- Dialect is determined in `initDataDB()` right after `DBConnect` returns — inspect `concurrentDB.DriverName()`
- `DefaultDBConnect` already knows which driver it opened (mysql vs sqlite); the dialect is set from the opened DB's driver name, which is more reliable than env var alone
- `IsMySQLDataDB` kept as thin wrapper `return app.Dialect().Name() == "mysql"` during transition, removed in Patch 7

### tools/search wiring (no new package needed)

The existing interface assertion pattern in `tools/search` is extended:

- `filter.go`: `buildResolversExpr` already receives `likeEscape string` as a parameter. Add a `notEqualOp string` parameter the same way, computed from the resolver in `resolveExpr` via `interface{ NotEqualValueOperator() string }` assertion.
- `sort.go`: `SortField.BuildExpr(fieldResolver)` already receives the resolver. Use interface assertion `interface{ IsMySQL() bool }` (or `interface{ DefaultCollectionSort() string }`) to decide rowid handling.
- `tools/dbutils/json.go`: `JSONEach` callers in `core` are replaced with `app.Dialect().JSONEachExpr(column)`. `dbutils.JSONEach` kept as SQLite-only fallback.

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
- `tools/search/filter.go:347` `resolveEqualExpr` — add `notEqualOp string` param, computed from resolver via interface assertion
- `tools/search/sort.go:35` — interface assertion `interface{ IsMySQL() bool }` on resolver
- `tools/dbutils/json.go:12` `JSONEach` — callers in `core` use `app.Dialect().JSONEachExpr(column)` instead

**Verification:** `go build ./...` + `go test ./...`

### Patch 5: Migrate schema sync call sites (12 sites)

**Files & changes:**
- `core/collection_record_table_sync.go:96` — `txApp.Dialect().AddColumnDirectly()`
- `core/collection_record_table_sync.go:104` — `txApp.Dialect().RenameColumnSQL(...)`
- `core/collection_record_table_sync.go:168` — `app.Dialect().RunPostSyncOptimize(db, logger)`
- `core/collection_record_table_sync.go:262` — `txApp.Dialect().RenameColumnSQL(...)`
- `core/collection_record_table_sync.go:287` — `txApp.Dialect().SingleToMultiConversionSQL(...)`
- `core/collection_record_table_sync.go:339` — `txApp.Dialect().MultiToSingleConversionSQL(...)`
- `core/collection_record_table_sync.go:426` — `txApp.Dialect().DropIndexSQL(...)`
- `core/collection_record_table_sync.go:457` — `txApp.Dialect().SupportsPartialIndexes()` (if false, strip WHERE)
- `core/base.go:1371` periodic PRAGMA — `app.Dialect().RunPostSyncOptimize(db, logger)`

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
- Grep for any remaining `isMySQLDataDB`, `IsMySQLDataDB`, `PB_DATABASE_DRIVER` references in Go source (excluding CLI help text in `pocketbase.go` and `cmd/serve.go` which are user-facing docs)
- Update tests: tests that set `PB_DATABASE_DRIVER=mysql` env var still work because dialect detection reads env var at boot

**Verification:**
- `go build ./...` passes
- `go test ./...` passes
- `grep -r "isMySQLDataDB\|IsMySQLDataDB" core/ tools/ migrations/ apis/` returns zero results
- Runtime QA: `node scripts/mysql-runtime-qa.mjs --skip-docker --mysql-port 3306 --mysql-password "" --mysql-database pocketbase_qa`
- Export patch stack

## Key Design Decisions

1. **Dialect determined from DB driver name, not just env var** — more reliable, works even if env var is missing but DSN implies MySQL
2. **No new package for tools/search** — extend existing interface assertion pattern, pass dialect info as parameters (matching the existing `likeEscape` pattern)
3. **Migration skips use `dialect.Name() != "sqlite"`** — dialect-agnostic, PG will also skip legacy SQLite migrations
4. **System table DDL in dialect** — `CollectionsTableDDL()` and `ParamsTableDDL()` move the hardcoded MySQL CREATE TABLE strings out of migration logic
5. **`IsMySQLDataDB` kept as wrapper during transition** — avoids breaking all 50 sites in one patch, removed cleanly at the end

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

## Risk & Rollback

- Each patch is independently revertable via `git revert`
- No behavior change until call sites are migrated (Patch 1 is pure addition)
- If runtime QA fails after any patch, the issue is isolated to that patch's category
- Patch stack is re-exported only after Patch 7 passes all verification
