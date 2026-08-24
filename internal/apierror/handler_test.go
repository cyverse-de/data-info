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

// TestContentTypeMatchesTheReference pins the header every response carries.
//
// All three answers were measured against the running QA service on 2026-08-24 rather than
// reasoned about, because two of them are surprising: an error that reaches the reference's
// default exception handler carries no content type at all, and the unrecognised-path body
// is labelled text/html despite being JSON.
func TestContentTypeMatchesTheReference(t *testing.T) {
	tests := []struct {
		name    string
		style   Style
		method  string
		path    string
		handler echo.HandlerFunc
		want    string
	}{
		{
			name:    "a trap route's thrown code is JSON",
			style:   StyleTrap,
			handler: func(echo.Context) error { return New(ErrNotOwner) },
			want:    JSONContentType,
		},
		{
			name:    "an ok route's thrown code carries no content type",
			style:   StyleOK,
			handler: func(echo.Context) error { return New(ErrDoesNotExist) },
			want:    "",
		},
		{
			name:    "a schema failure is JSON even on an ok route",
			style:   StyleOK,
			handler: func(echo.Context) error { return New(ErrIllegalArgument).AsSchemaFailure() },
			want:    JSONContentType,
		},
		{
			name:    "a schema failure is JSON on a trap route too",
			style:   StyleTrap,
			handler: func(echo.Context) error { return New(ErrIllegalArgument).AsSchemaFailure() },
			want:    JSONContentType,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEcho()
			var middleware []echo.MiddlewareFunc
			if tt.style != StyleTrap {
				middleware = append(middleware, WithStyle(tt.style))
			}
			e.GET("/x", tt.handler, middleware...)

			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

			if got := rec.Header().Get(echo.HeaderContentType); got != tt.want {
				t.Errorf("Content-Type = %q, want %q (body %s)", got, tt.want, rec.Body.String())
			}
		})
	}
}

// TestUnrecognizedPathIsLabelledHTML covers the content type on the not-found body, which is
// text/html for a body that is plainly JSON. It comes from compojure's route/not-found with
// nothing overriding the default, and callers see it on every unknown path.
func TestUnrecognizedPathIsLabelledHTML(t *testing.T) {
	e := newTestEcho()
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/no/such/route", nil))

	if got := rec.Header().Get(echo.HeaderContentType); got != unrecognizedPathContentType {
		t.Errorf("Content-Type = %q, want %q", got, unrecognizedPathContentType)
	}
}

// TestWrongMethodIsAnUnrecognizedPath covers a status difference, not just a header.
//
// compojure matches a route on its method and path together, so a request with the wrong
// method does not match and falls through to route/not-found. echo answers 405 by default,
// which would have been a different status and a different body for the same request --
// verified against the running service, which answers DELETE on a GET route with a 404.
func TestWrongMethodIsAnUnrecognizedPath(t *testing.T) {
	e := newTestEcho()
	e.GET("/x", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/x", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	want := `{"success":false,"reason":"unrecognized service path"}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if got := rec.Header().Get(echo.HeaderContentType); got != unrecognizedPathContentType {
		t.Errorf("Content-Type = %q, want %q", got, unrecognizedPathContentType)
	}
}
