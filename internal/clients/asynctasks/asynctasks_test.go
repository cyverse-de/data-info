package asynctasks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// recorder stands in for the async-tasks service, capturing what reached it.
type recorder struct {
	method string
	path   string
	query  string
	body   string

	status   int
	location string
	response string
}

func (r *recorder) serve() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)

		r.method, r.path, r.query, r.body = req.Method, req.URL.Path, req.URL.RawQuery, string(body)

		if r.location != "" {
			w.Header().Set("Location", r.location)
		}
		if r.status != 0 {
			w.WriteHeader(r.status)
		}
		_, _ = io.WriteString(w, r.response)
	}))
}

func testClient(t *testing.T, rec *recorder) (*Client, func()) {
	t.Helper()

	server := rec.serve()
	client, err := New(server.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client, server.Close
}

func TestNormalizeID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"abc-123", "abc-123"},
		{"/tasks/abc-123", "abc-123"},
		{"http://async-tasks:60000/tasks/abc-123", "abc-123"},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := NormalizeID(tc.in); got != tc.want {
				t.Errorf("NormalizeID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Create hands back the Location header verbatim, because callers pass that value on as
// async-task-id and terrain polls it.
func TestCreateReturnsTheLocation(t *testing.T) {
	rec := &recorder{status: http.StatusCreated, location: "/tasks/abc-123"}
	client, done := testClient(t, rec)
	defer done()

	id, err := client.Create(context.Background(), Task{
		Type:     TypeMove,
		Username: "u",
		Data:     map[string]any{"sources": []string{"/z/home/u/a"}, "destination": "/z/home/u/b"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if id != "/tasks/abc-123" {
		t.Errorf("id = %q, want the Location header verbatim", id)
	}
	if rec.method != http.MethodPost || rec.path != "/tasks" {
		t.Errorf("request = %s %s, want POST /tasks", rec.method, rec.path)
	}

	var sent map[string]any
	if err := json.Unmarshal([]byte(rec.body), &sent); err != nil {
		t.Fatalf("decoding what was sent: %v", err)
	}
	if sent["type"] != TypeMove || sent["username"] != "u" {
		t.Errorf("body = %s, want the task's type and username", rec.body)
	}
	// An id and dates are the service's to assign; sending empty ones would overwrite them.
	for _, key := range []string{"id", "start_date", "end_date"} {
		if _, present := sent[key]; present {
			t.Errorf("body carried %q, which the service assigns", key)
		}
	}
}

func TestCreateWithoutALocationIsAnError(t *testing.T) {
	rec := &recorder{status: http.StatusCreated}
	client, done := testClient(t, rec)
	defer done()

	if _, err := client.Create(context.Background(), Task{Type: TypeMove}); err == nil {
		t.Fatal("Create succeeded although no id was returned")
	}
}

// The completed variant is what sets the end date, and it is distinguished only by a query
// parameter. Getting this wrong leaves every task locked forever.
func TestAddStatusMarksCompletionWithAQueryParameter(t *testing.T) {
	cases := []struct {
		name      string
		complete  bool
		wantQuery string
	}{
		{name: "progress", wantQuery: ""},
		{name: "terminal", complete: true, wantQuery: "complete=true"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{status: http.StatusOK}
			client, done := testClient(t, rec)
			defer done()

			status := Status{Status: StatusRunning, Detail: "[pod] /z/home/u/a: moved"}

			var err error
			if tc.complete {
				err = client.AddCompletedStatus(context.Background(), "/tasks/abc-123", status)
			} else {
				err = client.AddStatus(context.Background(), "/tasks/abc-123", status)
			}
			if err != nil {
				t.Fatalf("adding a status: %v", err)
			}

			if rec.path != "/tasks/abc-123/status" {
				t.Errorf("path = %q, want the task's status path with the id normalized", rec.path)
			}
			if rec.query != tc.wantQuery {
				t.Errorf("query = %q, want %q", rec.query, tc.wantQuery)
			}

			var sent Status
			if err := json.Unmarshal([]byte(rec.body), &sent); err != nil {
				t.Fatalf("decoding what was sent: %v", err)
			}
			if sent.Status != status.Status || sent.Detail != status.Detail {
				t.Errorf("body = %s, want the status and its detail", rec.body)
			}
		})
	}
}

// The service spells these with underscores, unlike the rest of the DE, and reads
// include_null_end by presence rather than value.
func TestFilterQuery(t *testing.T) {
	since := time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC)

	got := Filter{
		Types:          []string{TypeMove, TypeRename},
		IncludeNullEnd: true,
		EndDateSince:   &since,
	}.query()

	if names := got["type"]; len(names) != 2 || names[0] != TypeMove || names[1] != TypeRename {
		t.Errorf("type = %q, want one parameter per type", names)
	}
	if got.Get("include_null_end") == "" {
		t.Error("include_null_end was not set")
	}
	if want := "9999-12-31T23:59:59Z"; got.Get("end_date_since") != want {
		t.Errorf("end_date_since = %q, want %q", got.Get("end_date_since"), want)
	}
	if _, present := got["start_date_since"]; present {
		t.Error("start_date_since was sent although it was not set")
	}
}

// A filter that matches nothing answers with a bare null rather than an empty array.
func TestByFilterHandlesANullResponse(t *testing.T) {
	rec := &recorder{status: http.StatusOK, response: "null"}
	client, done := testClient(t, rec)
	defer done()

	tasks, err := client.ByFilter(context.Background(), Filter{Types: []string{TypeMove}})
	if err != nil {
		t.Fatalf("ByFilter: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("tasks = %+v, want none", tasks)
	}
}

func TestByFilterDecodesTasks(t *testing.T) {
	rec := &recorder{status: http.StatusOK, response: `[
		{"id":"abc","type":"data-move","username":"u","start_date":"2026-08-21T00:00:00Z","end_date":null,
		 "data":{"sources":["/z/home/u/a"],"destination":"/z/home/u/b"}}
	]`}
	client, done := testClient(t, rec)
	defer done()

	tasks, err := client.ByFilter(context.Background(), Filter{Types: []string{TypeMove}})
	if err != nil {
		t.Fatalf("ByFilter: %v", err)
	}

	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(tasks))
	}
	if tasks[0].EndDate != nil {
		t.Error("end date was decoded as set, but the task has not finished")
	}
	if tasks[0].Data["destination"] != "/z/home/u/b" {
		t.Errorf("data = %+v, want the destination to survive decoding", tasks[0].Data)
	}
}

func TestErrorsCarryTheStatusAndBody(t *testing.T) {
	rec := &recorder{status: http.StatusInternalServerError, response: `{"reason":"database is down"}`}
	client, done := testClient(t, rec)
	defer done()

	_, err := client.GetByID(context.Background(), "abc")
	if err == nil {
		t.Fatal("GetByID succeeded although the service returned 500")
	}
	if want := "database is down"; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want it to carry %q", err, want)
	}
}

func TestNewRejectsAnUnusableBaseURL(t *testing.T) {
	for _, base := range []string{"", "async-tasks:60000", "/tasks"} {
		if _, err := New(base, time.Second); err == nil {
			t.Errorf("New(%q) succeeded, want an error naming the missing scheme or host", base)
		}
	}
}
