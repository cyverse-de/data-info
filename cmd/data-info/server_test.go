package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cyverse-de/data-info/internal/config"
	"github.com/cyverse-de/data-info/internal/handlers"
	dimw "github.com/cyverse-de/data-info/internal/middleware"
	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

const testConfigYAML = `
anonfiles:
  baseurl: https://example.org/anon-files/
  mappings:
    /iplant/home/: cyverse/home/
kifshare:
  externalurl: https://example.org/dl
irods:
  password: notprod
icat:
  password: notprod
`

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "service.yml")
	if err := os.WriteFile(path, []byte(testConfigYAML), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	cfg, err := config.Load(config.Settings{
		ConfigPath: path,
		DotEnvPath: filepath.Join(dir, "absent.env"),
		EnvPrefix:  "DATAINFOSERVERTEST_",
	})
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	return cfg
}

// stubProber reports a fixed result, so router tests never touch the network.
func stubProber(err error) handlers.Prober {
	return handlers.ProberFunc(func(context.Context) error { return err })
}

// testServer builds the real router with unreachable backends, which is the interesting
// case for the status endpoints.
func testServer(t *testing.T) *echo.Echo {
	t.Helper()
	return testServerWithDeps(t, Deps{
		IRODSProbe: stubProber(errors.New("irods is unreachable")),
		ICATProbe:  stubProber(errors.New("icat is unreachable")),
	})
}

func testServerWithDeps(t *testing.T, deps Deps) *echo.Echo {
	t.Helper()
	log := logrus.New()
	log.SetOutput(io.Discard)
	return buildServer(testConfig(t), "1.2.3-test", logrus.NewEntry(log), deps)
}

func TestStatusEndpoint(t *testing.T) {
	e := testServer(t)

	tests := []struct {
		name       string
		target     string
		wantStatus int
		wantExpect string
	}{
		{"no expecting parameter", "/", http.StatusOK, ""},
		{"expecting ourselves", "/?expecting=data-info", http.StatusOK, "data-info"},
		{"expecting another service", "/?expecting=terrain", http.StatusInternalServerError, "terrain"},
		{"parameter name is case-insensitive", "/?EXPECTING=data-info", http.StatusOK, "data-info"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.target, nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}

			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decoding body: %v (%s)", err, rec.Body.String())
			}

			// The response shape is a contract shared with terrain and the DE's
			// monitoring, so assert on the exact key names.
			for key, want := range map[string]any{
				"service":     "data-info",
				"description": "DE service for data information logic and iRODS interactions.",
				"version":     "1.2.3-test",
				"expecting":   tt.wantExpect,
			} {
				if body[key] != want {
					t.Errorf("%s = %v, want %v", key, body[key], want)
				}
			}
			if _, ok := body["docs-url"]; !ok {
				t.Error("docs-url is missing")
			}
			if _, ok := body["iRODS"]; !ok {
				t.Error("iRODS is missing")
			}
		})
	}
}

// TestDocsURL pins the shape clojure-commons' get-docs-status produced: always http, and
// always with the port.
func TestDocsURL(t *testing.T) {
	e := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "data-info:60000"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if want := "http://data-info:60000/docs"; body["docs-url"] != want {
		t.Errorf("docs-url = %v, want %v", body["docs-url"], want)
	}
}

func TestHealthz(t *testing.T) {
	e := testServer(t)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestReadyzFailsWhenBackendsAreUnreachable covers the reason readiness is separate from
// liveness: an unreachable backend must fail readiness without restarting the pod.
func TestReadyzFailsWhenBackendsAreUnreachable(t *testing.T) {
	e := testServer(t)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	var body struct {
		Ready    bool              `json:"ready"`
		Backends map[string]string `json:"backends"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Ready {
		t.Error("ready = true, want false")
	}
	for _, name := range []string{"irods", "icat"} {
		if _, ok := body.Backends[name]; !ok {
			t.Errorf("backends is missing %q", name)
		}
	}
}

func TestAdminConfigMasksCredentials(t *testing.T) {
	e := testServer(t)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/config", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}

	// Legacy property names, because callers diff this against the Clojure service.
	if body["data-info.port"] != "60000" {
		t.Errorf("data-info.port = %q, want %q", body["data-info.port"], "60000")
	}
	for _, key := range []string{"data-info.irods.password", "data-info.icat.password"} {
		if body[key] != "********" {
			t.Errorf("%s = %q, want it masked", key, body[key])
		}
	}
}

func TestUnrecognizedPath(t *testing.T) {
	e := testServer(t)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/no/such/thing", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if want := `{"success":false,"reason":"unrecognized service path"}`; rec.Body.String() != want {
		t.Errorf("body = %q, want %q", rec.Body.String(), want)
	}
}

func TestIsUploadRoute(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodPost, "/data", true},
		{http.MethodPost, "/data/", true},
		{http.MethodPut, "/data/:data-id", true},
		{http.MethodGet, "/data", false},
		{http.MethodPost, "/deleter", false},
		{http.MethodPut, "/data/:data-id/name", false},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			e := echo.New()
			c := e.NewContext(httptest.NewRequest(tt.method, "/", nil), httptest.NewRecorder())
			c.SetPath(tt.path)
			if got := isUploadRoute(c); got != tt.want {
				t.Errorf("isUploadRoute = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestUploadTimeoutExceedsRequestTimeout guards the split itself: if these ever collapse
// to one value, large uploads start failing and only on large files.
func TestUploadTimeoutExceedsRequestTimeout(t *testing.T) {
	cfg := testConfig(t)
	if cfg.Timeouts.Upload <= cfg.Timeouts.Request {
		t.Errorf("upload timeout %s must exceed request timeout %s",
			cfg.Timeouts.Upload, cfg.Timeouts.Request)
	}
	if cfg.Timeouts.Upload < time.Hour {
		t.Errorf("upload timeout %s is too short for a large file", cfg.Timeouts.Upload)
	}
}

// TestReadyzSucceedsWhenBackendsAreReachable is the other half of readiness: it must
// actually pass once the backends answer.
func TestReadyzSucceedsWhenBackendsAreReachable(t *testing.T) {
	e := testServerWithDeps(t, Deps{IRODSProbe: stubProber(nil), ICATProbe: stubProber(nil)})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (%s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Ready    bool              `json:"ready"`
		Backends map[string]string `json:"backends"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if !body.Ready {
		t.Error("ready = false, want true")
	}
	for name, state := range body.Backends {
		if state != "ok" {
			t.Errorf("backend %s = %q, want %q", name, state, "ok")
		}
	}
}

// TestStatusReportsIRODSReachability covers the iRODS flag in both directions.
func TestStatusReportsIRODSReachability(t *testing.T) {
	tests := []struct {
		name  string
		probe error
		want  bool
	}{
		{"reachable", nil, true},
		{"unreachable", errors.New("connection refused"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := testServerWithDeps(t, Deps{IRODSProbe: stubProber(tt.probe), ICATProbe: stubProber(nil)})

			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decoding body: %v", err)
			}
			if body["iRODS"] != tt.want {
				t.Errorf("iRODS = %v, want %v", body["iRODS"], tt.want)
			}
			// An iRODS outage must not make the status endpoint itself fail.
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
			}
		})
	}
}

