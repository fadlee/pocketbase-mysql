package core

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/search"
)

// TestRegisterJoinExpr tests that registerJoinExpr registers a raw join
// that skips collection/list-rule lookup and is not quoted in the output SQL.
func TestRegisterJoinExpr(t *testing.T) {
	t.Run("registers raw join skipping collection/list-rule lookup", func(t *testing.T) {
		// registerJoinExpr does not use r.app, so a zero-value resolver is sufficient
		r := &RecordFieldResolver{}

		// registerJoinExpr should succeed even without allowHiddenFields
		// because it skips the collection/list-rule lookup entirely
		err := r.registerJoinExpr("json_each({:jeParam})", "test_raw_alias", nil)
		if err != nil {
			t.Fatalf("Expected no error from registerJoinExpr, got %v", err)
		}

		// verify the join was registered with RawTableExpr=true
		if len(r.joins) != 1 {
			t.Fatalf("Expected 1 join, got %d", len(r.joins))
		}
		if r.joins[0].TableName != "json_each({:jeParam})" {
			t.Fatalf("Expected TableName %q, got %q", "json_each({:jeParam})", r.joins[0].TableName)
		}
		if r.joins[0].TableAlias != "test_raw_alias" {
			t.Fatalf("Expected TableAlias %q, got %q", "test_raw_alias", r.joins[0].TableAlias)
		}
		if !r.joins[0].RawTableExpr {
			t.Fatal("Expected RawTableExpr to be true")
		}

		// no list rule joins should have been registered
		if len(r.listRuleJoins) != 0 {
			t.Fatalf("Expected 0 list rule joins, got %d", len(r.listRuleJoins))
		}
	})

	t.Run("replaces existing join with same alias", func(t *testing.T) {
		r := &RecordFieldResolver{}

		err := r.registerJoinExpr("json_each({:p1})", "dup_alias", nil)
		if err != nil {
			t.Fatal(err)
		}

		err = r.registerJoinExpr("JSON_TABLE({:p2}, '$' COLUMNS (val VARCHAR(255) PATH '$'))", "dup_alias", nil)
		if err != nil {
			t.Fatal(err)
		}

		if len(r.joins) != 1 {
			t.Fatalf("Expected 1 join after replace, got %d", len(r.joins))
		}
		if r.joins[0].TableName != "JSON_TABLE({:p2}, '$' COLUMNS (val VARCHAR(255) PATH '$'))" {
			t.Fatalf("Expected replaced TableName, got %q", r.joins[0].TableName)
		}
	})

	t.Run("raw join builds unquoted in MultiMatchSubquery", func(t *testing.T) {
		// verify that a Join with RawTableExpr=true renders unquoted via
		// MultiMatchSubquery.Build (the primary consumer of the flag)
		r := &RecordFieldResolver{}
		err := r.registerJoinExpr("json_each({:jeParam})", "je_alias", nil)
		if err != nil {
			t.Fatal(err)
		}

		// build a MultiMatchSubquery using the registered join
		joins := make([]*search.Join, 0, len(r.joins))
		for _, j := range r.joins {
			joins = append(joins, &search.Join{
				TableName:    j.TableName,
				TableAlias:   j.TableAlias,
				On:           j.On,
				RawTableExpr: j.RawTableExpr,
			})
		}

		mm := &search.MultiMatchSubquery{
			TargetTableAlias: "target",
			FromTableName:    "from_table",
			FromTableAlias:   "from_alias",
			ValueIdentifier:  "from_alias.field",
			Joins:            joins,
		}

		sqlDB, err := sql.Open("sqlite", "file::memory:?cache=shared")
		if err != nil {
			t.Fatal(err)
		}
		db := dbx.NewFromDB(sqlDB, "sqlite")

		result := mm.Build(db, dbx.Params{})

		// the raw table expression should appear unquoted
		if !strings.Contains(result, "LEFT JOIN json_each({:jeParam}) `je_alias`") {
			t.Fatalf("Expected unquoted raw table expression in build result:\n%s", result)
		}
	})
}
