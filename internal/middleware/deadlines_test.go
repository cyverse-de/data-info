package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// TestDeadlinesToleratesRecorders makes sure the middleware does not fail a request just
// because the writer cannot take a deadline, which is the case under httptest.
func TestDeadlinesToleratesRecorders(t *testing.T) {
	e := echo.New()
	e.Use(Deadlines(time.Second, time.Minute, func(echo.Context) bool { return false }))
	e.GET("/x", func(c echo.Context) error { return c.String(http.StatusOK, "served") })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (%s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if rec.Body.String() != "served" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "served")
	}
}

// TestDeadlinesAppliesToRealConnections exercises the path httptest cannot: a real server
// where SetWriteDeadline is honoured.
func TestDeadlinesAppliesToRealConnections(t *testing.T) {
	e := echo.New()
	e.Use(Deadlines(50*time.Millisecond, time.Hour, func(c echo.Context) bool {
		return c.Path() == "/slow-upload"
	}))
	e.GET("/slow", func(c echo.Context) error {
		time.Sleep(300 * time.Millisecond)
		return c.String(http.StatusOK, "late")
	})
	e.GET("/slow-upload", func(c echo.Context) error {
		time.Sleep(300 * time.Millisecond)
		return c.String(http.StatusOK, "late but allowed")
	})

	srv := httptest.NewServer(e)
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}

	// The ordinary budget is shorter than the handler takes, so the write fails.
	resp, err := client.Get(srv.URL + "/slow")
	if err == nil {
		_, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr == nil {
			t.Error("expected the short deadline to break the response")
		}
	}

	// The long budget covers it.
	resp, err = client.Get(srv.URL + "/slow-upload")
	if err != nil {
		t.Fatalf("upload-budget request failed: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if string(body) != "late but allowed" {
		t.Errorf("body = %q, want %q", body, "late but allowed")
	}
}
