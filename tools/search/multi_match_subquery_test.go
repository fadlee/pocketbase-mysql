package search_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/search"
)

func TestMultiMatchSubqueryBuild(t *testing.T) {
	// create a dummy db
	sqlDB, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db := dbx.NewFromDB(sqlDB, "sqlite")

	mm := search.MultiMatchSubquery{
		TargetTableAlias: "test_TargetTableAlias",
		FromTableName:    "test_FromTableName",
		FromTableAlias:   "test_FromTableAlias",
		ValueIdentifier:  "({:mm},{:external})",
		Joins: []*search.Join{
			{TableName: "join_table1", TableAlias: "join_alias1"},
			{TableName: "join_table2", TableAlias: "join_alias2", On: dbx.NewExp("123={:join}", dbx.Params{"join": "test_join"})},
		},
		Params: dbx.Params{"mm": "test_mm"},
	}

	params := dbx.Params{"external": "test_external"}

	result := mm.Build(db, params)

	expectedResult := "SELECT ({:mm},{:external}) as [[multiMatchValue]] FROM `test_FromTableName` `test_FromTableAlias` LEFT JOIN `join_table1` `join_alias1` LEFT JOIN `join_table2` `join_alias2` ON 123={:join} WHERE `test_FromTableAlias`.`id` = `test_TargetTableAlias`.`id`"
	if expectedResult != result {
		t.Fatalf("Expected build result\n%v\ngot\n%v", expectedResult, result)
	}

	// the params from all expressions should be merged in the root
	rawParams, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}

	expectedParams := []byte(`{"external":"test_external","join":"test_join","mm":"test_mm"}`)
	if !bytes.Equal(rawParams, expectedParams) {
		t.Fatalf("Expected final params\n%s\ngot\n%s", expectedParams, rawParams)
	}
}

func TestMultiMatchSubqueryBuildRawTableExpr(t *testing.T) {
	// create a dummy db
	sqlDB, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db := dbx.NewFromDB(sqlDB, "sqlite")

	t.Run("json_each raw table expression (alias still quoted)", func(t *testing.T) {
		mm := search.MultiMatchSubquery{
			TargetTableAlias: "test_TargetTableAlias",
			FromTableName:    "test_FromTableName",
			FromTableAlias:   "test_FromTableAlias",
			ValueIdentifier:  "test_FromTableAlias.field",
			Joins: []*search.Join{
				{
					TableName:    "json_each({:jeParam})",
					TableAlias:   "je_alias",
					RawTableExpr: true,
				},
			},
			Params: dbx.Params{"jeParam": "test_je"},
		}

		result := mm.Build(db, dbx.Params{})

		expectedResult := "SELECT `test_FromTableAlias`.`field` as [[multiMatchValue]] FROM `test_FromTableName` `test_FromTableAlias` LEFT JOIN json_each({:jeParam}) `je_alias` WHERE `test_FromTableAlias`.`id` = `test_TargetTableAlias`.`id`"
		if expectedResult != result {
			t.Fatalf("Expected build result\n%v\ngot\n%v", expectedResult, result)
		}
	})

	t.Run("JSON_TABLE raw table expression (alias still quoted)", func(t *testing.T) {
		mm := search.MultiMatchSubquery{
			TargetTableAlias: "test_TargetTableAlias",
			FromTableName:    "test_FromTableName",
			FromTableAlias:   "test_FromTableAlias",
			ValueIdentifier:  "test_FromTableAlias.field",
			Joins: []*search.Join{
				{
					TableName:    "JSON_TABLE({:jtParam}, '$[*]' COLUMNS (value VARCHAR(255) PATH '$'))",
					TableAlias:   "jt_alias",
					RawTableExpr: true,
				},
			},
			Params: dbx.Params{"jtParam": "test_jt"},
		}

		result := mm.Build(db, dbx.Params{})

		expectedResult := "SELECT `test_FromTableAlias`.`field` as [[multiMatchValue]] FROM `test_FromTableName` `test_FromTableAlias` LEFT JOIN JSON_TABLE({:jtParam}, '$[*]' COLUMNS (value VARCHAR(255) PATH '$')) `jt_alias` WHERE `test_FromTableAlias`.`id` = `test_TargetTableAlias`.`id`"
		if expectedResult != result {
			t.Fatalf("Expected build result\n%v\ngot\n%v", expectedResult, result)
		}
	})

	t.Run("regular join table names are still quoted", func(t *testing.T) {
		mm := search.MultiMatchSubquery{
			TargetTableAlias: "test_TargetTableAlias",
			FromTableName:    "test_FromTableName",
			FromTableAlias:   "test_FromTableAlias",
			ValueIdentifier:  "test_FromTableAlias.field",
			Joins: []*search.Join{
				{TableName: "regular_table", TableAlias: "regular_alias"},
			},
		}

		result := mm.Build(db, dbx.Params{})

		expectedResult := "SELECT `test_FromTableAlias`.`field` as [[multiMatchValue]] FROM `test_FromTableName` `test_FromTableAlias` LEFT JOIN `regular_table` `regular_alias` WHERE `test_FromTableAlias`.`id` = `test_TargetTableAlias`.`id`"
		if expectedResult != result {
			t.Fatalf("Expected build result\n%v\ngot\n%v", expectedResult, result)
		}
	})
}

func TestMultiMatchSubqueryBuildRawValueIdentifier(t *testing.T) {
	// create a dummy db
	sqlDB, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db := dbx.NewFromDB(sqlDB, "sqlite")

	t.Run("raw value identifier is not quoted", func(t *testing.T) {
		mm := search.MultiMatchSubquery{
			TargetTableAlias:   "test_TargetTableAlias",
			FromTableName:      "test_FromTableName",
			FromTableAlias:     "test_FromTableAlias",
			ValueIdentifier:    "SOME_RAW_COLUMN_EXPR",
			ValueIdentifierRaw: true,
		}

		result := mm.Build(db, dbx.Params{})

		expectedResult := "SELECT SOME_RAW_COLUMN_EXPR as [[multiMatchValue]] FROM `test_FromTableName` `test_FromTableAlias`  WHERE `test_FromTableAlias`.`id` = `test_TargetTableAlias`.`id`"
		if expectedResult != result {
			t.Fatalf("Expected build result\n%v\ngot\n%v", expectedResult, result)
		}
	})

	t.Run("regular value identifier is still quoted", func(t *testing.T) {
		mm := search.MultiMatchSubquery{
			TargetTableAlias: "test_TargetTableAlias",
			FromTableName:    "test_FromTableName",
			FromTableAlias:   "test_FromTableAlias",
			ValueIdentifier:  "test_FromTableAlias.field",
		}

		result := mm.Build(db, dbx.Params{})

		expectedResult := "SELECT `test_FromTableAlias`.`field` as [[multiMatchValue]] FROM `test_FromTableName` `test_FromTableAlias`  WHERE `test_FromTableAlias`.`id` = `test_TargetTableAlias`.`id`"
		if expectedResult != result {
			t.Fatalf("Expected build result\n%v\ngot\n%v", expectedResult, result)
		}
	})
}
