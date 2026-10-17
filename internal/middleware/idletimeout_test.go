package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// TestIdleTimeoutToleratesRecorders checks that a writer that cannot take deadlines, such as
// httptest.NewRecorder, does not fail the request.
func TestIdleTimeoutToleratesRecorders(t *testing.T) {
	e := echo.New()
	e.Use(IdleTimeout(time.Second, time.Minute, func(echo.Context) bool { return false }))
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

// TestIdleTimeoutFiresWhenTheStreamStalls stalls mid-stream for longer than the budget, then
// writes enough to reach the socket; that write must fail.
func TestIdleTimeoutFiresWhenTheStreamStalls(t *testing.T) {
	const budget = 100 * time.Millisecond

	writeErr := make(chan error, 1)

	e := echo.New()
	e.Use(IdleTimeout(budget, time.Hour, func(echo.Context) bool { return false }))
	e.GET("/stall", func(c echo.Context) error {
		c.Response().WriteHeader(http.StatusOK)
		if _, err := c.Response().Write([]byte("first")); err != nil {
			writeErr <- err
			return nil
		}
		c.Response().Flush()

		time.Sleep(5 * budget)

		// Large enough to overflow net/http's response buffer; a short write would just be
		// buffered, giving the deadline nothing to bite on.
		_, err := c.Response().Write(make([]byte, 1<<20))
		writeErr <- err
		return nil
	})

	srv := httptest.NewServer(e)
	defer srv.Close()

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(srv.URL + "/stall")
	if err == nil {
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
	}

	select {
	case err := <-writeErr:
		if err == nil {
			t.Error("the write after the stall succeeded; the idle timeout did not fire")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler never reported a write result")
	}
}

// TestIdleTimeoutAllowsSlowButProgressingTransfer checks that a download that keeps making
// progress outlives its idle budget.
func TestIdleTimeoutAllowsSlowButProgressingTransfer(t *testing.T) {
	const (
		budget = 150 * time.Millisecond
		chunks = 12
		pause  = 60 * time.Millisecond // well under budget, but 12 of them far exceed it
	)

	e := echo.New()
	e.Use(IdleTimeout(budget, time.Hour, func(echo.Context) bool { return false }))
	e.GET("/slow-stream", func(c echo.Context) error {
		c.Response().WriteHeader(http.StatusOK)
		for i := 0; i < chunks; i++ {
			if _, err := c.Response().Write([]byte("chunk")); err != nil {
				return err
			}
			c.Response().Flush()
			time.Sleep(pause)
		}
		return nil
	})

	srv := httptest.NewServer(e)
	defer srv.Close()

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(srv.URL + "/slow-stream")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the stream failed after %d bytes: %v", len(body), err)
	}

	if want := strings.Repeat("chunk", chunks); string(body) != want {
		t.Errorf("got %d bytes, want %d; the transfer was truncated", len(body), len(want))
	}
}

// TestIdleTimeoutExtendsWhileTheBodyIsRead checks that a slow but steady upload body is not
// cut off.
func TestIdleTimeoutExtendsWhileTheBodyIsRead(t *testing.T) {
	const budget = 150 * time.Millisecond

	e := echo.New()
	e.Use(IdleTimeout(budget, time.Hour, func(echo.Context) bool { return false }))
	e.POST("/upload", func(c echo.Context) error {
		n, err := io.Copy(io.Discard, c.Request().Body)
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, strconv.FormatInt(n, 10))
	})

	srv := httptest.NewServer(e)
	defer srv.Close()

	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		for i := 0; i < 10; i++ {
			if _, err := pw.Write([]byte("0123456789")); err != nil {
				return
			}
			time.Sleep(60 * time.Millisecond)
		}
	}()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/upload", pr)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	if string(body) != "100" {
		t.Errorf("server read %s bytes, want 100", body)
	}
}
