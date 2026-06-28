# Dialect Abstraction Refactor Plan

## Goal

Replace the ~53 scattered `if isMySQLDataDB(app)` branches with a single `Dialect` interface, eliminating dialect-specific conditionals from business logic. This makes the codebase simpler and prepares for adding PostgreSQL as a third dialect.

## Current State

- `core/db_dialect.go` defines `isMySQLDataDB(app App)` and `IsMySQLDataDB(app App)` — checks env var `PB_DATABASE_DRIVER` **first**, then falls back to runtime `DriverName()`
- ~53 call sites across ~15 files branch on this function (treat 53 as the current count, not an exhaustive guarantee — re-grep in Patch 7)
- 3 duplicate copies of the detection logic (in `migrations/1640988000_init.go`, `core/migrations_runner.go`, `tools/search/filter.go`)
- `tools/search` and `tools/dbutils` read the env var directly (no access to `App`)
- This document is a refactor design/spec, not a step-by-step executable agent plan. If the work will be delegated to agentic workers, convert it into a checkbox implementation plan under `docs/superpowers/plans/` before execution.

### Important constraints discovered during review

- **Detection is currently lazy + env-var-first.** `isMySQLDataDB` is evaluated at each call site (e.g. inside `Field.ColumnType(app)`), and it returns `true` whenever `PB_DATABASE_DRIVER=mysql` *regardless of the actual opened driver*. Several tests depend on this: e.g. `TestSelectFieldMySQLColumnType` / `TestRelationFieldMySQLColumnType` call `tests.NewTestApp()` (which opens **SQLite**), then `t.Setenv("PB_DATABASE_DRIVER", "mysql")` **after** boot, and expect MySQL column types. A naive "compute dialect once at boot from `DriverName()`" approach **breaks these tests**. See the detection design below for the resolution.
- **The aux DB is always SQLite**, even when the data DB is MySQL. `app.Dialect()` describes the **data** DB only and must never be applied to aux-DB operations (`AuxDB`, `AuxConcurrentDB`, `AuxNonconcurrentDB`).
- **`tools/search` and `tools/dbutils` cannot import `core`** (would create an import cycle). This is the real reason those packages use anonymous interface assertions on the resolver and receive dialect info as primitives (strings/bools) rather than a `Dialect` value. This pattern must be preserved.
- **`tools/dbutils.JSONEach` currently has its own `PB_DATABASE_DRIVER` branch.** The refactor must remove that env read. After all `core` callers are migrated to `app.Dialect().JSONEachColumnExpr(...)`, `dbutils.JSONEach` becomes a SQLite-only fallback/helper.

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
    LikeColumnContainsExpr(left, right, escape string, negated bool) string
    EqualityOperators() search.EqualityOperators
    RelationEqualityExpr(left, right string) string
    JSONArrayContainsExpr(jsonCol, idCol string) string
    JSONEachColumnExpr(column string) string
    JSONEachParamExpr(paramName string) string // paramName is bare, e.g. "dataEachTEST"; method adds {:...}
    JSONArrayLengthExpr(column string) string
    JSONExtractExpr(column, path string) string

    // Schema sync
    AddColumnDirectly() bool  // true for MySQL (ALTER TABLE ADD), false for SQLite (table recreation)
    RenameColumnSQL(table, old, new, colType string) string
    SingleToMultiConversionSQL(table, col, temp string) string
    MultiToSingleConversionSQL(table, col, temp string) string

    // Maintenance — three distinct concerns, kept as separate methods.
    // Use dbx.Builder because some call sites run inside transactions where the
    // handle is *dbx.Tx rather than *dbx.DB.
    //   PostSchemaSyncOptimize: run after a schema sync (collection_record_table_sync.go).
    //   PeriodicMaintenance:    run by the daily cron against the *data* DB only
    //                           (main-DB wal_checkpoint + optimize for SQLite; no-op for MySQL).
    //   Checkpoint:             run before backups against the *data* DB only
    //                           (SQLite wal_checkpoint; no-op for MySQL).
    // The aux DB checkpoint is NOT covered here — it is always SQLite.
    PostSchemaSyncOptimize(db dbx.Builder, logger *slog.Logger)
    PeriodicMaintenance(db dbx.Builder, logger *slog.Logger)
    Checkpoint(db dbx.Builder, logger *slog.Logger)

    // Sort / count
    DefaultCollectionSort() string  // "rowid ASC" / "id ASC"
    CountColumn(isView bool) string // SQLite non-view: "_rowid_"; MySQL non-view: "id"; views: ""

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

