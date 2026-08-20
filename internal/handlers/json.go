package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/labstack/echo/v4"
)

// writeJSON sends v as the response body.
//
// Use this instead of echo.Context.JSON. echo's JSON encodes with a json.Encoder, which
// appends a trailing newline; the Clojure service's bodies come from cheshire/encode,
// which does not. Responses are compared byte for byte against that service during the
// port, so the newline is a real difference rather than a cosmetic one.
func writeJSON(c echo.Context, status int, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.JSONBlob(status, body)
}

// writeJSONOK is writeJSON with a 200.
func writeJSONOK(c echo.Context, v any) error {
	return writeJSON(c, http.StatusOK, v)
}
