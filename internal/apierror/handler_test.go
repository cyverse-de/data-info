package apierror

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/labstack/echo/v4"
)

// testUpstream is an error response from another service, as a client reports it.
var testUpstream = &UpstreamError{
	Service:     "metadata",
	Status:      http.StatusBadRequest,
	ContentType: "application/json; charset=utf-8",
	Body:        []byte(`{"error_code":"ERR_EXISTS","reason":"duplicate"}`),
}

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
			wantBody:   `{"error_code":"ERR_UNCHECKED_EXCEPTION","message":"boom"}`,
		},
		{
			name:   "a failed catalog query does not reach the response",
			method: http.MethodPost,
			path:   "/deleter",
			handler: func(echo.Context) error {
				return fmt.Errorf("stat: %w", &icat.QueryError{Op: "getting 1 item(s)",
					Err: errors.New(`pq: relation "r_data_main" does not exist`)})
			},
			wantStatus: http.StatusInternalServerError,
			wantBody:   `{"error_code":"ERR_UNCHECKED_EXCEPTION","message":"a query against the iRODS catalog failed"}`,
		},
		{
			name:       "ok route reports an unexpected error under reason",
			method:     http.MethodPost,
			path:       "/path-info",
			style:      StyleOK,
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
			name:   "trap route relays an upstream error as received",
			method: http.MethodPatch,
			path:   "/data/abc/metadata",
			handler: func(echo.Context) error {
				return fmt.Errorf("updating: %w", testUpstream)
			},
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error_code":"ERR_EXISTS","reason":"duplicate"}`,
		},
		{
			name:       "ok route relays an upstream error as received too",
			method:     http.MethodPost,
			path:       "/path-info",
			style:      StyleOK,
			handler:    func(echo.Context) error { return testUpstream },
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error_code":"ERR_EXISTS","reason":"duplicate"}`,
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

// TestUnrecognizedPath pins the not-found body, which is deliberately not the error envelope.
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

func TestCanceledRequestWritesNothing(t *testing.T) {
	e := newTestEcho()
	e.GET("/x", func(echo.Context) error { return context.Canceled })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
}

// TestContentTypeMatchesTheReference pins each error's Content-Type, including the
// surprising ones: none for a StyleOK handler error, text/html for the JSON not-found body.
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

// TestUnrecognizedPathIsLabelledHTML pins the text/html label on the JSON not-found body.
func TestUnrecognizedPathIsLabelledHTML(t *testing.T) {
	e := newTestEcho()
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/no/such/route", nil))

	if got := rec.Header().Get(echo.HeaderContentType); got != unrecognizedPathContentType {
		t.Errorf("Content-Type = %q, want %q", got, unrecognizedPathContentType)
	}
}

// TestWrongMethodIsAnUnrecognizedPath pins that a wrong method gets the unknown-path 404, not
// echo's default 405.
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

// TestNoContentTypeMeansNoHeader checks that net/http doesn't sniff in a Content-Type when
// none is wanted.
func TestNoContentTypeMeansNoHeader(t *testing.T) {
	e := newTestEcho()
	e.GET("/x", func(echo.Context) error { return New(ErrDoesNotExist) }, WithStyle(StyleOK))

	// Served through a real server rather than a recorder. A recorder keeps the map key
	// with a nil value, which is indistinguishable from an empty one; what matters is
	// whether net/http writes a header line, and only a real response shows that.
	srv := httptest.NewServer(e)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // read below

	if values, present := resp.Header[http.CanonicalHeaderKey("Content-Type")]; present {
		t.Errorf("Content-Type = %q, want no header line at all", values)
	}

	// The body still has to arrive; suppressing the header must not suppress the write.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	if len(body) == 0 {
		t.Error("body is empty")
	}
}

// TestHeadContentTypeDependsOnWhoRejected: a HEAD rejected by validation carries the JSON
// Content-Type, and one the handler rejects carries none.
func TestHeadContentTypeDependsOnWhoRejected(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "a request rejected by validation is labelled",
			err:  New(ErrIllegalArgument).AsSchemaFailure().WithStatus(http.StatusBadRequest),
			want: JSONContentType,
		},
		{
			name: "an id the handler could not resolve is not",
			err:  New(ErrDoesNotExist).WithStatus(http.StatusNotFound),
			want: "",
		},
		{
			name: "a user the handler could not resolve is not",
			err:  New(ErrNotAUser).WithStatus(http.StatusUnprocessableEntity),
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEcho()
			e.HEAD("/x", func(echo.Context) error { return tt.err })

			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/x", nil))

			if got := rec.Header().Get(echo.HeaderContentType); got != tt.want {
				t.Errorf("Content-Type = %q, want %q", got, tt.want)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("body = %q, want empty for a HEAD", rec.Body.String())
			}
		})
	}
}

// TestWrongMethodSendsNoAllowHeader checks that echo's Allow header is dropped, since a wrong
// method answers as an unknown path.
func TestWrongMethodSendsNoAllowHeader(t *testing.T) {
	e := newTestEcho()
	e.GET("/x", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/x", nil))

	if got := rec.Header().Get("Allow"); got != "" {
		t.Errorf("Allow = %q, want the header to be absent", got)
	}
}

func TestUpstreamErrorKeepsItsContentType(t *testing.T) {
	e := newTestEcho()
	e.GET("/x", func(echo.Context) error { return testUpstream })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if got := rec.Header().Get(echo.HeaderContentType); got != testUpstream.ContentType {
		t.Errorf("Content-Type = %q, want %q", got, testUpstream.ContentType)
	}
}

func TestUpstreamErrorCode(t *testing.T) {
	tests := []struct {
		name string
		body string
		want Code
	}{
		{"an error envelope", `{"error_code":"ERR_EXISTS"}`, ErrExists},
		{"JSON without a code", `{"reason":"nope"}`, ErrRequestFailed},
		{"not JSON", `<html>bad gateway</html>`, ErrRequestFailed},
		{"empty", ``, ErrRequestFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := (&UpstreamError{Body: []byte(tt.body)}).Code()
			if got != tt.want {
				t.Errorf("Code() = %s, want %s", got, tt.want)
			}
		})
	}
}