Add a small equality-operator value type in `tools/search` (not `core`, otherwise `tools/search` would need to import `core` and create a cycle):

```go
type EqualityOperatorSet struct {
    EqualOp     string // value comparison operator
    NullEqualOp string // comparison operator when either side disables null fallback
    NullConcat  string // connector for empty/null fallback expression
    NullExpr    string // null fallback expression suffix
}

type EqualityOperators struct {
    Equal    EqualityOperatorSet
    NotEqual EqualityOperatorSet
}
```

`core.Dialect.EqualityOperators()` should return `search.EqualityOperators`. This is allowed because `core` already imports `tools/search`; `tools/search` must not import `core`. Do not reduce equality handling to only `NotEqualValueOperator()`: `tools/search.resolveEqualExpr` currently changes the null-safe equality operator, null fallback expression, and boolean connector for MySQL. The equality values must be branch-aware because `NullConcat` and `NullExpr` differ between `=` and `!=`; a single flat struct cannot represent both branches safely.

`JSONEachColumnExpr(column)` returns a **bare table-valued expression without an alias**. Callers remain responsible for aliasing via `registerJoin(tableExpr, alias, ...)`, `search.Join.TableAlias`, or explicit SQL formatting. This keeps SQLite `json_each(...)` and MySQL `JSON_TABLE(...)` usable in the existing join patterns.

`JSONEachParamExpr(paramName)` receives the **bare dbx parameter name** (for example `dataEachTEST`, not `{:dataEachTEST}`) and returns a bare table-valued expression using the placeholder (`json_each({:dataEachTEST})` for SQLite, `JSON_TABLE({:dataEachTEST}, ...)` for MySQL). This avoids mixed call sites where some pass a placeholder and others pass a raw name.

`JSONExtractExpr(column, path)` owns JSON path extraction for JSON/geo filter fields. This closes the current `dbutils.JSONExtract(...)` gap in `RecordFieldResolver`: SQLite keeps the existing `json_valid/json_object/JSON_EXTRACT` wrapper, MySQL uses a matching expression that preserves scalar/string comparison semantics instead of blindly returning quoted JSON strings, and future dialects do not need to touch resolver business logic. The MySQL implementation must define and test root path, object path, scalar non-JSON column, string comparison, and null comparison behavior; do not leave this as an unspecified "MySQL-safe" placeholder. After `RecordFieldResolver` is migrated, `dbutils.JSONExtract` is either removed if unused or documented as a SQLite/default helper like `dbutils.JSONEach`.

### App wiring & dialect detection

- `BaseApp` gets a `dialect Dialect` field
- `App` interface gets a `Dialect() Dialect` method
- Do **not** add `SetDialect(Dialect)` to the public `App` interface. A mutable public dialect setter can make runtime SQL generation diverge from the actual opened DB. For tests that need MySQL semantics without a MySQL connection, set `PB_DATABASE_DRIVER=mysql` before `tests.NewTestApp()` so the cached boot-time dialect is initialized consistently.
- Ensure transaction/shallow-copy app paths inherit `dialect`. All `txApp.Dialect()` calls in migrations/schema sync must see the same data dialect as the parent app. The transaction clone path lives in `core/db_tx.go:createTxApp()`; do not assume this is only a `core/base.go` change.
- `Dialect()` must be nil-safe before/without boot: if `app.dialect` is unset, return `DialectForDriver(driverNameFrom(app.ConcurrentDB()))` when possible, otherwise `SQLiteDialect{}`. The helper must guard nil DB handles: `driverNameFrom(db dbx.Builder) string` returns `""` unless `db != nil` and implements `DriverName() string`. The transitional wrappers (`isMySQLDataDB` / `IsMySQLDataDB`) should also avoid panics for nil or unbootstrapped app values while they still exist.

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

- The dialect is set in `initDataDB()` right after `DBConnect` returns, via `app.dialect = DialectForDriver(concurrentDB.DriverName())`. The env-var-first check inside the factory preserves the existing behavior where setting `PB_DATABASE_DRIVER=mysql` before boot forces MySQL semantics even on a SQLite test connection.
- **Do not** switch to "driver name only" — that was the original (rejected) idea and it breaks the env-var-only column-type tests described in Current State.
- Because the affected tests call `t.Setenv(...)` **after** `NewTestApp()` (i.e. after `initDataDB` already ran), Patch 7 must update those tests to set `PB_DATABASE_DRIVER=mysql` *before* `NewTestApp()`. Avoid exposing a public or test-only dialect setter solely for these tests unless a future non-test use case appears.
- `IsMySQLDataDB` is kept during transition, removed in Patch 7. To keep Patch 1 behavior-neutral, either keep the wrapper on the old lazy env-var-first logic until the post-boot env-var tests are migrated, or migrate those tests in Patch 1 before switching the wrapper to `return app.Dialect().Name() == "mysql"`. Do not claim Patch 1 is behavior-neutral if the wrapper immediately uses the cached boot-time dialect while tests still mutate `PB_DATABASE_DRIVER` after boot.

