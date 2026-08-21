package locks

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
)

// fakeReader answers with a fixed task list.
type fakeReader struct {
	tasks  []asynctasks.Task
	filter asynctasks.Filter
	err    error
}

func (f *fakeReader) ByFilter(_ context.Context, filter asynctasks.Filter) ([]asynctasks.Task, error) {
	f.filter = filter
	return f.tasks, f.err
}

func TestConflicts(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"the same path", "/z/home/u/a", "/z/home/u/a", true},
		{"a trailing slash is not a different path", "/z/home/u/a/", "/z/home/u/a", true},
		{"a child of a locked collection", "/z/home/u/a/b", "/z/home/u/a", true},
		{"an ancestor of a locked path", "/z/home/u", "/z/home/u/a/b", true},
		{"unrelated siblings", "/z/home/u/a", "/z/home/u/b", false},

		// The reference compares against a path plus a slash without requiring the match
		// to end on a component boundary, so it calls these conflicts. They are not: /ab
		// is not inside /a, in either direction.
		{"a sibling whose name extends the other", "/z/home/u/a", "/z/home/u/ab", false},
		{"the same, reversed", "/z/home/u/ab", "/z/home/u/a", false},
		{"a deep path under a same-prefixed sibling", "/z/home/u/ab/c", "/z/home/u/a", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Conflicts(tc.a, tc.b); got != tc.want {
				t.Errorf("Conflicts(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			// The relation has to be symmetric, or which argument came from the request
			// would change the answer.
			if got := Conflicts(tc.b, tc.a); got != tc.want {
				t.Errorf("Conflicts(%q, %q) = %v, want %v (not symmetric)", tc.b, tc.a, got, tc.want)
			}
		})
	}
}

