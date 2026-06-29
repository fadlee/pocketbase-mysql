# Dialect Abstraction Refactor Plan

## Goal

Replace the ~53 scattered `if isMySQLDataDB(app)` branches with a single `Dialect` interface, eliminating dialect-specific conditionals from business logic. This makes the codebase simpler and prepares for adding PostgreSQL as a third dialect.

## Current State

- `core/db_dialect.go` defines `isMySQLDataDB(app App)` and `IsMySQLDataDB(app App)` — checks env var `PB_DATABASE_DRIVER` **first**, then falls back to runtime `DriverName()`
- ~53 call sites across ~15 files branch on this function (treat 53 as the current count, not an exhaustive guarantee — re-grep in Patch 7)
- 3 duplicate/near-duplicate copies of the detection logic (in `migrations/1640988000_init.go`, `core/migrations_runner.go`, `tools/search/filter.go`), plus additional direct env reads in `tools/search/sort.go`, `tools/dbutils/json.go`, and `migrations/1778828400_normalize_indexes.go`
- `tools/search` and `tools/dbutils` read the env var directly (no access to `App`)
- This document is a refactor design/spec, not a step-by-step executable agent plan. If the work will be delegated to agentic workers, convert it into a checkbox implementation plan under `docs/superpowers/plans/` before execution.
- Command examples use `grep` in several places for brevity. On Windows or when following repo tooling conventions, use equivalent `rg` commands.

## Finalization Rule

This document is the stable design/spec reference for the dialect abstraction refactor. Do not keep revising this file during implementation for normal sequencing details. Execute the work from a separate checkbox implementation plan under `docs/superpowers/plans/`.

The executable plan must convert every unresolved runtime SQL decision into an explicit spike task that runs before any dependent call-site migration. No implementation task may depend on an unresolved SQL semantics decision. If a spike rejects a proposed interface shape, stop and update the executable plan before migrating call sites; do not add resolver-side hacks after migration.

Final design constraints that should not be reopened during implementation:

- Keep the exported `core.Dialect` interface minimal (`Name()` only). Add package-level or narrow capability interfaces as consuming patches need them; do not turn `core.Dialect` into one broad public interface.
- Do not expose a public `SetDialect` or mutable dialect injection API.
- `app.Dialect()` always means the data DB dialect; aux DB paths stay SQLite-only and never route through the data dialect.
- Runtime-sensitive SQL behavior (`JSON_TABLE` scalar values, request-body JSON binding, JSON extraction comparison semantics, JSON array length normalization, and `strftime(...)` datetime parsing) must be proven by runtime MySQL spikes before call-site migration.
- Prefer explicit safe contracts over string sentinels. In particular, count-column override should use `(column string, ok bool)` semantics rather than returning `""` as an implicit sentinel if this capability is introduced.
- `JSONEachColumnExpr(...)` and `JSONEachParamExpr(...)` are string-normalized table-valued helpers for PocketBase relation/file/select/request-body scalar values. They are not a general-purpose native JSON scalar comparison engine unless a later runtime spike explicitly broadens that contract.
- `JSONExtractExpr(...)` must not be locked as comparison-agnostic until the runtime spike proves it preserves string equality, numeric ordering, null fallback, object/array text behavior, scalar fallback, and `:lower` modifier composition. If one expression cannot preserve those semantics, make the contract operator/context-aware before migration.
- `strftime(...)` is behavior-porting work, not just dialect cleanup. Keep it isolated in its own execution phase after raw expression-valued multi-match identifiers are proven.
- Bootstrap must clean up any partially opened DB resources if an error occurs after data DB handles are opened, including later aux DB, logger, migration, collection reload, or settings reload failures.
- Env-var dialect tests must not use `t.Parallel()`. `PB_DATABASE_DRIVER` may remain in focused dialect/connect tests only; behavior tests must not rely on mutating the env after app bootstrap.

### Important constraints discovered during review

- **Detection is currently lazy + env-var-first.** `isMySQLDataDB` is evaluated at each call site (e.g. inside `Field.ColumnType(app)`), and it returns `true` whenever `PB_DATABASE_DRIVER=mysql` *regardless of the actual opened driver*. Several tests depend on this: e.g. `TestSelectFieldMySQLColumnType` / `TestRelationFieldMySQLColumnType` call `tests.NewTestApp()` (which opens **SQLite**), then `t.Setenv("PB_DATABASE_DRIVER", "mysql")` **after** boot, and expect MySQL column types. A naive "compute dialect once at boot from `DriverName()`" approach **breaks these tests**. See the detection design below for the resolution.
- **Prove env-forced boot before relying on it.** The preferred test migration is to set `PB_DATABASE_DRIVER=mysql` before booting a SQLite-backed test app with a focused custom `DBConnect`, so the cached boot-time dialect is initialized consistently without requiring a real MySQL DSN. This must be validated before Patch 1 changes wrapper behavior. Patch 0 must be executable against the current codebase, so it proves the strategy through the existing `core.IsMySQLDataDB(app)` wrapper; Patch 1 adds the permanent `app.Dialect().Name()` coverage after `Dialect()` exists. The custom connector must support full bootstrap, including the aux DB path, while opening SQLite handles for both data and aux files; the expected state is data dialect forced to MySQL and aux operations still SQLite-compatible. If this SQLite-backed strategy fails, do not blindly migrate tests this way; either keep transitional wrappers lazy until those tests are redesigned or introduce narrower package-internal dialect tests.
- **The aux DB is always SQLite**, even when the data DB is MySQL. `app.Dialect()` describes the **data** DB only and must never be applied to aux-DB operations (`AuxDB`, `AuxConcurrentDB`, `AuxNonconcurrentDB`).
- **`tools/search` and `tools/dbutils` cannot import `core`** (would create an import cycle). This is the real reason those packages use anonymous interface assertions on the resolver and receive dialect info as primitives (strings/bools) rather than a `Dialect` value. This pattern must be preserved.
- **`tools/dbutils.JSONEach` currently has its own `PB_DATABASE_DRIVER` branch.** The refactor must remove that env read. After all `core` callers are migrated to `app.Dialect().JSONEachColumnExpr(...)`, `dbutils.JSONEach` becomes a SQLite-only fallback/helper.
- **`tools/search.TokenFunctions["strftime"]` is data-query SQL, not aux-only logging.** MySQL must support `strftime(...)` filters through a dialect-aware token-function primitive; do not whitelist it as an ignored SQLite-only helper. By contrast, `geoDistance(...)` uses portable trigonometric functions (`acos`, `cos`, `radians`, `sin`) and remains unchanged unless runtime QA proves otherwise.
- **This refactor has intentional public Go API breaks.** Adding `Dialect() Dialect` to `core.App` affects any external code that manually implements the public `core.App` interface. Changing `tools/search.TokenFunctions` callable shape affects direct in-repo and external users of that map. Making `tools/dbutils.JSONEach` SQLite/default-only changes its current env-sensitive behavior for external callers. Capture these breaks in patch notes/release notes instead of treating them as invisible internal cleanup.
- **Custom `DBConnect` must use one consistent data driver per app instance.** `PB_DATABASE_DRIVER=mysql` remains an authoritative dialect override for compatibility, including tests. If a custom `BaseAppConfig.DBConnect` ignores that env var and opens SQLite, `app.Dialect()` will still report MySQL. This env-vs-driver mismatch is intentional for focused tests and must not fail validation by itself. The validation scope is only that the concurrent and nonconcurrent data DB handles report the same driver when both expose `DriverName() string`; a single app instance cannot safely use different data drivers for read/write handles.
- **Do not declare unimplemented dialect capability methods.** If Patch 1 introduces one broad `Dialect` interface with all methods listed below, both `SQLiteDialect` and `MySQLDialect` must provide real, tested implementations for every method in that same patch. Do not add compile-only placeholder methods that return knowingly wrong SQL or panic. Preferred execution is to grow the interface incrementally, or split it into narrow package-internal capability interfaces, so each patch adds only the methods it consumes and tests.
- **Runtime SQL decision gates must happen before call-site migration.** MySQL JSON extraction, `JSON_TABLE` scalar behavior, `JSONArrayLengthExpr(...)`, and `strftime(...)` datetime parsing are not just generated-SQL formatting details. Prove the exact SQL expressions with runtime MySQL before replacing existing call sites. If the proposed comparison-agnostic `JSONExtractExpr(column, path)`, `JSONEachParamExpr(paramName)`, `JSONArrayLengthExpr(column)`, or `StrftimeExpr(args)` contract cannot preserve current behavior, revise the interface first rather than migrating call sites and adding resolver-side hacks later.

## Design

### Dialect interface

The final target interface can contain the methods below, but implementation should not force all of them into Patch 1 unless every method is implemented and tested there. To keep patches small and safe, either:

- grow `Dialect` incrementally as each patch migrates a category of call sites, or
- keep `Dialect` as a small base interface (`Name()` initially) and add narrow capability interfaces used by call sites, for example `ColumnDialect`, `IntrospectionDialect`, `SearchDialect`, `SchemaSyncDialect`, `MaintenanceDialect`, and `MigrationDialect`.

Do not use stubs as a sequencing shortcut. A method should be added to an interface only in the same patch that adds its real implementation, consuming call site, and focused tests.

Patch 1 should prefer a deliberately small interface such as:

```go
type Dialect interface {
    Name() string
}
```

Only add the broader methods below as their consuming patches introduce real, tested implementations. This avoids importing `tools/search`, `dbx`, or `slog` into dialect code before those capabilities are actually used.

Final target shape for `core/db_dialect.go` (not Patch 1 content):

