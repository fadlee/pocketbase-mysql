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
