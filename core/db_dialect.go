package core

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/dbutils"
	"github.com/pocketbase/pocketbase/tools/search"
	"github.com/pocketbase/pocketbase/tools/security"
)

const (
	envDatabaseDriver = "PB_DATABASE_DRIVER"
	envDatabaseDSN    = "PB_DATABASE_DSN"
)

// Dialect name constants for the supported data database dialects.
const (
	DialectSQLiteName = "sqlite"
	DialectMySQLName  = "mysql"
)

// Dialect describes the SQL dialect used by the app's data database.
//
// It is intentionally minimal (only exposing Name()) and is meant to be
// used as a replacement for the scattered driver-name conditionals.
type Dialect interface {
	Name() string
}

// columnDialect is a local (unexported) capability interface that exposes
// dialect-specific column type strings used by the field ColumnType methods.
//
// It is intentionally kept separate from the exported [Dialect] interface so
// that the public surface stays minimal while the concrete dialect types can
// still be type-asserted to provide column type information.
type columnDialect interface {
	VarCharColumnType(max int) string
	PrimaryKeyColumnType() string
	EditorColumnType() string
	JSONArrayColumnType() string
	JSONValueColumnType(defaultValue string) string
}

// introspectionDialect is a local (unexported) capability interface that
// exposes dialect-specific SQL queries for table metadata introspection
// (columns, indexes, table existence, views, etc.).
//
// It is intentionally kept separate from the exported [Dialect] interface so
// that the public surface stays minimal while the concrete dialect types can
// still be type-asserted to provide introspection SQL.
type introspectionDialect interface {
	TableColumnsQuery() string
	TableInfoQuery() string
	TableIndexesQuery() string
	HasTableQuery() string
	ViewsQuery() string
	IndexOwnerQuery() string
}

// equalityDialect is a local (unexported) capability interface that exposes
// dialect-specific SQL primitives for equality (=, !=) and LIKE expression
// building used by the search filter package.
//
// It is intentionally kept separate from the exported [Dialect] interface so
// that the public surface stays minimal while the concrete dialect types can
// still be type-asserted to provide search primitives.
type equalityDialect interface {
	EqualityOperators() search.EqualityOperators
	LikeEscapeClause() string
	LikeColumnContainsExpr(column string) string
}

// rowidDialect is a local (unexported) capability interface that exposes
// dialect-specific count override column semantics.
//
// It is intentionally kept separate from the exported [Dialect] interface so
// that the public surface stays minimal while the concrete dialect types can
// still be type-asserted to provide the count override column.
//
// CountOverrideColumn returns the column to use for COUNT(DISTINCT ...) queries
// and a boolean indicating whether an override should be applied. When the
// boolean is false, the caller should keep the default "id" column.
type rowidDialect interface {
	CountOverrideColumn(isView bool) (string, bool)
}

// jsonEachDialect is a local (unexported) capability interface that exposes
// dialect-specific JSON table expression generation for array expansion.
//
// It is intentionally kept separate from the exported [Dialect] interface so
// that the public surface stays minimal while the concrete dialect types can
// still be type-asserted to provide JSON each expression generation.
type jsonEachDialect interface {
	// JSONEachColumnExpr returns a JSON table expression for a column reference.
	// For SQLite: json_each(CASE WHEN ... END)
	// For MySQL: JSON_TABLE(CASE WHEN ... END, '$[*]' COLUMNS(value VARCHAR(255) PATH '$'))
	JSONEachColumnExpr(column string) string

	// JSONEachParamExpr returns a JSON table expression for a parameter placeholder.
	// For SQLite: json_each({:placeholder})
	// For MySQL: JSON_TABLE({:placeholder}, '$[*]' COLUMNS(value VARCHAR(255) PATH '$'))
	JSONEachParamExpr(placeholder string) string

	// JSONEachOnClause returns the ON clause expression for JSON table joins.
	// For SQLite: nil (json_each doesn't require ON)
	// For MySQL: dbx.NewExp("1=1") (JSON_TABLE requires ON)
	JSONEachOnClause() dbx.Expression
}

// jsonLengthDialect is a local (unexported) capability interface that
// exposes dialect-specific JSON array length expression generation.
//
// It is intentionally kept separate from the exported [Dialect] interface
// so that the public surface stays minimal while the concrete dialect types
// can be type-asserted to provide JSON array length expressions.
type jsonLengthDialect interface {
	// JSONArrayLengthExpr returns a SQL expression that computes the length
	// of a JSON array column with normalization for non-array values.
	//
	// For SQLite: json_array_length(CASE WHEN ... END)
	// For MySQL: JSON_LENGTH(CASE WHEN ... END)
	//
	// The expression must return:
	//   - array length for JSON arrays
	//   - 0 for empty string or SQL NULL
	//   - 1 for scalar values (JSON or non-JSON)
	JSONArrayLengthExpr(column string) string
}

// jsonExtractDialect is a local (unexported) capability interface that
// exposes dialect-specific JSON path extraction expression generation.
//
// It is intentionally kept separate from the exported [Dialect] interface
// so that the public surface stays minimal while the concrete dialect types
// can be type-asserted to provide JSON extraction expressions.
type jsonExtractDialect interface {
	// JSONExtractExpr returns a SQL expression that extracts a value at
	// the given JSON path from a column.
	//
	// For SQLite: (CASE WHEN json_valid(col) THEN JSON_EXTRACT(col, '$.path') ELSE JSON_EXTRACT(json_object('pb', col), '$.pb.path') END)
	// For MySQL: (CASE WHEN JSON_VALID(col) THEN JSON_UNQUOTE(JSON_EXTRACT(col, '$.path')) ELSE JSON_UNQUOTE(JSON_EXTRACT(JSON_OBJECT('pb', col), '$.pb.path')) END)
	//
	// The path is prefixed with "." if it doesn't start with "[".
	// An empty path extracts the root value.
	//
	// The expression must work for string, numeric, and null comparisons
	// without resolver-side hacks.
	JSONExtractExpr(column string, path string) string
}

