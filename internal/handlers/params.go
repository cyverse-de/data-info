package handlers

import (
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
)

// The query-parameter readers every endpoint shares. Each reports a malformed value as a
// schema failure, because that is where the Clojure service rejects it: compojure-api coerces
// a route's declared parameters before the handler runs, so a non-numeric page never reaches
// the code that would have used it.

// boolParam reads a boolean query parameter.
//
// Only true and false are accepted, case-insensitively. ring-swagger's coercion accepts
// exactly those, and anything else stays a string and fails the Boolean schema, so a request
// carrying yes or 1 is rejected rather than quietly read as one value or the other.
func boolParam(c echo.Context, name string) (bool, error) {
	raw := c.QueryParam(name)
	if raw == "" {
		return false, nil
	}

	switch strings.ToLower(raw) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, schemaError(name + " must be true or false")
	}
}

// requiredIntParam reads an integer query parameter the route declares as required.
//
// Negative and zero values pass: the declared type is an integer and nothing narrower, so
// whatever range check the endpoint needs is the endpoint's own and produces its own error
// code. The tabular endpoints depend on this -- their page and size checks raise
// ERR_PAGE_NOT_POS and ERR_CHUNK_TOO_SMALL rather than a schema failure.
func requiredIntParam(c echo.Context, name string) (int64, error) {
	raw := strings.TrimSpace(c.QueryParam(name))
	if raw == "" {
		return 0, schemaError(name + " is required")
	}

	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, schemaError(name + " must be an integer")
	}
	return value, nil
}

// presentParam reads a query parameter the route declares as required but not as non-blank,
// so a value made entirely of whitespace is accepted.
//
// The separator needs this and would be broken by the obvious alternative. It is declared
// s/Str rather than NonBlankString, a tab is its most common value for a TSV, and echo has
// already percent-decoded it by the time a handler sees it -- so a blank check would reject
// every tab- and space-delimited preview the DE asks for.
func presentParam(c echo.Context, name string) (string, error) {
	values, ok := c.QueryParams()[name]
	if !ok || len(values) == 0 {
		return "", schemaError(name + " is required")
	}
	return values[0], nil
}
