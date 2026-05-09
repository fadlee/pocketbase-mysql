//go:build !no_default_driver

package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	"github.com/pocketbase/dbx"
	_ "modernc.org/sqlite"
)

func DefaultDBConnect(dbPath string) (*dbx.DB, error) {
	if strings.EqualFold(os.Getenv(envDatabaseDriver), "mysql") && filepath.Base(dbPath) == "data.db" {
		dsn := os.Getenv(envDatabaseDSN)
		if dsn == "" {
			return nil, fmt.Errorf("%s is required when %s=mysql", envDatabaseDSN, envDatabaseDriver)
		}

		return dbx.Open("mysql", dsn)
	}

	// Note: the busy_timeout pragma must be first because
	// the connection needs to be set to block on busy before WAL mode
	// is set in case it hasn't been already set by another connection.
	pragmas := "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=journal_size_limit(200000000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-32000)"

	db, err := dbx.Open("sqlite", dbPath+pragmas)
	if err != nil {
		return nil, err
	}

	return db, nil
}