// strftimeDialect is a local (unexported) capability interface that
// exposes dialect-specific strftime expression generation.
//
// It is intentionally kept separate from the exported [Dialect] interface
// so that the public surface stays minimal while the concrete dialect types
// can be type-asserted to provide strftime expressions.
type strftimeDialect interface {
	// StrftimeExpr returns a dialect-specific SQL expression for the
	// strftime token function, given the resolved arguments.
	//
	// args[0] is the format string (TokenText).
	// args[1] is the time value (TokenText, TokenIdentifier, or TokenNumber).
	// args[2:] are modifiers (TokenText).
	//
	// The returned expression string is used as the ResolverResult.Identifier.
	// The returned params map is merged into the ResolverResult.Params.
	// An error is returned for unsupported modifiers or format tokens.
	StrftimeExpr(args []search.TokenFunctionArg) (expr string, params dbx.Params, err error)
}

// queryViewDialect is a local (unexported) capability interface that
// exposes dialect-specific query and view SQL generation behavior.
//
// It is intentionally kept separate from the exported [Dialect] interface
// so that the public surface stays minimal while the concrete dialect types
// can be type-asserted to provide query/view-specific behavior.
type queryViewDialect interface {
	// DefaultCollectionSort returns the ORDER BY expression used for
	// default collection listing queries.
	//
	// For SQLite: "rowid ASC"
	// For MySQL: "id ASC"
	DefaultCollectionSort() string

	// RequiresSubqueryAlias returns true if the dialect requires an alias
	// for subqueries in FROM clauses.
	//
	// For SQLite: false (subqueries don't need aliases)
	// For MySQL: true (subqueries require aliases)
	RequiresSubqueryAlias() bool

	// IDCastType returns the SQL type to cast ID columns to when
	// normalizing view query IDs.
	//
	// For SQLite: "TEXT"
	// For MySQL: "CHAR(255)"
	IDCastType() string

	// IsIDStringType returns true if the provided column type is already
	// string-compatible and doesn't need casting for ID normalization.
	//
	// For SQLite: true only if colType is "TEXT"
	// For MySQL: true if colType contains "CHAR" or "TEXT" (case-insensitive)
	IsIDStringType(colType string) bool
}

// schemaSyncDialect is a local (unexported) capability interface that
// exposes dialect-specific schema sync SQL generation.
//
// It is intentionally kept separate from the exported [Dialect] interface
// so that the public surface stays minimal while the concrete dialect types
// can be type-asserted to provide schema sync behavior.
type schemaSyncDialect interface {
	// AddColumnDirectly returns true if the dialect supports adding columns
	// directly via ALTER TABLE ADD COLUMN without table recreation.
	//
	// For SQLite: false (requires table recreation)
	// For MySQL: true (ALTER TABLE ADD COLUMN)
	AddColumnDirectly() bool

	// RenameColumnSQL returns a SQL statement for renaming a column and
	// optionally changing its type in one operation.
	//
	// For SQLite: returns empty string (use RenameColumn helper / table recreation)
	// For MySQL: returns "ALTER TABLE [[table]] CHANGE [[old]] [[new]] colType"
	RenameColumnSQL(table, old, new, colType string) string

	// SingleToMultiConversionSQL returns a SQL UPDATE statement for
	// converting a single-value column to a multi-value (JSON array) column.
	//
	// For SQLite: uses lowercase json_valid/json_type/json_array with == comparison
	// For MySQL: uses uppercase JSON_VALID/JSON_TYPE/JSON_ARRAY with = comparison
	SingleToMultiConversionSQL(table, col, temp string) string

	// MultiToSingleConversionSQL returns a SQL UPDATE statement for
	// converting a multi-value (JSON array) column to a single-value column
	// (keeping the last element).
	//
	// For SQLite: uses lowercase json functions with $[#-1] syntax
	// For MySQL: uses uppercase JSON_UNQUOTE/JSON_EXTRACT/JSON_LENGTH/CONCAT
	MultiToSingleConversionSQL(table, col, temp string) string

	// DropIndexSQL returns a SQL DROP INDEX statement.
	//
	// For SQLite: "DROP INDEX IF EXISTS [[name]]"
	// For MySQL: "DROP INDEX [[name]] ON [[table]]"
	DropIndexSQL(name, table string) string

	// SupportsPartialIndexes returns true if the dialect supports partial
	// indexes (indexes with WHERE clauses).
	//
	// For SQLite: true
	// For MySQL: false (WHERE clause is stripped)
	SupportsPartialIndexes() bool
}

// maintenanceDialect is a local (unexported) capability interface that
// exposes dialect-specific database maintenance operations.
//
// All methods are best-effort (no error return) and log warnings internally
// for SQLite operations that fail. MySQL implementations are no-ops.
//
// The aux DB is always SQLite and is NOT covered by this interface —
// aux DB maintenance remains unconditional SQLite.
type maintenanceDialect interface {
	// PostSchemaSyncOptimize runs optimization after a schema sync.
	// For SQLite: PRAGMA optimize
	// For MySQL: no-op
	PostSchemaSyncOptimize(db dbx.Builder, logger *slog.Logger)

	// PeriodicMaintenance runs periodic maintenance on the data DB.
	// For SQLite: PRAGMA wal_checkpoint(TRUNCATE) + PRAGMA optimize
	// For MySQL: no-op
	PeriodicMaintenance(db dbx.Builder, logger *slog.Logger)

	// Checkpoint runs a WAL checkpoint before backups on the data DB.
	// For SQLite: PRAGMA wal_checkpoint(TRUNCATE)
	// For MySQL: no-op
	Checkpoint(db dbx.Builder, logger *slog.Logger)
}

// migrationDialect is a local (unexported) capability interface that
// exposes dialect-specific system migration SQL.
//
// It is intentionally kept separate from the exported [Dialect] interface
// so that the public surface stays minimal.
type migrationDialect interface {
	// CollectionsTableDDL returns the complete DDL for the _collections
	// system table, including any index creation statements.
	CollectionsTableDDL() string

	// ParamsTableDDL returns the complete DDL for the _params system table.
	ParamsTableDDL() string

	// MigrationAppliedColumnType returns the SQL column type for the
	// _migrations.applied column.
	//
	// For SQLite: "INTEGER"
	// For MySQL: "BIGINT"
	MigrationAppliedColumnType() string
}

