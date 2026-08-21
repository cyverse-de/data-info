package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/icattest"
	"github.com/cyverse-de/data-info/internal/worker"
	"github.com/labstack/echo/v4"
)

// errStub stands in for a backend the write tests never reach.
var errStub = errors.New("no backend in this test")

// serveWithBody is serveRoute for a request that carries one, which the routes taking a data
// id in the path all do.
func serveWithBody(
	t *testing.T,
	method, pattern, target string,
	handler echo.HandlerFunc,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()

	e := echo.New()
	e.HideBanner = true
	e.HTTPErrorHandler = apierror.HTTPErrorHandler(nil)
	e.Add(method, pattern, handler)

	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// recordingCreator captures the task a handler asked for without talking to anything.
type recordingCreator struct {
	created []asynctasks.Task
	err     error
}

func (r *recordingCreator) Create(_ context.Context, task asynctasks.Task) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	r.created = append(r.created, task)
	return "/tasks/00000000-0000-0000-0000-00000000000" + string(rune('0'+len(r.created))), nil
}

// noTasks answers the lock check with nothing running.
type noTasks struct{ tasks []asynctasks.Task }

func (n noTasks) ByFilter(context.Context, asynctasks.Filter) ([]asynctasks.Task, error) {
	return n.tasks, nil
}

// moveDeps wires the write handlers with a task recorder and a runner that swallows jobs, so
// a test can assert on what a request validated and recorded without any backend.
func moveDeps(t *testing.T) (Deps, *icattest.Fake, *recordingCreator) {
	t.Helper()

	deps, fake := testDeps(t)
	creator := &recordingCreator{}

	deps.Tasks = noTasks{}
	deps.Creator = creator
	deps.Worker = worker.NewRunner(stubTasks{}, deps.Logger(), "test")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = deps.Worker.Shutdown(ctx)
	})

	return deps, fake, creator
}

// stubTasks lets the runner start without a backend; the jobs it runs do nothing useful here
// because the tests assert on what was validated and recorded, not on what was moved.
type stubTasks struct{}

func (stubTasks) GetByID(context.Context, string) (*asynctasks.Task, error) {
	return nil, errStub
}
func (stubTasks) AddStatus(context.Context, string, asynctasks.Status) error          { return nil }
func (stubTasks) AddCompletedStatus(context.Context, string, asynctasks.Status) error { return nil }

func TestMoveRejectsBadRequests(t *testing.T) {
	deps, fake, _ := moveDeps(t)
	fake.AddCollection(testHome+"/dest", icat.AccessOwn)
	fake.AddCollection(testHome+"/readonly", icat.AccessRead)
	fake.AddDataObject(testHome+"/dest/a.txt", 10, icat.AccessOwn)
	writes := NewWrites(deps)

	cases := []struct {
		name     string
		body     string
		wantCode int
		wantErr  string
	}{
		{
			name:     "no destination",
			body:     `{"sources":["` + testHome + `/a.txt"]}`,
			wantCode: http.StatusBadRequest,
			wantErr:  string(apierror.ErrIllegalArgument),
		},
		{
			name:     "a source that is not there",
			body:     `{"sources":["` + testHome + `/missing"],"dest":"` + testHome + `/dest"}`,
			wantCode: http.StatusInternalServerError,
			wantErr:  string(apierror.ErrDoesNotExist),
		},
		{
			name:     "a destination that is not there",
			body:     `{"sources":["` + testHome + `/a.txt"],"dest":"` + testHome + `/nowhere"}`,
			wantCode: http.StatusInternalServerError,
			wantErr:  string(apierror.ErrDoesNotExist),
		},
		{
			name:     "a destination that is a file",
			body:     `{"sources":["` + testHome + `/a.txt"],"dest":"` + testHome + `/dest/a.txt"}`,
			wantCode: http.StatusInternalServerError,
			wantErr:  string(apierror.ErrNotAFolder),
		},
		{
			// Ownership, not write access: moving something changes who can see it.
			// ERR_NOT_OWNER is one of the few codes the status table maps off 500.
			name:     "a source the caller does not own",
			body:     `{"sources":["` + testHome + `/a.txt"],"dest":"` + testHome + `/dest"}`,
			wantCode: http.StatusForbidden,
			wantErr:  string(apierror.ErrNotOwner),
		},
		{
			name:     "a destination the caller cannot write to",
			body:     `{"sources":["` + testHome + `/owned"],"dest":"` + testHome + `/readonly"}`,
			wantCode: http.StatusInternalServerError,
			wantErr:  string(apierror.ErrNotWriteable),
		},
		{
			name:     "something already at the destination",
			body:     `{"sources":["` + testHome + `/owned/a.txt"],"dest":"` + testHome + `/dest"}`,
			wantCode: http.StatusInternalServerError,
			wantErr:  string(apierror.ErrExists),
		},
	}

	fake.AddCollection(testHome+"/owned", icat.AccessOwn)
	fake.AddDataObject(testHome+"/owned/a.txt", 10, icat.AccessOwn)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(t, apierror.StyleTrap, http.MethodPost, "/mover?user="+testUser, tc.body, writes.Move)

			if rec.Code != tc.wantCode {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if got := errorCodeOf(t, rec); got != tc.wantErr {
				t.Errorf("error_code = %q, want %q", got, tc.wantErr)
			}
		})
	}
}

