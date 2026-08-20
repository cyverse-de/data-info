package apierror

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// newTestEcho returns an echo instance wired the way the service wires it.
func newTestEcho() *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HTTPErrorHandler = HTTPErrorHandler(nil)
	return e
}

func TestHTTPErrorHandler(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		style      Style
		handler    echo.HandlerFunc
		wantStatus int
		wantBody   string
	}{
		{
			name:       "trap route honours the table",
			method:     http.MethodPost,
			path:       "/deleter",
			handler:    func(echo.Context) error { return New(ErrNotOwner).With("path", "/a") },
			wantStatus: http.StatusForbidden,
			wantBody:   `{"error_code":"ERR_NOT_OWNER","path":"/a"}`,
		},
		{
			name:       "trap route preserves the ERR_DOES_NOT_EXIST wart",
			method:     http.MethodPost,
			path:       "/deleter",
			handler:    func(echo.Context) error { return New(ErrDoesNotExist).With("path", "/a") },
			wantStatus: http.StatusInternalServerError,
			wantBody:   `{"error_code":"ERR_DOES_NOT_EXIST","path":"/a"}`,
		},
		{
			name:       "ok route answers 500 even for a mapped code",
			method:     http.MethodPost,
			path:       "/path-info",
			style:      StyleOK,
			handler:    func(echo.Context) error { return New(ErrNotOwner).With("path", "/a") },
			wantStatus: http.StatusInternalServerError,
			wantBody:   `{"error_code":"ERR_NOT_OWNER","path":"/a"}`,
		},
		{
			name:       "unexpected errors become ERR_UNCHECKED_EXCEPTION",
			method:     http.MethodPost,
			path:       "/deleter",
			handler:    func(echo.Context) error { return errors.New("boom") },
			wantStatus: http.StatusInternalServerError,
			wantBody:   `{"error_code":"ERR_UNCHECKED_EXCEPTION","reason":"boom"}`,
		},
		{
			name:       "deadline exceeded reports as unavailable",
			method:     http.MethodPost,
			path:       "/deleter",
			handler:    func(echo.Context) error { return context.DeadlineExceeded },
			wantStatus: http.StatusInternalServerError,
			wantBody:   `{"error_code":"ERR_UNAVAILABLE","reason":"context deadline exceeded"}`,
		},
		{
			name:       "HEAD answers with a bare status",
			method:     http.MethodHead,
			path:       "/data/abc",
			handler:    func(echo.Context) error { return New(ErrNotReadable).WithStatus(http.StatusForbidden) },
			wantStatus: http.StatusForbidden,
			wantBody:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEcho()
			h := tt.handler
			if tt.style != StyleTrap {
				h = WithStyle(tt.style)(h)
			}
			e.Add(tt.method, tt.path, h)

			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
		})
	}
}

// TestUnrecognizedPath pins the not-found body, which is deliberately not the error
// envelope -- it matches data_info.util.service/unrecognized-path-response.
func TestUnrecognizedPath(t *testing.T) {
	e := newTestEcho()
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/no/such/route", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	want := `{"success":false,"reason":"unrecognized service path"}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// TestCanceledRequestWritesNothing covers the client-hung-up case.
func TestCanceledRequestWritesNothing(t *testing.T) {
	e := newTestEcho()
	e.GET("/x", func(echo.Context) error { return context.Canceled })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
}