// relationJoinDialect is a local (unexported) capability interface that
// exposes dialect-specific relation join SQL generation.
//
// The key difference is that MySQL uses JSON_CONTAINS for multi-value
// relation joins (single join with array membership check), while SQLite
// uses json_each subquery expansion (two joins — raw table + equality).
type relationJoinDialect interface {
	// UseJSONContainsForMultiRelations returns true if the dialect uses
	// JSON_CONTAINS for multi-value relation joins.
	//
	// For SQLite: false (uses json_each subquery expansion)
	// For MySQL: true (uses JSON_CONTAINS in ON clause)
	UseJSONContainsForMultiRelations() bool

	// RelationValueEqualsExpr returns a SQL expression for comparing two
	// relation values for equality.
	//
	// For SQLite: "left = right"
	// For MySQL: "BINARY left = BINARY right"
	RelationValueEqualsExpr(left, right string) string

	// RelationArrayContainsExpr returns a SQL expression for checking if
	// an array column contains a value.
	//
	// For MySQL: "JSON_CONTAINS(arrayCol, JSON_QUOTE(idCol))"
	// For SQLite: "" (uses json_each subquery instead)
	RelationArrayContainsExpr(arrayCol, idCol string) string
}

// queryViewDialectIfAvailable returns the queryViewDialect capability if
// the provided dialect implements it, or nil otherwise.
func queryViewDialectIfAvailable(d Dialect) queryViewDialect {
	if qd, ok := d.(queryViewDialect); ok {
		return qd
	}
	return nil
}

// schemaSyncDialectIfAvailable returns the schemaSyncDialect capability if
// the provided dialect implements it, or nil otherwise.
func schemaSyncDialectIfAvailable(d Dialect) schemaSyncDialect {
	if sd, ok := d.(schemaSyncDialect); ok {
		return sd
	}
	return nil
}

// maintenanceDialectIfAvailable returns the maintenanceDialect capability if
// the provided dialect implements it, or nil otherwise.
func maintenanceDialectIfAvailable(d Dialect) maintenanceDialect {
	if md, ok := d.(maintenanceDialect); ok {
		return md
	}
	return nil
}

// relationJoinDialectIfAvailable returns the relationJoinDialect capability if
// the provided dialect implements it, or nil otherwise.
func relationJoinDialectIfAvailable(d Dialect) relationJoinDialect {
	if rjd, ok := d.(relationJoinDialect); ok {
		return rjd
	}
	return nil
}

// SQLiteDialect represents the SQLite data database dialect.
type SQLiteDialect struct{}

// Name implements the [Dialect] interface.
func (SQLiteDialect) Name() string {
	return DialectSQLiteName
}

// VarCharColumnType implements the [columnDialect] interface.
//
// SQLite has no VARCHAR type, so a TEXT column is returned regardless of max.
func (SQLiteDialect) VarCharColumnType(max int) string {
	return "TEXT DEFAULT '' NOT NULL"
}

// PrimaryKeyColumnType implements the [columnDialect] interface.
func (SQLiteDialect) PrimaryKeyColumnType() string {
	// note: the default is just a last resort fallback to avoid empty
	// string values in case the record was inserted with raw sql and
	// it is not actually used when operating with the db abstraction
	return "TEXT PRIMARY KEY DEFAULT ('r'||lower(hex(randomblob(7)))) NOT NULL"
}

// EditorColumnType implements the [columnDialect] interface.
func (SQLiteDialect) EditorColumnType() string {
	return "TEXT DEFAULT '' NOT NULL"
}

// JSONArrayColumnType implements the [columnDialect] interface.
func (SQLiteDialect) JSONArrayColumnType() string {
	return "JSON DEFAULT '[]' NOT NULL"
}

// JSONValueColumnType implements the [columnDialect] interface.
func (SQLiteDialect) JSONValueColumnType(defaultValue string) string {
	return fmt.Sprintf("JSON DEFAULT '%s' NOT NULL", defaultValue)
}

// TableColumnsQuery implements the [introspectionDialect] interface.
func (SQLiteDialect) TableColumnsQuery() string {
	return "SELECT name FROM PRAGMA_TABLE_INFO({:tableName})"
}

// TableInfoQuery implements the [introspectionDialect] interface.
func (SQLiteDialect) TableInfoQuery() string {
	return "SELECT * FROM PRAGMA_TABLE_INFO({:tableName})"
}

// TableIndexesQuery implements the [introspectionDialect] interface.
func (SQLiteDialect) TableIndexesQuery() string {
	return "SELECT name, sql FROM sqlite_master WHERE sql is not null AND type = 'index' AND tbl_name = {:tableName}"
}

// HasTableQuery implements the [introspectionDialect] interface.
func (SQLiteDialect) HasTableQuery() string {
	return "SELECT (1) FROM sqlite_schema WHERE type IN ('table', 'view') AND LOWER(name) = LOWER({:tableName}) LIMIT 1"
}

// ViewsQuery implements the [introspectionDialect] interface.
func (SQLiteDialect) ViewsQuery() string {
	return "SELECT name, sql FROM sqlite_master WHERE sql is not null AND type = 'view'"
}

// IndexOwnerQuery implements the [introspectionDialect] interface.
func (SQLiteDialect) IndexOwnerQuery() string {
	return "SELECT tbl_name FROM sqlite_master WHERE type = 'index' AND LOWER(tbl_name) != LOWER({:oldName}) AND LOWER(tbl_name) != LOWER({:newName}) AND LOWER(name) = LOWER({:indexName}) LIMIT 1"
}

// EqualityOperators implements the [equalityDialect] interface.
func (SQLiteDialect) EqualityOperators() search.EqualityOperators {
	return search.EqualityOperators{
		Equal: search.EqualityOperatorSet{
			EqualOp:     "=",
			NullEqualOp: "IS",
			NullConcat:  "OR",
			NullExpr:    "IS NULL",
		},
		NotEqual: search.EqualityOperatorSet{
			EqualOp:     "IS NOT",
			NullEqualOp: "IS NOT",
			NullConcat:  "AND",
			NullExpr:    "IS NOT NULL",
		},
	}
}

// LikeEscapeClause implements the [equalityDialect] interface.
func (SQLiteDialect) LikeEscapeClause() string {
	return " ESCAPE '\\'"
}

