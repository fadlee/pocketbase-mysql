package core

import (
	"database/sql"
	"fmt"

	"github.com/pocketbase/dbx"
)

// TableColumns returns all column names of a single table by its name.
func (app *BaseApp) TableColumns(tableName string) ([]string, error) {
	columns := []string{}
	if isMySQLDataDB(app) {
		err := app.ConcurrentDB().NewQuery(`
			SELECT COLUMN_NAME
			FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = {:tableName}
			ORDER BY ORDINAL_POSITION
		`).Bind(dbx.Params{"tableName": tableName}).Column(&columns)

		return columns, err
	}

	err := app.ConcurrentDB().NewQuery("SELECT name FROM PRAGMA_TABLE_INFO({:tableName})").
		Bind(dbx.Params{"tableName": tableName}).
		Column(&columns)

	return columns, err
}

type TableInfoRow struct {
	// the `db:"pk"` tag has special semantic so we cannot rename
	// the original field without specifying a custom mapper
	PK int

	Index        int            `db:"cid"`
	Name         string         `db:"name"`
	Type         string         `db:"type"`
	NotNull      bool           `db:"notnull"`
	DefaultValue sql.NullString `db:"dflt_value"`
}

// TableInfo returns the "table_info" pragma result for the specified table.
func (app *BaseApp) TableInfo(tableName string) ([]*TableInfoRow, error) {
	info := []*TableInfoRow{}
	if isMySQLDataDB(app) {
		err := app.ConcurrentDB().NewQuery(`
			SELECT
				ORDINAL_POSITION - 1 AS cid,
				COLUMN_NAME AS name,
				COLUMN_TYPE AS type,
				CASE WHEN IS_NULLABLE = 'NO' THEN 1 ELSE 0 END AS notnull,
				COLUMN_DEFAULT AS dflt_value,
				CASE WHEN COLUMN_KEY = 'PRI' THEN 1 ELSE 0 END AS pk
			FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = {:tableName}
			ORDER BY ORDINAL_POSITION
		`).Bind(dbx.Params{"tableName": tableName}).All(&info)
		if err != nil {
			return nil, err
		}
		if len(info) == 0 {
			return nil, fmt.Errorf("empty table info probably due to invalid or missing table %s", tableName)
		}

		return info, nil
	}

	err := app.ConcurrentDB().NewQuery("SELECT * FROM PRAGMA_TABLE_INFO({:tableName})").
		Bind(dbx.Params{"tableName": tableName}).
		All(&info)
	if err != nil {
		return nil, err
	}

	// mattn/go-sqlite3 doesn't throw an error on invalid or missing table
	// so we additionally have to check whether the loaded info result is nonempty
	if len(info) == 0 {
		return nil, fmt.Errorf("empty table info probably due to invalid or missing table %s", tableName)
	}

	return info, nil
}

// TableIndexes returns a name grouped map with all non empty index of the specified table.
//
// Note: This method doesn't return an error on nonexisting table.
func (app *BaseApp) TableIndexes(tableName string) (map[string]string, error) {
	indexes := []struct {
		Name string
		Sql  string
	}{}
	if isMySQLDataDB(app) {
		err := app.ConcurrentDB().NewQuery(`
			SELECT INDEX_NAME AS name, CONCAT('INDEX ', INDEX_NAME) AS sql
			FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = {:tableName} AND INDEX_NAME != 'PRIMARY'
			GROUP BY INDEX_NAME
		`).Bind(dbx.Params{"tableName": tableName}).All(&indexes)
		if err != nil {
			return nil, err
		}

		result := make(map[string]string, len(indexes))
		for _, idx := range indexes {
			result[idx.Name] = idx.Sql
		}

		return result, nil
	}

	err := app.ConcurrentDB().Select("name", "sql").
		From("sqlite_master").
		AndWhere(dbx.NewExp("sql is not null")).
		AndWhere(dbx.HashExp{
			"type":     "index",
			"tbl_name": tableName,
		}).
		All(&indexes)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string, len(indexes))

	for _, idx := range indexes {
		result[idx.Name] = idx.Sql
	}

	return result, nil
}

// DeleteTable drops the specified table.
//
// This method is a no-op if a table with the provided name doesn't exist.
//
// NB! Be aware that this method is vulnerable to SQL injection and the
// "dangerousTableName" argument must come only from trusted input!
func (app *BaseApp) DeleteTable(dangerousTableName string) error {
	_, err := app.NonconcurrentDB().NewQuery(fmt.Sprintf(
		"DROP TABLE IF EXISTS {{%s}}",
		dangerousTableName,
	)).Execute()

	return err
}

// HasTable checks if a table (or view) with the provided name exists (case insensitive).
// in the data.db.
func (app *BaseApp) HasTable(tableName string) bool {
	if isMySQLDataDB(app) {
		return app.mysqlHasTable(tableName)
	}

	return app.hasTable(app.ConcurrentDB(), tableName)
}

// AuxHasTable checks if a table (or view) with the provided name exists (case insensitive)
// in the auixiliary.db.
func (app *BaseApp) AuxHasTable(tableName string) bool {
	return app.hasTable(app.AuxConcurrentDB(), tableName)
}

func (app *BaseApp) hasTable(db dbx.Builder, tableName string) bool {
	var exists int

	err := db.Select("(1)").
		From("sqlite_schema").
		AndWhere(dbx.HashExp{"type": []any{"table", "view"}}).
		AndWhere(dbx.NewExp("LOWER([[name]])=LOWER({:tableName})", dbx.Params{"tableName": tableName})).
		Limit(1).
		Row(&exists)

	return err == nil && exists > 0
}

func (app *BaseApp) mysqlHasTable(tableName string) bool {
	var exists int

	err := app.ConcurrentDB().NewQuery(`
		SELECT 1
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE()
			AND TABLE_TYPE IN ('BASE TABLE', 'VIEW')
			AND LOWER(TABLE_NAME) = LOWER({:tableName})
		LIMIT 1
	`).Bind(dbx.Params{"tableName": tableName}).Row(&exists)

	return err == nil && exists > 0
}

// Vacuum executes VACUUM on the data.db in order to reclaim unused data db disk space.
func (app *BaseApp) Vacuum() error {
	return app.vacuum(app.NonconcurrentDB())
}

// AuxVacuum executes VACUUM on the auxiliary.db in order to reclaim unused auxiliary db disk space.
func (app *BaseApp) AuxVacuum() error {
	return app.vacuum(app.AuxNonconcurrentDB())
}

func (app *BaseApp) vacuum(db dbx.Builder) error {
	_, err := db.NewQuery("VACUUM").Execute()

	return err
}
