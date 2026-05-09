# MySQL Gap Analysis

Baseline: PocketBase v0.38.0.

This document tracks the first SQLite-specific areas that must be addressed before a MySQL backend can work. It intentionally records gaps only; runtime behavior remains unchanged.

## Baseline Verification

- `go build ./...`: passed.
- `go test ./...`: passed.
- Upstream source imported from tag `v0.38.0` on branch `mysql/main`.

## Initial High-Risk Areas

### Table and index introspection

Files:

- `core/db_table.go`
- `core/collection_validate.go`

Current behavior uses SQLite introspection directly:

- `PRAGMA_TABLE_INFO` for table columns and column metadata.
- `sqlite_master` and `sqlite_schema` for indexes, tables, and views.
- Global index-name validation via `sqlite_master`.

MySQL work needed:

- Replace table and column discovery with `information_schema` queries or a dialect abstraction.
- Decide whether PocketBase index names remain globally unique or become table-scoped for MySQL.
- Preserve existing SQLite behavior as the default path.

### Record table schema sync

File:

- `core/collection_record_table_sync.go`

Current behavior relies on DB operations and SQLite-specific SQL during collection schema changes:

- Record table creation, rename, column add/drop/rename/type changes.
- Index drop/recreate around schema updates.
- `PRAGMA optimize` after sync.
- `sqlite_master` access when updating view metadata.
- `json_extract` usage when transforming JSON-like field data.

MySQL work needed:

- Audit generated DDL for `CREATE TABLE`, `ALTER TABLE`, `RENAME TABLE`, `DROP COLUMN`, `MODIFY COLUMN`, and index operations.
- Replace `PRAGMA optimize` with no-op or a MySQL-specific maintenance strategy.
- Move SQLite JSON transform SQL behind dialect-specific helpers.
- Account for MySQL implicit commits around DDL.

### Initial migrations

Files:

- `migrations/1640988000_init.go`
- `migrations/1640988000_aux_init.go`

Current behavior uses SQLite date functions and SQLite-compatible schema definitions:

- `strftime('%Y-%m-%d %H:%M:%fZ')` defaults.
- Log index expression using `strftime`.

MySQL work needed:

- Define MySQL timestamp defaults, likely `DATETIME(3)` in UTC.
- Replace expression indexes with MySQL-compatible generated columns or alternate query/index strategy if needed.
- Decide whether auxiliary/log DB remains SQLite during PoC.

### Query/filter expression functions

Files:

- `tools/search/token_functions.go`
- `core/record_field_resolver_runner.go`
- `core/record_field_resolver_test.go`

Current behavior exposes SQLite-like functions and generated SQL:

- `strftime(...)` is supported in filter expressions.
- JSON fields are wrapped through `dbutils.JSONExtract(...)`.
- Tests assert SQLite-shaped SQL for filters.

MySQL work needed:

- Decide whether filter functions produce dialect-specific SQL or whether unsupported functions fail explicitly in MySQL mode.
- Add dialect-aware tests for generated SQL.
- Keep SQLite SQL output unchanged.

### Connection setup

File:

- `core/db_connect.go`

Current behavior registers and opens the default SQLite driver.

MySQL work needed:

- Add a minimal MySQL connection path for PoC without replacing default SQLite behavior.
- Decide whether auxiliary DB remains SQLite for PoC.
- Ensure driver and DSN configuration are explicit, not inferred from database path.

## Recommended Next Step

Start Milestone 2 with the smallest PoC:

1. Add MySQL driver dependency.
2. Add explicit database driver and DSN configuration path.
3. Keep SQLite as default.
4. Boot with `PB_DATABASE_DRIVER=mysql` and record the first real failure after connection.

Do not implement broad dialect abstraction until the PoC confirms the first concrete startup blockers.

## Milestone 2 PoC Result

Minimal MySQL connection routing was added behind environment variables:

- `PB_DATABASE_DRIVER=mysql`
- `PB_DATABASE_DSN=<mysql dsn>`

For this PoC, only `data.db` is routed to MySQL. `auxiliary.db` remains on the default SQLite connection because `DBConnectFunc` receives only the target path and the blueprint recommends keeping auxiliary SQLite during the first boot experiment.

Manual QA results:

- Missing DSN path works: running with `PB_DATABASE_DRIVER=mysql` and no `PB_DATABASE_DSN` exits with `PB_DATABASE_DSN is required when PB_DATABASE_DRIVER=mysql`.
- MySQL 8.4 container connection works: PocketBase reaches migration execution and emits MySQL SQL.
- First startup blocker after connection:

