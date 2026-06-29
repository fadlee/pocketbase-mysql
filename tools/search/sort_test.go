package search_test

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/tools/search"
)

func TestSortFieldBuildExpr(t *testing.T) {
	resolver := search.NewSimpleFieldResolver("test1", "test2", "test3", "test4.sub")

	scenarios := []struct {
		sortField        search.SortField
		expectError      bool
		expectExpression string
	}{
		// empty
		{search.SortField{"", search.SortDesc}, true, ""},
		// unknown field
		{search.SortField{"unknown", search.SortAsc}, true, ""},
		// placeholder field
		{search.SortField{"'test'", search.SortAsc}, true, ""},
		// null field
		{search.SortField{"null", search.SortAsc}, true, ""},
		// allowed field - asc
		{search.SortField{"test1", search.SortAsc}, false, "[[test1]] ASC"},
		// allowed field - desc
		{search.SortField{"test1", search.SortDesc}, false, "[[test1]] DESC"},
		// special @random field (ignore direction)
		{search.SortField{"@random", search.SortDesc}, false, "RANDOM()"},
		// special _rowid_ field
		{search.SortField{"@rowid", search.SortDesc}, false, "[[_rowid_]] DESC"},
	}

	for _, s := range scenarios {
		t.Run(fmt.Sprintf("%s_%s", s.sortField.Name, s.sortField.Name), func(t *testing.T) {
			result, err := s.sortField.BuildExpr(resolver)

			hasErr := err != nil
			if hasErr != s.expectError {
				t.Fatalf("Expected hasErr %v, got %v (%v)", s.expectError, hasErr, err)
			}

			if result != s.expectExpression {
				t.Fatalf("Expected expression %v, got %v", s.expectExpression, result)
			}
		})
	}
}

// TestSortFieldBuildExprRowidDialects verifies that @rowid sort output
// is dialect-specific when the resolver implements the rowidSortResolver
// capability, and falls back to the SQLite default otherwise.
func TestSortFieldBuildExprRowidDialects(t *testing.T) {
	scenarios := []struct {
		name             string
		resolver         search.FieldResolver
		direction        string
		expectError      bool
		expectExpression string
	}{
		{
			"SimpleFieldResolver (SQLite default fallback)",
			search.NewSimpleFieldResolver("id", "test1"),
			search.SortDesc,
			false,
			"[[_rowid_]] DESC",
		},
		{
			"SimpleFieldResolver (SQLite default fallback, asc)",
			search.NewSimpleFieldResolver("id", "test1"),
			search.SortAsc,
			false,
			"[[_rowid_]] ASC",
		},
		{
			"mysql rowid resolver (resolves id)",
			&mysqlRowidTestResolver{identifier: "[[demo1.id]]"},
			search.SortDesc,
			false,
			"[[demo1.id]] DESC",
		},
		{
			"mysql rowid resolver (resolves id, asc)",
			&mysqlRowidTestResolver{identifier: "[[demo1.id]]"},
			search.SortAsc,
			false,
			"[[demo1.id]] ASC",
		},
		{
			"mysql rowid resolver (invalid id)",
			&mysqlRowidTestResolver{identifier: ""},
			search.SortDesc,
			true,
			"",
		},
		{
			"mysql rowid resolver (null id)",
			&mysqlRowidTestResolver{identifier: "null"},
			search.SortDesc,
			true,
			"",
		},
		{
			"sqlite rowid resolver (static expression)",
			&sqliteRowidTestResolver{},
			search.SortDesc,
			false,
			"[[_rowid_]] DESC",
		},
	}

	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			result, err := (&search.SortField{Name: "@rowid", Direction: s.direction}).BuildExpr(s.resolver)

			hasErr := err != nil
			if hasErr != s.expectError {
				t.Fatalf("Expected hasErr %v, got %v (%v)", s.expectError, hasErr, err)
			}

			if result != s.expectExpression {
				t.Fatalf("Expected expression %q, got %q", s.expectExpression, result)
			}
		})
	}
}