// LikeColumnContainsExpr implements the [equalityDialect] interface.
func (SQLiteDialect) LikeColumnContainsExpr(column string) string {
	return fmt.Sprintf("'%%' || %s || '%%'", column)
}

// CountOverrideColumn implements the [rowidDialect] interface.
//
// SQLite non-view collections can use the builtin _rowid_ column for
// COUNT(DISTINCT ...) queries to minimize the need of a covering index
// with the "id" field. Views don't have a _rowid_ column, so no override
// is provided and the default "id" column is used.
func (SQLiteDialect) CountOverrideColumn(isView bool) (string, bool) {
	if isView {
		return "", false
	}

	return "_rowid_", true
}

// JSONEachColumnExpr implements the [jsonEachDialect] interface.
//
// Returns the SQLite json_each expression directly rather than delegating
// to dbutils.JSONEach, so the result is always SQLite regardless of
// PB_DATABASE_DRIVER env var.
func (SQLiteDialect) JSONEachColumnExpr(column string) string {
	// note: we are not using the new and shorter "if(x,y)" syntax for
	// compatibility with custom drivers that use older SQLite version
	return fmt.Sprintf(
		`json_each(CASE WHEN iif(json_valid([[%s]]), json_type([[%s]])='array', FALSE) THEN [[%s]] ELSE json_array([[%s]]) END)`,
		column, column, column, column,
	)
}

// JSONEachParamExpr implements the [jsonEachDialect] interface.
func (SQLiteDialect) JSONEachParamExpr(placeholder string) string {
	return fmt.Sprintf("json_each({:%s})", placeholder)
}

// JSONEachOnClause implements the [jsonEachDialect] interface.
//
// SQLite's json_each() doesn't require an ON clause for LEFT JOINs.
func (SQLiteDialect) JSONEachOnClause() dbx.Expression {
	return nil
}

// JSONArrayLengthExpr implements the [jsonLengthDialect] interface.
func (SQLiteDialect) JSONArrayLengthExpr(column string) string {
	return dbutils.JSONArrayLength(column)
}

// JSONExtractExpr implements the [jsonExtractDialect] interface.
func (SQLiteDialect) JSONExtractExpr(column string, path string) string {
	return dbutils.JSONExtract(column, path)
}

// StrftimeExpr implements the [strftimeDialect] interface.
//
// Returns the SQLite strftime expression using the resolved argument
// identifiers (placeholders and column references).
func (SQLiteDialect) StrftimeExpr(args []search.TokenFunctionArg) (string, dbx.Params, error) {
	identifiers := make([]string, 0, len(args))
	for _, arg := range args {
		identifiers = append(identifiers, arg.Result.Identifier)
	}
	return "strftime(" + strings.Join(identifiers, ",") + ")", nil, nil
}

// DefaultCollectionSort implements the [queryViewDialect] interface.
func (SQLiteDialect) DefaultCollectionSort() string {
	return "rowid ASC"
}

// RequiresSubqueryAlias implements the [queryViewDialect] interface.
func (SQLiteDialect) RequiresSubqueryAlias() bool {
	return false
}

// IDCastType implements the [queryViewDialect] interface.
func (SQLiteDialect) IDCastType() string {
	return "TEXT"
}

// IsIDStringType implements the [queryViewDialect] interface.
func (SQLiteDialect) IsIDStringType(colType string) bool {
	return strings.EqualFold(colType, "TEXT")
}

// AddColumnDirectly implements the [schemaSyncDialect] interface.
func (SQLiteDialect) AddColumnDirectly() bool {
	return false
}

// RenameColumnSQL implements the [schemaSyncDialect] interface.
//
// SQLite uses the RenameColumn helper or table recreation, so an empty
// string is returned to indicate no direct rename SQL.
func (SQLiteDialect) RenameColumnSQL(table, old, new, colType string) string {
	return ""
}

// SingleToMultiConversionSQL implements the [schemaSyncDialect] interface.
func (SQLiteDialect) SingleToMultiConversionSQL(table, col, temp string) string {
	return fmt.Sprintf(
		`UPDATE {{%s}} set [[%s]] = (
			CASE
				WHEN COALESCE([[%s]], '') = ''
				THEN '[]'
				ELSE (
					CASE
						WHEN json_valid([[%s]]) AND json_type([[%s]]) == 'array'
						THEN [[%s]]
						ELSE json_array([[%s]])
					END
				)
			END
		)`,
		table, col, temp, temp, temp, temp, temp,
	)
}

// MultiToSingleConversionSQL implements the [schemaSyncDialect] interface.
func (SQLiteDialect) MultiToSingleConversionSQL(table, col, temp string) string {
	return fmt.Sprintf(
		`UPDATE {{%s}} set [[%s]] = (
			CASE
				WHEN COALESCE([[%s]], '[]') = '[]'
				THEN ''
				ELSE (
					CASE
						WHEN json_valid([[%s]]) AND json_type([[%s]]) == 'array'
						THEN COALESCE(json_extract([[%s]], '$[#-1]'), '')
						ELSE [[%s]]
					END
				)
			END
		)`,
		table, col, temp, temp, temp, temp, temp,
	)
}

// DropIndexSQL implements the [schemaSyncDialect] interface.
func (SQLiteDialect) DropIndexSQL(name, table string) string {
	return fmt.Sprintf("DROP INDEX IF EXISTS [[%s]]", name)
}

// SupportsPartialIndexes implements the [schemaSyncDialect] interface.
func (SQLiteDialect) SupportsPartialIndexes() bool {
	return true
}

// PostSchemaSyncOptimize implements the [maintenanceDialect] interface.
func (SQLiteDialect) PostSchemaSyncOptimize(db dbx.Builder, logger *slog.Logger) {
	_, err := db.NewQuery("PRAGMA optimize").Execute()
	if err != nil {
		logger.Warn("Failed to run PRAGMA optimize after record table sync", slog.String("error", err.Error()))
	}
}