// TestResponsesHaveNoTrailingNewline pins a byte-level contract detail. echo's c.JSON
// encodes with a json.Encoder and appends a newline; cheshire/encode, which produced the
// Clojure bodies, does not. Responses are diffed byte for byte during the port, so a
// stray newline is a real difference.
func TestResponsesHaveNoTrailingNewline(t *testing.T) {
	e := testServerWithDeps(t, Deps{IRODSProbe: stubProber(nil), ICATProbe: stubProber(nil)})

	for _, target := range []string{"/", "/healthz", "/readyz", "/admin/config", "/no/such/thing"} {
		t.Run(target, func(t *testing.T) {
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))

			body := rec.Body.String()
			if body == "" {
				t.Fatal("empty body")
			}
			if strings.HasSuffix(body, "\n") {
				t.Errorf("body ends with a newline: %q", body)
			}
		})
	}
}

// TestStatusBodyMatchesCapturedShape compares GET / against the response captured from the
// running QA service, so the key set and their order cannot drift.
func TestStatusBodyMatchesCapturedShape(t *testing.T) {
	captured, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contract", "status-clojure.json"))
	if err != nil {
		t.Fatalf("reading the captured status body: %v", err)
	}

	e := testServerWithDeps(t, Deps{IRODSProbe: stubProber(nil), ICATProbe: stubProber(nil)})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got, want := keyOrder(t, rec.Body.Bytes()), keyOrder(t, captured); got != want {
		t.Errorf("key order = %s\n          want %s", got, want)
	}
}

// keyOrder returns the top-level JSON keys in the order they appear, which is what a
// byte-level diff against the Clojure service is sensitive to.
func keyOrder(t *testing.T, body []byte) string {
	t.Helper()

	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("body is not a JSON object: %v", err)
	}

	var keys []string
	depth := 0
	for dec.More() || depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("scanning body: %v", err)
		}
		switch v := tok.(type) {
		case json.Delim:
			if v == '{' || v == '[' {
				depth++
			} else {
				depth--
			}
		case string:
			if depth == 0 {
				keys = append(keys, v)
				// Skip the value.
				if _, err := dec.Token(); err != nil {
					t.Fatalf("scanning value for %q: %v", v, err)
				}
			}
		}
		if depth == 0 && !dec.More() {
			break
		}
	}
	return strings.Join(keys, ",")
}

// TestBlankExpectingIsRejected matches the Clojure route, which types expecting as an
// optional NonBlankString. Supplying the parameter with a blank value fails schema
// coercion there rather than being treated as absent.
func TestBlankExpectingIsRejected(t *testing.T) {
	e := testServerWithDeps(t, Deps{IRODSProbe: stubProber(nil), ICATProbe: stubProber(nil)})

	tests := []struct {
		name       string
		target     string
		wantStatus int
	}{
		{"absent is fine", "/", http.StatusOK},
		{"blank is rejected", "/?expecting=", http.StatusBadRequest},
		{"whitespace is rejected", "/?expecting=%20", http.StatusBadRequest},
		{"a real value is accepted", "/?expecting=data-info", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.target, nil))
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestRepeatedQueryParamsKeepTheirOrder covers the lowercasing middleware end to end.
// Ranging over a parsed url.Values would randomise this, so a repeated parameter would
// resolve differently from one request to the next.
func TestRepeatedQueryParamsKeepTheirOrder(t *testing.T) {
	e := echo.New()
	e.Pre(dimw.LowercaseQueryParams())

	var got []string
	e.GET("/x", func(c echo.Context) error {
		got = c.QueryParams()["info-type"]
		return c.NoContent(http.StatusOK)
	})

	for i := 0; i < 50; i++ {
		got = nil
		e.ServeHTTP(httptest.NewRecorder(),
			httptest.NewRequest(http.MethodGet, "/x?INFO-TYPE=csv&info-type=bam&Info-Type=vcf", nil))

		if len(got) != 3 || got[0] != "csv" || got[1] != "bam" || got[2] != "vcf" {
			t.Fatalf("run %d: got %v, want [csv bam vcf]", i, got)
		}
	}
}