```text
CREATE TABLE IF NOT EXISTS `_migrations` (file VARCHAR(255) PRIMARY KEY NOT NULL, applied INTEGER NOT NULL)
INSERT INTO `_migrations` (`applied`, `file`) VALUES (1778344523546328, '1640988000_aux_init.go')
failed to save applied migration info for 1640988000_aux_init.go: Error 1264 (22003): Out of range value for column 'applied' at row 1
```

Interpretation:

- The driver/DSN path is not the blocker.
- The next blocker is schema type generation for system migration metadata: SQLite `INTEGER` accepted the microtimestamp, but MySQL `INTEGER` is too small.
- The next implementation target should be dialect-aware system table column types, starting with migration metadata before broader collection schema work.

## Milestone 2 Migration Metadata Fix Result

The first PoC blocker was addressed by making the migration metadata `applied` column use `BIGINT` when the active data DB driver is MySQL. SQLite keeps the previous `INTEGER` type.

Manual QA with a fresh MySQL 8.4 container now reaches the next startup blocker:

```text
CREATE TABLE IF NOT EXISTS `_migrations` (file VARCHAR(255) PRIMARY KEY NOT NULL, applied BIGINT NOT NULL)
INSERT INTO `_migrations` (`applied`, `file`) VALUES (1778344809419336, '1640988000_aux_init.go')
CREATE TABLE `_params` (
    `id`      TEXT PRIMARY KEY DEFAULT ('r'||lower(hex(randomblob(7)))) NOT NULL,
    `value`   JSON DEFAULT NULL,
    `created` TEXT DEFAULT (strftime('%Y-%m-%d %H:%M:%fZ')) NOT NULL,
    `updated` TEXT DEFAULT (strftime('%Y-%m-%d %H:%M:%fZ')) NOT NULL
);
failed to apply migration 1640988000_init.go: _params exec error: Error 3770 (HY000): Default value expression of column 'id' contains a disallowed function: `randomblob`.
```

Interpretation:

- Migration metadata can now be recorded in MySQL.
- The next blocker is the initial system schema migration, starting with `_params`.
- MySQL needs dialect-specific replacements for SQLite defaults such as `randomblob(...)` ID generation and `strftime(...)` timestamp defaults.

## Milestone 2 Initial System Schema SQL Result

The initial `_params` and `_collections` schema now uses a MySQL-specific branch while preserving the original SQLite SQL path.

Manual QA with a fresh MySQL 8.4 container now reaches the next startup blocker:

```text
CREATE TABLE `_params` (
    `id`      VARCHAR(15) PRIMARY KEY NOT NULL,
    `value`   JSON DEFAULT NULL,
    `created` VARCHAR(255) DEFAULT "" NOT NULL,
    `updated` VARCHAR(255) DEFAULT "" NOT NULL
);
CREATE TABLE `_collections` (...);
CREATE INDEX idx__collections_type on `_collections` (`type`)
CREATE TABLE `_mfas` (`collectionRef` TEXT DEFAULT '' NOT NULL, ...)
failed to apply migration 1640988000_init.go: _mfas error: Error 1101 (42000): BLOB, TEXT, GEOMETRY or JSON column 'collectionRef' can't have a default value
```

Interpretation:

- The PoC now passes the hardcoded `_params` and `_collections` startup SQL.
- The next blocker is generated collection record table DDL from field `ColumnType` values.
- MySQL cannot use `TEXT DEFAULT ''`, so text/autodate field column generation needs dialect-aware types before system collections such as `_mfas` can be created.

## Milestone 2 Text and Autodate Field DDL Result

Generated MySQL record table DDL now uses MySQL-safe column types for text and autodate fields while preserving SQLite defaults.

Manual QA with a fresh MySQL 8.4 container now reaches the next startup blocker:

```text
CREATE TABLE `_mfas` (`collectionRef` VARCHAR(255) DEFAULT '' NOT NULL, `created` VARCHAR(255) DEFAULT '' NOT NULL, `id` VARCHAR(15) PRIMARY KEY NOT NULL, `method` VARCHAR(255) DEFAULT '' NOT NULL, `recordRef` VARCHAR(255) DEFAULT '' NOT NULL, `updated` VARCHAR(255) DEFAULT '' NOT NULL)
CREATE INDEX `idx_mfas_collectionRef_recordRef` ON `_mfas` (`collectionRef`, `recordRef`)
CREATE TABLE `_otps` (... `password` TEXT DEFAULT '' NOT NULL, ...)
failed to apply migration 1640988000_init.go: _otps error: Error 1101 (42000): BLOB, TEXT, GEOMETRY or JSON column 'password' can't have a default value
```