// PeriodicMaintenance implements the [maintenanceDialect] interface.
func (SQLiteDialect) PeriodicMaintenance(db dbx.Builder, logger *slog.Logger) {
	_, err := db.NewQuery("PRAGMA wal_checkpoint(TRUNCATE)").Execute()
	if err != nil {
		logger.Warn("Failed to run periodic PRAGMA wal_checkpoint for the main DB", slog.String("error", err.Error()))
	}

	_, err = db.NewQuery("PRAGMA optimize").Execute()
	if err != nil {
		logger.Warn("Failed to run periodic PRAGMA optimize", slog.String("error", err.Error()))
	}
}

// Checkpoint implements the [maintenanceDialect] interface.
func (SQLiteDialect) Checkpoint(db dbx.Builder, logger *slog.Logger) {
	_, _ = db.NewQuery("PRAGMA wal_checkpoint(TRUNCATE)").Execute()
}

// CollectionsTableDDL implements the [migrationDialect] interface.
func (SQLiteDialect) CollectionsTableDDL() string {
	return `
		CREATE TABLE IF NOT EXISTS {{_collections}} (
			[[id]]         TEXT PRIMARY KEY DEFAULT ('r'||lower(hex(randomblob(7)))) NOT NULL,
			[[system]]     BOOLEAN DEFAULT FALSE NOT NULL,
			[[type]]       TEXT DEFAULT "base" NOT NULL,
			[[name]]       TEXT UNIQUE NOT NULL,
			[[fields]]     JSON DEFAULT "[]" NOT NULL,
			[[indexes]]    JSON DEFAULT "[]" NOT NULL,
			[[listRule]]   TEXT DEFAULT NULL,
			[[viewRule]]   TEXT DEFAULT NULL,
			[[createRule]] TEXT DEFAULT NULL,
			[[updateRule]] TEXT DEFAULT NULL,
			[[deleteRule]] TEXT DEFAULT NULL,
			[[options]]    JSON DEFAULT "{}" NOT NULL,
			[[created]]    TEXT DEFAULT (strftime('%Y-%m-%d %H:%M:%fZ')) NOT NULL,
			[[updated]]    TEXT DEFAULT (strftime('%Y-%m-%d %H:%M:%fZ')) NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx__collections_type on {{_collections}} ([[type]]);
	`
}

// ParamsTableDDL implements the [migrationDialect] interface.
func (SQLiteDialect) ParamsTableDDL() string {
	return `
		CREATE TABLE IF NOT EXISTS {{_params}} (
			[[id]]      TEXT PRIMARY KEY DEFAULT ('r'||lower(hex(randomblob(7)))) NOT NULL,
			[[value]]   JSON DEFAULT NULL,
			[[created]] TEXT DEFAULT (strftime('%Y-%m-%d %H:%M:%fZ')) NOT NULL,
			[[updated]] TEXT DEFAULT (strftime('%Y-%m-%d %H:%M:%fZ')) NOT NULL
		);
	`
}

// MigrationAppliedColumnType implements the [migrationDialect] interface.
func (SQLiteDialect) MigrationAppliedColumnType() string {
	return "INTEGER"
}

// UseJSONContainsForMultiRelations implements the [relationJoinDialect] interface.
func (SQLiteDialect) UseJSONContainsForMultiRelations() bool {
	return false
}

// RelationValueEqualsExpr implements the [relationJoinDialect] interface.
func (SQLiteDialect) RelationValueEqualsExpr(left, right string) string {
	return fmt.Sprintf("%s = %s", left, right)
}

// RelationArrayContainsExpr implements the [relationJoinDialect] interface.
//
// SQLite uses json_each subquery expansion instead of JSON_CONTAINS.
func (SQLiteDialect) RelationArrayContainsExpr(arrayCol, idCol string) string {
	return ""
}

// MySQLDialect represents the MySQL data database dialect.
type MySQLDialect struct{}

// Name implements the [Dialect] interface.
func (MySQLDialect) Name() string {
	return DialectMySQLName
}

// VarCharColumnType implements the [columnDialect] interface.
func (MySQLDialect) VarCharColumnType(max int) string {
	return fmt.Sprintf("VARCHAR(%d) DEFAULT '' NOT NULL", max)
}

// PrimaryKeyColumnType implements the [columnDialect] interface.
func (MySQLDialect) PrimaryKeyColumnType() string {
	return "VARCHAR(15) PRIMARY KEY NOT NULL"
}

// EditorColumnType implements the [columnDialect] interface.
func (MySQLDialect) EditorColumnType() string {
	return "LONGTEXT NOT NULL"
}

// JSONArrayColumnType implements the [columnDialect] interface.
//
// MySQL disallows literal DEFAULT values on JSON columns, so no default is
// specified; the zero value is supplied at the application layer.
func (MySQLDialect) JSONArrayColumnType() string {
	return "JSON NOT NULL"
}

// JSONValueColumnType implements the [columnDialect] interface.
//
// MySQL disallows literal DEFAULT values on JSON columns, so the defaultValue
// is ignored and the zero value is supplied at the application layer.
func (MySQLDialect) JSONValueColumnType(defaultValue string) string {
	return "JSON NOT NULL"
}

// TableColumnsQuery implements the [introspectionDialect] interface.
func (MySQLDialect) TableColumnsQuery() string {
	return `SELECT COLUMN_NAME
			FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = {:tableName}
			ORDER BY ORDINAL_POSITION`
}

// TableInfoQuery implements the [introspectionDialect] interface.
func (MySQLDialect) TableInfoQuery() string {
	return `SELECT
				ORDINAL_POSITION - 1 AS cid,
				COLUMN_NAME AS name,
				COLUMN_TYPE AS type,
				CASE WHEN IS_NULLABLE = 'NO' THEN 1 ELSE 0 END AS notnull,
				COLUMN_DEFAULT AS dflt_value,
				CASE WHEN COLUMN_KEY = 'PRI' THEN 1 ELSE 0 END AS pk
			FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = {:tableName}
			ORDER BY ORDINAL_POSITION`
}

// TableIndexesQuery implements the [introspectionDialect] interface.
func (MySQLDialect) TableIndexesQuery() string {
	return `SELECT INDEX_NAME AS name, CONCAT('INDEX ', INDEX_NAME) AS sql
			FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = {:tableName} AND INDEX_NAME != 'PRIMARY'
			GROUP BY INDEX_NAME`
}

