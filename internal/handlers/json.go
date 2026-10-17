package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/labstack/echo/v4"
)

// writeJSON is used instead of c.JSON so bodies have no trailing newline and are labelled
// "application/json;charset=utf-8", as callers have always received.
func writeJSON(c echo.Context, status int, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Blob(status, apierror.JSONContentType, body)
}

func writeJSONOK(c echo.Context, v any) error {
	return writeJSON(c, http.StatusOK, v)
}
