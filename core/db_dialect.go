package core

import (
	"fmt"
	"os"
	"strings"

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
// MySQL doesn't support `IS NOT` with non-NULL operands (only `IS NOT NULL`,
// `IS NOT TRUE`, etc.), so `<>` is used for value comparisons while keeping
// `IS NOT NULL` for the null check which works in both drivers.
func (MySQLDialect) EqualityOperators() search.EqualityOperators {
	return search.EqualityOperators{
		Equal: search.EqualityOperatorSet{
			EqualOp:     "=",
			NullEqualOp: "IS",
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