// HasTableQuery implements the [introspectionDialect] interface.
func (MySQLDialect) HasTableQuery() string {
	return `SELECT 1
			FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = DATABASE()
				AND TABLE_TYPE IN ('BASE TABLE', 'VIEW')
				AND LOWER(TABLE_NAME) = LOWER({:tableName})
			LIMIT 1`
}

// ViewsQuery implements the [introspectionDialect] interface.
func (MySQLDialect) ViewsQuery() string {
	return "SELECT TABLE_NAME AS name, VIEW_DEFINITION AS `sql` FROM information_schema.VIEWS WHERE TABLE_SCHEMA = DATABASE()"
}

// IndexOwnerQuery implements the [introspectionDialect] interface.
func (MySQLDialect) IndexOwnerQuery() string {
	return `SELECT TABLE_NAME
			FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = DATABASE()
				AND LOWER(TABLE_NAME) != LOWER({:oldName})
				AND LOWER(TABLE_NAME) != LOWER({:newName})
				AND LOWER(INDEX_NAME) = LOWER({:indexName})
			LIMIT 1`
}

// EqualityOperators implements the [equalityDialect] interface.
//
// MySQL doesn't support `IS` with non-NULL operands (only `IS NULL`,
// `IS NOT NULL`, `IS TRUE`, etc.), so `<=>` (null-safe equality) is used
// for the NullEqualOp to preserve SQLite's `IS` semantics where
// `NULL IS NULL` returns true. For not-equal, `<>` is used for value
// comparisons while keeping `IS NOT NULL` for the null check.
func (MySQLDialect) EqualityOperators() search.EqualityOperators {
	return search.EqualityOperators{
		Equal: search.EqualityOperatorSet{
			EqualOp:     "=",
			NullEqualOp: "<=>",
			NullConcat:  "OR",
			NullExpr:    "IS NULL",
		},
		NotEqual: search.EqualityOperatorSet{
			EqualOp:     "<>",
			NullEqualOp: "<>",
			NullConcat:  "AND",
			NullExpr:    "IS NOT NULL",
		},
	}
}

// LikeEscapeClause implements the [equalityDialect] interface.
func (MySQLDialect) LikeEscapeClause() string {
	return " ESCAPE '\\\\'"
}

// LikeColumnContainsExpr implements the [equalityDialect] interface.
func (MySQLDialect) LikeColumnContainsExpr(column string) string {
	return fmt.Sprintf("CONCAT('%%', %s, '%%')", column)
}

// CountOverrideColumn implements the [rowidDialect] interface.
//
// MySQL doesn't have a _rowid_ column, so no count override is provided
// and the default "id" column is used for both regular collections and views.
func (MySQLDialect) CountOverrideColumn(isView bool) (string, bool) {
	return "", false
}

// JSONEachColumnExpr implements the [jsonEachDialect] interface.
//
// Returns the MySQL JSON_TABLE expression directly rather than delegating
// to dbutils.JSONEach, so the result is always MySQL regardless of
// PB_DATABASE_DRIVER env var.
func (MySQLDialect) JSONEachColumnExpr(column string) string {
	return fmt.Sprintf(
		`JSON_TABLE(CASE WHEN JSON_VALID([[%s]]) AND JSON_TYPE([[%s]]) = 'ARRAY' THEN [[%s]] ELSE JSON_ARRAY([[%s]]) END, '$[*]' COLUMNS(value VARCHAR(255) PATH '$'))`,
		column, column, column, column,
	)
}

// JSONEachParamExpr implements the [jsonEachDialect] interface.
func (MySQLDialect) JSONEachParamExpr(placeholder string) string {
	return fmt.Sprintf(
		`JSON_TABLE({:%s}, '$[*]' COLUMNS(value VARCHAR(255) PATH '$'))`,
		placeholder,
	)
}

// JSONEachOnClause implements the [jsonEachDialect] interface.
//
// MySQL's JSON_TABLE() requires an ON clause for LEFT JOINs, so a
// dummy "1=1" expression is returned.
func (MySQLDialect) JSONEachOnClause() dbx.Expression {
	return dbx.NewExp("1=1")
}

// JSONArrayLengthExpr implements the [jsonLengthDialect] interface.
//
// MySQL doesn't have json_array_length() or iif(), so JSON_LENGTH() and
// IF() are used to mirror the SQLite normalization contract:
//   - JSON array → array length
//   - empty string or SQL NULL → 0
//   - scalar values (JSON or non-JSON) → 1
func (MySQLDialect) JSONArrayLengthExpr(column string) string {
	return fmt.Sprintf(
		`JSON_LENGTH(CASE WHEN IF(JSON_VALID([[%s]]), JSON_TYPE([[%s]]) = 'ARRAY', FALSE) THEN [[%s]] ELSE (CASE WHEN [[%s]] = '' OR [[%s]] IS NULL THEN JSON_ARRAY() ELSE JSON_ARRAY([[%s]]) END) END)`,
		column, column, column, column, column, column,
	)
}

// JSONExtractExpr implements the [jsonExtractDialect] interface.
//
// MySQL's JSON_EXTRACT returns quoted JSON strings (e.g. "alice" instead of
// alice), so JSON_UNQUOTE is used to preserve SQLite-like comparison
// semantics for string, numeric, and null values.
//
// JSON_UNQUOTE on non-string JSON values (numbers, booleans, null) returns
// them as-is, so numeric and null comparisons work correctly.
func (MySQLDialect) JSONExtractExpr(column string, path string) string {
	// prefix the path with dot if it is not starting with array notation
	if path != "" && !strings.HasPrefix(path, "[") {
		path = "." + path
	}

	return fmt.Sprintf(
		"(CASE WHEN JSON_VALID([[%s]]) THEN JSON_UNQUOTE(JSON_EXTRACT([[%s]], '$%s')) ELSE JSON_UNQUOTE(JSON_EXTRACT(JSON_OBJECT('pb', [[%s]]), '$.pb%s')) END)",
		column,
		column,
		path,
		column,
		path,
	)
}