Interpretation:

- The `_mfas` system record table can now be created in MySQL.
- The next blocker is `PasswordField.ColumnType`, which still emits `TEXT DEFAULT '' NOT NULL`.
- Similar text-backed fields such as email, URL, editor, file/select/relation variants will need the same dialect-aware audit.

## Milestone 2 Text-Backed Field DDL Result

Additional text-backed field types now use MySQL-safe column definitions while preserving SQLite defaults.

Covered field types:

- `PasswordField`
- `EmailField`
- `URLField`
- `EditorField`
- `DateField`
- single-value `FileField`
- single-value `SelectField`
- single-value `RelationField`

Manual QA with a fresh MySQL 8.4 container now reaches the next startup blocker:

```text
CREATE TABLE `_superusers` (... `email` VARCHAR(255) DEFAULT '' NOT NULL, ...)
CREATE UNIQUE INDEX `idx_tokenKey_pbc_3142635823` ON `_superusers` (`tokenKey`)
CREATE UNIQUE INDEX `idx_email_pbc_3142635823` ON `_superusers` (`email`) WHERE `email` != ''
failed to apply migration 1640988000_init.go: _superusers error: indexes: (1: Failed to create index idx_email_pbc_3142635823 - Error 1064 (42000): ... near 'WHERE `email` != ''' at line 1..).
```

Interpretation:

- MySQL can now create the early system tables through `_superusers` table creation.
- The next blocker is SQLite-style partial index SQL stored in collection index definitions.
- MySQL needs dialect-aware index generation or index normalization, starting with filtered unique indexes such as `WHERE email != ''`.

## Milestone 2 Partial Index PoC Result

MySQL index creation now drops parsed `WHERE` predicates during collection index creation. This is a PoC-only compatibility step for SQLite partial indexes.

Manual QA with a fresh MySQL 8.4 container now reaches the next startup blocker:

```text
CREATE UNIQUE INDEX `idx_email_pbc_3142635823` ON `_superusers` (`email`)
CREATE UNIQUE INDEX `idx_email__pb_users_auth_` ON `users` (`email`)
INSERT INTO `_migrations` (`applied`, `file`) VALUES (..., '1640988000_init.go')
failed to apply migration 1717233556_v0.23_migrate.go: failed to fetch old settings: Error 1054 (42S22): Unknown column 'key' in 'where clause'
```

Interpretation:

- The initial system migration can now complete on MySQL.
- The next blocker is a historical migration that expects the legacy `_params` table shape with a `key` column.
- Since this fork starts from a fresh v0.38.0 schema, older upgrade migrations need a MySQL-aware skip/reapply strategy rather than assuming legacy SQLite schema exists.

## Milestone 2 Legacy v0.23 Migration Skip Result

The legacy v0.23 migration group now exits early for MySQL. These migrations are upgrade-only paths for older SQLite schemas and are not needed for the fresh MySQL v0.38.0 schema created by `1640988000_init.go`.

Manual QA with a fresh MySQL 8.4 container now reaches the next startup blocker:

```text
INSERT INTO `_migrations` (`applied`, `file`) VALUES (..., '1717233556_v0.23_migrate.go')
INSERT INTO `_migrations` (`applied`, `file`) VALUES (..., '1717233557_v0.23_migrate2.go')
INSERT INTO `_migrations` (`applied`, `file`) VALUES (..., '1717233558_v0.23_migrate3.go')
INSERT INTO `_migrations` (`applied`, `file`) VALUES (..., '1717233559_v0.23_migrate4.go')
failed to apply migration 1763020353_update_default_auth_alert_templates.go: Error 1054 (42S22): Unknown column 'rowid' in 'order clause'; failed query: SELECT {{_collections}}.* FROM `_collections` WHERE `type`={:p0} ORDER BY `rowid` ASC
```

Interpretation:

- The v0.23 legacy migration assumptions no longer block MySQL boot.
- The next blocker is SQLite-specific ordering by `rowid` in collection queries.
- MySQL needs a replacement default collection ordering, likely by a stable explicit column such as `id` or `created`, after checking all `rowid` usage.

## Milestone 2 Collection Ordering Result

Collection listing now uses `ORDER BY id ASC` for MySQL while preserving SQLite's `ORDER BY rowid ASC` behavior.

Manual QA with a fresh MySQL 8.4 container now reaches a running server:

```text
SELECT `_collections`.* FROM `_collections` WHERE `type`='auth' ORDER BY `id` ASC
INSERT INTO `_migrations` (`applied`, `file`) VALUES (..., '1763020353_update_default_auth_alert_templates.go')
Server started at http://127.0.0.1:18090
REST API:  http://127.0.0.1:18090/api/
Dashboard: http://127.0.0.1:18090/_/
```

Interpretation:

- The current PoC can boot PocketBase against MySQL and reach the server start state.
- Remaining warnings still include SQLite-specific maintenance/introspection calls such as `PRAGMA optimize`, `sqlite_schema`, and `sqlite_master` during schema/cache operations.
- Next work should shift from boot blockers to a focused audit of runtime correctness: introspection helpers, CRUD record creation, auth login, index semantics, and JSON/list fields.

## Runtime QA Record List Count Result

The first focused runtime QA pass confirmed that a fresh MySQL 8.4 backend can boot, create a superuser, authenticate through the REST API, create a simple base collection through the collections API, and insert a simple record.

The first runtime blocker appeared when listing records with pagination metadata:

```text
SELECT `qa_posts`.* FROM `qa_posts` ORDER BY `qa_posts`.`title` ASC LIMIT 30
SELECT COUNT(DISTINCT `qa_posts`.`_rowid_`) FROM `qa_posts`
ERROR GET /api/collections/qa_posts/records?sort=title
└─ Error 1054 (42S22): Unknown column 'qa_posts._rowid_' in 'field list'
```

The records list API now keeps the SQLite `_rowid_` count optimization only for SQLite-backed record tables. MySQL leaves the search provider default `id` count column, producing a portable count query for normal record collections.

Manual QA with a fresh MySQL 8.4 container now passes the simple runtime CRUD probe:

```text
POST /api/collections/_superusers/auth-with-password -> 200
POST /api/collections -> 200
POST /api/collections/qa_posts/records -> 200
GET /api/collections/qa_posts/records?sort=title -> 200
{"totalItems":1,"totalPages":1}
```

Remaining warnings still include MySQL-incompatible SQLite maintenance/introspection queries during schema sync, especially `PRAGMA optimize`, `sqlite_schema`, and `sqlite_master`. The next runtime QA target should exercise schema changes and index/list/JSON fields to turn those warnings into concrete compatibility fixes.

## Runtime QA PRAGMA Optimize Result

The record-table schema sync path no longer runs SQLite `PRAGMA optimize` for MySQL-backed data DBs. SQLite keeps the existing immediate post-sync optimization and periodic optimization behavior.

Manual QA with a fresh MySQL 8.4 container still passes collection creation, and the server log no longer emits the previous warning:

```text
WARN Failed to run PRAGMA optimize after record table sync
└─ Error 1064 (42000): ... near 'PRAGMA optimize'
```

Remaining runtime compatibility noise is now focused on schema/index introspection queries using `sqlite_schema` and `sqlite_master` during collection creation and validation.

## Runtime QA Introspection Result

The high-traffic data DB metadata helpers now use MySQL `information_schema` when the data DB driver is MySQL:

- table existence checks use `information_schema.TABLES`.
- table columns and table info use `information_schema.COLUMNS`.
- table index lookup and collection index-name validation use `information_schema.STATISTICS`.

Manual QA with a fresh MySQL 8.4 container still boots and can create an indexed collection through the REST API:

```text
POST /api/collections/_superusers/auth-with-password -> 200
POST /api/collections qa_introspection with idx_qa_introspection_title -> 200
CREATE TABLE `qa_introspection` (...)
CREATE INDEX `idx_qa_introspection_title` ON `qa_introspection` (`title`)
```

The collection creation log now shows `information_schema.TABLES` and `information_schema.STATISTICS` for data DB metadata lookups instead of `sqlite_schema` and `sqlite_master`.

## Runtime QA LIKE Filter Result

Basic text filtering exposed the next runtime blocker:

```text
GET /api/collections/qa_introspection/records?filter=title~"intro" -> 400
Error 1064 (42000): ... near ''\' LIMIT 30'
SELECT ... WHERE `qa_introspection`.`title` LIKE '%intro%' ESCAPE '\'
```

MySQL requires the backslash escape character to be escaped in the generated SQL string literal. The record field resolver now provides a MySQL-specific `LIKE` escape clause while the generic search resolver keeps the existing SQLite clause.

Manual QA after restart now passes:

```text
GET /api/collections/qa_introspection/records?filter=title~"intro" -> 200
SELECT ... WHERE `qa_introspection`.`title` LIKE '%intro%' ESCAPE '\\'
{"totalItems":1,"totalPages":1}
```
