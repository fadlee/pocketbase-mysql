package search

// EqualityOperatorSet groups the SQL operator strings used when building
// equality (=) or inequality (!=) expressions for a specific dialect.
type EqualityOperatorSet struct {
	EqualOp     string
	NullEqualOp string
	NullConcat  string
	NullExpr    string
}

// EqualityOperators holds the operator sets for both equality and
// inequality comparisons.
type EqualityOperators struct {
	Equal    EqualityOperatorSet
	NotEqual EqualityOperatorSet
}
