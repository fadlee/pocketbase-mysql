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