// TestSortFieldBuildExprRowidIgnoresEnv verifies that the generic
// SimpleFieldResolver always returns the SQLite default @rowid expression
// regardless of the PB_DATABASE_DRIVER env var value.
func TestSortFieldBuildExprRowidIgnoresEnv(t *testing.T) {
	original := os.Getenv("PB_DATABASE_DRIVER")
	t.Cleanup(func() { os.Setenv("PB_DATABASE_DRIVER", original) })

	resolver := search.NewSimpleFieldResolver("id", "test1")

	for _, driver := range []string{"", "sqlite", "mysql", "MySQL"} {
		t.Run("driver="+driver, func(t *testing.T) {
			os.Setenv("PB_DATABASE_DRIVER", driver)

			result, err := (&search.SortField{Name: "@rowid", Direction: search.SortDesc}).BuildExpr(resolver)
			if err != nil {
				t.Fatalf("Expected no error, got %v", err)
			}

			expected := "[[_rowid_]] DESC"
			if result != expected {
				t.Fatalf("Expected expression %q, got %q", expected, result)
			}
		})
	}
}

// mysqlRowidTestResolver is a test resolver that implements the
// rowidSortResolver capability to simulate MySQL @rowid behavior.
type mysqlRowidTestResolver struct {
	identifier string
}

func (r *mysqlRowidTestResolver) UpdateQuery(_ *dbx.SelectQuery) error { return nil }
func (r *mysqlRowidTestResolver) Resolve(field string) (*search.ResolverResult, error) {
	if field == "id" {
		if r.identifier == "" {
			return &search.ResolverResult{Identifier: ""}, nil
		}
		return &search.ResolverResult{Identifier: r.identifier}, nil
	}
	return nil, fmt.Errorf("failed to resolve field %q", field)
}
func (r *mysqlRowidTestResolver) RowidSortExpr(direction string) (string, error) {
	result, err := r.Resolve("id")
	if err != nil || len(result.Params) > 0 || result.Identifier == "" || strings.ToLower(result.Identifier) == "null" {
		return "", fmt.Errorf("invalid sort field %q", "@rowid")
	}
	return fmt.Sprintf("%s %s", result.Identifier, direction), nil
}

// sqliteRowidTestResolver is a test resolver that implements the
// rowidSortResolver capability to simulate SQLite @rowid behavior.
type sqliteRowidTestResolver struct{}

func (r *sqliteRowidTestResolver) UpdateQuery(_ *dbx.SelectQuery) error { return nil }
func (r *sqliteRowidTestResolver) Resolve(field string) (*search.ResolverResult, error) {
	return &search.ResolverResult{Identifier: "[[" + field + "]]"}, nil
}
func (r *sqliteRowidTestResolver) RowidSortExpr(direction string) (string, error) {
	return fmt.Sprintf("[[_rowid_]] %s", direction), nil
}

func TestParseSortFromString(t *testing.T) {
	scenarios := []struct {
		value    string
		expected string
	}{
		{"", `[{"name":"","direction":"ASC"}]`},
		{"test", `[{"name":"test","direction":"ASC"}]`},
		{"+test", `[{"name":"test","direction":"ASC"}]`},
		{"-test", `[{"name":"test","direction":"DESC"}]`},
		{"test1,-test2,+test3", `[{"name":"test1","direction":"ASC"},{"name":"test2","direction":"DESC"},{"name":"test3","direction":"ASC"}]`},
		{"@random,-test", `[{"name":"@random","direction":"ASC"},{"name":"test","direction":"DESC"}]`},
		{"-@rowid,-test", `[{"name":"@rowid","direction":"DESC"},{"name":"test","direction":"DESC"}]`},
	}

	for _, s := range scenarios {
		t.Run(s.value, func(t *testing.T) {
			result := search.ParseSortFromString(s.value)
			encoded, _ := json.Marshal(result)
			encodedStr := string(encoded)

			if encodedStr != s.expected {
				t.Fatalf("Expected expression %s, got %s", s.expected, encodedStr)
			}
		})
	}
}
