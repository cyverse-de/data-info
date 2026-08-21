package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/config"
	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/labstack/echo/v4"
)

// multipartBody builds an upload body from an ordered list of parts. A part with an empty
// filename is sent as a plain field, which is how a caller's stray form values arrive.
func multipartBody(t *testing.T, parts []struct{ field, filename, content string }) (string, io.Reader) {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	for _, p := range parts {
		var (
			field io.Writer
			err   error
		)
		if p.filename == "" {
			field, err = w.CreateFormField(p.field)
		} else {
			field, err = w.CreateFormFile(p.field, p.filename)
		}
		if err != nil {
			t.Fatalf("building the multipart body: %v", err)
		}
		if _, err := io.WriteString(field, p.content); err != nil {
			t.Fatalf("writing a multipart part: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing the multipart body: %v", err)
	}

	return w.FormDataContentType(), &buf
}

// serveUpload runs one multipart request through a router wired the way the service wires
// it, so the real error handler and route style apply.
func serveUpload(
	t *testing.T,
	method, target string,
	parts []struct{ field, filename, content string },
	handler echo.HandlerFunc,
) *httptest.ResponseRecorder {
	t.Helper()

	e := echo.New()
	e.HideBanner = true
	e.HTTPErrorHandler = apierror.HTTPErrorHandler(nil)

	// The service registers these routes StyleOK, because their errors are raised in the
	// reference's multipart middleware rather than inside its trap.
	ok := apierror.WithStyle(apierror.StyleOK)
	e.Add(method, "/data", handler, ok)
	e.Add(method, "/data/:data-id", handler, ok)

	contentType, body := multipartBody(t, parts)
	req := httptest.NewRequest(method, target, body)
	req.Header.Set(echo.HeaderContentType, contentType)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func errorCodeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding the error envelope %q: %v", rec.Body.String(), err)
	}

	code, _ := envelope["error_code"].(string)
	return code
}

// oneFile is the ordinary body: a single part named file.
func oneFile(name string) []struct{ field, filename, content string } {
	return []struct{ field, filename, content string }{{"file", name, "hello"}}
}

func TestUploadRejectsRequestsBeforeWriting(t *testing.T) {
	deps, fake := testDeps(t)
	deps.BadChars = config.DefaultBadChars
	fake.AddCollection(testHome+"/readonly", icat.AccessRead)
	writes := NewWrites(deps)

	cases := []struct {
		name     string
		target   string
		parts    []struct{ field, filename, content string }
		wantCode int
		wantErr  string
	}{
		{
			// Not a 400: the reference identifies the caller inside multipart middleware
			// that runs before its parameters are coerced, so a missing user arrives as a
			// nil username and is reported as an unknown one.
			name:     "missing user",
			target:   "/data?dest=" + testHome,
			parts:    oneFile("new.txt"),
			wantCode: http.StatusInternalServerError,
			wantErr:  string(apierror.ErrNotAUser),
		},
		{
			name:     "missing dest",
			target:   "/data?user=" + testUser,
			parts:    oneFile("new.txt"),
			wantCode: http.StatusInternalServerError,
			wantErr:  string(apierror.ErrDoesNotExist),
		},
		{
			name:     "no file part",
			target:   "/data?user=" + testUser + "&dest=" + testHome,
			parts:    []struct{ field, filename, content string }{{"notfile", "new.txt", "hello"}},
			wantCode: http.StatusBadRequest,
			wantErr:  string(apierror.ErrIllegalArgument),
		},
		{
			name:     "file part carries no filename",
			target:   "/data?user=" + testUser + "&dest=" + testHome,
			parts:    []struct{ field, filename, content string }{{"file", "", "hello"}},
			wantCode: http.StatusBadRequest,
			wantErr:  string(apierror.ErrIllegalArgument),
		},
		{
			name:     "filename holds a forbidden character",
			target:   "/data?user=" + testUser + "&dest=" + testHome,
			parts:    oneFile("na\tme.txt"),
			wantCode: http.StatusInternalServerError,
			wantErr:  string(apierror.ErrBadOrMissingField),
		},
		{
			name:     "unknown user",
			target:   "/data?user=nobody&dest=" + testHome,
			parts:    oneFile("new.txt"),
			wantCode: http.StatusInternalServerError,
			wantErr:  string(apierror.ErrNotAUser),
		},
		{
			name:     "destination already holds the file",
			target:   "/data?user=" + testUser + "&dest=" + testHome,
			parts:    oneFile("a.txt"),
			wantCode: http.StatusInternalServerError,
			wantErr:  string(apierror.ErrExists),
		},
		{
			name:     "destination directory is missing",
			target:   "/data?user=" + testUser + "&dest=" + testHome + "/nope",
			parts:    oneFile("new.txt"),
			wantCode: http.StatusInternalServerError,
			wantErr:  string(apierror.ErrDoesNotExist),
		},
		{
			name:     "destination directory is not writeable",
			target:   "/data?user=" + testUser + "&dest=" + testHome + "/readonly",
			parts:    oneFile("new.txt"),
			wantCode: http.StatusInternalServerError,
			wantErr:  string(apierror.ErrNotWriteable),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveUpload(t, http.MethodPost, tc.target, tc.parts, writes.Upload)

			if rec.Code != tc.wantCode {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if got := errorCodeOf(t, rec); got != tc.wantErr {
				t.Errorf("error_code = %q, want %q", got, tc.wantErr)
			}
		})
	}
}

