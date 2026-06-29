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

func TestDialectForDriver(t *testing.T) {
	t.Run("empty driver returns SQLite", func(t *testing.T) {
		require.Equal(t, core.DialectSQLiteName, core.DialectForDriver("").Name())
	})

	t.Run("sqlite driver returns SQLite", func(t *testing.T) {
		require.Equal(t, core.DialectSQLiteName, core.DialectForDriver("sqlite").Name())
	})

	t.Run("mysql driver returns MySQL", func(t *testing.T) {
		require.Equal(t, core.DialectMySQLName, core.DialectForDriver("mysql").Name())
	})

	t.Run("env var overrides driver name", func(t *testing.T) {
		t.Setenv("PB_DATABASE_DRIVER", "mysql")
		require.Equal(t, core.DialectMySQLName, core.DialectForDriver("sqlite").Name())
	})

	t.Run("env var sqlite does not override mysql driver", func(t *testing.T) {
		t.Setenv("PB_DATABASE_DRIVER", "sqlite")
		require.Equal(t, core.DialectMySQLName, core.DialectForDriver("mysql").Name())
	})
}

func TestEnvForcedMySQLDialectWithSQLiteTestApp(t *testing.T) {
	t.Setenv("PB_DATABASE_DRIVER", "mysql")

	app, err := tests.NewTestAppWithConfig(core.BaseAppConfig{
		DBConnect: func(dbPath string) (*dbx.DB, error) {
			pragmas := "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
			return dbx.Open("sqlite", dbPath+pragmas)
		},
	})
	require.NoError(t, err)
	defer app.Cleanup()

	require.NotNil(t, app.Dialect())
	require.Equal(t, core.DialectMySQLName, app.Dialect().Name())
}

func TestIntrospectionDialectSQLite(t *testing.T) {
	d := core.SQLiteDialect{}

	require.Equal(t, "SELECT name FROM PRAGMA_TABLE_INFO({:tableName})", d.TableColumnsQuery())
	require.Equal(t, "SELECT * FROM PRAGMA_TABLE_INFO({:tableName})", d.TableInfoQuery())
	require.Equal(t, "SELECT name, sql FROM sqlite_master WHERE sql is not null AND type = 'index' AND tbl_name = {:tableName}", d.TableIndexesQuery())
	require.Equal(t, "SELECT (1) FROM sqlite_schema WHERE type IN ('table', 'view') AND LOWER(name) = LOWER({:tableName}) LIMIT 1", d.HasTableQuery())
	require.Equal(t, "SELECT name, sql FROM sqlite_master WHERE sql is not null AND type = 'view'", d.ViewsQuery())
	require.Equal(t, "SELECT tbl_name FROM sqlite_master WHERE type = 'index' AND LOWER(tbl_name) != LOWER({:oldName}) AND LOWER(tbl_name) != LOWER({:newName}) AND LOWER(name) = LOWER({:indexName}) LIMIT 1", d.IndexOwnerQuery())
}

func TestIntrospectionDialectMySQL(t *testing.T) {
	d := core.MySQLDialect{}

	require.Equal(t, `SELECT COLUMN_NAME
			FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = {:tableName}
			ORDER BY ORDINAL_POSITION`, d.TableColumnsQuery())

	require.Equal(t, `SELECT
				ORDINAL_POSITION - 1 AS cid,
				COLUMN_NAME AS name,
				COLUMN_TYPE AS type,
				CASE WHEN IS_NULLABLE = 'NO' THEN 1 ELSE 0 END AS notnull,
				COLUMN_DEFAULT AS dflt_value,
				CASE WHEN COLUMN_KEY = 'PRI' THEN 1 ELSE 0 END AS pk
			FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = {:tableName}
			ORDER BY ORDINAL_POSITION`, d.TableInfoQuery())

	require.Equal(t, `SELECT INDEX_NAME AS name, CONCAT('INDEX ', INDEX_NAME) AS sql
			FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = {:tableName} AND INDEX_NAME != 'PRIMARY'
			GROUP BY INDEX_NAME`, d.TableIndexesQuery())

	require.Equal(t, `SELECT 1
			FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = DATABASE()
				AND TABLE_TYPE IN ('BASE TABLE', 'VIEW')
				AND LOWER(TABLE_NAME) = LOWER({:tableName})
			LIMIT 1`, d.HasTableQuery())

	require.Equal(t, `SELECT TABLE_NAME AS name, VIEW_DEFINITION AS sql
			FROM information_schema.VIEWS
			WHERE TABLE_SCHEMA = DATABASE()`, d.ViewsQuery())

	require.Equal(t, `SELECT TABLE_NAME
			FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = DATABASE()
				AND LOWER(TABLE_NAME) != LOWER({:oldName})
				AND LOWER(TABLE_NAME) != LOWER({:newName})
				AND LOWER(INDEX_NAME) = LOWER({:indexName})
			LIMIT 1`, d.IndexOwnerQuery())
}