// The keys inside a task's data are a contract with whoever wrote the task, which during a
// rollback is a Clojure replica. Each shape here is the one that service writes.
func TestLockedPaths(t *testing.T) {
	cases := []struct {
		name string
		task asynctasks.Task
		want []string
	}{
		{
			name: "a move locks its sources and where they will land",
			task: asynctasks.Task{
				Type: asynctasks.TypeMove,
				Data: map[string]any{
					"sources":     []any{"/z/home/u/a", "/z/home/u/b/c"},
					"destination": "/z/home/u/dest",
				},
			},
			want: []string{"/z/home/u/a", "/z/home/u/b/c", "/z/home/u/dest/a", "/z/home/u/dest/c"},
		},
		{
			name: "a rename locks both ends",
			task: asynctasks.Task{
				Type: asynctasks.TypeRename,
				Data: map[string]any{"source": "/z/home/u/old", "destination": "/z/home/u/new"},
			},
			want: []string{"/z/home/u/new", "/z/home/u/old"},
		},
		{
			name: "a delete locks the paths and where they land in the trash",
			task: asynctasks.Task{
				Type: asynctasks.TypeDelete,
				Data: map[string]any{
					"paths":       []any{"/z/home/u/gone"},
					"trash-paths": map[string]any{"/z/home/u/gone": "/z/trash/home/u/gone.abc"},
				},
			},
			want: []string{"/z/home/u/gone", "/z/trash/home/u/gone.abc"},
		},
		{
			name: "emptying the trash stores a list where a delete stores a map",
			task: asynctasks.Task{
				Type: asynctasks.TypeDeleteTrash,
				Data: map[string]any{"trash-paths": []any{"/z/trash/home/u/a", "/z/trash/home/u/b"}},
			},
			want: []string{"/z/trash/home/u/a", "/z/trash/home/u/b"},
		},
		{
			name: "a restore locks the trash paths and where they are restored to",
			task: asynctasks.Task{
				Type: asynctasks.TypeRestore,
				Data: map[string]any{
					"paths": []any{"/z/trash/home/u/a"},
					"restoration-paths": map[string]any{
						"/z/trash/home/u/a": map[string]any{"restored-path": "/z/home/u/a"},
					},
				},
			},
			want: []string{"/z/home/u/a", "/z/trash/home/u/a"},
		},
		{
			// Uploads clean up after themselves on a hidden object nobody else can name,
			// so they hold nothing.
			name: "an upload cleanup locks nothing",
			task: asynctasks.Task{
				Type: asynctasks.TypeUploadCleanup,
				Data: map[string]any{"path": "/z/home/u/.x.partial-1"},
			},
			want: nil,
		},
		{
			// A task written oddly must not take the whole lock check down with it: that
			// would block every write in the DE.
			name: "data of the wrong shape is skipped rather than failing",
			task: asynctasks.Task{
				Type: asynctasks.TypeMove,
				Data: map[string]any{"sources": "not a list", "destination": 42},
			},
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LockedPaths([]asynctasks.Task{tc.task})

			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("LockedPaths = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	locking := []asynctasks.Task{{
		Type: asynctasks.TypeMove,
		Data: map[string]any{
			"sources":     []any{"/z/home/u/moving"},
			"destination": "/z/home/u/dest",
		},
	}}

	cases := []struct {
		name      string
		tasks     []asynctasks.Task
		paths     []string
		wantPaths []string
	}{
		{
			name:  "nothing is running",
			paths: []string{"/z/home/u/a"},
		},
		{
			name:  "no paths to check",
			tasks: locking,
		},
		{
			name:  "an unrelated path",
			tasks: locking,
			paths: []string{"/z/home/u/elsewhere"},
		},
		{
			name:      "the path being moved",
			tasks:     locking,
			paths:     []string{"/z/home/u/moving"},
			wantPaths: []string{"/z/home/u/moving"},
		},
		{
			name:      "something inside the path being moved",
			tasks:     locking,
			paths:     []string{"/z/home/u/moving/inner"},
			wantPaths: []string{"/z/home/u/moving/inner"},
		},
		{
			name:      "where the move will land",
			tasks:     locking,
			paths:     []string{"/z/home/u/dest/moving"},
			wantPaths: []string{"/z/home/u/dest/moving"},
		},
		{
			name:      "every conflicting path is reported, not just the first",
			tasks:     locking,
			paths:     []string{"/z/home/u/moving", "/z/home/u/fine", "/z/home/u/dest/moving"},
			wantPaths: []string{"/z/home/u/moving", "/z/home/u/dest/moving"},
		},
		{
			name:  "a sibling whose name merely starts the same is allowed",
			tasks: locking,
			paths: []string{"/z/home/u/moving-elsewhere"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeReader{tasks: tc.tasks}

			err := Validate(context.Background(), reader, tc.paths)

			if len(tc.wantPaths) == 0 {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}

			var apiErr *apierror.Error
			if !errors.As(err, &apiErr) {
				t.Fatalf("Validate returned %v, want an *apierror.Error", err)
			}
			if apiErr.Code != apierror.ErrConflict {
				t.Errorf("code = %q, want %q", apiErr.Code, apierror.ErrConflict)
			}

			got, _ := apiErr.Extra["paths"].([]string)
			if strings.Join(got, ",") != strings.Join(tc.wantPaths, ",") {
				t.Errorf("paths = %q, want %q", got, tc.wantPaths)
			}
		})
	}
}

// Selecting unfinished work needs both halves of the filter. The date alone excludes exactly
// the tasks that matter, because theirs is null.
func TestValidateAsksForUnfinishedWork(t *testing.T) {
	reader := &fakeReader{}

	if err := Validate(context.Background(), reader, []string{"/z/home/u/a"}); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if !reader.filter.IncludeNullEnd {
		t.Error("the filter did not ask for tasks with no end date")
	}
	if reader.filter.EndDateSince == nil || reader.filter.EndDateSince.Year() != 9999 {
		t.Errorf("end date filter = %v, want a date no real task can be past", reader.filter.EndDateSince)
	}

	want := map[string]bool{
		asynctasks.TypeMove: true, asynctasks.TypeRename: true, asynctasks.TypeDelete: true,
		asynctasks.TypeDeleteTrash: true, asynctasks.TypeRestore: true,
	}
	if len(reader.filter.Types) != len(want) {
		t.Fatalf("types = %q, want the five locking types", reader.filter.Types)
	}
	for _, got := range reader.filter.Types {
		if !want[got] {
			t.Errorf("types included %q, which does not hold a lock", got)
		}
	}
}

func TestValidateReportsAReadFailure(t *testing.T) {
	reader := &fakeReader{err: errors.New("async-tasks is down")}

	// A lock check that cannot see the task list must refuse rather than assume nothing is
	// running: assuming would let two moves onto the same tree.
	if err := Validate(context.Background(), reader, []string{"/z/home/u/a"}); err == nil {
		t.Fatal("Validate succeeded although the task list could not be read")
	}
}