func TestOverwriteRejectsRequestsBeforeWriting(t *testing.T) {
	deps, fake := testDeps(t)
	fake.SetUUID("11111111-1111-1111-1111-111111111111", testHome+"/a.txt")
	fake.SetUUID("22222222-2222-2222-2222-222222222222", testHome)
	fake.AddDataObject(testHome+"/hidden.txt", 10, 0)
	fake.SetUUID("33333333-3333-3333-3333-333333333333", testHome+"/hidden.txt")
	writes := NewWrites(deps)

	cases := []struct {
		name     string
		target   string
		wantCode int
		wantErr  string
	}{
		{
			name:     "unknown user",
			target:   "/data/11111111-1111-1111-1111-111111111111?user=nobody",
			wantCode: http.StatusInternalServerError,
			wantErr:  string(apierror.ErrNotAUser),
		},
		{
			name:     "unknown id",
			target:   "/data/44444444-4444-4444-4444-444444444444?user=" + testUser,
			wantCode: http.StatusInternalServerError,
			wantErr:  string(apierror.ErrDoesNotExist),
		},
		{
			name:     "id names a folder",
			target:   "/data/22222222-2222-2222-2222-222222222222?user=" + testUser,
			wantCode: http.StatusInternalServerError,
			wantErr:  string(apierror.ErrNotAFile),
		},
		{
			name:     "file is not readable",
			target:   "/data/33333333-3333-3333-3333-333333333333?user=" + testUser,
			wantCode: http.StatusInternalServerError,
			wantErr:  string(apierror.ErrNotReadable),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveUpload(t, http.MethodPut, tc.target, oneFile("a.txt"), writes.Overwrite)

			if rec.Code != tc.wantCode {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if got := errorCodeOf(t, rec); got != tc.wantErr {
				t.Errorf("error_code = %q, want %q", got, tc.wantErr)
			}
		})
	}
}

// The parts before the file have to be drained rather than skipped: a multipart body is
// readable only in order, so a handler that jumped ahead would read one part's bytes as
// another's.
func TestUploadPartFindsTheFileAfterOtherParts(t *testing.T) {
	parts := []struct{ field, filename, content string }{
		{"ignored", "", "some value"},
		{"other", "other.txt", "not the upload"},
		{"file", "wanted.txt", "the upload"},
	}

	e := echo.New()
	contentType, body := multipartBody(t, parts)
	req := httptest.NewRequest(http.MethodPost, "/data", body)
	req.Header.Set(echo.HeaderContentType, contentType)
	c := e.NewContext(req, httptest.NewRecorder())

	part, err := uploadPart(c)
	if err != nil {
		t.Fatalf("uploadPart: %v", err)
	}
	defer part.Close() //nolint:errcheck // test cleanup

	if part.FileName() != "wanted.txt" {
		t.Errorf("filename = %q, want %q", part.FileName(), "wanted.txt")
	}

	contents, err := io.ReadAll(part)
	if err != nil {
		t.Fatalf("reading the part: %v", err)
	}
	if string(contents) != "the upload" {
		t.Errorf("contents = %q, want %q", contents, "the upload")
	}
}

func TestUploadPartRejectsANonMultipartRequest(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/data", bytes.NewReader(nil))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c := e.NewContext(req, httptest.NewRecorder())

	if _, err := uploadPart(c); err == nil {
		t.Fatal("uploadPart accepted a request that was not multipart")
	}
}

// The upload routes report a nil username and a nil path literally, because that is what the
// reference's validators put in the envelope. Callers parse these keys, so the exact shape is
// asserted rather than just the code.
func TestUploadReportsNilsTheWayTheReferenceDoes(t *testing.T) {
	deps, fake := testDeps(t)
	fake.SetUUID("11111111-1111-1111-1111-111111111111", testHome+"/a.txt")
	writes := NewWrites(deps)

	cases := []struct {
		name    string
		method  string
		target  string
		handler echo.HandlerFunc
		want    string
	}{
		{
			name:    "upload without a user",
			method:  http.MethodPost,
			target:  "/data?dest=" + testHome,
			handler: writes.Upload,
			want:    `{"error_code":"ERR_NOT_A_USER","users":[null]}`,
		},
		{
			name:    "upload without a destination",
			method:  http.MethodPost,
			target:  "/data?user=" + testUser,
			handler: writes.Upload,
			want:    `{"error_code":"ERR_DOES_NOT_EXIST","paths":[null]}`,
		},
		{
			name:    "overwrite without a user",
			method:  http.MethodPut,
			target:  "/data/11111111-1111-1111-1111-111111111111",
			handler: writes.Overwrite,
			want:    `{"error_code":"ERR_NOT_A_USER","users":[null]}`,
		},
		{
			name:    "overwrite an id that resolves to nothing",
			method:  http.MethodPut,
			target:  "/data/44444444-4444-4444-4444-444444444444?user=" + testUser,
			handler: writes.Overwrite,
			want:    `{"error_code":"ERR_DOES_NOT_EXIST","paths":[null]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveUpload(t, tc.method, tc.target, oneFile("new.txt"), tc.handler)

			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tc.want {
				t.Errorf("body = %s, want %s", got, tc.want)
			}
		})
	}
}
