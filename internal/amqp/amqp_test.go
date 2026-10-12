package amqp

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// blockedPublisher is a publisher whose sends block until released, as a write does when the
// broker stops reading under a resource alarm.
func blockedPublisher(t *testing.T) (*Publisher, chan struct{}, chan string) {
	t.Helper()

	discard := logrus.New()
	discard.SetOutput(io.Discard)

	release := make(chan struct{})
	sent := make(chan string, 10)
	p := &Publisher{log: logrus.NewEntry(discard), lock: make(chan struct{}, 1), timeout: 50 * time.Millisecond}
	p.send = func(_ context.Context, _ *logrus.Entry, routingKey string, _ []byte, _ time.Time) {
		<-release
		sent <- routingKey
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	return p, release, sent
}

// TestPublishDoesNotWaitOnABlockedBroker is EmptyTrash's case: it publishes before its task
// can reach a terminal status, so a publish that never returned would leave the task's paths
// locked and the shutdown drain overrunning.
func TestPublishDoesNotWaitOnABlockedBroker(t *testing.T) {
	p, release, sent := blockedPublisher(t)

	for i, key := range []string{"first", "second"} {
		start := time.Now()
		p.Publish(context.Background(), key, "update")
		if waited := time.Since(start); waited > time.Second {
			t.Fatalf("publish %d waited %s on a blocked broker", i, waited)
		}
	}

	close(release)
	if got := <-sent; got != "first" {
		t.Errorf("sent %q, want the first event once the broker recovered", got)
	}
	select {
	case got := <-sent:
		t.Errorf("sent %q; an event that arrived while another was blocked should be dropped", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestCloseDoesNotWaitOnABlockedBroker(t *testing.T) {
	p, _, _ := blockedPublisher(t)
	p.Publish(context.Background(), "first", "update")

	start := time.Now()
	p.Close()
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("Close waited %s on a blocked broker", waited)
	}
}
