package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestPathParamsAreDecodedOnce(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   string
	}{
		{"plain", "/groups/staff", "staff"},
		{"an escaped @, kept encoded by net/url", "/groups/%40grouper-x", "@grouper-x"},
		{"an unescaped @", "/groups/@grouper-x", "@grouper-x"},
		{"an escaped space, decoded by net/url", "/groups/a%20b", "a b"},
		{"an escaped percent sign is not decoded twice", "/groups/100%2541", "100%41"},
		{"an escaped slash stays inside the parameter", "/groups/a%2Fb", "a/b"},
		{"an escaped hash", "/users/alice%23iplant/groups", "alice#iplant"},
		{"a plus is not a space in a path", "/groups/a+b", "a+b"},
	}

	e := echo.New()
	e.Pre(RouteOnEscapedPath())
	e.Use(DecodePathParams())
	var got, escaped string
	capture := func(name string) echo.HandlerFunc {
		return func(c echo.Context) error {
			got, escaped = c.Param(name), c.Request().URL.EscapedPath()
			return c.NoContent(http.StatusOK)
		}
	}
	e.GET("/groups/:group-name", capture("group-name"))
	e.GET("/users/:username/groups", capture("username"))

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, escaped = "", ""
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			e.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: the route did not match", rec.Code)
			}
			if got != tt.want {
				t.Errorf("param = %q, want %q", got, tt.want)
			}
			// Wildcard routes read the iRODS path from the escaped URL themselves.
			if escaped != tt.target {
				t.Errorf("escaped path = %q, want it left as %q", escaped, tt.target)
			}
		})
	}
}
