package search

import (
	"fmt"
	"strings"
)

const (
	randomSortKey string = "@random"
	rowidSortKey  string = "@rowid"
)

// sort field directions
const (
	SortAsc  string = "ASC"
	SortDesc string = "DESC"
)

// SortField defines a single search sort field.
type SortField struct {
	Name      string `json:"name"`
	Direction string `json:"direction"`
}

// rowidSortResolver is an optional capability interface that resolvers
// can implement to provide dialect-specific @rowid sort expressions.
//
// When a resolver does not implement this interface, the SQLite default
// (`[[_rowid_]] {direction}`) is used.
type rowidSortResolver interface {
	RowidSortExpr(direction string) (string, error)
}

// BuildExpr resolves the sort field into a valid db sort expression.
func (s *SortField) BuildExpr(fieldResolver FieldResolver) (string, error) {
	// special case for random sort
	if s.Name == randomSortKey {
		return "RANDOM()", nil
	}

	// special case for the builtin rowid column
	if s.Name == rowidSortKey {
		if r, ok := fieldResolver.(rowidSortResolver); ok {
			return r.RowidSortExpr(s.Direction)
		}

		// SQLite default fallback
		return fmt.Sprintf("[[_rowid_]] %s", s.Direction), nil
	}

	result, err := fieldResolver.Resolve(s.Name)

	// invalidate empty fields and non-column identifiers
	if err != nil || len(result.Params) > 0 || result.Identifier == "" || strings.ToLower(result.Identifier) == "null" {
		return "", fmt.Errorf("invalid sort field %q", s.Name)
	}

	return fmt.Sprintf("%s %s", result.Identifier, s.Direction), nil
}

// ParseSortFromString parses the provided string expression
// into a slice of SortFields.
//
// Example:
//
//	fields := search.ParseSortFromString("-name,+created")
func ParseSortFromString(str string) (fields []SortField) {
	data := strings.Split(str, ",")

	for _, field := range data {
		// trim whitespaces
		field = strings.TrimSpace(field)
		if strings.HasPrefix(field, "-") {
			fields = append(fields, SortField{strings.TrimPrefix(field, "-"), SortDesc})
		} else {
			fields = append(fields, SortField{strings.TrimPrefix(field, "+"), SortAsc})
		}
	}

	return
}
