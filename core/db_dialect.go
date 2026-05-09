package core

import (
	"os"
	"strings"
)

const (
	envDatabaseDriver = "PB_DATABASE_DRIVER"
	envDatabaseDSN    = "PB_DATABASE_DSN"
)

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
