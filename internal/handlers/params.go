package handlers

import (
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
)

// boolParam accepts only true or false (any case); anything else, such as yes or 1, is a
// schema failure.
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

// requiredIntParam does no range check, so endpoints can raise their own codes
// (ERR_PAGE_NOT_POS, ERR_CHUNK_TOO_SMALL) for non-positive values.
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

// presentParam requires the parameter but accepts a blank value, because a separator is
// usually a tab or space and echo has already decoded it.
func presentParam(c echo.Context, name string) (string, error) {
	values, ok := c.QueryParams()[name]
	if !ok || len(values) == 0 {
		return "", schemaError(name + " is required")
	}
	return values[0], nil
}
