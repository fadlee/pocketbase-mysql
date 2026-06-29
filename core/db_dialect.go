package core

import (
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

// SQLiteDialect represents the SQLite data database dialect.
type SQLiteDialect struct{}

// Name implements the [Dialect] interface.
func (SQLiteDialect) Name() string {
	return DialectSQLiteName
}

// MySQLDialect represents the MySQL data database dialect.
type MySQLDialect struct{}

// Name implements the [Dialect] interface.
func (MySQLDialect) Name() string {
	return DialectMySQLName
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
	if isMySQLDataDB(app) {
		return "JSON NOT NULL"
	}

	return "JSON DEFAULT '[]' NOT NULL"
}