// StrftimeExpr implements the [strftimeDialect] interface.
//
// MySQL doesn't have strftime(). This implementation translates the SQLite
// format string to MySQL DATE_FORMAT tokens and wraps the time value
// appropriately.
//
// Format token mapping:
//   %Y → %Y (4-digit year)
//   %m → %m (2-digit month)
//   %d → %d (2-digit day)
//   %H → %H (2-digit hour 24h)
//   %M → %i (2-digit minute — SQLite %M is minutes, MySQL %M is month name)
//   %S → %s (2-digit second)
//   %f → %f (fractional seconds — MySQL returns 6 digits, SQLite returns 3)
//
// Supported modifiers:
//   unixepoch — wraps time value with FROM_UNIXTIME()
//   utc       — no-op (PocketBase stores UTC strings)
//
// Unsupported modifiers (localtime, timezone offsets, relative date math)
// return an error.
//
// The 'Z' suffix in datetime strings is handled by REPLACE(timeValue, 'Z', '')
// because MySQL's DATE_FORMAT doesn't parse the 'Z' suffix.
func (MySQLDialect) StrftimeExpr(args []search.TokenFunctionArg) (string, dbx.Params, error) {
	if len(args) < 1 {
		return "", nil, fmt.Errorf("expected at least 1 argument")
	}

	// extract format string from the first argument's literal
	formatStr := args[0].Literal

	// translate SQLite format tokens to MySQL DATE_FORMAT tokens
	mappedFormat := translateStrftimeFormat(formatStr)

	// create a new placeholder for the translated format
	formatPlaceholder := "strftimeFmt" + security.PseudorandomString(8)
	params := dbx.Params{formatPlaceholder: mappedFormat}

	// strftime(format) — no time value
	if len(args) == 1 {
		expr := fmt.Sprintf("DATE_FORMAT(UTC_TIMESTAMP(3), {:%s})", formatPlaceholder)
		return expr, params, nil
	}

	// extract time value identifier
	timeValueIdentifier := args[1].Result.Identifier

	// process modifiers
	hasUnixepoch := false
	for i := 2; i < len(args); i++ {
		mod := strings.ToLower(strings.TrimSpace(args[i].Literal))
		switch mod {
		case "unixepoch":
			hasUnixepoch = true
		case "utc":
			// no-op, PocketBase stores UTC strings
		case "localtime":
			return "", nil, fmt.Errorf("[strftime] unsupported MySQL modifier %q", mod)
		default:
			return "", nil, fmt.Errorf("[strftime] unsupported MySQL modifier %q", mod)
		}
	}

	// build the time value expression
	var timeValueExpr string
	if hasUnixepoch {
		// numeric Unix seconds → FROM_UNIXTIME
		timeValueExpr = fmt.Sprintf("FROM_UNIXTIME(%s)", timeValueIdentifier)
	} else {
		// datetime string — remove 'Z' suffix for MySQL compatibility
		timeValueExpr = fmt.Sprintf("REPLACE(%s, 'Z', '')", timeValueIdentifier)
	}

	expr := fmt.Sprintf("DATE_FORMAT(%s, {:%s})", timeValueExpr, formatPlaceholder)
	return expr, params, nil
}

// DefaultCollectionSort implements the [queryViewDialect] interface.
func (MySQLDialect) DefaultCollectionSort() string {
	return "id ASC"
}

// RequiresSubqueryAlias implements the [queryViewDialect] interface.
func (MySQLDialect) RequiresSubqueryAlias() bool {
	return true
}

// IDCastType implements the [queryViewDialect] interface.
func (MySQLDialect) IDCastType() string {
	return "CHAR(255)"
}

// IsIDStringType implements the [queryViewDialect] interface.
func (MySQLDialect) IsIDStringType(colType string) bool {
	rowType := strings.ToUpper(colType)
	return strings.Contains(rowType, "CHAR") || strings.Contains(rowType, "TEXT")
}

// AddColumnDirectly implements the [schemaSyncDialect] interface.
func (MySQLDialect) AddColumnDirectly() bool {
	return true
}

// RenameColumnSQL implements the [schemaSyncDialect] interface.
func (MySQLDialect) RenameColumnSQL(table, old, new, colType string) string {
	return fmt.Sprintf("ALTER TABLE [[%s]] CHANGE [[%s]] [[%s]] %s", table, old, new, colType)
}

// SingleToMultiConversionSQL implements the [schemaSyncDialect] interface.
func (MySQLDialect) SingleToMultiConversionSQL(table, col, temp string) string {
	return fmt.Sprintf(
		`UPDATE {{%s}} set [[%s]] = (
			CASE
				WHEN COALESCE([[%s]], '') = ''
				THEN JSON_ARRAY()
				ELSE (
					CASE
						WHEN JSON_VALID([[%s]]) AND JSON_TYPE([[%s]]) = 'ARRAY'
						THEN [[%s]]
						ELSE JSON_ARRAY([[%s]])
					END
				)
			END
		)`,
		table, col, temp, temp, temp, temp, temp,
	)
}

// MultiToSingleConversionSQL implements the [schemaSyncDialect] interface.
func (MySQLDialect) MultiToSingleConversionSQL(table, col, temp string) string {
	return fmt.Sprintf(
		`UPDATE {{%s}} set [[%s]] = (
			CASE
				WHEN JSON_VALID([[%s]]) AND JSON_TYPE([[%s]]) = 'ARRAY'
				THEN COALESCE(JSON_UNQUOTE(JSON_EXTRACT([[%s]], CONCAT('$[', JSON_LENGTH([[%s]]) - 1, ']'))), '')
				WHEN COALESCE([[%s]], '') = ''
				THEN ''
				ELSE [[%s]]
			END
		)`,
		table, col, temp, temp, temp, temp, temp, temp,
	)
}

// DropIndexSQL implements the [schemaSyncDialect] interface.
func (MySQLDialect) DropIndexSQL(name, table string) string {
	return fmt.Sprintf("DROP INDEX [[%s]] ON [[%s]]", name, table)
}

// SupportsPartialIndexes implements the [schemaSyncDialect] interface.
func (MySQLDialect) SupportsPartialIndexes() bool {
	return false
}

// PostSchemaSyncOptimize implements the [maintenanceDialect] interface.
//
// MySQL has no equivalent to SQLite's PRAGMA optimize.
func (MySQLDialect) PostSchemaSyncOptimize(db dbx.Builder, logger *slog.Logger) {
	// no-op
}

