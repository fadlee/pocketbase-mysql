package search

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ganigeorgiev/fexpr"
	"github.com/pocketbase/dbx"
)

// TokenFunction is the callable shape for all registered token functions.
//
// The fieldResolver parameter provides access to the resolver (and through
// it, dialect capabilities) so that token functions can generate
// dialect-specific SQL expressions.
//
// The argTokenResolverFunc parameter is a closure that resolves individual
// argument tokens to ResolverResults.
type TokenFunction func(
	fieldResolver FieldResolver,
	argTokenResolverFunc func(fexpr.Token) (*ResolverResult, error),
	args ...fexpr.Token,
) (*ResolverResult, error)

var TokenFunctions = map[string]TokenFunction{
	// geoDistance(lonA, latA, lonB, latB) calculates the Haversine
	// distance between 2 points in kilometres (https://www.movable-type.co.uk/scripts/latlong.html).
	//
	// The accepted arguments at the moment could be either a plain number or a column identifier (including NULL).
	// If the column identifier cannot be resolved and converted to a numeric value, it resolves to NULL.
	//
	// Similar to the built-in SQLite functions, geoDistance doesn't apply
	// a "match-all" constraints in case there are multiple relation fields arguments.
	// Or in other words, if a collection has "orgs" multiple relation field pointing to "orgs" collection that has "office" as "geoPoint" field,
	// then the filter: `geoDistance(orgs.office.lon, orgs.office.lat, 1, 2) < 200`
	// will evaluate to true if for at-least-one of the "orgs.office" records the function result in a value satisfying the condition (aka. "result < 200").
	"geoDistance": func(fieldResolver FieldResolver, argTokenResolverFunc func(fexpr.Token) (*ResolverResult, error), args ...fexpr.Token) (*ResolverResult, error) {
		if len(args) != 4 {
			return nil, fmt.Errorf("[geoDistance] expected 4 arguments, got %d", len(args))
		}

		resolvedArgs := make([]*ResolverResult, 4)
		for i, arg := range args {
			if arg.Type != fexpr.TokenIdentifier && arg.Type != fexpr.TokenNumber {
				return nil, fmt.Errorf("[geoDistance] argument %d must be an identifier or number", i)
			}
			resolved, err := argTokenResolverFunc(arg)
			if err != nil {
				return nil, fmt.Errorf("[geoDistance] failed to resolve argument %d: %w", i, err)
			}
			resolvedArgs[i] = resolved
		}

		lonA := resolvedArgs[0].Identifier
		latA := resolvedArgs[1].Identifier
		lonB := resolvedArgs[2].Identifier
		latB := resolvedArgs[3].Identifier

		return &ResolverResult{
			NullFallback: NullFallbackDisabled,
			Identifier: `(6371 * acos(` +
				`cos(radians(` + latA + `)) * cos(radians(` + latB + `)) * ` +
				`cos(radians(` + lonB + `) - radians(` + lonA + `)) + ` +
				`sin(radians(` + latA + `)) * sin(radians(` + latB + `))` +
				`))`,
			Params: mergeParams(resolvedArgs[0].Params, resolvedArgs[1].Params, resolvedArgs[2].Params, resolvedArgs[3].Params),
		}, nil
	},

	// strftime(format, [timeValue, modifier1, modifier2, ...]) returns
	// a date string formatted according to the specified format argument.
	//
	// It is similar to the builtin SQLite strftime function (https://sqlite.org/lang_datefunc.html)
	// with the main difference that NULL results will be normalized for
	// consistency with the non-nullable PocketBase "text" and "date" fields.
	//
	// The function accepts 1, 2 or 3+ arguments.
	//
	// (1) The first (format) argument must be always a formatting string
	// with valid substitutions as listed in https://sqlite.org/lang_datefunc.html.
	//
	// (2) The second (time-value) argument is optional and must be either a date string, number or collection field identifier
	// that matches one of the formats listed in https://sqlite.org/lang_datefunc.html#time_values.
	//
	// (3+) The remaining (modifiers) optional arguments are expected to be
	// string literals matching the listed modifiers in https://sqlite.org/lang_datefunc.html#modifiers.
	//
	// A multi-match constraint will be also applied in case the time-value
	// is an identifier as a result of a multi-value relation field.
	"strftime": func(fieldResolver FieldResolver, argTokenResolverFunc func(fexpr.Token) (*ResolverResult, error), args ...fexpr.Token) (*ResolverResult, error) {
		totalArgs := len(args)

		if totalArgs < 1 {
			return nil, fmt.Errorf("[strftime] expected at least 1 arguments, got %d", len(args))
		}

		if totalArgs > 10 {
			return nil, fmt.Errorf("[strftime] too many arguments (max allowed 10, got %d)", totalArgs)
		}

		// format arg
		if args[0].Type != fexpr.TokenText {
			return nil, errors.New("[strftime] expects the first argument to be a format string")
		}

		formatArgResult, err := argTokenResolverFunc(args[0])
		if err != nil {
			return nil, fmt.Errorf("[strftime] failed to resolve format argument: %w", err)
		}

		// no further arguments → strftime(format)
		if totalArgs == 1 {
			result := &ResolverResult{
				NullFallback: NullFallbackEnforced,
				Identifier:   "strftime(" + formatArgResult.Identifier + ")",
				Params:       dbx.Params{},
			}
			if err = concatUniqueParams(result.Params, formatArgResult.Params); err != nil {
				return nil, err
			}

			// try dialect-specific expression
			if sr, ok := fieldResolver.(strftimeResolver); ok {
				dialectArgs := []TokenFunctionArg{
					{Token: args[0].Type, Literal: args[0].Literal, Result: formatArgResult},
				}
				expr, dparams, derr := sr.StrftimeExpr(dialectArgs)
				if derr != nil {
					return nil, fmt.Errorf("[strftime] %w", derr)
				}
				result.Identifier = expr
				if dparams != nil {
					if err = concatUniqueParams(result.Params, dparams); err != nil {
						return nil, err
					}
				}
			}

			return result, nil
		}

		// time-value arg
		allowedTimeValueTokens := []fexpr.TokenType{fexpr.TokenText, fexpr.TokenIdentifier, fexpr.TokenNumber}
		if !slices.Contains(allowedTimeValueTokens, args[1].Type) {
			return nil, errors.New("[strftime] expects the second argument to be of a valid time-value type")
		}

		timeValueArgResult, err := argTokenResolverFunc(args[1])
		if err != nil {
			return nil, fmt.Errorf("[strftime] failed to resolve time-value argument: %w", err)
		}

		// modifiers args
		resolvedModifierArgs := make([]*ResolverResult, totalArgs-2)
		for i, arg := range args[2:] {
			if arg.Type != fexpr.TokenText {
				return nil, fmt.Errorf("[strftime] invalid modifier argument %d - can be only string", i)
			}

			resolved, err := argTokenResolverFunc(arg)
			if err != nil {
				return nil, fmt.Errorf("[strftime] failed to resolve modifier argument %d: %w", i, err)
			}

			resolvedModifierArgs[i] = resolved
		}

		// build TokenFunctionArg slice for dialect
		dialectArgs := make([]TokenFunctionArg, totalArgs)
		dialectArgs[0] = TokenFunctionArg{Token: args[0].Type, Literal: args[0].Literal, Result: formatArgResult}
		dialectArgs[1] = TokenFunctionArg{Token: args[1].Type, Literal: args[1].Literal, Result: timeValueArgResult}
		for i, m := range resolvedModifierArgs {
			dialectArgs[i+2] = TokenFunctionArg{Token: args[i+2].Type, Literal: args[i+2].Literal, Result: m}
		}

		// generating new ResolverResult
		result := &ResolverResult{
			NullFallback: NullFallbackEnforced,
			Params:       dbx.Params{},
		}

		// merge all params
		if err = concatUniqueParams(result.Params, formatArgResult.Params); err != nil {
			return nil, err
		}
		if err = concatUniqueParams(result.Params, timeValueArgResult.Params); err != nil {
			return nil, err
		}
		for _, m := range resolvedModifierArgs {
			if err = concatUniqueParams(result.Params, m.Params); err != nil {
				return nil, err
			}
		}

		// try dialect-specific expression
		if sr, ok := fieldResolver.(strftimeResolver); ok {
			expr, dparams, derr := sr.StrftimeExpr(dialectArgs)
			if derr != nil {
				return nil, fmt.Errorf("[strftime] %w", derr)
			}
			result.Identifier = expr
			if dparams != nil {
				if err = concatUniqueParams(result.Params, dparams); err != nil {
					return nil, err
				}
			}

			// multi-match: call StrftimeExpr with multi-match time value
			if timeValueArgResult.MultiMatchSubQuery != nil {
				mmArgs := make([]TokenFunctionArg, len(dialectArgs))
				copy(mmArgs, dialectArgs)
				mmArgs[1] = TokenFunctionArg{
					Token:   dialectArgs[1].Token,
					Literal: dialectArgs[1].Literal,
					Result: &ResolverResult{
						Identifier:         timeValueArgResult.MultiMatchSubQuery.ValueIdentifier,
						Params:             timeValueArgResult.MultiMatchSubQuery.Params,
						MultiMatchSubQuery: nil,
					},
				}

				mmExpr, mmParams, mmErr := sr.StrftimeExpr(mmArgs)
				if mmErr != nil {
					return nil, fmt.Errorf("[strftime] multi-match: %w", mmErr)
				}

				// clone MultiMatchSubQuery before mutating
				mmClone := cloneMultiMatchSubquery(timeValueArgResult.MultiMatchSubQuery)
				mmClone.ValueIdentifier = mmExpr
				result.MultiMatchSubQuery = mmClone

				if mmParams != nil {
					if err = concatUniqueParams(mmClone.Params, mmParams); err != nil {
						return nil, err
					}
					if err = concatUniqueParams(result.Params, mmParams); err != nil {
						return nil, err
					}
				}
			}

			return result, nil
		}

		// SQLite/default fallback (clone-safe)
		identifiers := make([]string, 0, totalArgs)
		identifiers = append(identifiers, formatArgResult.Identifier)
		identifiers = append(identifiers, timeValueArgResult.Identifier)
		for _, m := range resolvedModifierArgs {
			identifiers = append(identifiers, m.Identifier)
		}

		result.Identifier = "strftime(" + strings.Join(identifiers, ",") + ")"

		if timeValueArgResult.MultiMatchSubQuery != nil {
			// clone MultiMatchSubQuery before mutating
			mmClone := cloneMultiMatchSubquery(timeValueArgResult.MultiMatchSubQuery)

			// replace the regular time-value identifier with the multi-match one
			identifiers[1] = timeValueArgResult.MultiMatchSubQuery.ValueIdentifier
			mmClone.ValueIdentifier = "strftime(" + strings.Join(identifiers, ",") + ")"

			result.MultiMatchSubQuery = mmClone

			if err = concatUniqueParams(mmClone.Params, result.Params); err != nil {
				return nil, err
			}
		}

		return result, nil
	},
}

func concatUniqueParams(destParams, newParams dbx.Params) error {
	for k, v := range newParams {
		found, ok := destParams[k]
		if ok && v != found {
			return fmt.Errorf("conflicting param key %s", k)
		}

		destParams[k] = v
	}

	return nil
}