// Nothing may be recorded for a request that could not have succeeded: a task's existence is
// what locks its paths, so one created for work that never happens locks them for nothing.
func TestAFailedMoveRecordsNoTask(t *testing.T) {
	deps, _, creator := moveDeps(t)
	writes := NewWrites(deps)

	body := `{"sources":["` + testHome + `/missing"],"dest":"` + testHome + `/nowhere"}`
	rec := serve(t, apierror.StyleTrap, http.MethodPost, "/mover?user="+testUser, body, writes.Move)

	if rec.Code == http.StatusOK {
		t.Fatalf("the request succeeded, but nothing it named exists: %s", rec.Body.String())
	}
	if len(creator.created) != 0 {
		t.Errorf("recorded %d tasks for a request that failed validation", len(creator.created))
	}
}

// The paths a move locks are the sources and where each of them will land, not the
// destination collection -- which already exists and is not being moved.
func TestAMoveLocksItsSourcesAndDestinations(t *testing.T) {
	deps, fake, creator := moveDeps(t)
	fake.AddCollection(testHome+"/dest", icat.AccessOwn)
	fake.AddCollection(testHome+"/owned", icat.AccessOwn)
	writes := NewWrites(deps)

	body := `{"sources":["` + testHome + `/owned"],"dest":"` + testHome + `/dest"}`
	rec := serve(t, apierror.StyleTrap, http.MethodPost, "/mover?user="+testUser, body, writes.Move)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	if len(creator.created) != 1 {
		t.Fatalf("recorded %d tasks, want exactly one", len(creator.created))
	}

	task := creator.created[0]
	if task.Type != asynctasks.TypeMove {
		t.Errorf("type = %q, want %q", task.Type, asynctasks.TypeMove)
	}
	if task.Username != testUser {
		t.Errorf("username = %q, want %q", task.Username, testUser)
	}

	// The behaviour has to complete the task, or a job whose process dies locks its paths
	// for good rather than for ten minutes.
	if len(task.Behaviors) != 1 {
		t.Fatalf("behaviors = %+v, want the stall timeout", task.Behaviors)
	}

	var response multiMoveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body.String(), err)
	}
	if response.TaskID == "" {
		t.Error("no task id was returned, so the caller cannot follow the move")
	}
}

// An empty list of sources is accepted rather than refused. The reference's schema allows it,
// and a caller that built its list by filtering should get a no-op rather than an error.
func TestMovingNothingIsANoOpRatherThanAnError(t *testing.T) {
	deps, fake, creator := moveDeps(t)
	fake.AddCollection(testHome+"/dest", icat.AccessOwn)
	writes := NewWrites(deps)

	body := `{"sources":[],"dest":"` + testHome + `/dest"}`
	rec := serve(t, apierror.StyleTrap, http.MethodPost, "/mover?user="+testUser, body, writes.Move)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if len(creator.created) != 1 {
		t.Errorf("recorded %d tasks, want one that moves nothing", len(creator.created))
	}
}

// Renaming something to what it is already called is not an error and needs no task, and the
// response says so by carrying no task id.
func TestRenamingToTheSameNameDoesNothing(t *testing.T) {
	deps, fake, creator := moveDeps(t)
	fake.AddDataObject(testHome+"/same.txt", 10, icat.AccessOwn)
	fake.SetUUID("11111111-1111-1111-1111-111111111111", testHome+"/same.txt")
	writes := NewWrites(deps)

	rec := serveWithBody(t, http.MethodPut, "/data/:data-id/name",
		"/data/11111111-1111-1111-1111-111111111111/name?user="+testUser,
		writes.RenameByID, `{"filename":"same.txt"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var response moveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body.String(), err)
	}
	if response.TaskID != "" {
		t.Errorf("task id = %q, want none: nothing had to happen", response.TaskID)
	}
	if len(creator.created) != 0 {
		t.Errorf("recorded %d tasks for a rename that was a no-op", len(creator.created))
	}
}