```go
type Dialect interface {
    Name() string  // DialectSQLiteName, DialectMySQLName

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
    StrftimeExpr(args []search.TokenFunctionArg) (*search.ResolverResult, error)

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
    CountColumn(isView bool) string // SQLite non-view: "_rowid_"; all other cases: "" (use provider default)

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

Add dialect name constants and use them instead of raw string comparisons outside tests:

```go
const (
    DialectSQLiteName = "sqlite"
    DialectMySQLName  = "mysql"
)
```

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

Put exported search dialect primitive types in a dedicated file such as `tools/search/dialect_primitives.go` (or an equivalently named focused file), not opportunistically inside `filter.go`. This keeps the public-ish `tools/search` API discoverable and avoids burying cross-package types in implementation files.

Initial operator matrix to preserve current behavior:

| Dialect | Branch | `EqualOp` | `NullEqualOp` | `NullConcat` | `NullExpr` |
|---|---|---|---|---|---|
| SQLite | `=` | `=` | `IS` | `OR` | `IS NULL` |
| SQLite | `!=` | `IS NOT` | `IS NOT` | `AND` | `IS NOT NULL` |
| MySQL | `=` | `=` | `IS` | `OR` | `IS NULL` |
| MySQL | `!=` | `<>` | `<>` | `AND` | `IS NOT NULL` |

If MySQL equal-branch null-safe comparison is later changed to `<=>`, add targeted tests that prove the new expression preserves PocketBase's empty-string/null fallback behavior before changing the matrix.

Add a small token-function argument value type in `tools/search` for dialect-aware token functions without importing `core`:

```go
type TokenFunctionArg struct {
    Type     fexpr.TokenType
    Literal  string
    Resolved *ResolverResult
}
```

`core.Dialect.StrftimeExpr(args []search.TokenFunctionArg)` should return a fully resolved `*search.ResolverResult`. The argument slice includes primitive token metadata plus resolved identifiers/params so the dialect can validate SQLite-compatible format/modifier semantics and map them to MySQL SQL without requiring `tools/search` to import `core` or pass raw `fexpr.Token` values into dialect code. Dialect implementations must not mutate `TokenFunctionArg.Resolved`; they must return a new `ResolverResult` and, when needed, a cloned/updated multi-match subquery value identifier. Clone the `MultiMatchSubquery` struct before changing `ValueIdentifier`; clone its `Params` map; clone the `Joins` slice header before reusing existing join pointers (the joins are treated as immutable in this path). Never mutate the original `Resolved.MultiMatchSubQuery` in place.

Do not add `TokenFunctionArg` in Patch 1 unless Patch 1 also introduces the first real consumer (`StrftimeExpr(...)`) and tests. Preferred sequencing is to add this type in Patch 4D, when the token-function callable shape and resolver delegation are implemented. This avoids adding public package surface that is not yet used.

`JSONEachColumnExpr(column)` returns a **bare table-valued expression without an alias**. Callers remain responsible for aliasing via `registerJoin(tableExpr, alias, ...)`, `search.Join.TableAlias`, or explicit SQL formatting. This keeps SQLite `json_each(...)` and MySQL `JSON_TABLE(...)` usable in the existing join patterns.

`JSONEachParamExpr(paramName)` receives the **bare dbx parameter name** (for example `dataEachTEST`, not `{:dataEachTEST}`) and returns a bare table-valued expression using the placeholder (`json_each({:dataEachTEST})` for SQLite, `JSON_TABLE({:dataEachTEST}, ...)` for MySQL). This avoids mixed call sites where some pass a placeholder and others pass a raw name. The initial contract is that the returned expression must work with the existing request-body binding (`json.Marshal(...)` output stored as a Go `[]byte`). If runtime MySQL proves that this cannot work reliably with an expression-only change, revise the call-site contract in Patch 4C to bind a string (for example `string(bodyItemsRaw)`) and add tests that assert both generated SQL and bound param value types. Do not silently change param types without coverage.

Both JSON-each methods must expose exactly one column named `value`, with scalar JSON string values usable without JSON quotes for relation/file/select ID comparisons. The MySQL implementation must document the chosen `JSON_TABLE` column type and length. Do not assume `VARCHAR(255)` is always sufficient without checking relation IDs, file values, select values, and request-body arrays; if a wider type is needed and MySQL supports it in `JSON_TABLE`, prefer the wider type or document the truncation tradeoff explicitly. Runtime QA must prove the bound-value behavior for Go `[]byte` JSON payloads and for string array values longer than typical 15-character record IDs.

The existing `tools/dbutils.JSONEach` MySQL branch uses `JSON_TABLE(... COLUMNS(value VARCHAR(255) PATH '$'))`. Do not copy that `VARCHAR(255)` assumption into `MySQLDialect.JSONEachColumnExpr(...)` / `JSONEachParamExpr(...)` unless Patch 4C runtime tests prove it is sufficient. Patch 4C acceptance requires either a proven wider/safe column type or explicit documentation of the length tradeoff, plus tests for long scalar values.

`JSONExtractExpr(column, path)` owns JSON path extraction for JSON/geo filter fields. This closes the current `dbutils.JSONExtract(...)` gap in `RecordFieldResolver`: SQLite keeps the existing `json_valid/json_object/JSON_EXTRACT` wrapper, MySQL uses a matching expression that preserves scalar/string comparison semantics instead of blindly returning quoted JSON strings, and future dialects do not need to touch resolver business logic. The method is intentionally comparison-agnostic: it receives only `column` and `path`, not the operator or right operand type. Therefore the returned MySQL expression must work for string, numeric, and null comparisons without resolver-side hacks; if tests prove this is impossible, revisit this interface before implementation. The MySQL implementation must define and test root path, object path, scalar non-JSON column, string comparison, numeric comparison, and null comparison behavior; do not leave this as an unspecified "MySQL-safe" placeholder. Start by testing an expression such as `CASE WHEN JSON_VALID([[col]]) THEN JSON_UNQUOTE(JSON_EXTRACT([[col]], '$.path')) ELSE JSON_UNQUOTE(JSON_EXTRACT(JSON_OBJECT('pb', [[col]]), '$.pb.path')) END`, but change it if numeric comparison semantics differ from SQLite/PocketBase expectations. Document whether MySQL JSON strings, numbers, objects, arrays, and SQL NULL are returned as unquoted strings or JSON text so equality/null fallback behavior is predictable. After `RecordFieldResolver` is migrated, `dbutils.JSONExtract` is either removed if unused or documented as a SQLite/default helper like `dbutils.JSONEach`.

`JSONArrayLengthExpr(column)` must preserve the current `dbutils.JSONArrayLength(...)` normalization contract, not just call a native length function. SQLite currently returns `0` for empty string or SQL NULL, treats scalar non-array values as a one-element array, and returns array length for arrays. The MySQL implementation must define and test empty string, SQL NULL, scalar non-JSON string/number, array, object, invalid JSON, and JSON scalar values. If object behavior differs from SQLite expectations, document it explicitly and add runtime coverage before migrating `:length` call sites.

Treat MySQL `JSONExtractExpr(...)` as a Patch 4C.0 decision gate, not only a generated-SQL cleanup. Before adding the method to the broad dialect interface or migrating any call sites, prove with runtime MySQL that the chosen expression preserves numeric ordering, string equality, object/array text behavior, and null fallback semantics. The spike must end with one explicit decision: keep the comparison-agnostic `JSONExtractExpr(column, path)` contract, change the method to be operator-aware, or pass resolver field/value metadata. If one comparison-agnostic expression cannot preserve both string and numeric behavior, stop and revise the interface before migration instead of adding resolver-side hacks after migration.

The Patch 4C.0 JSON extraction spike must also cover modifier composition for `:lower`, because `RecordFieldResolver` may wrap JSON extraction results in `LOWER(...)`. Define whether MySQL lower-cases JSON numbers/booleans/objects/arrays via string coercion, rejects unsupported shapes, or preserves SQLite-like behavior. Add generated-SQL or runtime coverage for at least one JSON `:lower` filter so modifier composition does not become an unreviewed behavior change.

`StrftimeExpr(args)` owns `strftime(...)` search-token SQL. SQLite returns the current `strftime(...)` expression and preserves `NullFallbackEnforced` plus multi-match `ValueIdentifier` behavior. MySQL must translate the SQLite-compatible subset used by PocketBase filters to MySQL expressions. At minimum support the existing accepted shapes from `tools/search/token_functions.go`: `strftime(format)`, `strftime(format, timeValue)`, and `strftime(format, timeValue, modifier...)`, with the same validation for first argument text, time-value token types, string modifiers, max 10 args, params merging, and multi-match propagation. Unsupported SQLite format substitutions or modifiers should return a clear filter build error rather than silently generating wrong SQL. Add tests for common formats used by clients (`%Y`, `%m`, `%d`, `%H`, `%M`, `%S`, `%Y-%m-%d`, `%Y-%m-%d %H:%M:%fZ`) and at least one unsupported modifier/format failure.

When `strftime(...)` is refactored, the SQLite/default fallback must also stop mutating resolver-owned multi-match state in place. Today the implementation updates `timeValueArgResult.MultiMatchSubQuery.ValueIdentifier` directly; Patch 4D must clone the `MultiMatchSubquery`, clone its `Params` map, clone the `Joins` slice header, and then update the clone. This cloning requirement applies to both the default fallback and dialect-specific implementations.

`TokenFunctionArg.Type` is the original parsed token type, not the resolved SQL type. The initial MySQL implementation should treat only `fexpr.TokenNumber` literals as numeric time values requiring an explicitly supported modifier such as `unixepoch`; identifier and text/param time values are treated as date/datetime strings unless runtime tests prove a broader numeric-identifier contract is required. Do not infer Go/SQL value types from `ResolverResult.Identifier` string formatting.

MySQL `StrftimeExpr` translation target:
- Map SQLite format tokens to MySQL `DATE_FORMAT` tokens: `%Y -> %Y`, `%m -> %m`, `%d -> %d`, `%H -> %H`, `%M -> %i`, `%S -> %S`, `%f -> %f`. For `%f`, document and test MySQL microsecond precision; if SQLite-compatible millisecond precision is required by existing tests, normalize with `LEFT(DATE_FORMAT(..., '%f'), 3)` or an equivalent expression rather than returning six digits silently.
- `strftime(format)` maps to `DATE_FORMAT(UTC_TIMESTAMP(3), mappedFormat)` so it remains deterministic about UTC semantics rather than relying on server-local time.
- `strftime(format, timeValue)` maps to `DATE_FORMAT(timeValue, mappedFormat)` for text/date/datetime column values and text parameters. Numeric time values are rejected unless paired with an explicitly supported modifier.
- Initially support only these modifiers: `unixepoch` for numeric Unix seconds (`FROM_UNIXTIME(value)` before `DATE_FORMAT`) and `utc` as a no-op because PocketBase stores UTC strings. Reject `localtime`, timezone offsets, relative date math (`+1 day`, `start of month`, etc.), Julian day handling, and unknown modifiers with a clear `[strftime] unsupported MySQL modifier ...` error.
- Preserve multi-match behavior by applying the same MySQL expression to both the regular time-value identifier and `MultiMatchSubQuery.ValueIdentifier`.

Run a SQLite baseline test and a MySQL datetime spike before finalizing this implementation. First record the current SQLite output shape for representative inputs such as `strftime('%Y-%m-%d %H:%M:%fZ', '2026-01-02 03:04:05.123Z')`; then test `DATE_FORMAT` with PocketBase-style UTC strings with trailing `Z` and fractional seconds, plus the same value without `Z`. If MySQL does not parse the stored format reliably, wrap the time value with an explicit normalization such as `STR_TO_DATE(...)` or `REPLACE(..., 'Z', '')` and document the accepted input formats. The `%f` mapping must match the recorded SQLite-compatible output shape or be documented as an intentional behavior difference; do not silently return six microsecond digits if existing behavior expects milliseconds.

### App wiring & dialect detection

- `BaseApp` gets a `dialect Dialect` field
- `App` interface gets a `Dialect() Dialect` method
- Do **not** add `SetDialect(Dialect)` to the public `App` interface. A mutable public dialect setter can make runtime SQL generation diverge from the actual opened DB. For tests that need MySQL semantics without a MySQL connection, set `PB_DATABASE_DRIVER=mysql` before booting a SQLite-backed test app with the focused custom connector from Patch 0 so the cached boot-time dialect is initialized consistently.
- Ensure transaction/shallow-copy app paths inherit `dialect`. All `txApp.Dialect()` calls in migrations/schema sync must see the same data dialect as the parent app. The transaction clone path lives in `core/db_tx.go:createTxApp()`; do not assume this is only a `core/base.go` change.
- `Dialect()` must be nil-safe before/without boot: if the `*BaseApp` receiver itself is nil, return `DialectForDriver("")`; if `app.dialect` is unset, return `DialectForDriver(driverNameFrom(app.ConcurrentDB()))` when possible, otherwise `SQLiteDialect{}`. The helper must guard nil DB handles: `driverNameFrom(db dbx.Builder) string` returns `""` unless `db != nil` and implements `DriverName() string`. Transactions commonly expose `*dbx.Tx`, so driver-name fallback must not assume every `dbx.Builder` has a driver name.
- The transitional wrappers (`isMySQLDataDB` / `IsMySQLDataDB`) should avoid panics for nil interface values, typed nil `*BaseApp` values, and unbootstrapped app values while they still exist. If arbitrary external typed-nil `core.App` implementations cannot be made safe without reflection, document that limitation in the wrapper comment and tests; do not accidentally call `app.ConcurrentDB()` or `app.Dialect()` on a typed-nil value before checking known safe cases.

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
- `initDataDB()` opens both concurrent and nonconcurrent data handles. Derive the cached dialect from the concurrent handle for compatibility, but also validate that both handles report the same `DriverName()` when the method is available. If both data handles expose `DriverName() string` and the names differ, bootstrap must fail with a clear error. This validation must compare the two data handles to each other, not reject an intentional env-forced dialect that differs from the opened driver in focused tests. A custom stateful `DBConnect` that returns different drivers for read/write handles is unsupported because schema sync and query generation need one data dialect.
- Any `initDataDB()` error after one or both data DB handles have opened must close all successfully opened handles before returning, including the new driver-mismatch validation path. Do not add a leak-prone failure path while tightening validation.
- **Do not** switch to "driver name only" — that was the original (rejected) idea and it breaks the env-var-only column-type tests described in Current State.
- Because the affected tests call `t.Setenv(...)` **after** `NewTestApp()` (i.e. after `initDataDB` already ran), Patch 1 or Patch 2 must update those tests before the call sites switch to cached `app.Dialect()` semantics. Patch 7 only verifies no behavior tests still use env mutation. Avoid exposing a public or test-only dialect setter solely for these tests unless a future non-test use case appears.
- `IsMySQLDataDB` is kept during transition, removed in Patch 7. To keep Patch 1 behavior-neutral, either keep the wrapper on the old lazy env-var-first logic until the post-boot env-var tests are migrated, or migrate those tests in Patch 1 before switching the wrapper to `return app.Dialect().Name() == "mysql"`. Do not claim Patch 1 is behavior-neutral if the wrapper immediately uses the cached boot-time dialect while tests still mutate `PB_DATABASE_DRIVER` after boot.
- After `ResetBootstrapState()`, the cached dialect is cleared and `Dialect()` falls back to env/driver detection again because the app is unbootstrapped. This means changing `PB_DATABASE_DRIVER` after reset can change the fallback result until the next bootstrap. Add focused coverage for this behavior rather than treating it as a stale-cache bug.

### tools/search wiring (no new package needed)

The existing interface assertion pattern in `tools/search` is extended. `tools/search` cannot import `core`, so it only ever receives **primitive** dialect info via assertions on the resolver — never a `Dialect` value or a generic `IsMySQL` boolean (a bare boolean would just recreate the dialect conditional we are removing and would not extend to a third dialect).

- `filter.go`: `buildResolversExpr` already receives `likeEscape string` as a parameter. Add an `EqualityOperators` parameter the same way, computed from the resolver in `resolveTokenizedExpr()` via `interface{ EqualityOperators() EqualityOperators }` assertion (defaulting to the SQLite operators when the assertion fails). This removes the direct `PB_DATABASE_DRIVER` read at `filter.go:347` without losing MySQL's `<>`, `AND`, and `IS NOT NULL` not-equal behavior.
- `filter.go`: `resolveTokenizedExpr()` must compute all resolver-provided primitives (`likeEscape`, `EqualityOperators`, and LIKE column expression behavior) before calling `buildResolversExpr(...)`; do not only update `buildResolversExpr()` and forget its caller.
- `filter.go`: dialectize LIKE expressions where the right operand is another column. SQLite keeps `left LIKE ('%' || right || '%') ESCAPE '\\'`; MySQL uses `left LIKE CONCAT('%', right, '%') ESCAPE '\\\\'`. Do not only change `LikeEscapeClause()` because the string concatenation operator is also dialect-specific. If the primitive is `LikeColumnContainsExpr(left, right, escape string, negated bool) string`, the resulting `dbx.NewExp(...)` must still carry `left.Params`; parameter-backed LIKE branches must continue using `mergeParams(left.Params, wrapLikeParams(right.Params))`.
- `filter.go`: add a SQLite/default helper for LIKE column-operand formatting, e.g. `defaultLikeColumnContainsExpr(left, right, escape string, negated bool) string`, and use it whenever the resolver does not implement `interface{ LikeColumnContainsExpr(left, right, escape string, negated bool) string }`. Generic resolvers must not be forced to implement dialect primitives.
- `filter.go`: `manyVsManyExpr` and `manyVsOneExpr` build SQL later, after the resolver is no longer available. Store `likeEscape` and `EqualityOperators` on these expression structs when they are created. Do not call `defaultLikeEscapeClause()` from their `Build()` methods after this refactor.
- `token_functions.go`: change the token-function callable shape so token functions receive the `FieldResolver` in addition to the recursive token resolver callback. The current signature only receives `func(fexpr.Token) (*ResolverResult, error)`, so `strftime(...)` cannot assert resolver primitives without this change. Use an explicit type, for example:
  ```go
  type TokenFunction func(
      fieldResolver FieldResolver,
      argTokenResolverFunc func(fexpr.Token) (*ResolverResult, error),
      args ...fexpr.Token,
  ) (*ResolverResult, error)
  ```
  Update `resolveToken()` to call `fn(fieldResolver, func(argToken fexpr.Token) (*ResolverResult, error) { return resolveToken(argToken, fieldResolver) }, args...)`. Then add a resolver primitive assertion for dialect-aware `strftime(...)`, e.g. `interface{ StrftimeExpr(args []TokenFunctionArg) (*ResolverResult, error) }`. When the resolver implements it, `TokenFunctions["strftime"]` delegates to that method after resolving and validating args. When not implemented, keep the existing SQLite/default `strftime(...)` behavior. `tools/search` must not infer dialect from env vars.
- `sort.go`: `SortField.BuildExpr(fieldResolver)` already receives the resolver. Instead of an `IsMySQL() bool` flag, assert a method that returns the concrete rowid sort expression, e.g. `interface{ RowidSortIdentifier() string }` — SQLite returns `[[_rowid_]]`, MySQL returns the resolved `id` identifier, a future dialect returns its own. Constrain this primitive to return a simple unqualified identifier placeholder, not an arbitrary SQL expression; `BuildExpr` then just formats `"<identifier> <direction>"` and `search.Provider` remains responsible for first-table prefixing when needed. If a future dialect needs expression-valued rowid sorting, change the primitive to return structured metadata (`Identifier`, `AlreadyQualified`, `IsExpression`) before implementation rather than extending string heuristics. This removes the direct `PB_DATABASE_DRIVER` read at `sort.go:35` and keeps the dialect as the single source of truth.
- `multi_match_subquery.go`: `search.Join.TableName` is currently always passed through `db.QuoteTableName(...)`. Add a raw table-expression flag, e.g. `RawTableExpr bool`, for table-valued expressions such as `json_each(...)` and `JSON_TABLE(...)`. `Build()` should skip `QuoteTableName` only when this flag is true, while still quoting `TableAlias`. This is required for dialect `JSONEachColumnExpr(...)` results used in multi-match joins. Also add an explicit raw/expression value path for expression-valued `MultiMatchSubquery.ValueIdentifier` (for example `strftime(...)`, `DATE_FORMAT(...)`, or JSON extraction) because `Build()` currently quotes `ValueIdentifier`; do not rely on accidental `db.QuoteColumnName(...)` formatting.
- `record_field_resolver.go`: `registerJoin(tableName, tableAlias, on)` must also support raw table-valued expressions for main-query joins, not only multi-match joins. Add either `registerJoinExpr(tableExpr, tableAlias string, on dbx.Expression)` or extend `registerJoin(tableName, tableAlias string, on dbx.Expression, rawTableExpr ...bool)`. All `JSONEachColumnExpr(...)` and `JSONEachParamExpr(...)` joins must use the raw-expression path so `json_each(...)` / `JSON_TABLE(...)` are not quoted as literal table names when `RecordFieldResolver.UpdateQuery(...)` applies joins to the main query. When `RawTableExpr` is true, skip collection/list-rule lookup in `registerJoin` because the table name is a dialect SQL expression, not a collection/table identifier. Add generated-SQL coverage for both raw and regular joins in the main query path.
- Raw join support must be additive. Preserve the current compatibility behavior where non-collection expression/subquery table strings can fail `loadCollection(tableName)` and still be registered when hidden-field list-rule checks do not apply. `RawTableExpr` should prevent accidental collection/list-rule lookup for known SQL expressions, but regular non-collection expression joins must not become hard errors solely because this flag was introduced.
- `record_field_resolver.go`: raw join rendering must be shared by both `UpdateQuery(...)` and `updateQueryWithCollectionListRule(...)`. The latter applies joins collected by cloned list-rule resolvers and can contain the same JSON table-valued expressions. Do not fix only the primary join loop.
- `record_field_resolver.go`: preserve dbx `LeftJoin` raw-string behavior in the main-query path. Do not manually quote regular or raw main-query joins unless generated-SQL tests prove dbx requires it; current call sites pass dbx-style raw table strings such as `json_each(...) alias` and `{{alias}}` placeholders.
- `tools/dbutils/json.go`: all `JSONEach` callers in `core` are replaced with `app.Dialect().JSONEachColumnExpr(column)`. Then remove the `PB_DATABASE_DRIVER` branch from `dbutils.JSONEach`; it is kept as a SQLite-only fallback/helper.
- `tools/dbutils/json.go`: migrate `core` JSON path extraction from `dbutils.JSONExtract(column, path)` to `app.Dialect().JSONExtractExpr(column, path)`. Then either remove `dbutils.JSONExtract` if unused or keep it documented as a SQLite/default helper with no env-var dialect detection.
- `record_field_resolver_runner.go`: request-body `@request.body.<field>:each` joins currently hardcode `json_each({:param})`; replace them with `app.Dialect().JSONEachParamExpr(paramName)` where `paramName` is the bare dbx parameter name without `{:...}`. MySQL can use a `JSON_TABLE({:param}, '$[*]' COLUMNS(value ... PATH '$'))` expression. The returned table-valued expression must expose a column named `value` and must not include an alias; existing callers continue to alias the join and reference `[[alias.value]]`. Use the dialect's documented `value` column type consistently for relation/file/select IDs and add runtime coverage proving string JSON array values compare correctly against record IDs without JSON quote mismatches or silent truncation.
- `record_field_resolver_runner.go`: validate the MySQL `JSONEachParamExpr(...)` implementation with runtime MySQL because request body `:each` binds `json.Marshal(...)` output as a Go `[]byte`. If MySQL requires casting or string conversion, implement the dialect expression accordingly (for example `JSON_TABLE(CAST({:param} AS JSON), ...)` if that is what the driver requires) rather than assuming plain `JSON_TABLE({:param}, ...)` works.
- MySQL JSON table-valued expression support targets MySQL 8.x. MariaDB compatibility is not part of this refactor unless explicitly added and runtime-tested; `JSON_TABLE` and JSON casting behavior differ across engines/versions.
- Runtime QA must assert the database engine/version before JSON_TABLE-dependent tests run. When `--skip-docker` points at MariaDB or an old MySQL version, fail early with an explicit unsupported-engine message rather than surfacing confusing SQL syntax/runtime failures.
- `record_field_resolver_runner.go`: multivalue `:length` currently uses SQLite `dbutils.JSONArrayLength(...)`; replace it with `app.Dialect().JSONArrayLengthExpr(column)` or explicitly mark `:length` as an out-of-scope MySQL gap before implementation. Prefer migrating it in this refactor because it is query-generation dialect behavior.
- Generic `tools/search` resolvers without dialect primitive methods default to SQLite behavior. MySQL behavior is provided by resolvers like `RecordFieldResolver` that implement the primitive methods; `tools/search` must not infer the data dialect from env vars.
- `tools/search.SimpleFieldResolver` JSON path extraction remains SQLite/default behavior unless a future resolver primitive is introduced. This is intentional because generic search resolvers have no app/dialect context; do not add env-var detection there.
- `tools/search.TokenFunctions["geoDistance"]` remains unchanged because the generated trigonometric SQL is portable to SQLite and MySQL; include it in runtime QA only if a MySQL failure appears while testing nearby token-function changes.

### Aux DB caveat

`app.Dialect()` is the **data** dialect only; the aux DB is always SQLite. The periodic cron in `base.go` currently runs `PRAGMA wal_checkpoint(TRUNCATE)` against **both** the main and aux DBs unconditionally, plus `PRAGMA optimize` against the main DB (guarded today by `!isMySQLDataDB`):

- The aux-DB `wal_checkpoint` must **always** use SQLite behavior — do not route it through `app.Dialect()`.
- The main-DB `wal_checkpoint` is also SQLite-specific and currently errors (swallowed as a warning) when the data DB is MySQL. Fold both the main-DB `wal_checkpoint` and `PRAGMA optimize` into a single data-dialect maintenance call (see `RunPostSyncOptimize` discussion) so MySQL becomes a clean no-op instead of a logged error.
- Backup creation in `core/base_backup.go` also runs `PRAGMA wal_checkpoint(TRUNCATE)` against both data and aux DBs. Route only the **data** DB checkpoint through the dialect (`Checkpoint`); keep the aux checkpoint as an unconditional SQLite call.
- `core/log_query.go` uses SQLite `strftime` intentionally because logs live in the aux DB. Do not route log queries through the data dialect.

## Patch Breakdown

### Patch 0: Validate env-forced SQLite test bootstrap

Before changing `IsMySQLDataDB` wrappers or migrating post-boot env-var tests, prove that the preferred test migration strategy actually works with the current SQLite-backed test app.

**Files:**
- `core/db_dialect_test.go` — new focused dialect factory/bootstrap spike test, or `core/base_test.go` if the project prefers bootstrap tests there

**Required current-state spike test:**
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

The custom `DBConnect` is intentional. With the default connector, setting `PB_DATABASE_DRIVER=mysql` before `tests.NewTestApp()` attempts a real MySQL connection and requires `PB_DATABASE_DSN`; that does not validate the desired SQLite-backed test strategy. Patch 0 runs before `app.Dialect()` exists, so it proves the strategy through the existing `core.IsMySQLDataDB(app)` wrapper. Patch 1 adds the permanent `app.Dialect().Name() == core.DialectMySQLName` assertion after the new API exists.

The custom connector must allow the entire app bootstrap to complete, including aux DB initialization, while opening SQLite for both data and aux paths. This deliberately proves only dialect detection semantics, not MySQL connectivity. Patch 1 should add permanent coverage that the data dialect is forced to MySQL while aux-only operations still use SQLite-compatible SQL and are not routed through `app.Dialect()`.

**Decision gate:**
- **Path A (spike passes):** Patch 1 may use cached boot-time dialect semantics and must add equivalent `app.Dialect().Name()` coverage after `Dialect()` exists. Patch 1 or Patch 2 must migrate env-var-only field/query tests by setting `PB_DATABASE_DRIVER=mysql` before a SQLite-backed test app is booted with the focused custom connector, or by replacing those tests with narrower dialect method tests.
- **Path B (spike fails):** do **not** migrate those tests blindly. Keep transitional wrappers on the old lazy env-var-first logic until a safe test strategy is chosen, or replace those behavior tests with narrower resolver/dialect unit tests that do not require MySQL DDL on a SQLite test app. Do not add a public `SetDialect` solely for tests.

**Verification:**
- Run the new spike test directly.
- Keep or remove the spike test based on usefulness after Patch 1; if kept, it becomes permanent coverage for env-forced bootstrap behavior.

### Patch 1: Add Dialect interface + implementations + App.Dialect() wiring

**Files:**
- `core/db_dialect.go` — rewrite: add `Dialect` interface, `SQLiteDialect`, `MySQLDialect`, `DialectForDriver(name string) Dialect` factory. If Patch 1 only introduces the base `Name()` dialect interface, do not import `tools/search` yet. When `EqualityOperators()` is introduced in Patch 4A, import/use `tools/search.EqualityOperators` rather than defining the type in `core`.
- `core/db_dialect.go` — add dialect name constants (`DialectSQLiteName`, `DialectMySQLName`) and use them for non-test dialect name comparisons instead of raw string literals
- `core/db_dialect.go` — add compile-time assertions only for the capability interfaces introduced in this patch, for example `var _ Dialect = SQLiteDialect{}` / `var _ Dialect = MySQLDialect{}` if Patch 1 keeps `Dialect` small. If Patch 1 introduces a broad final interface, it must also add real implementations and focused tests for every method in that interface.
- If Patch 1 implements complex methods before their call sites migrate (`StrftimeExpr`, `JSONExtractExpr`, schema conversion SQL, maintenance, and system DDL), add focused method-level tests in Patch 1. Otherwise, keep Patch 1 to interface/factory/simple wiring and move each complex method into the first patch that consumes it. Do not leave compile-only implementations that return knowingly wrong SQL.
- `tools/search/dialect_primitives.go` — add the `EqualityOperators` value type used by `tools/search` and returned by `core.Dialect.EqualityOperators()` only if Patch 1 introduces the first capability that consumes it; otherwise add this focused file in Patch 4A.
- `tools/search/dialect_primitives.go` or `tools/search/token_functions.go` — add the `TokenFunctionArg` value type only in Patch 4D unless Patch 1 also introduces `StrftimeExpr(...)` and its focused tests.
- `core/base.go` — add `dialect Dialect` field to `BaseApp` struct, set in `initDataDB()`, add nil-safe `Dialect()` method
- `core/base.go` — when setting the cached dialect, fail bootstrap if concurrent and nonconcurrent data DB handles both implement `DriverName() string` and report different drivers; document mismatches from custom `DBConnect` as unsupported because one app instance has only one data dialect
- `core/base.go` — close any successfully opened data DB handles before returning from `initDataDB()` on later errors, including the new driver-mismatch validation path and a failure opening the nonconcurrent handle after the concurrent handle already opened
- `core/db_tx.go` `createTxApp()` — ensure data and aux transaction apps inherit the parent data dialect; `clone := *app` should preserve the field, but keep this file in scope and test it explicitly
- `core/base.go` shallow-copy paths — ensure `UnsafeWithoutHooks()` inherits the parent data dialect
- `core/base.go` `ResetBootstrapState()` — clear `app.dialect = nil` together with DB handles so re-bootstrap cannot observe stale dialect state
- `core/app.go` — add `Dialect() Dialect` to `App` interface
- `core/app.go` — document this as an intentional public Go API break for external code that manually implements `core.App`
- `pocketbase.go` — keep in Patch 1 verification scope because it has `var _ core.App = (*PocketBase)(nil)` and embeds `core.App`. It should continue compiling after `BaseApp` implements `Dialect()`, but this public wrapper conformance must be checked explicitly.
- Public API note: external structs that embed `core.App` should inherit the new method through the embedded interface, while external structs that manually implement the full `core.App` interface must add `Dialect() Dialect`.
- Public API verification: search for `var _ core.App`, `var _ App`, and manual app implementations across `core/`, `apis/`, `cmd/`, `examples/`, `plugins/`, and tests. `go build ./...` catches in-repo implementations; release notes must include migration guidance for external implementations (`embed core.App`/`*core.BaseApp`, or add `Dialect() core.Dialect`).
- Keep `isMySQLDataDB` and `IsMySQLDataDB` without behavior change until env-var-only tests are migrated. Either keep their old lazy env-var-first logic in Patch 1, or move the affected test updates into Patch 1 before changing the wrappers to `return app.Dialect().Name() == "mysql"`.
- While wrappers still exist, make `IsMySQLDataDB(nil)` and unbootstrapped app values nil-safe. The wrapper must continue env-var-first behavior without panicking when `ConcurrentDB()` is nil.
- Do not add a public `SetDialect` or test-only dialect injection helper for this refactor. If Patch 0 Path A passes, migrate env-var-only tests by setting `PB_DATABASE_DRIVER=mysql` before booting a SQLite-backed test app with the focused custom connector, or replace them with narrower dialect method tests. If Path B applies, keep wrappers lazy and redesign those tests before switching wrappers or call sites to cached dialect behavior.
- Add focused tests for `Dialect()` before bootstrap, on a nil `*BaseApp` receiver if nil receiver support is kept, after SQLite bootstrap, env-forced MySQL bootstrap, data transaction inheritance, nested aux-to-data transaction inheritance (`AuxRunInTransaction` followed by `RunInTransaction`), aux transaction preserving the data dialect without treating aux operations as data-dialect operations, `UnsafeWithoutHooks().Dialect()`, and `ResetBootstrapState()` clearing stale dialect state.
- Add focused tests for `driverNameFrom(dbx.Builder)` fallback behavior: nil builder, a builder without `DriverName() string` such as transaction-like handles, and a builder exposing `DriverName() string`. This documents why transaction fallback cannot be the source of dialect truth.
- Add focused tests for `Dialect()` fallback after `ResetBootstrapState()` when `PB_DATABASE_DRIVER` changes while the app is unbootstrapped. This documents that fallback detection is environment-sensitive only when no cached boot-time dialect exists.
- Add focused tests for `IsMySQLDataDB(nil)`, a typed nil `*BaseApp` passed through the wrapper if supported, and an unbootstrapped app while the transitional wrappers still exist.
- Add a focused bootstrap failure test where custom `DBConnect` returns two data handles with different `DriverName()` values. The test should assert the error is clear and should not conflate this with the allowed env-forced MySQL dialect on SQLite handles.
- Add a focused bootstrap failure test proving already opened data handles are closed when `initDataDB()` fails after opening them. This can be a lightweight fake/open-handle counter if direct db close observation is awkward; do not leave the new validation path untested for leaks.
- Suggested test placement: pure factory tests in `core/db_dialect_test.go`; bootstrap/reset/unsafe-copy tests in `core/base_test.go`; transaction inheritance tests in `core/db_tx_test.go`.

**Verification:**
- `go build ./...` passes
- `go test ./...` passes
- `go test -tags no_default_driver ./core` or an equivalent no-default-driver compile/test target passes; if the full package test suite cannot run under this tag because custom `DBConnect` setup is required, document the narrower compile target used
- Do not assume the Patch 0 SQLite-backed spike also validates `no_default_driver`: under that tag the default SQLite driver blank import from `core/db_connect.go` is excluded. If a no-default-driver test opens SQLite through custom `DBConnect`, the test package must import the SQLite driver explicitly or use a compile-only target that does not open SQLite.
- No behavior change if call sites still use `isMySQLDataDB` wrappers; if wrappers now use `app.Dialect()`, the post-boot env-var mutation tests must already be migrated in this patch

### Patch 2: Migrate ColumnType call sites (re-grep before and after)

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
- Before editing, run a broad field scan such as `rg -n "jsonArrayColumnType|JSON DEFAULT '\\[\\]'|JSON NOT NULL|ColumnType\(app" core/field_*.go` and update the checklist if the current upstream baseline has additional field column-type branches. Do not rely only on the line-number list above.
- If `core/field_select_test.go`, `core/field_relation_test.go`, or any other field tests still set `PB_DATABASE_DRIVER=mysql` after `tests.NewTestApp()`, migrate them in this patch before replacing ColumnType call sites with cached `app.Dialect()` behavior. Do not wait until Patch 7 for these tests.

**Test migration grep:** `rg 't\.Setenv\("PB_DATABASE_DRIVER"|os\.Setenv\("PB_DATABASE_DRIVER"' -- '*_test.go'` should show only focused dialect/connect tests after Patch 2. At the time of writing, the known post-bootstrap behavior tests are `core/field_select_test.go` and `core/field_relation_test.go`.

**Verification:** `go build ./...` + `go test ./...`
- Additional grep: `rg -n "isMySQLDataDB|jsonArrayColumnType" core/field_*.go core/db_dialect.go` should show only transitional wrappers that are intentionally kept for later cleanup.

### Patch 3: Migrate introspection call sites (7 sites)

**Files & changes:**
- `core/db_table.go:13` `TableColumns` — `app.Dialect().TableColumnsQuery()`
- `core/db_table.go:46` `TableInfo` — `app.Dialect().TableInfoQuery()`
- `core/db_table.go:93` `TableIndexes` — `app.Dialect().TableIndexesQuery()`
- `core/db_table.go:151` `HasTable` — `app.Dialect().HasTableQuery()`
- `core/collection_record_table_sync.go:181` `recordTableExistsForSchemaSync` — execute `txApp.Dialect().HasTableQuery()` through `txApp.DB()` so schema-sync existence checks observe transaction state. Do not fall back to `app.HasTable(tableName)` from inside this transaction-sensitive helper.
- `core/collection_record_table_sync.go:236` view lookup — `txApp.Dialect().ViewsQuery()`
- `core/collection_validate.go:565` index name validation — add a dedicated dialect method `IndexOwnerQuery() string`. This is a cross-table index-owner lookup, not the same shape as table-scoped introspection, so do not overload `TableIndexesQuery()`. The current MySQL branch selects `TABLE_NAME` from `information_schema.STATISTICS`; the SQLite branch selects `tbl_name` from `sqlite_master`. Preserve that result semantics and add duplicate-index-name validation coverage for both dialects.

**Verification:** `go build ./...` + `go test ./...`

### Patch 4: Migrate filter/search/query call sites (legacy aggregate checklist)

> Execution note: do **not** implement this as one patch. Keep the detailed call-site checklist below for coverage tracking, but execute it as Patch 4A through Patch 4E. This split keeps equality/LIKE, raw joins, JSON extraction, `strftime`, and API/runtime QA failures isolated and revertable.

**Files & changes:**
- `core/record_field_resolver.go:85` `LikeEscapeClause` — `r.app.Dialect().LikeEscapeClause()`
- `core/record_field_resolver.go` — expose resolver methods for `EqualityOperators()`, `LikeColumnContainsExpr(...)`, `RowidSortIdentifier()`, and `StrftimeExpr(args []search.TokenFunctionArg)` using `r.app.Dialect()` so `tools/search` can obtain primitive dialect behavior without importing `core`
- `core/record_field_resolver_runner.go:62` `relationValueEquals` — `resolver.app.Dialect().RelationEqualityExpr(left, right)`
- `core/record_field_resolver_runner.go:70` `relationArrayContainsIdentifier` — `resolver.app.Dialect().JSONArrayContainsExpr(jsonCol, idCol)`
- `core/record_field_resolver_runner.go:359,378` request-body `json_each({:param})` joins — `resolver.app.Dialect().JSONEachParamExpr(paramName)`
- `core/record_field_resolver_runner.go:584,636,699,751` — 4 JOIN sites use `JSONArrayContainsExpr` / `JSONEachColumnExpr` via dialect
- Update both `registerJoin(dbutils.JSONEach(...), ...)` and `search.Join{TableName: dbutils.JSONEach(...)}` sites. Preserve existing `TableAlias` / join alias behavior because dialect methods return no alias. Main-query `registerJoin(...)` JSON table-valued-expression sites must use the new raw-expression register path; multi-match `search.Join{TableName: ...}` sites must set `RawTableExpr: true`.
- `core/record_field_resolver_runner.go:812,817` — `dbutils.JSONArrayLength(...)` becomes `resolver.app.Dialect().JSONArrayLengthExpr(...)`
- `core/record_field_resolver_runner.go:830,848` — `dbutils.JSONEach(...)` becomes `resolver.app.Dialect().JSONEachColumnExpr(...)`
- `core/record_field_resolver_runner.go:495,501,886,888` — JSON/geo/root path extraction `dbutils.JSONExtract(...)` becomes `resolver.app.Dialect().JSONExtractExpr(...)`
- `core/collection_query.go:42` sort — `app.Dialect().DefaultCollectionSort()`
- `core/collection_query.go:322,341,351` — `app.Dialect().IDCastType()`, `app.Dialect().RequiresSubqueryAlias()`
- `apis/record_crud.go:83` count — `countCol := e.App.Dialect().CountColumn(collection.IsView())`; call `searchProvider.CountCol(countCol)` **only when `countCol != ""`**. Do not call `CountCol("")`: the current provider would build invalid `COUNT(DISTINCT [[table.]])`, not `count(*)`. When the dialect returns `""`, leave the provider default count column (`id`) unless this patch explicitly changes `tools/search.Provider` to support `count(*)`.
- Count column behavior matrix: SQLite non-view collections return `_rowid_`; SQLite views return `""` (caller skips `CountCol`); MySQL non-view collections return `""` (caller skips `CountCol` and preserves the provider default `id` behavior); MySQL views return `""` (caller skips `CountCol`). Add focused coverage around `apis/record_crud.go` or the dialect methods so this does not regress to an empty identifier or a SQLite `_rowid_` on MySQL.
- `core/view.go:56,169` — `app.Dialect().RequiresSubqueryAlias()`
- `core/view.go:263` — `dbutils.JSONEach(cleanFieldName)` becomes `app.Dialect().JSONEachColumnExpr(cleanFieldName)` while preserving explicit `_je_file` aliasing
- `core/record_model.go:1536` — `dbutils.JSONEach(prefixedFieldName)` becomes `app.Dialect().JSONEachColumnExpr(prefixedFieldName)`
- `core/record_query_expand.go:113` — `dbutils.JSONEach(indirectRelField.Name)` becomes `app.Dialect().JSONEachColumnExpr(indirectRelField.Name)`
- `tools/search/filter.go:194-212` LIKE column operand formatting — use resolver-provided `LikeColumnContainsExpr(...)` instead of hardcoded SQLite `'||'` concatenation
- `tools/search/filter.go:347` `resolveEqualExpr` — add `EqualityOperators` param, computed from resolver via `interface{ EqualityOperators() EqualityOperators }` assertion (removes the direct env-var read and preserves MySQL null-safe equality behavior)
- `tools/search/filter.go:156-168` `resolveTokenizedExpr` — thread `likeEscape`, `EqualityOperators`, and LIKE column expression primitives into `buildResolversExpr(...)`
- `tools/search/filter.go:669-683,740-744` `manyVsManyExpr` / `manyVsOneExpr` — store `likeEscape` and `EqualityOperators` on the expression structs at creation time; remove `defaultLikeEscapeClause()` calls from `Build()`
- `tools/search/sort.go:35` — replace the direct env-var read with `interface{ RowidSortIdentifier() string }` assertion on the resolver (returns `[[_rowid_]]` for SQLite, resolved `id` identifier for MySQL); no `IsMySQL` boolean
- `tools/search/provider.go:263-270` — review `@rowid` provider prefixing after `RowidSortIdentifier()` is introduced. Either keep prefixing only for the SQLite/default rowid expression, or make `RowidSortIdentifier()` return an exact fully-qualified identifier where needed. Add provider-level tests, not only `SortField.BuildExpr` tests.
- Decide the public `@rowid` MySQL semantics before implementing Patch 4A. Preferred compatibility behavior is: keep accepting `@rowid` on MySQL and map it to the dialect's stable id identifier, while documenting that portable fields such as `-created,-id` remain recommended for cross-dialect clients. If the project instead wants to reject or strongly discourage `@rowid` on MySQL, change the primitive shape before implementation so it can return a clear error rather than only a string.
- `tools/search/token_functions.go:61-181` — make `strftime(...)` dialect-aware via resolver primitive delegation. Generic resolvers keep the current SQLite/default SQL. `RecordFieldResolver` delegates to `app.Dialect().StrftimeExpr(...)` for data DB filters.
- `tools/search/multi_match_subquery.go:51-54` — add and use a raw table-expression flag on `search.Join` so table-valued expressions returned by `JSONEachColumnExpr(...)` are not quoted as literal table names.
- `tools/search/multi_match_subquery.go:61-64` — add an explicit raw/expression value path for `MultiMatchSubquery.ValueIdentifier` (for example `ValueIdentifierRaw bool` or a separate `ValueExpression` field). Do not rely on `db.QuoteColumnName(...)` accidentally preserving function expressions; `strftime(...)`, `DATE_FORMAT(...)`, and JSON extraction expressions must render as expressions, not quoted column names.
- `core/record_field_resolver.go:408` — add raw table-expression support to `registerJoin(...)` or add a sibling `registerJoinExpr(...)`, and ensure `UpdateQuery(...)` respects `search.Join.RawTableExpr` when appending joins.
- `core/record_field_resolver.go` — use the same raw-join rendering helper in both `UpdateQuery(...)` and `updateQueryWithCollectionListRule(...)` so cloned list-rule resolvers do not regress.
- `tools/dbutils/json.go:12` `JSONEach` — remove the env-var MySQL branch after callers in `core` use `app.Dialect().JSONEachColumnExpr(column)` instead; `JSONEach` remains SQLite-only
- `tools/dbutils/json.go:44` `JSONExtract` — remove from `core` callers; either delete if unused or keep as SQLite/default-only helper with no env-var dialect detection
- `tools/dbutils/json.go` — document the public behavior change: `JSONEach` and any remaining JSON helpers are SQLite/default helpers and do not inspect `PB_DATABASE_DRIVER`. Treat this as a public package behavior break for external callers.
- `tools/dbutils/json_test.go` — assert `JSONEach` is SQLite-only and does not change when `PB_DATABASE_DRIVER=mysql`
- `tools/search/simple_field_resolver.go:102-123` — leave generic JSON path extraction as SQLite/default behavior and add/keep a comment that generic resolvers do not infer dialect from env vars

**Additional focused tests:**
- `core/record_field_resolver_test.go` — add MySQL generated-SQL coverage for relation-many joins, back-relation-many joins, request body `:each`, multivalue `:length`, JSON/geo path extraction, and multi-match equality/null fallback behavior.
- `core/record_field_resolver_test.go` — JSON/geo path extraction tests must cover MySQL root path, object path, scalar non-JSON column fallback, string comparison, and null comparison so `JSONExtractExpr(...)` semantics are explicit and not left to implementation guesswork.
- `core/record_field_resolver_test.go` — JSON/geo path extraction tests must include modifier composition with `:lower` so JSON extraction remains safe when wrapped by resolver modifiers.
- `core/record_field_resolver_test.go` or runtime QA — `:length` tests must prove MySQL `JSONArrayLengthExpr(...)` preserves SQLite normalization for empty string, SQL NULL, scalar non-array values, arrays, and object/invalid-JSON behavior as explicitly documented by the dialect.
- `tools/search/filter_test.go` — add resolver-backed tests for MySQL equality operators and LIKE column-operand formatting without relying on `PB_DATABASE_DRIVER`.
- `tools/search/filter_test.go` — add a same-process cache-safety test that builds the same filter string first with a SQLite/default resolver and then with a MySQL-primitive resolver (and/or reverse order) to prove dialect-specific resolver behavior is not cached from the first build. Cover both equality/null fallback and LIKE column-operand formatting (`||` vs `CONCAT`) in this cache-safety test.
- `tools/search/sort_test.go` — add resolver-backed `@rowid` tests proving resolver-provided rowid identifiers are used and env vars are ignored.
- `tools/search/sort_test.go` or provider tests — add same-process cache-safety coverage that builds `@rowid` sorting with a SQLite/default resolver and then a MySQL-primitive resolver (and reverse order) so resolver-specific sort identifiers are not cached from the first build.
- `tools/search/provider_test.go` or `tools/search/sort_test.go` — add provider-level `@rowid` tests that exercise the prefixing logic in `search.Provider`, not only direct `SortField.BuildExpr(...)` tests.
- `tools/search/token_functions_test.go` — keep SQLite/default `strftime(...)` coverage and add resolver-backed MySQL `strftime(...)` tests without relying on `PB_DATABASE_DRIVER`. Include multi-match `ValueIdentifier` propagation.
- `tools/search/token_functions_test.go` — assert both SQLite/default and MySQL `strftime(...)` paths clone multi-match subquery state before changing `ValueIdentifier`; the original resolved time-value result must remain unchanged after token-function resolution.
- `tools/search/token_functions_test.go` — add same-process cache-safety coverage that builds the same `strftime(...)` filter with default and MySQL resolver primitives in both orders.
- `tools/search/multi_match_subquery_test.go` — add coverage proving `Join{RawTableExpr: true}` renders `LEFT JOIN json_each(...) {{alias}}` / `LEFT JOIN JSON_TABLE(...) {{alias}}` without quoting the table-valued expression, while regular tables are still quoted.
- `tools/search/multi_match_subquery_test.go` — add coverage proving expression-valued `ValueIdentifier` renders as a raw expression and regular column identifiers are still quoted.
- `core/record_field_resolver_test.go` — add generated-SQL coverage for main-query `:each` / request-body `:each` joins proving table-valued expressions are not quoted as table names outside multi-match subqueries.
- `core/record_field_resolver_test.go` — add coverage where raw table-valued joins are collected by a cloned list-rule resolver and applied through `updateQueryWithCollectionListRule(...)`.
- `scripts/mysql-runtime-qa.mjs` — add runtime MySQL coverage for filters using `strftime(...)` and request-body `@request.body.<field>:each`. The request-body case must prove the MySQL `JSONEachParamExpr(...)` works with the actual bound value produced by `json.Marshal(...)` in Go, not only generated SQL.

**Post-4A..4E coverage checks:**
- `go build ./...` + `go test ./...`
- Remove now-unused `os` imports in `tools/search/filter.go`, `tools/search/sort.go`, and `tools/dbutils/json.go` in this patch, not later, otherwise `go build` fails before Patch 7.
- Additional grep: `rg -n "dbutils\.JSONEach\(" core/` should return zero results unless a deliberately SQLite-only data path is explicitly documented.
- Additional grep: `rg -n "dbutils\.JSONExtract\(" core/` should return zero results unless a deliberately SQLite-only data path is explicitly documented.
- Additional grep: `rg -n "strftime\(" tools/search core/ apis/` should show `tools/search/token_functions.go` as dialect-aware/default fallback code, aux-only `core/log_query.go`, tests, or explicitly documented dialect implementations only.
- Runtime QA must include at least one MySQL record-list/filter case for `strftime('%Y', created)` or equivalent supported format and one request-body `:each` case that binds an array payload.

#### Patch 4A execution split: search primitives (equality, LIKE, rowid sort)

Move only these items from the aggregate Patch 4 checklist:
- `core/record_field_resolver.go` methods: `LikeEscapeClause()`, `EqualityOperators()`, `LikeColumnContainsExpr(...)`, `RowidSortIdentifier()`
- `tools/search/filter.go` equality operators, LIKE column-operand formatting, resolver primitive threading, and `manyVsManyExpr` / `manyVsOneExpr` captured primitives
- `tools/search/sort.go` `@rowid` resolver primitive
- `tools/search/provider.go` `@rowid` prefixing review
- `ui/src/apiPreview/docsList.js` `@rowid` API-preview wording. The current UI says `@rowid` is SQLite-specific and MySQL apps should use portable fields. If this split makes `@rowid` resolve through the dialect and map to `id` on MySQL, update the docs to match the new behavior. If the project commits built UI assets, regenerate `ui/dist` after the source update.

Required tests:
- `tools/search/filter_test.go` resolver-backed MySQL equality and LIKE column-operand tests without `PB_DATABASE_DRIVER`
- `tools/search/filter_test.go` same-process cache-safety test that builds equivalent filters with SQLite/default and MySQL-primitive resolvers in both orders
- `tools/search/sort_test.go` resolver-backed `@rowid` tests proving env vars are ignored
- `tools/search/sort_test.go` or provider tests same-process cache-safety test for `@rowid` sorting with SQLite/default and MySQL-primitive resolvers in both orders
- `tools/search/provider_test.go` or `tools/search/sort_test.go` provider-level `@rowid` prefixing tests
- Manual/UI verification if docs are updated: confirm the API preview no longer claims `@rowid` is unusable on MySQL if compatibility mapping is kept, or explicitly keeps a documented warning if the team wants to discourage it despite compatibility mapping.

Verification:
- `go build ./...`
- focused `tools/search` tests
- remove unused `os` imports from `tools/search/filter.go` and `tools/search/sort.go` in this split, not later

#### Patch 4B execution split: raw table-expression join plumbing

Move only these items from the aggregate Patch 4 checklist:
- `tools/search/multi_match_subquery.go` `Join.RawTableExpr` support
- `tools/search/multi_match_subquery.go` explicit expression-valued `ValueIdentifier` support (`ValueIdentifierRaw` or equivalent)
- `core/record_field_resolver.go` `registerJoin(...)` raw-expression support or `registerJoinExpr(...)`
- `RecordFieldResolver.UpdateQuery(...)` and `updateQueryWithCollectionListRule(...)` rendering for raw joins

Additional requirements:
- When `RawTableExpr` is true, `registerJoin` must skip collection/list-rule lookup because the table name is a dialect SQL expression, not a collection/table identifier.
- Raw join support must preserve existing compatibility for regular non-collection expression/subquery table strings that currently register successfully after `loadCollection(tableName)` returns nil. The new flag prevents lookup for known raw expressions; it must not make legacy expression joins fail unless a test proves they were invalid.
- Add coverage for expression-valued `MultiMatchSubquery.ValueIdentifier`. Introduce an explicit raw/expression value path instead of relying on accidental `db.QuoteColumnName(...)` formatting.
- Preserve dbx `LeftJoin` raw-string behavior in the main-query path. Do not manually quote joins in `UpdateQuery(...)` unless tests prove it is necessary.

Required tests:
- `tools/search/multi_match_subquery_test.go` regular table joins are still quoted
- `tools/search/multi_match_subquery_test.go` `Join{RawTableExpr: true}` renders `LEFT JOIN json_each(...) {{alias}}` / `LEFT JOIN JSON_TABLE(...) {{alias}}` without quoting the table-valued expression
- `tools/search/multi_match_subquery_test.go` expression-valued `ValueIdentifier` behavior
- `core/record_field_resolver_test.go` main-query raw joins are not quoted as table names
- `core/record_field_resolver_test.go` list-rule/cloned-resolver raw joins are not quoted as table names

Verification:
- `go build ./...`
- focused raw join tests above

#### Patch 4C execution split: JSON/relation query generation

Depends on Patch 4B. Do not migrate `JSONEachColumnExpr(...)` / `JSONEachParamExpr(...)` joins until raw table-expression support is merged and tested.

If runtime QA is run against MySQL before Patch 4C, add the engine/version preflight earlier in the QA script rather than waiting for this split. The current fork already contains `JSON_TABLE` SQL behind env-sensitive helpers, so MariaDB or old MySQL variants should fail fast with an explicit unsupported-engine message whenever JSON_TABLE-dependent QA is enabled.

Move only these items from the aggregate Patch 4 checklist:
- `core/record_field_resolver_runner.go` relation equality, JSON array contains, request-body `:each`, JSON table-valued joins, `:length`, and JSON/geo/root path extraction
- `core/view.go:263`, `core/record_model.go:1536`, and `core/record_query_expand.go:113` `dbutils.JSONEach(...)` migrations
- `tools/dbutils/json.go` env detection cleanup, after core callers are migrated
- `tools/search/simple_field_resolver.go` SQLite/default documentation comment

Additional requirements:
- Do not edit `tools/dbutils.JSONEach` until grep confirms `core/` has zero `dbutils.JSONEach(` call sites.
- Do not edit `tools/dbutils.JSONExtract` until grep confirms `core/` has zero `dbutils.JSONExtract(` call sites.
- Before migrating JSON extraction call sites, complete a Patch 4C.0 runtime spike that chooses the final `JSONExtractExpr` interface shape. The spike must prove or reject the comparison-agnostic contract using runtime MySQL numeric ordering, string equality, null fallback, object/array text behavior, and scalar non-JSON fallback.
- MySQL `JSONExtractExpr(...)` tests must cover root path, object path, scalar non-JSON column fallback, string comparison, numeric comparison, and null comparison.
- MySQL `JSONExtractExpr(...)` tests must include at least one `:lower` modifier composition case so wrapping the expression with `LOWER(...)` is reviewed explicitly.
- MySQL `JSONArrayLengthExpr(...)` tests must cover empty string, SQL NULL, scalar non-JSON string/number, array, object, invalid JSON, and JSON scalar values; generated SQL alone is not enough for this method because native MySQL JSON length behavior differs from SQLite normalization.
- MySQL `JSONEachColumnExpr(...)` / `JSONEachParamExpr(...)` must expose a `value` column with string values usable for relation/file/select IDs without JSON quote mismatches.
- `JSONEachParamExpr(...)` must either work with the existing Go `[]byte` JSON payload binding or Patch 4C must intentionally change the call-site binding to a string with tests proving the param value type. Do not assume `JSON_TABLE({:param}, ...)` works with the MySQL driver without runtime proof.
- Treat `JSONExtractExpr(...)` as a runtime decision gate. If runtime MySQL proves that one comparison-agnostic expression cannot preserve numeric ordering, string equality, and null fallback behavior, stop and revise the interface before migrating all call sites.
- Document and test the MySQL `JSON_TABLE` column type/length used for the `value` column. Include relation IDs, select values, file-like strings, and request-body array values; do not silently truncate long scalar values. The runtime preflight must state the minimum supported Oracle MySQL version for the chosen `JSON_TABLE` syntax and explicitly reject MariaDB/old MySQL variants before JSON_TABLE-dependent tests run.

Required tests:
- `core/record_field_resolver_test.go` MySQL generated-SQL coverage for relation-many joins, back-relation-many joins, request body `:each`, multivalue `:length`, JSON/geo path extraction, and multi-match equality/null fallback behavior
- `core/record_field_resolver_test.go` main-query `:each` and request-body `:each` joins prove table-valued expressions are not quoted as table names
- `tools/dbutils/json_test.go` `JSONEach` remains SQLite-only and ignores `PB_DATABASE_DRIVER=mysql`
- `scripts/mysql-runtime-qa.mjs` or focused runtime spike — MySQL JSON extraction numeric ordering, string equality, null fallback, object/array behavior, JSON `:lower` behavior, `JSONArrayLengthExpr(...)` normalization behavior, and JSON_TABLE scalar string length behavior
- `scripts/mysql-runtime-qa.mjs` — add an engine/version preflight before JSON_TABLE-dependent checks. Query `SELECT VERSION()` (or equivalent) and fail early with a clear unsupported-engine message for MariaDB or MySQL versions without the required JSON_TABLE behavior.

Verification:
- `go build ./...`
- `go test ./...`
- Runtime MySQL spike/QA for JSON extraction numeric ordering, string equality, null fallback, object/array behavior, JSON `:lower` behavior, `JSONArrayLengthExpr(...)` normalization behavior, and JSON_TABLE scalar string length behavior must pass before Patch 4C is considered complete. Generated SQL tests alone are not sufficient for this split.
- Patch 4C is not complete until the chosen `JSON_TABLE` `value` column type/length is proven by runtime coverage or explicitly documented. Do not leave an unreviewed `VARCHAR(255)` copy from the old `dbutils.JSONEach` branch.
- `rg -n "dbutils\.JSONEach\(" core/` returns zero results unless every remaining occurrence is explicitly documented as SQLite-only and safe
- `rg -n "dbutils\.JSONExtract\(" core/` returns zero results unless every remaining occurrence is explicitly documented as SQLite-only and safe

#### Patch 4D execution split: strftime token function delegation

Depends on Patch 4B expression-valued `MultiMatchSubquery.ValueIdentifier` coverage. Do not migrate MySQL `strftime(...)` multi-match SQL until expression-valued identifiers are known to render safely.

Move only these items from the aggregate Patch 4 checklist:
- `core/record_field_resolver.go` `StrftimeExpr(args []search.TokenFunctionArg)` resolver primitive
- `tools/search/token_functions.go` callable signature change and resolver delegation

Additional requirements:
- Treat the `TokenFunctions` callable signature change as a Go API break for any direct users of `tools/search.TokenFunctions`; update all in-repo tests and document this in patch notes if this fork exposes the package to plugins.
- Generic resolvers keep SQLite/default `strftime(...)` behavior. `RecordFieldResolver` delegates to `app.Dialect().StrftimeExpr(...)`.
- Run a SQLite baseline test plus a MySQL datetime parsing spike before finalizing the MySQL implementation. Verify PocketBase stored datetime strings with trailing `Z`, values without `Z`, fractional seconds, and `%f` formatting. If plain `DATE_FORMAT(timeValue, mappedFormat)` is not reliable, normalize with `STR_TO_DATE(...)`, `REPLACE(..., 'Z', '')`, or an equivalent explicit expression. The MySQL output for `%f` must match the recorded SQLite-compatible output shape or be documented as an intentional behavior difference.

Required tests:
- `tools/search/token_functions_test.go` existing SQLite/default `strftime(...)` coverage still passes
- `tools/search/token_functions_test.go` resolver-backed MySQL `strftime(...)` tests without `PB_DATABASE_DRIVER`
- `tools/search/token_functions_test.go` clone-safety tests proving default SQLite fallback and MySQL delegation do not mutate the original `MultiMatchSubquery` returned by the time-value resolver
- `tools/search/token_functions_test.go` same-process cache-safety test for default and MySQL resolver-backed `strftime(...)` behavior in both orders
- `tools/search/token_functions_test.go` multi-match `ValueIdentifier` propagation
- `core/record_field_resolver_test.go` relation-many `strftime(...)` filter generated SQL uses the dialect expression in both regular and multi-match identifiers
- runtime MySQL coverage for `strftime('%Y-%m-%d %H:%M:%fZ', created)` or an equivalent supported datetime format that proves fractional-second and trailing-`Z` behavior

Verification:
- `go build ./...`
- focused token-function tests
- Runtime MySQL datetime parsing spike/QA must pass for supported `strftime(...)` formats, including trailing-`Z` PocketBase datetime strings and fractional seconds. Generated SQL tests alone are not sufficient for this split.
- `rg -n "strftime\(" tools/search core/ apis/` shows only dialect-aware/default fallback code, aux-only `core/log_query.go`, tests, or dialect implementations

#### Patch 4E execution split: count, collection query, view, and runtime QA

Move only these items from the aggregate Patch 4 checklist:
- `core/collection_query.go` default sort, `IDCastType()`, and `RequiresSubqueryAlias()`
- `apis/record_crud.go` count-column selection
- `core/view.go:56,169` subquery alias checks
- `scripts/mysql-runtime-qa.mjs` runtime query coverage

Additional requirements:
- Add one provider/default-count assertion proving skipped `CountCol("")` uses the provider default instead of generating `COUNT(DISTINCT [[table.]])`.
- Runtime QA must cover one relation-many filter using `JSON_TABLE`, not only request-body `:each`.

Verification:
- `go build ./...`
- `go test ./...`
- Runtime QA includes at least one MySQL record-list/filter case for `strftime('%Y', created)` or equivalent supported format, one relation-many JSON_TABLE filter case, and one request-body `:each` case that binds an array payload.

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
- Partial indexes are not supported by MySQL. If the current fork strips `WHERE` clauses for MySQL, preserve that behavior intentionally and document the semantic tradeoff: converting a partial unique index to a full unique index can reject rows that SQLite would allow outside the predicate. Add or keep coverage for this behavior if schema sync tests already exercise it.
- `core/base.go:1360-1377` periodic cron — route the **main** DB through `app.Dialect().PeriodicMaintenance(app.NonconcurrentDB(), app.Logger())` (SQLite: `wal_checkpoint(TRUNCATE)` + `PRAGMA optimize`; MySQL: no-op). The **aux** DB `wal_checkpoint(TRUNCATE)` stays as an unconditional SQLite call (aux is always SQLite) — do NOT route it through the dialect.
- `core/base_backup.go:88-89` backup checkpoint — route the **data** DB checkpoint through `txApp.Dialect().Checkpoint(txApp.DB(), txApp.Logger())` or an equivalent SQLite-only dialect guard; keep the **aux** DB checkpoint as unconditional SQLite. `Checkpoint` accepts `dbx.Builder` because this call is inside nested transactions and `txApp.DB()` is commonly `*dbx.Tx`, not `*dbx.DB`.
- The maintenance methods intentionally return no error and preserve current best-effort behavior. `PostSchemaSyncOptimize` and `PeriodicMaintenance` should log warnings internally for SQLite errors and no-op for MySQL. `Checkpoint` should preserve backup creation's current non-critical behavior: ignore SQLite checkpoint errors silently or log at debug level only, but do not turn checkpoint failure into backup failure. If implementation chooses `error` returns instead, update all call sites consistently and preserve the existing warn/ignore policy.

**Verification:** `go build ./...` + `go test ./...`
- Runtime QA is recommended after this patch if schema conversion SQL, partial index behavior, or maintenance behavior changed: `node scripts/mysql-runtime-qa.mjs --skip-docker --mysql-port 3306 --mysql-password "" --mysql-database pocketbase_qa`

### Patch 6: Migrate migration call sites (10 sites)

**Files & changes:**
- `migrations/1640988000_init.go:58,83,145` — `txApp.Dialect().CollectionsTableDDL()`, `ParamsTableDDL()`
- `migrations/1640988000_init.go:161` — remove local `isMySQLDataDB` duplicate
- `migrations/1640988000_init.go` — review the entire file, not only the listed line numbers, for SQLite-only defaults/functions around the initial DDL. `CollectionsTableDDL()` and `ParamsTableDDL()` must cover every dialect-specific system table DDL branch introduced by the MySQL fork.
- `migrations/1640988000_init.go` — run and review a focused grep for `randomblob`, `strftime`, `json_each`, `PRAGMA`, and `_rowid_`. Every remaining occurrence must be inside a SQLite-only branch, aux-only migration, dialect implementation, or explicitly documented as safe for all supported data dialects.
- `migrations/1717233556_v0.23_migrate.go:23` — `txApp.Dialect().Name() != core.DialectSQLiteName` or equivalent constant (skip for non-SQLite)
- `migrations/1717233557_v0.23_migrate2.go:11` — same
- `migrations/1717233558_v0.23_migrate3.go:24` — same
- `migrations/1717233559_v0.23_migrate4.go:12` — same
- `migrations/1778828400_normalize_indexes.go:18` — `txApp.Dialect().Name() != core.DialectSQLiteName` or equivalent constant
- This intentionally skips these legacy SQLite migrations for all non-SQLite dialects, not only MySQL. If a future dialect needs equivalent migration behavior, it must add its own dialect-specific migration path rather than run SQLite migration SQL.
- Document in the migration patch notes that these legacy migrations are SQLite-data migrations and are no-ops for non-SQLite data DBs. Future dialects that need equivalent transformations must add new migration versions rather than trying to reuse already-skipped legacy SQLite migrations.
- Consider replacing direct `Dialect().Name() != core.DialectSQLiteName` checks with a capability method if this grows beyond the listed legacy migrations. For this patch stack, name checks are acceptable only when using dialect name constants and tests cover the skip behavior.
- `migrations/1778828400_normalize_indexes.go` — remove now-unused `os`/`strings` imports after replacing the direct `PB_DATABASE_DRIVER` read
- `core/migrations_runner.go:267` `migrationAppliedColumnType` — `app.Dialect().MigrationAppliedColumnType()`
- Add focused coverage or a migration-runner assertion proving nested `AuxRunInTransaction` + `RunInTransaction` migration execution observes the **data** dialect for `_migrations` metadata DDL, records skipped non-SQLite legacy migrations correctly, and does not reinterpret aux DB operations as data-dialect operations.

**Verification:** `go build ./...` + `go test ./...` + runtime QA against MySQL
- Focused migration grep: `rg -n "randomblob|strftime|json_each|PRAGMA|_rowid_" migrations/1640988000_init.go migrations/1640988000_aux_init.go` and classify every remaining occurrence. Aux init migration is allowed to keep SQLite-only SQL because the aux DB is always SQLite.

### Patch 7: Remove isMySQLDataDB + cleanup

**Files & changes:**
- `core/db_dialect.go` — remove `isMySQLDataDB` and `IsMySQLDataDB` functions
- Remove `jsonArrayColumnType` helper (now in dialect)
- Grep for any remaining `isMySQLDataDB`, `IsMySQLDataDB`, `PB_DATABASE_DRIVER` references in Go source across **`core/`, `tools/`, `migrations/`, `apis/`, `forms/`, `cmd/`, `examples/`, `plugins/`, and root `pocketbase.go`**. The only allowed production references are:
  - the `envDatabaseDriver`/`envDatabaseDSN` consts and the `DialectForDriver` factory in `core/db_dialect.go` and `core/db_connect.go`
  - user-facing CLI help text (whitelist explicitly: `pocketbase.go`, `cmd/serve.go`)
- tests may reference `PB_DATABASE_DRIVER` only in focused dialect/connect tests, not in field/query behavior tests
- **Verify env-var-only tests were already migrated** (the deliverable flagged in App wiring/Patch 2): `core/field_select_test.go`, `core/field_relation_test.go`, and any other test that previously did `t.Setenv("PB_DATABASE_DRIVER", "mysql")` after `NewTestApp()` must no longer rely on post-boot env mutation. Patch 7 is a cleanup verification step, not the first migration point for these tests.
- Migrate any test outside focused dialect/connect tests that uses `PB_DATABASE_DRIVER` to influence field/query/sort behavior, including `tools/search/*_test.go` if present.
- Include `plugins/` in cleanup greps because plugin code is production Go code.
- Confirm `tools/dbutils/json.go` no longer imports `os` or `strings` solely for dialect detection.
- Confirm `tools/search/filter.go`, `tools/search/sort.go`, and `tools/dbutils/json.go` no longer import `os` solely for dialect detection. Any remaining `strings` import must be needed for non-env string handling.
- Include `core/field.go` in the `_rowid_` grep whitelist for the reserved field-name list and its tests. That occurrence is validation metadata, not data-DB query generation, and should remain.
- Update release/patch notes for the intentional public Go API/behavior breaks: `core.App.Dialect()`, `tools/search.TokenFunctions` callable shape, and `tools/dbutils.JSONEach` no longer being env-sensitive.

**Verification:**
- `go build ./...` passes
- `go test ./...` passes
- no-default-driver compile/test target from Patch 1 still passes
- `rg -n "isMySQLDataDB|IsMySQLDataDB" core/ tools/ migrations/ apis/ forms/ cmd/ examples/ plugins/ pocketbase.go` returns zero results
- Production `rg` for `PB_DATABASE_DRIVER` in `core/ tools/ migrations/ apis/ forms/ cmd/ plugins/ pocketbase.go` returns only `core/db_dialect.go` + `core/db_connect.go` (the detection factory/connect path) plus user-facing help text in `pocketbase.go` and `cmd/serve.go`; any test references are limited to focused dialect/connect tests
- `rg -n "dbutils\.JSONEach\(" core/` returns zero results unless every remaining occurrence is explicitly documented as SQLite-only and safe
- `rg -n "dbutils\.JSONExtract\(" core/` returns zero results unless every remaining occurrence is explicitly documented as SQLite-only and safe
- Query-generation greps for `json_each(`, `json_array_length(`, `JSON_EXTRACT(`, `JSON_LENGTH(`, `JSON_UNQUOTE(`, `JSON_CONTAINS(`, `JSON_TABLE(`, `_rowid_`, and `PRAGMA` in `core/ apis/ tools/search` are reviewed. Remaining raw MySQL/SQLite query-generation functions must be either inside dialect implementations (`core/db_dialect.go`), aux-DB-only paths (`core/log_query.go`, aux checkpoints), tests, `tools/search.SimpleFieldResolver`'s documented SQLite/default JSON path behavior, or explicitly documented SQLite-only helpers. In particular, raw MySQL JSON conversion SQL must not remain in `core/collection_record_table_sync.go` after `SingleToMultiConversionSQL(...)` and `MultiToSingleConversionSQL(...)` are migrated.
- Query-generation grep for `strftime(` in `tools/search core/ apis/` must confirm that data DB search filters are routed through dialect-aware `tools/search.TokenFunctions["strftime"]` delegation and `core.Dialect.StrftimeExpr(...)`. Remaining raw `strftime(` is allowed only in aux-DB-only log paths, SQLite/default fallback code, tests, or dialect implementations.
- `rg -n "os\.Getenv\(.*PB_DATABASE_DRIVER|PB_DATABASE_DRIVER" tools/search tools/dbutils core migrations apis forms cmd plugins pocketbase.go` returns only approved detection/connect/help/test locations described above
- Runtime QA: `node scripts/mysql-runtime-qa.mjs --skip-docker --mysql-port 3306 --mysql-password "" --mysql-database pocketbase_qa`
- Refresh docs that describe MySQL gaps/runtime coverage if behavior changed: at minimum review `docs/mysql-gap-analysis.md`, `docs/mysql-upstream-workflow.md`, and `AGENTS.md`.
- Add or update explicit release/patch notes for public API and behavior breaks. If the repo does not have a changelog, add a short migration section to `README.md` or the most appropriate docs page covering `core.App.Dialect()`, `tools/search.TokenFunctions` callable shape, `tools/dbutils.JSONEach` no longer being env-sensitive, the MySQL-only engine/version requirement for `JSON_TABLE`, and MariaDB being unsupported unless separately implemented and tested.
- If `@rowid` behavior was changed to be dialect-backed, confirm the API preview docs in `ui/src/apiPreview/docsList.js` and committed `ui/dist` bundle no longer contradict the new behavior.
- Export patch stack as the final implementation step: `node scripts/export-mysql-patches.mjs v0.39.4` (or the current upstream baseline if it changed). Before exporting, confirm the baseline tag from `AGENTS.md`, the current branch history/merge-base, or existing patch-stack metadata so this command does not accidentally export against a stale upstream tag. Review the generated `patches/mysql-poc/` diff and commit it separately if following the repo's patch-stack workflow.

## Key Design Decisions

1. **Detection keeps env-var-first priority, cached after boot** — `DialectForDriver` checks `PB_DATABASE_DRIVER=mysql` first, then the opened driver name. This is set once in `initDataDB()` and exposed via `app.Dialect()`. Before bootstrap or after reset, `Dialect()` may still derive from the current env/driver fallback because no cached dialect exists. We deliberately did **not** switch to "driver name only" because the env-var override must be preserved; field/query behavior tests are migrated away from post-boot env-var mutation before wrappers depend on cached `app.Dialect()`.
2. **`app.Dialect()` is the data dialect only** — the aux DB is always SQLite and must never be routed through the dialect.
3. **No new package for tools/search** — `tools/search`/`tools/dbutils` cannot import `core` (cycle), so they extend the existing interface-assertion pattern and receive **primitives** (e.g. `EqualityOperators`, `RowidSortIdentifier() string`, LIKE/JSON expression strings), never a `Dialect` value or an `IsMySQL` boolean.
4. **Equality and LIKE are expression-level dialect behavior** — `LIKE ESCAPE` alone is insufficient because MySQL also differs in column concatenation and not-equal/null-fallback behavior (`<>`, `AND`, `IS NOT NULL`).
5. **JSON path extraction is dialect behavior** — `RecordFieldResolver` must use `Dialect.JSONExtractExpr(...)` for JSON/geo filters; generic `tools/search.SimpleFieldResolver` remains SQLite/default because it has no app context.
6. **`strftime(...)` filters are dialect behavior** — `tools/search.TokenFunctions["strftime"]` remains SQLite/default for generic resolvers but delegates to `RecordFieldResolver.StrftimeExpr(...)` for data DB filters so MySQL gets supported SQL instead of SQLite `strftime(...)`.
7. **Maintenance split into three methods** — `PostSchemaSyncOptimize` (per-sync), `PeriodicMaintenance` (daily cron, main DB only), and `Checkpoint` (backup, main DB only) are distinct concerns and kept separate rather than overloaded into one method.
8. **Migration skips use `dialect.Name() != core.DialectSQLiteName`** — dialect-agnostic, PG will also skip legacy SQLite migrations.
9. **System table DDL in dialect** — `CollectionsTableDDL()` and `ParamsTableDDL()` move the hardcoded MySQL CREATE TABLE strings out of migration logic.
10. **`IsMySQLDataDB` kept as wrapper during transition** — avoids breaking all ~53 sites in one patch, removed cleanly at the end.
11. **PostgreSQL is enabled later, not implemented here** — this refactor removes MySQL-specific conditionals from business logic and creates dialect seams for future PostgreSQL work; it does not guarantee PostgreSQL SQL compatibility in this patch stack.
12. **Public API breaks are intentional and documented** — `core.App`, `tools/search.TokenFunctions`, and `tools/dbutils.JSONEach` behavior change as part of removing scattered dialect conditionals.
13. **Runtime gates protect uncertain SQL semantics** — MySQL `JSONExtractExpr(...)`, `JSON_TABLE(...)` scalar behavior, and `strftime(...)` datetime parsing must be runtime-proven before legacy branches are removed.

## Files Touched Summary

| File | Patches |
|---|---|
| `core/db_dialect.go` | 1, 2, 7 |
| `core/db_dialect_test.go` | 0, 1 |
| `core/db_connect.go` | 1 and 7 verification (allowed env-var connect path) |
| `core/db_connect_nodefaultdriver.go` | 1 and 7 no-default-driver verification |
| `core/base.go` | 1, 5 |
| `core/base_test.go` | 1 |
| `core/db_tx.go` | 1 |
| `core/db_tx_test.go` | 1 |
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
| `core/record_field_resolver.go` | 4A, 4B, 4D |
| `core/record_field_resolver_runner.go` | 4C |
| `core/record_field_resolver_test.go` | 4B, 4C, 4D |
| `core/log_query.go` | none (aux DB SQLite-only; document as intentional) |
| `core/collection_query.go` | 4E |
| `core/view.go` | 4C, 4E |
| `core/view_test.go` | 4C or 4E if generated SQL assertions change |
| `apis/record_crud.go` | 4E |
| `apis/record_crud_test.go` | 4E if count behavior coverage is added here |
| `tools/search/filter.go` | 4A |
| `tools/search/simple_field_resolver.go` | 1 or 4C (if `EqualityOperators` type lives here instead of `filter.go`) |
| `tools/search/sort.go` | 4A |
| `tools/search/provider.go` | 4A, 4E |
| `tools/search/token_functions.go` | 1, 4D |
| `tools/search/multi_match_subquery.go` | 4B |
| `ui/src/apiPreview/docsList.js` | 4A if `@rowid` semantics become dialect-backed |
| `ui/dist/` | 4A if UI docs source changes and built assets are committed |
| `tools/dbutils/json.go` | 4C, 7 |
| `tools/dbutils/json_test.go` | 4C |
| `tools/search/filter_test.go` | 4A |
| `tools/search/sort_test.go` | 4A |
| `tools/search/provider_test.go` | 4A or 4E |
| `tools/search/token_functions_test.go` | 4D |
| `tools/search/multi_match_subquery_test.go` | 4B |
| `scripts/mysql-runtime-qa.mjs` | 4E, 7 |
| `README.md` or release notes | 7 public API/behavior break documentation |
| `docs/mysql-gap-analysis.md` | 7 docs refresh if runtime coverage or dialect gaps changed |
| `docs/mysql-upstream-workflow.md` | 7 docs refresh if patch-stack workflow or baseline notes changed |
| `core/collection_record_table_sync.go` | 3, 5 |
| `core/collection_record_table_sync_test.go` | 5 if schema sync SQL expectations change |
| `core/record_model.go` | 4C |
| `core/record_model_test.go` | 4C if generated SQL assertions change |
| `core/record_query_expand.go` | 4C |
| `migrations/1640988000_init.go` | 6 |
| `migrations/1717233556_v0.23_migrate.go` | 6 |
| `migrations/1717233557_v0.23_migrate2.go` | 6 |
| `migrations/1717233558_v0.23_migrate3.go` | 6 |
| `migrations/1717233559_v0.23_migrate4.go` | 6 |
| `migrations/1778828400_normalize_indexes.go` | 6 |
| `core/migrations_runner.go` | 6 |
| `core/field_select_test.go` | 1 or 2 (remove post-boot env-var mutation), 7 verification |
| `core/field_relation_test.go` | 1 or 2 (remove post-boot env-var mutation), 7 verification |
| `pocketbase.go` | 1 verification for `core.App` conformance, 7 env-var help-text whitelist |

## Risk & Rollback

- Each patch is independently revertable via `git revert`
- No behavior change until call sites are migrated, provided Patch 1 keeps the legacy wrapper behavior or migrates the post-boot env-var tests before switching wrappers to cached `app.Dialect()`
- If runtime QA fails after any patch, the issue is isolated to that patch's category
- Patches that add public API breaks are not independently revertable after downstream call-site patches land unless compatibility shims are temporarily restored. In particular, reverting `core.App.Dialect()` or the `tools/search.TokenFunctions` signature after later patches requires reverting dependent patches or adding transitional compatibility code intentionally.
- Patch stack is re-exported only after Patch 7 passes all verification
