package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/labstack/echo/v4"
)

// writeJSON sends v as the response body.
//
// Use this instead of echo.Context.JSON, for two reasons that are both wire differences
// rather than cosmetic ones. echo's JSON encodes with a json.Encoder, which appends a
// trailing newline where cheshire/encode does not. And echo labels a body
// "application/json" where ring labels it "application/json;charset=utf-8" -- measured
// against the running service, which sends the charset on every response it serves.
func writeJSON(c echo.Context, status int, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Blob(status, apierror.JSONContentType, body)
}

// writeJSONOK is writeJSON with a 200.
func writeJSONOK(c echo.Context, v any) error {
	return writeJSON(c, http.StatusOK, v)
}
