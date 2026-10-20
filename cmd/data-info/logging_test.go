package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/panics"
	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// loggedServer is a router with the production logging middleware and a handful of routes
// that end each way a request can.
func loggedServer(log *logrus.Entry) *echo.Echo {
	e := echo.New()
	e.HTTPErrorHandler = apierror.HTTPErrorHandler(errorLogger(log))
	e.Use(requestLogger(log))
	e.Use(recoverer(log))

	ok := func(c echo.Context) error { return c.NoContent(http.StatusOK) }
	e.GET("/healthz", ok)
	e.GET("/readyz", func(c echo.Context) error { return c.NoContent(http.StatusInternalServerError) })
	e.GET("/ok", ok)
	e.GET("/missing", func(echo.Context) error { return apierror.New(apierror.ErrDoesNotExist) })
	e.GET("/unavailable", func(echo.Context) error { return apierror.New(apierror.ErrUnavailable) })
	e.GET("/panic", func(echo.Context) error { panic("boom") })
	e.GET("/background-panic", func(echo.Context) error {
		return &panics.Error{Value: "boom", Stack: []byte("goroutine 7 [running]:")}
	})
	return e
}

func TestRequestLogging(t *testing.T) {
	type line struct {
		level   logrus.Level
		message string
	}

	tests := []struct {
		name          string
		target        string
		route         string
		wantLines     []line
		wantErrorCode string
		wantStack     bool
	}{
		{
			name:      "a passing probe is logged only at debug",
			target:    "/healthz",
			wantLines: []line{{logrus.DebugLevel, "request"}},
		},
		{
			name:      "a passing probe with a query string is still only debug",
			target:    "/healthz?x=1",
			route:     "/healthz",
			wantLines: []line{{logrus.DebugLevel, "request"}},
		},
		{
			name:      "a failing probe is logged like any request",
			target:    "/readyz",
			wantLines: []line{{logrus.InfoLevel, "request"}},
		},
		{
			name:      "a success is one line",
			target:    "/ok",
			wantLines: []line{{logrus.InfoLevel, "request"}},
		},
		{
			// It answers 500, but the caller asked for something that is not there.
			name:   "a caller error is not logged as an error",
			target: "/missing",
			wantLines: []line{
				{logrus.DebugLevel, "request refused"},
				{logrus.InfoLevel, "request"},
			},
			wantErrorCode: string(apierror.ErrDoesNotExist),
		},
		{
			name:   "a backend failure is logged as an error",
			target: "/unavailable",
			wantLines: []line{
				{logrus.ErrorLevel, "request failed"},
				{logrus.InfoLevel, "request"},
			},
			wantErrorCode: string(apierror.ErrUnavailable),
		},
		{
			name:   "a panic is logged with its stack and still gets a request line",
			target: "/panic",
			wantLines: []line{
				{logrus.ErrorLevel, "handler panicked; this is a bug in data-info"},
				{logrus.ErrorLevel, "request failed"},
				{logrus.InfoLevel, "request"},
			},
			wantErrorCode: string(apierror.ErrUncheckedException),
		},
		{
			name:   "a panic recovered off the handler's goroutine is logged with its stack",
			target: "/background-panic",
			wantLines: []line{
				{logrus.ErrorLevel, "request failed"},
				{logrus.InfoLevel, "request"},
			},
			wantErrorCode: string(apierror.ErrUncheckedException),
			wantStack:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, hook := logtest.NewNullLogger()
			logger.SetLevel(logrus.DebugLevel)
			e := loggedServer(logrus.NewEntry(logger))

			e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, tt.target, nil))

			entries := hook.AllEntries()
			if len(entries) != len(tt.wantLines) {
				t.Fatalf("got %d log lines, want %d: %v", len(entries), len(tt.wantLines), entries)
			}
			for i, want := range tt.wantLines {
				if entries[i].Level != want.level || entries[i].Message != want.message {
					t.Errorf("line %d = %s %q, want %s %q",
						i, entries[i].Level, entries[i].Message, want.level, want.message)
				}
			}

			if tt.wantStack {
				if _, ok := entries[0].Data["stack"]; !ok {
					t.Errorf("the failure was logged without its stack: %v", entries[0].Data)
				}
			}

			request := hook.LastEntry()
			if got, _ := request.Data["error_code"].(string); got != tt.wantErrorCode {
				t.Errorf("error_code = %q, want %q", got, tt.wantErrorCode)
			}
			wantRoute := tt.route
			if wantRoute == "" {
				wantRoute = tt.target
			}
			if request.Data["route"] != wantRoute {
				t.Errorf("route = %v, want %s", request.Data["route"], wantRoute)
			}
		})
	}
}

func TestNewLogger(t *testing.T) {
	tests := []struct {
		level   string
		wantErr bool
	}{
		{"debug", false},
		{"info", false},
		{"warn", false},
		{"verbose", true},
		{"", true},
	}

	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			log, err := newLogger(tt.level)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}

			var buf bytes.Buffer
			log.Logger.SetOutput(&buf)
			log.Warn("hello")

			var got map[string]any
			if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
				t.Fatalf("log line is not JSON: %v (%s)", err, buf.String())
			}
			for key, want := range map[string]any{"msg": "hello", "service": "data-info", "level": "warning"} {
				if got[key] != want {
					t.Errorf("%s = %v, want %v", key, got[key], want)
				}
			}
		})
	}
}

func TestConfigureLibraryLogger(t *testing.T) {
	tests := []struct {
		service logrus.Level
		want    logrus.Level
	}{
		{logrus.DebugLevel, logrus.DebugLevel},
		{logrus.InfoLevel, logrus.WarnLevel},
		{logrus.WarnLevel, logrus.WarnLevel},
		{logrus.ErrorLevel, logrus.ErrorLevel},
	}

	for _, tt := range tests {
		t.Run(tt.service.String(), func(t *testing.T) {
			lib := logrus.New()
			configureLibraryLogger(lib, tt.service)
			if got := lib.GetLevel(); got != tt.want {
				t.Errorf("level = %v, want %v", got, tt.want)
			}

			var buf bytes.Buffer
			lib.SetOutput(&buf)
			lib.WithField("service", "kept").Error("from the library")

			var got map[string]any
			if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
				t.Fatalf("log line is not JSON: %v (%s)", err, buf.String())
			}
			for key, want := range map[string]any{"msg": "from the library", "project": "discoenv", "service": "kept"} {
				if got[key] != want {
					t.Errorf("%s = %v, want %v", key, got[key], want)
				}
			}
		})
	}
}
