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

// TestIdleTimeoutToleratesRecorders makes sure a writer that cannot take deadlines does
// not fail the request; that is the case under httptest.NewRecorder.
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

// TestIdleTimeoutFiresWhenTheStreamStalls covers the timeout doing its job. The handler
// streams a chunk, then goes quiet for longer than the budget, then tries to write again.
// The second write has to fail.
//
// Two details make this test meaningful rather than incidental. The stall happens
// mid-stream rather than before the first write, and the write that follows it is large
// enough to overflow net/http's response buffer. A handler that merely thinks for a while
// and then returns a short body never reaches the socket at all -- the body sits in the
// buffer -- so the deadline has nothing to bite on and the request completes normally.
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

		// Large enough to overflow net/http's response buffer and force a socket write.
		// A short write would simply be buffered, and the deadline would have nothing to
		// bite on until some later flush.
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

// TestIdleTimeoutAllowsSlowButProgressingTransfer is the regression this middleware was
// rewritten for. A download that keeps making progress must survive well past its budget,
// because the Jetty timeout it replaces was an idle timeout, not a deadline. Under the
// previous absolute-deadline implementation a large download on a non-upload route was
// truncated once it outlived timeouts.request.
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

// TestIdleTimeoutExtendsWhileTheBodyIsRead is the upload half: a request body that arrives
// slowly but steadily must not be cut off.
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