### tools/search wiring (no new package needed)

The existing interface assertion pattern in `tools/search` is extended. `tools/search` cannot import `core`, so it only ever receives **primitive** dialect info via assertions on the resolver — never a `Dialect` value or a generic `IsMySQL` boolean (a bare boolean would just recreate the dialect conditional we are removing and would not extend to a third dialect).

- `filter.go`: `buildResolversExpr` already receives `likeEscape string` as a parameter. Add an `EqualityOperators` parameter the same way, computed from the resolver in `resolveTokenizedExpr()` via `interface{ EqualityOperators() EqualityOperators }` assertion (defaulting to the SQLite operators when the assertion fails). This removes the direct `PB_DATABASE_DRIVER` read at `filter.go:347` without losing MySQL's `<=>`, `AND`, and `IS NOT NULL` behavior.
- `filter.go`: `resolveTokenizedExpr()` must compute all resolver-provided primitives (`likeEscape`, `EqualityOperators`, and LIKE column expression behavior) before calling `buildResolversExpr(...)`; do not only update `buildResolversExpr()` and forget its caller.
- `filter.go`: dialectize LIKE expressions where the right operand is another column. SQLite keeps `left LIKE ('%' || right || '%') ESCAPE '\\'`; MySQL uses `left LIKE CONCAT('%', right, '%') ESCAPE '\\\\'`. Do not only change `LikeEscapeClause()` because the string concatenation operator is also dialect-specific. If the primitive is `LikeColumnContainsExpr(left, right, escape string, negated bool) string`, the resulting `dbx.NewExp(...)` must still carry `left.Params`; parameter-backed LIKE branches must continue using `mergeParams(left.Params, wrapLikeParams(right.Params))`.
- `filter.go`: add a SQLite/default helper for LIKE column-operand formatting, e.g. `defaultLikeColumnContainsExpr(left, right, escape string, negated bool) string`, and use it whenever the resolver does not implement `interface{ LikeColumnContainsExpr(left, right, escape string, negated bool) string }`. Generic resolvers must not be forced to implement dialect primitives.
- `filter.go`: `manyVsManyExpr` and `manyVsOneExpr` build SQL later, after the resolver is no longer available. Store `likeEscape` and `EqualityOperators` on these expression structs when they are created. Do not call `defaultLikeEscapeClause()` from their `Build()` methods after this refactor.
- `sort.go`: `SortField.BuildExpr(fieldResolver)` already receives the resolver. Instead of an `IsMySQL() bool` flag, assert a method that returns the concrete rowid sort expression, e.g. `interface{ RowidSortIdentifier() string }` — SQLite returns `[[_rowid_]]`, MySQL returns the resolved `id` identifier, a future dialect returns its own. `BuildExpr` then just formats `"<identifier> <direction>"`. This removes the direct `PB_DATABASE_DRIVER` read at `sort.go:35` and keeps the dialect as the single source of truth.
- `tools/dbutils/json.go`: all `JSONEach` callers in `core` are replaced with `app.Dialect().JSONEachColumnExpr(column)`. Then remove the `PB_DATABASE_DRIVER` branch from `dbutils.JSONEach`; it is kept as a SQLite-only fallback/helper.
- `tools/dbutils/json.go`: migrate `core` JSON path extraction from `dbutils.JSONExtract(column, path)` to `app.Dialect().JSONExtractExpr(column, path)`. Then either remove `dbutils.JSONExtract` if unused or keep it documented as a SQLite/default helper with no env-var dialect detection.
- `record_field_resolver_runner.go`: request-body `@request.body.<field>:each` joins currently hardcode `json_each({:param})`; replace them with `app.Dialect().JSONEachParamExpr(paramName)` where `paramName` is the bare dbx parameter name without `{:...}`. MySQL can use `JSON_TABLE({:param}, '$[*]' COLUMNS(value VARCHAR(255) PATH '$'))`. The returned table-valued expression must expose a column named `value` and must not include an alias; existing callers continue to alias the join and reference `[[alias.value]]`.
- `record_field_resolver_runner.go`: multivalue `:length` currently uses SQLite `dbutils.JSONArrayLength(...)`; replace it with `app.Dialect().JSONArrayLengthExpr(column)` or explicitly mark `:length` as an out-of-scope MySQL gap before implementation. Prefer migrating it in this refactor because it is query-generation dialect behavior.
- Generic `tools/search` resolvers without dialect primitive methods default to SQLite behavior. MySQL behavior is provided by resolvers like `RecordFieldResolver` that implement the primitive methods; `tools/search` must not infer the data dialect from env vars.
- `tools/search.SimpleFieldResolver` JSON path extraction remains SQLite/default behavior unless a future resolver primitive is introduced. This is intentional because generic search resolvers have no app/dialect context; do not add env-var detection there.

