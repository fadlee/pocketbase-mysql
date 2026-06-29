package dbutils

import (
	"fmt"
	"os"
	"strings"
)

// JSONEach returns JSON_EACH SQLite string expression with
// some normalizations for non-json columns.
//
// Transitional: this helper still checks PB_DATABASE_DRIVER for unmigrated
// call sites in core/view.go, core/record_query_expand.go, and
// core/record_model.go. The dialect methods (SQLiteDialect.JSONEachColumnExpr,
// MySQLDialect.JSONEachColumnExpr) do NOT use this helper and are
// env-var-independent. The env var check will be removed in Task 11.1
// once all call sites are migrated to use the dialect.
func JSONEach(column string) string {
	if strings.EqualFold(os.Getenv("PB_DATABASE_DRIVER"), "mysql") {
		return fmt.Sprintf(
			`JSON_TABLE(CASE WHEN JSON_VALID([[%s]]) AND JSON_TYPE([[%s]]) = 'ARRAY' THEN [[%s]] ELSE JSON_ARRAY([[%s]]) END, '$[*]' COLUMNS(value VARCHAR(255) PATH '$'))`,
			column, column, column, column,
		)
	}

	// note: we are not using the new and shorter "if(x,y)" syntax for
	// compatibility with custom drivers that use older SQLite version
	return fmt.Sprintf(
		`json_each(CASE WHEN iif(json_valid([[%s]]), json_type([[%s]])='array', FALSE) THEN [[%s]] ELSE json_array([[%s]]) END)`,
		column, column, column, column,
	)
}

// JSONArrayLength returns JSON_ARRAY_LENGTH SQLite string expression
// with some normalizations for non-json columns.
//
// It works with both json and non-json column values.
//
// Returns 0 for empty string or NULL column values.
func JSONArrayLength(column string) string {
	// note: we are not using the new and shorter "if(x,y)" syntax for
	// compatibility with custom drivers that use older SQLite version
	return fmt.Sprintf(
		`json_array_length(CASE WHEN iif(json_valid([[%s]]), json_type([[%s]])='array', FALSE) THEN [[%s]] ELSE (CASE WHEN [[%s]] = '' OR [[%s]] IS NULL THEN json_array() ELSE json_array([[%s]]) END) END)`,
		column, column, column, column, column, column,
	)
}

// JSONExtract returns a JSON_EXTRACT SQLite string expression with
// some normalizations for non-json columns.
func JSONExtract(column string, path string) string {
	// prefix the path with dot if it is not starting with array notation
	if path != "" && !strings.HasPrefix(path, "[") {
		path = "." + path
	}

	return fmt.Sprintf(
		// note: the extra object wrapping is needed to workaround the cases where a json_extract is used with non-json columns.
		"(CASE WHEN json_valid([[%s]]) THEN JSON_EXTRACT([[%s]], '$%s') ELSE JSON_EXTRACT(json_object('pb', [[%s]]), '$.pb%s') END)",
		column,
		column,
		path,
		column,
		path,
	)
}
