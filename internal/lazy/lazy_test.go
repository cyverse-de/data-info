package lazy

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/semaphore"
)

func TestGoStartsImmediately(t *testing.T) {
	started := make(chan struct{})

	v := Go(context.Background(), nil, func(context.Context) (int, error) {
		close(started)
		return 42, nil
	})

	// The work must be under way before anything reads the value. A computation that only
	// began on first read would turn concurrent lookups into sequential ones.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the computation did not start until it was read")
	}

	got, err := v.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != 42 {
		t.Errorf("got %d, want 42", got)
	}
}

func TestGetIsRepeatable(t *testing.T) {
	var calls atomic.Int64

	v := Go(context.Background(), nil, func(context.Context) (int, error) {
		calls.Add(1)
		return 7, nil
	})

	for i := 0; i < 5; i++ {
		got, err := v.Get(context.Background())
		if err != nil || got != 7 {
			t.Fatalf("Get %d = %d, %v", i, got, err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("the computation ran %d times, want 1", n)
	}
}

func TestGetPropagatesError(t *testing.T) {
	want := errors.New("boom")
	v := Go(context.Background(), nil, func(context.Context) (int, error) { return 0, want })

	if _, err := v.Get(context.Background()); !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

// TestGetContextBoundsOnlyTheWait is the distinction that matters when several callers
// share a value: one giving up must not cancel the work the others are waiting for.
func TestGetContextBoundsOnlyTheWait(t *testing.T) {
	release := make(chan struct{})
	v := Go(context.Background(), nil, func(context.Context) (int, error) {
		<-release
		return 1, nil
	})

	impatient, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := v.Get(impatient); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}

	close(release)

	got, err := v.Get(context.Background())
	if err != nil {
		t.Fatalf("the value failed after another caller gave up: %v", err)
	}
	if got != 1 {
		t.Errorf("got %d, want 1", got)
	}
}

func TestPeek(t *testing.T) {
	release := make(chan struct{})
	v := Go(context.Background(), nil, func(context.Context) (int, error) {
		<-release
		return 9, nil
	})

	if _, _, ok := v.Peek(); ok {
		t.Error("Peek reported a result before the computation finished")
	}

	close(release)
	if _, err := v.Get(context.Background()); err != nil {
		t.Fatalf("Get: %v", err)
	}

	got, err, ok := v.Peek()
	if !ok {
		t.Fatal("Peek reported no result after the computation finished")
	}
	if err != nil || got != 9 {
		t.Errorf("Peek = %d, %v; want 9, nil", got, err)
	}
}

func TestResolvedAndFailed(t *testing.T) {
	if got, _, ok := Resolved(3).Peek(); !ok || got != 3 {
		t.Errorf("Resolved(3).Peek() = %d, %v", got, ok)
	}

	want := errors.New("nope")
	if _, err, ok := Failed[int](want).Peek(); !ok || !errors.Is(err, want) {
		t.Errorf("Failed.Peek() = %v, %v", err, ok)
	}
}

// TestSemaphoreBoundsConcurrency covers the reason the semaphore is acquired inside the
// goroutine: dispatching many lookups must not block the dispatcher, but only so many may
// actually run.
func TestSemaphoreBoundsConcurrency(t *testing.T) {
	const (
		limit = 2
		total = 10
	)

	sem := semaphore.NewWeighted(limit)
	var inFlight, peak atomic.Int64

	values := make([]*Value[int], 0, total)
	dispatched := make(chan struct{})

	for i := 0; i < total; i++ {
		values = append(values, Go(context.Background(), sem, func(context.Context) (int, error) {
			current := inFlight.Add(1)
			for {
				old := peak.Load()
				if current <= old || peak.CompareAndSwap(old, current) {
					break
				}
			}
			<-dispatched
			inFlight.Add(-1)
			return 1, nil
		}))
	}

	// Dispatching all ten did not block, even though only two may run.
	close(dispatched)

	for i, v := range values {
		if _, err := v.Get(context.Background()); err != nil {
			t.Fatalf("value %d: %v", i, err)
		}
	}

	if got := peak.Load(); got > limit {
		t.Errorf("%d ran at once, want at most %d", got, limit)
	}
}

func TestAwaitReturnsFirstError(t *testing.T) {
	want := errors.New("second failed")

	first := Resolved(1)
	second := Failed[int](want)
	third := Resolved(3)

	err := Await(context.Background(), Wait(first), Wait(second), Wait(third))
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}

	if err := Await(context.Background(), Wait(first), Wait(third)); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}