### Aux DB caveat

`app.Dialect()` is the **data** dialect only; the aux DB is always SQLite. The periodic cron in `base.go` currently runs `PRAGMA wal_checkpoint(TRUNCATE)` against **both** the main and aux DBs unconditionally, plus `PRAGMA optimize` against the main DB (guarded today by `!isMySQLDataDB`):

- The aux-DB `wal_checkpoint` must **always** use SQLite behavior — do not route it through `app.Dialect()`.
- The main-DB `wal_checkpoint` is also SQLite-specific and currently errors (swallowed as a warning) when the data DB is MySQL. Fold both the main-DB `wal_checkpoint` and `PRAGMA optimize` into a single data-dialect maintenance call (see `RunPostSyncOptimize` discussion) so MySQL becomes a clean no-op instead of a logged error.
- Backup creation in `core/base_backup.go` also runs `PRAGMA wal_checkpoint(TRUNCATE)` against both data and aux DBs. Route only the **data** DB checkpoint through the dialect (`Checkpoint`); keep the aux checkpoint as an unconditional SQLite call.
- `core/log_query.go` uses SQLite `strftime` intentionally because logs live in the aux DB. Do not route log queries through the data dialect.

## Patch Breakdown

### Patch 1: Add Dialect interface + implementations + App.Dialect() wiring

**Files:**
- `core/db_dialect.go` — rewrite: add `Dialect` interface, `SQLiteDialect`, `MySQLDialect`, `DialectForDriver(name string) Dialect` factory; import/use `tools/search.EqualityOperators` rather than defining the type in `core`
- `tools/search/filter.go` or `tools/search/simple_field_resolver.go` — add the `EqualityOperators` value type used by `tools/search` and returned by `core.Dialect.EqualityOperators()`
- `core/base.go` — add `dialect Dialect` field to `BaseApp` struct, set in `initDataDB()`, add nil-safe `Dialect()` method
- `core/db_tx.go` `createTxApp()` — ensure data and aux transaction apps inherit the parent data dialect; `clone := *app` should preserve the field, but keep this file in scope and test it explicitly
- `core/base.go` shallow-copy paths — ensure `UnsafeWithoutHooks()` inherits the parent data dialect
- `core/base.go` `ResetBootstrapState()` — clear `app.dialect = nil` together with DB handles so re-bootstrap cannot observe stale dialect state
- `core/app.go` — add `Dialect() Dialect` to `App` interface
- Keep `isMySQLDataDB` and `IsMySQLDataDB` without behavior change until env-var-only tests are migrated. Either keep their old lazy env-var-first logic in Patch 1, or move the affected test updates into Patch 1 before changing the wrappers to `return app.Dialect().Name() == "mysql"`.
- Do not add a public `SetDialect` or test-only dialect injection helper for this refactor; migrate env-var-only tests by setting `PB_DATABASE_DRIVER=mysql` before `tests.NewTestApp()`.
- Add focused tests for `Dialect()` before bootstrap, after SQLite bootstrap, env-forced MySQL bootstrap, data transaction inheritance, nested aux-to-data transaction inheritance (`AuxRunInTransaction` followed by `RunInTransaction`), aux transaction preserving the data dialect without treating aux operations as data-dialect operations, `UnsafeWithoutHooks().Dialect()`, and `ResetBootstrapState()` clearing stale dialect state.

**Verification:**
- `go build ./...` passes
- `go test ./...` passes
- No behavior change if call sites still use `isMySQLDataDB` wrappers; if wrappers now use `app.Dialect()`, the post-boot env-var mutation tests must already be migrated in this patch

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
- Find all `jsonArrayColumnType(app)` callers and replace them with `app.Dialect().JSONArrayColumnType()`. Keep `jsonArrayColumnType` only as a temporary wrapper if needed during Patch 2, then remove it in Patch 7.

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

### Patch 4: Migrate filter/search/query call sites

