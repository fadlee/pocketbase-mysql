package core

import (
	"fmt"
	"os"
	"strings"
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
