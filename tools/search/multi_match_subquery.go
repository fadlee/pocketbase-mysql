package search

import (
	"fmt"
	"strings"

	"github.com/pocketbase/dbx"
)

var _ dbx.Expression = (*MultiMatchSubquery)(nil)

// cloneMultiMatchSubquery creates a shallow copy of the MultiMatchSubquery
// with cloned Params and Joins slice, so that mutations to the clone's
// ValueIdentifier, Params, or Joins do not affect the original.
//
// The individual Join entries are shared (not deep-copied) because join
// definitions are not mutated after creation.
func cloneMultiMatchSubquery(m *MultiMatchSubquery) *MultiMatchSubquery {
	clone := *m
	if m.Params != nil {
		clone.Params = make(dbx.Params, len(m.Params))
		for k, v := range m.Params {
			clone.Params[k] = v
		}
	}
	if m.Joins != nil {
		clone.Joins = make([]*Join, len(m.Joins))
		copy(clone.Joins, m.Joins)
	}
	return &clone
}

// Join defines common fields required for a single SQL JOIN clause.
//
// When RawTableExpr is true, TableName is treated as a raw SQL table
// expression (e.g. `json_each(...)` or `JSON_TABLE(...)`) and is written
// verbatim without being passed through db.QuoteTableName. The TableAlias
// is still quoted normally.
type Join struct {
	TableName    string
	TableAlias   string
	On           dbx.Expression
	RawTableExpr bool
}

// MultiMatchSubquery defines a multi-match record subquery expression.
//
// When ValueIdentifierRaw is true, ValueIdentifier is treated as a raw SQL
// expression and is written verbatim without being passed through
// db.QuoteColumnName.
type MultiMatchSubquery struct {
	TargetTableAlias   string
	FromTableName      string
	FromTableAlias     string
	ValueIdentifier    string
	ValueIdentifierRaw bool
	Joins              []*Join
	Params             dbx.Params
}

// Build converts the expression into a SQL fragment.
//
// Implements [dbx.Expression] interface.
func (m *MultiMatchSubquery) Build(db *dbx.DB, params dbx.Params) string {
	if m.TargetTableAlias == "" || m.FromTableName == "" || m.FromTableAlias == "" {
		return "0=1"
	}

	if params == nil {
		params = m.Params
	} else {
		// merge by updating the parent params
		for k, v := range m.Params {
			params[k] = v
		}
	}

	var mergedJoins strings.Builder
	for i, j := range m.Joins {
		if i > 0 {
			mergedJoins.WriteString(" ")
		}
		mergedJoins.WriteString("LEFT JOIN ")
		if j.RawTableExpr {
			// raw table expression (e.g. json_each(...) or JSON_TABLE(...))
			// should not be quoted
			mergedJoins.WriteString(j.TableName)
		} else {
			mergedJoins.WriteString(db.QuoteTableName(j.TableName))
		}
		mergedJoins.WriteString(" ")
		mergedJoins.WriteString(db.QuoteTableName(j.TableAlias))
		if j.On != nil {
			mergedJoins.WriteString(" ON ")
			mergedJoins.WriteString(j.On.Build(db, params))
		}
	}

	valueIdentifier := m.ValueIdentifier
	if !m.ValueIdentifierRaw {
		valueIdentifier = db.QuoteColumnName(m.ValueIdentifier)
	}

	return fmt.Sprintf(
		`SELECT %s as [[multiMatchValue]] FROM %s %s %s WHERE %s = %s`,
		valueIdentifier,
		db.QuoteTableName(m.FromTableName),
		db.QuoteTableName(m.FromTableAlias),
		mergedJoins.String(),
		db.QuoteColumnName(m.FromTableAlias+".id"),
		db.QuoteColumnName(m.TargetTableAlias+".id"),
	)
}