**Files & changes:**
- `core/record_field_resolver.go:85` `LikeEscapeClause` — `r.app.Dialect().LikeEscapeClause()`
- `core/record_field_resolver.go` — expose resolver methods for `EqualityOperators()`, `LikeColumnContainsExpr(...)`, and `RowidSortIdentifier()` using `r.app.Dialect()` so `tools/search` can obtain primitive dialect behavior without importing `core`
- `core/record_field_resolver_runner.go:62` `relationValueEquals` — `resolver.app.Dialect().RelationEqualityExpr(left, right)`
- `core/record_field_resolver_runner.go:70` `relationArrayContainsIdentifier` — `resolver.app.Dialect().JSONArrayContainsExpr(jsonCol, idCol)`
- `core/record_field_resolver_runner.go:359,378` request-body `json_each({:param})` joins — `resolver.app.Dialect().JSONEachParamExpr(paramName)`
- `core/record_field_resolver_runner.go:584,636,699,751` — 4 JOIN sites use `JSONArrayContainsExpr` / `JSONEachColumnExpr` via dialect
- Update both `registerJoin(dbutils.JSONEach(...), ...)` and `search.Join{TableName: dbutils.JSONEach(...)}` sites. Preserve existing `TableAlias` / join alias behavior because dialect methods return no alias.
- `core/record_field_resolver_runner.go:812,817` — `dbutils.JSONArrayLength(...)` becomes `resolver.app.Dialect().JSONArrayLengthExpr(...)`
- `core/record_field_resolver_runner.go:830,848` — `dbutils.JSONEach(...)` becomes `resolver.app.Dialect().JSONEachColumnExpr(...)`
- `core/record_field_resolver_runner.go:495,501,886,888` — JSON/geo/root path extraction `dbutils.JSONExtract(...)` becomes `resolver.app.Dialect().JSONExtractExpr(...)`
- `core/collection_query.go:42` sort — `app.Dialect().DefaultCollectionSort()`
- `core/collection_query.go:322,341,351` — `app.Dialect().IDCastType()`, `app.Dialect().RequiresSubqueryAlias()`
- `apis/record_crud.go:83` count — `countCol := e.App.Dialect().CountColumn(collection.IsView())`; call `searchProvider.CountCol(countCol)` **only when `countCol != ""`**. Do not call `CountCol("")`: the current provider would build invalid `COUNT(DISTINCT [[table.]])`, not `count(*)`. When the dialect returns `""` for views, leave the provider default count column unless this patch explicitly changes `tools/search.Provider` to support `count(*)`.
- Count column behavior matrix: SQLite non-view collections return `_rowid_`; SQLite views return `""` (caller skips `CountCol`); MySQL non-view collections return `id`; MySQL views return `""` (caller skips `CountCol`). Add focused coverage around `apis/record_crud.go` or the dialect methods so this does not regress to an empty identifier or a SQLite `_rowid_` on MySQL.
- `core/view.go:56,169` — `app.Dialect().RequiresSubqueryAlias()`
- `core/view.go:263` — `dbutils.JSONEach(cleanFieldName)` becomes `app.Dialect().JSONEachColumnExpr(cleanFieldName)` while preserving explicit `_je_file` aliasing
- `core/record_model.go:1536` — `dbutils.JSONEach(prefixedFieldName)` becomes `app.Dialect().JSONEachColumnExpr(prefixedFieldName)`
- `core/record_query_expand.go:113` — `dbutils.JSONEach(indirectRelField.Name)` becomes `app.Dialect().JSONEachColumnExpr(indirectRelField.Name)`
- `tools/search/filter.go:194-212` LIKE column operand formatting — use resolver-provided `LikeColumnContainsExpr(...)` instead of hardcoded SQLite `'||'` concatenation
- `tools/search/filter.go:347` `resolveEqualExpr` — add `EqualityOperators` param, computed from resolver via `interface{ EqualityOperators() EqualityOperators }` assertion (removes the direct env-var read and preserves MySQL null-safe equality behavior)
- `tools/search/filter.go:156-168` `resolveTokenizedExpr` — thread `likeEscape`, `EqualityOperators`, and LIKE column expression primitives into `buildResolversExpr(...)`
- `tools/search/filter.go:669-683,740-744` `manyVsManyExpr` / `manyVsOneExpr` — store `likeEscape` and `EqualityOperators` on the expression structs at creation time; remove `defaultLikeEscapeClause()` calls from `Build()`
- `tools/search/sort.go:35` — replace the direct env-var read with `interface{ RowidSortIdentifier() string }` assertion on the resolver (returns `[[_rowid_]]` for SQLite, resolved `id` identifier for MySQL); no `IsMySQL` boolean
- `tools/dbutils/json.go:12` `JSONEach` — remove the env-var MySQL branch after callers in `core` use `app.Dialect().JSONEachColumnExpr(column)` instead; `JSONEach` remains SQLite-only
- `tools/dbutils/json.go:44` `JSONExtract` — remove from `core` callers; either delete if unused or keep as SQLite/default-only helper with no env-var dialect detection
- `tools/dbutils/json_test.go` — assert `JSONEach` is SQLite-only and does not change when `PB_DATABASE_DRIVER=mysql`
- `tools/search/simple_field_resolver.go:102-123` — leave generic JSON path extraction as SQLite/default behavior and add/keep a comment that generic resolvers do not infer dialect from env vars

