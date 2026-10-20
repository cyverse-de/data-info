package metadata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cyverse-de/data-info/internal/apierror"
)

func TestErrorResponsesAreUpstreamErrors(t *testing.T) {
	const body = `{"error_code":"ERR_EXISTS","reason":"duplicate"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	client, err := New(srv.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = client.UpdateAVUs(context.Background(), "u", TargetFile, "id", map[string]any{})

	var upstream *apierror.UpstreamError
	if !errors.As(err, &upstream) {
		t.Fatalf("err = %v, want an *apierror.UpstreamError", err)
	}
	if upstream.Status != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", upstream.Status, http.StatusBadRequest)
	}
	if upstream.ContentType != "application/json; charset=utf-8" {
		t.Errorf("ContentType = %q", upstream.ContentType)
	}
	if string(upstream.Body) != body {
		t.Errorf("Body = %q, want %q", upstream.Body, body)
	}
}
