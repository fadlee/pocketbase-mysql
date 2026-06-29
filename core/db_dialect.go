package core

import (
	"fmt"
	"os"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/dbutils"
	"github.com/pocketbase/pocketbase/tools/search"
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
// used as a replacement for the scattered isMySQLDataDB conditionals.
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

func isMySQLDataDB(app App) bool {
	return IsMySQLDataDB(app)
}

func IsMySQLDataDB(app App) bool {
	if strings.EqualFold(os.Getenv(envDatabaseDriver), "mysql") {
		return true
	}

	db, ok := app.ConcurrentDB().(interface{ DriverName() string })
	return ok && strings.EqualFold(db.DriverName(), "mysql")
}

func jsonArrayColumnType(app App) string {
	return app.Dialect().(columnDialect).JSONArrayColumnType()
}