**Additional focused tests:**
- `core/record_field_resolver_test.go` — add MySQL generated-SQL coverage for relation-many joins, back-relation-many joins, request body `:each`, multivalue `:length`, JSON/geo path extraction, and multi-match equality/null fallback behavior.
- `core/record_field_resolver_test.go` — JSON/geo path extraction tests must cover MySQL root path, object path, scalar non-JSON column fallback, string comparison, and null comparison so `JSONExtractExpr(...)` semantics are explicit and not left to implementation guesswork.
- `tools/search/filter_test.go` — add resolver-backed tests for MySQL equality operators and LIKE column-operand formatting without relying on `PB_DATABASE_DRIVER`.
- `tools/search/filter_test.go` — add a same-process cache-safety test that builds the same filter string first with a SQLite/default resolver and then with a MySQL-primitive resolver (and/or reverse order) to prove dialect-specific resolver behavior is not cached from the first build. Cover both equality/null fallback and LIKE column-operand formatting (`||` vs `CONCAT`) in this cache-safety test.
- `tools/search/sort_test.go` — add resolver-backed `@rowid` tests proving resolver-provided rowid identifiers are used and env vars are ignored.

**Verification:** `go build ./...` + `go test ./...`
- Remove now-unused `os` imports in `tools/search/filter.go`, `tools/search/sort.go`, and `tools/dbutils/json.go` in this patch, not later, otherwise `go build` fails before Patch 7.
- Additional grep: `grep -rn "dbutils.JSONEach(" core/` should return zero results unless a deliberately SQLite-only data path is explicitly documented.
- Additional grep: `grep -rn "dbutils.JSONExtract(" core/` should return zero results unless a deliberately SQLite-only data path is explicitly documented.

### Patch 5: Migrate schema sync + maintenance call sites

**Files & changes:**
- `core/collection_record_table_sync.go:96` — `txApp.Dialect().AddColumnDirectly()`
- `core/collection_record_table_sync.go:104` — `txApp.Dialect().RenameColumnSQL(...)`
- `core/collection_record_table_sync.go:168` — `app.Dialect().PostSchemaSyncOptimize(app.NonconcurrentDB(), app.Logger())` after schema sync completes; the method receives the data DB builder and app logger explicitly
- `core/collection_record_table_sync.go:262` — `txApp.Dialect().RenameColumnSQL(...)`
- `core/collection_record_table_sync.go:287` — `txApp.Dialect().SingleToMultiConversionSQL(...)`; the raw MySQL `JSON_VALID` / `JSON_TYPE` / `JSON_ARRAY` conversion SQL moves into the MySQL dialect implementation
- `core/collection_record_table_sync.go:339` — `txApp.Dialect().MultiToSingleConversionSQL(...)`; the raw MySQL `JSON_UNQUOTE` / `JSON_EXTRACT` / `JSON_LENGTH` conversion SQL moves into the MySQL dialect implementation
- `core/collection_record_table_sync.go:426` — `txApp.Dialect().DropIndexSQL(...)`
- `core/collection_record_table_sync.go:457` — `txApp.Dialect().SupportsPartialIndexes()` (if false, strip WHERE)
- `core/base.go:1360-1377` periodic cron — route the **main** DB through `app.Dialect().PeriodicMaintenance(app.NonconcurrentDB(), app.Logger())` (SQLite: `wal_checkpoint(TRUNCATE)` + `PRAGMA optimize`; MySQL: no-op). The **aux** DB `wal_checkpoint(TRUNCATE)` stays as an unconditional SQLite call (aux is always SQLite) — do NOT route it through the dialect.
- `core/base_backup.go:88-89` backup checkpoint — route the **data** DB checkpoint through `txApp.Dialect().Checkpoint(txApp.DB(), txApp.Logger())` or an equivalent SQLite-only dialect guard; keep the **aux** DB checkpoint as unconditional SQLite. `Checkpoint` accepts `dbx.Builder` because this call is inside nested transactions and `txApp.DB()` is commonly `*dbx.Tx`, not `*dbx.DB`.

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
- This intentionally skips these legacy SQLite migrations for all non-SQLite dialects, not only MySQL. If a future dialect needs equivalent migration behavior, it must add its own dialect-specific migration path rather than run SQLite migration SQL.
- `migrations/1778828400_normalize_indexes.go` — remove now-unused `os`/`strings` imports after replacing the direct `PB_DATABASE_DRIVER` read
- `core/migrations_runner.go:267` `migrationAppliedColumnType` — `app.Dialect().MigrationAppliedColumnType()`