// PeriodicMaintenance implements the [maintenanceDialect] interface.
//
// MySQL has no equivalent to SQLite's PRAGMA wal_checkpoint or optimize.
func (MySQLDialect) PeriodicMaintenance(db dbx.Builder, logger *slog.Logger) {
	// no-op
}

// Checkpoint implements the [maintenanceDialect] interface.
//
// MySQL has no equivalent to SQLite's PRAGMA wal_checkpoint.
func (MySQLDialect) Checkpoint(db dbx.Builder, logger *slog.Logger) {
	// no-op
}

// CollectionsTableDDL implements the [migrationDialect] interface.
func (MySQLDialect) CollectionsTableDDL() string {
	return `
		CREATE TABLE {{_collections}} (
			[[id]]         VARCHAR(15) PRIMARY KEY NOT NULL,
			[[system]]     BOOLEAN DEFAULT FALSE NOT NULL,
			[[type]]       VARCHAR(255) DEFAULT "base" NOT NULL,
			[[name]]       VARCHAR(255) UNIQUE NOT NULL,
			[[fields]]     JSON NOT NULL,
			[[indexes]]    JSON NOT NULL,
			[[listRule]]   TEXT DEFAULT NULL,
			[[viewRule]]   TEXT DEFAULT NULL,
			[[createRule]] TEXT DEFAULT NULL,
			[[updateRule]] TEXT DEFAULT NULL,
			[[deleteRule]] TEXT DEFAULT NULL,
			[[options]]    JSON NOT NULL,
			[[created]]    VARCHAR(255) DEFAULT "" NOT NULL,
			[[updated]]    VARCHAR(255) DEFAULT "" NOT NULL
		);

		CREATE INDEX idx__collections_type on {{_collections}} ([[type]]);
	`
}

// ParamsTableDDL implements the [migrationDialect] interface.
func (MySQLDialect) ParamsTableDDL() string {
	return `
		CREATE TABLE {{_params}} (
			[[id]]      VARCHAR(15) PRIMARY KEY NOT NULL,
			[[value]]   JSON DEFAULT NULL,
			[[created]] VARCHAR(255) DEFAULT "" NOT NULL,
			[[updated]] VARCHAR(255) DEFAULT "" NOT NULL
		);
	`
}

// MigrationAppliedColumnType implements the [migrationDialect] interface.
func (MySQLDialect) MigrationAppliedColumnType() string {
	return "BIGINT"
}

// UseJSONContainsForMultiRelations implements the [relationJoinDialect] interface.
func (MySQLDialect) UseJSONContainsForMultiRelations() bool {
	return true
}

// RelationValueEqualsExpr implements the [relationJoinDialect] interface.
func (MySQLDialect) RelationValueEqualsExpr(left, right string) string {
	return fmt.Sprintf("BINARY %s = BINARY %s", left, right)
}

// RelationArrayContainsExpr implements the [relationJoinDialect] interface.
func (MySQLDialect) RelationArrayContainsExpr(arrayCol, idCol string) string {
	return fmt.Sprintf("JSON_CONTAINS(%s, JSON_QUOTE(%s))", arrayCol, idCol)
}

// translateStrftimeFormat converts SQLite strftime format tokens to MySQL
// DATE_FORMAT tokens.
//
// The only token that MUST be translated is %M (SQLite minutes → MySQL %i).
// Other tokens are either identical or have compatible behavior.
//
// %f (fractional seconds) is left as %f. MySQL returns 6-digit microseconds
// while SQLite returns 3-digit milliseconds. This is a documented behavior
// difference.
func translateStrftimeFormat(format string) string {
	// We need to translate %M → %i but NOT touch %%M or other escaped sequences.
	// SQLite uses %% for a literal %.
	var result strings.Builder
	i := 0
	for i < len(format) {
		if format[i] == '%' && i+1 < len(format) {
			next := format[i+1]
			if next == '%' {
				// literal %, skip both characters
				result.WriteString("%%")
				i += 2
				continue
			}
			if next == 'M' {
				// SQLite %M = minutes → MySQL %i
				result.WriteString("%i")
			} else if next == 'S' {
				// SQLite %S = seconds → MySQL %s (lowercase)
				result.WriteString("%s")
			} else {
				// all other tokens are identical or compatible
				result.WriteByte('%')
				result.WriteByte(next)
			}
			i += 2
		} else {
			result.WriteByte(format[i])
			i++
		}
	}
	return result.String()
}

// DialectForDriver returns the [Dialect] for the provided driver name.
//
// The PB_DATABASE_DRIVER env var takes precedence over the provided
// driverName, allowing operators to force a dialect independent of the
// underlying test/app driver (e.g. pointing a SQLite-backed test app at
// the MySQL code paths).
func DialectForDriver(driverName string) Dialect {
	if strings.EqualFold(os.Getenv(envDatabaseDriver), DialectMySQLName) || strings.EqualFold(driverName, DialectMySQLName) {
		return MySQLDialect{}
	}

	return SQLiteDialect{}
}

// CollectionsTableDDLFor returns the system _collections table DDL for the
// provided dialect, falling back to the SQLite default when the dialect
// does not implement the migrationDialect capability.
func CollectionsTableDDLFor(d Dialect) string {
	if md, ok := d.(migrationDialect); ok {
		return md.CollectionsTableDDL()
	}
	return SQLiteDialect{}.CollectionsTableDDL()
}

// ParamsTableDDLFor returns the system _params table DDL for the provided
// dialect, falling back to the SQLite default when the dialect does not
// implement the migrationDialect capability.
func ParamsTableDDLFor(d Dialect) string {
	if md, ok := d.(migrationDialect); ok {
		return md.ParamsTableDDL()
	}
	return SQLiteDialect{}.ParamsTableDDL()
}

// MigrationAppliedColumnTypeFor returns the _migrations.applied column type
// for the provided dialect, falling back to "INTEGER" (SQLite default) when
// the dialect does not implement the migrationDialect capability.
func MigrationAppliedColumnTypeFor(d Dialect) string {
	if md, ok := d.(migrationDialect); ok {
		return md.MigrationAppliedColumnType()
	}
	return "INTEGER"
}