**Verification:** `go build ./...` + `go test ./...` + runtime QA against MySQL

### Patch 7: Remove isMySQLDataDB + cleanup

**Files & changes:**
- `core/db_dialect.go` — remove `isMySQLDataDB` and `IsMySQLDataDB` functions
- Remove `jsonArrayColumnType` helper (now in dialect)
- Grep for any remaining `isMySQLDataDB`, `IsMySQLDataDB`, `PB_DATABASE_DRIVER` references in Go source across **`core/`, `tools/`, `migrations/`, `apis/`, `forms/`, `cmd/`, `examples/`, `plugins/`, and root `pocketbase.go`**. The only allowed production references are:
  - the `envDatabaseDriver`/`envDatabaseDSN` consts and the `DialectForDriver` factory in `core/db_dialect.go` and `core/db_connect.go`
  - user-facing CLI help text (whitelist explicitly: `pocketbase.go`, `cmd/serve.go`)
- tests may reference `PB_DATABASE_DRIVER` only in focused dialect/connect tests, not in field/query behavior tests
- **Migrate the env-var-only tests** (the deliverable flagged in App wiring): `core/field_select_test.go`, `core/field_relation_test.go`, and any other test that does `t.Setenv("PB_DATABASE_DRIVER", "mysql")` after `NewTestApp()`. Set the env var before `NewTestApp()` so `initDataDB()` caches the intended dialect; do not add a public or test-only dialect setter solely for these tests.
- Migrate any test outside focused dialect/connect tests that uses `PB_DATABASE_DRIVER` to influence field/query/sort behavior, including `tools/search/*_test.go` if present.
- Include `plugins/` in cleanup greps because plugin code is production Go code.
- Confirm `tools/dbutils/json.go` no longer imports `os` or `strings` solely for dialect detection.
- Confirm `tools/search/filter.go`, `tools/search/sort.go`, and `tools/dbutils/json.go` no longer import `os` solely for dialect detection. Any remaining `strings` import must be needed for non-env string handling.

**Verification:**
- `go build ./...` passes
- `go test ./...` passes
- `grep -rn "isMySQLDataDB\|IsMySQLDataDB" core/ tools/ migrations/ apis/ forms/ cmd/ examples/ plugins/ pocketbase.go` returns zero results
- Production grep for `PB_DATABASE_DRIVER` in `core/ tools/ migrations/ apis/ forms/ cmd/ plugins/ pocketbase.go` returns only `core/db_dialect.go` + `core/db_connect.go` (the detection factory/connect path) plus user-facing help text in `pocketbase.go` and `cmd/serve.go`; any test references are limited to focused dialect/connect tests
- `grep -rn "dbutils.JSONEach(" core/` returns zero results unless every remaining occurrence is explicitly documented as SQLite-only and safe
- `grep -rn "dbutils.JSONExtract(" core/` returns zero results unless every remaining occurrence is explicitly documented as SQLite-only and safe
- Query-generation greps for `json_each(`, `json_array_length(`, `JSON_EXTRACT(`, `JSON_LENGTH(`, `JSON_UNQUOTE(`, `JSON_CONTAINS(`, `JSON_TABLE(`, `_rowid_`, and `PRAGMA` in `core/ apis/ tools/search` are reviewed. Remaining raw MySQL/SQLite query-generation functions must be either inside dialect implementations (`core/db_dialect.go`), aux-DB-only paths (`core/log_query.go`, aux checkpoints), tests, `tools/search.SimpleFieldResolver`'s documented SQLite/default JSON path behavior, or explicitly documented SQLite-only helpers. In particular, raw MySQL JSON conversion SQL must not remain in `core/collection_record_table_sync.go` after `SingleToMultiConversionSQL(...)` and `MultiToSingleConversionSQL(...)` are migrated.
- `grep -rn "os.Getenv(.*PB_DATABASE_DRIVER\|PB_DATABASE_DRIVER" tools/search tools/dbutils core migrations apis forms plugins pocketbase.go` returns only approved detection/connect/help/test locations described above
- Runtime QA: `node scripts/mysql-runtime-qa.mjs --skip-docker --mysql-port 3306 --mysql-password "" --mysql-database pocketbase_qa`
- Export patch stack

## Key Design Decisions

1. **Detection keeps env-var-first priority, evaluated at boot** — `DialectForDriver` checks `PB_DATABASE_DRIVER=mysql` first, then the opened driver name. This is set once in `initDataDB()` and exposed via `app.Dialect()`. We deliberately did **not** switch to "driver name only" because the env-var override must be preserved; field/query behavior tests are migrated away from post-boot env-var mutation before wrappers depend on cached `app.Dialect()`.
2. **`app.Dialect()` is the data dialect only** — the aux DB is always SQLite and must never be routed through the dialect.
3. **No new package for tools/search** — `tools/search`/`tools/dbutils` cannot import `core` (cycle), so they extend the existing interface-assertion pattern and receive **primitives** (e.g. `EqualityOperators`, `RowidSortIdentifier() string`, LIKE/JSON expression strings), never a `Dialect` value or an `IsMySQL` boolean.
4. **Equality and LIKE are expression-level dialect behavior** — `LIKE ESCAPE` alone is insufficient because MySQL also differs in column concatenation and null-safe equality behavior (`<=>`, `AND`, `IS NOT NULL`).
5. **JSON path extraction is dialect behavior** — `RecordFieldResolver` must use `Dialect.JSONExtractExpr(...)` for JSON/geo filters; generic `tools/search.SimpleFieldResolver` remains SQLite/default because it has no app context.
6. **Maintenance split into three methods** — `PostSchemaSyncOptimize` (per-sync), `PeriodicMaintenance` (daily cron, main DB only), and `Checkpoint` (backup, main DB only) are distinct concerns and kept separate rather than overloaded into one method.
7. **Migration skips use `dialect.Name() != "sqlite"`** — dialect-agnostic, PG will also skip legacy SQLite migrations.
8. **System table DDL in dialect** — `CollectionsTableDDL()` and `ParamsTableDDL()` move the hardcoded MySQL CREATE TABLE strings out of migration logic.
9. **`IsMySQLDataDB` kept as wrapper during transition** — avoids breaking all ~53 sites in one patch, removed cleanly at the end.

## Files Touched Summary

| File | Patches |
|---|---|
| `core/db_dialect.go` | 1, 2, 7 |
| `core/base.go` | 1, 5 |
| `core/db_tx.go` | 1 |
| `core/base_backup.go` | 5 |
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
| `core/log_query.go` | none (aux DB SQLite-only; document as intentional) |
| `core/collection_query.go` | 4 |
| `core/view.go` | 4 |
| `apis/record_crud.go` | 4 |
| `tools/search/filter.go` | 4 |
| `tools/search/simple_field_resolver.go` | 1 or 4 (if `EqualityOperators` type lives here instead of `filter.go`) |
| `tools/search/sort.go` | 4 |
| `tools/dbutils/json.go` | 4, 7 |
| `tools/dbutils/json_test.go` | 4 |
| `tools/search/filter_test.go` | 4 |
| `tools/search/sort_test.go` | 4 |
| `core/collection_record_table_sync.go` | 3, 5 |
| `core/record_model.go` | 4 |
| `core/record_query_expand.go` | 4 |
| `migrations/1640988000_init.go` | 6 |
| `migrations/1717233556_v0.23_migrate.go` | 6 |
| `migrations/1717233557_v0.23_migrate2.go` | 6 |
| `migrations/1717233558_v0.23_migrate3.go` | 6 |
| `migrations/1717233559_v0.23_migrate4.go` | 6 |
| `migrations/1778828400_normalize_indexes.go` | 6 |
| `core/migrations_runner.go` | 6 |
| `core/field_select_test.go` | 7 (remove post-boot env-var mutation) |
| `core/field_relation_test.go` | 7 (remove post-boot env-var mutation) |

## Risk & Rollback

- Each patch is independently revertable via `git revert`
- No behavior change until call sites are migrated, provided Patch 1 keeps the legacy wrapper behavior or migrates the post-boot env-var tests before switching wrappers to cached `app.Dialect()`
- If runtime QA fails after any patch, the issue is isolated to that patch's category
- Patch stack is re-exported only after Patch 7 passes all verification
