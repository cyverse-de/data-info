// Package amqp publishes this service's one event: a request for the data-usage service to
// reindex a user after their trash is emptied.
package amqp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sirupsen/logrus"
)

// Config describes the broker and the exchange to publish on.
type Config struct {
	URI string

	Exchange   string
	Durable    bool
	AutoDelete bool
}

// envelope is the body of every published message. Consumers read both fields, so its shape
// is a contract.
type envelope struct {
	Message     any   `json:"message"`
	TimestampMS int64 `json:"timestamp_ms"`
}

// publishTimeout is how long Publish waits for an event to go out before leaving it to
// finish in the background. A broker under a memory or disk alarm stops reading, and a write
// already started then blocks with nothing to cancel it.
const publishTimeout = 10 * time.Second

// Publisher holds a connection to the broker. Every publish and reconnect holds one lock: a
// channel is not safe for concurrent use, and a reconnect would close one mid-publish.
type Publisher struct {
	cfg Config
	log *logrus.Entry

	// lock is a one-slot semaphore rather than a mutex so that a caller can stop waiting for
	// it. A publish blocked by the broker holds it for as long as the broker blocks.
	lock       chan struct{}
	connection *amqp.Connection
	channel    *amqp.Channel

	// send publishes one event while holding the lock. It is publishWithRetry, swapped in
	// tests for one that blocks the way a broker under an alarm does.
	send func(ctx context.Context, log *logrus.Entry, routingKey string, body []byte, now time.Time)

	// timeout is publishTimeout, shortened in tests.
	timeout time.Duration
}

// New builds a publisher and connects it. A broker that is down at startup is not fatal, since
// the event is only a hint to another service; the first publish reconnects.
func New(cfg Config, log *logrus.Entry) (*Publisher, error) {
	if cfg.URI == "" {
		return nil, fmt.Errorf("amqp: no broker URI was configured")
	}
	if cfg.Exchange == "" {
		return nil, fmt.Errorf("amqp: no exchange was configured")
	}

	p := &Publisher{cfg: cfg, log: log, lock: make(chan struct{}, 1), timeout: publishTimeout}
	p.send = p.publishWithRetry

	if err := p.reconnect(); err != nil {
		log.WithError(err).Warn("could not reach the AMQP broker at startup; " +
			"the first event published will try again")
	}
	return p, nil
}

// Publish sends a message, waiting at most publishTimeout so a broker that stops reading
// cannot hold up the caller. It logs rather than returns failures: the event is a hint, and
// the operation that produced it already succeeded.
func (p *Publisher) Publish(ctx context.Context, routingKey string, message any) {
	now := time.Now()

	body, err := json.Marshal(envelope{Message: message, TimestampMS: now.UnixMilli()})
	if err != nil {
		p.log.WithError(err).Error("could not encode an event; it will not be published")
		return
	}

	log := p.log.WithField("routing_key", routingKey)

	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	select {
	case p.lock <- struct{}{}:
	case <-ctx.Done():
		log.Error("an earlier event is still being published, so this one is dropped; " +
			"the broker is probably refusing writes under a memory or disk alarm, or unreachable")
		return
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { <-p.lock }()
		p.send(ctx, log, routingKey, body, now)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		log.Errorf("publishing an event took longer than %s, so it is being left to finish in "+
			"the background; the broker is probably refusing writes under a memory or disk "+
			"alarm, or unreachable", p.timeout)
	}
}

// publishWithRetry publishes, reconnecting and retrying once if the channel has gone away.
// The caller holds the lock.
func (p *Publisher) publishWithRetry(ctx context.Context, log *logrus.Entry, routingKey string, body []byte, now time.Time) {
	err := p.publishLocked(ctx, routingKey, body, now)
	if err == nil {
		log.Debug("published an event")
		return
	}
	log.WithError(err).Warn("publishing an event failed; reconnecting and retrying once")

	if err := p.reconnect(); err != nil {
		log.WithError(err).Error("could not reconnect to the AMQP broker; the event is lost. " +
			"Whatever consumes it will be out of date until the next event arrives")
		return
	}

	if err := p.publishLocked(ctx, routingKey, body, now); err != nil {
		log.WithError(err).Error("publishing an event failed again after reconnecting; it is lost")
		return
	}
	log.Info("published an event after reconnecting")
}

// publishLocked publishes on the current channel. The caller holds the lock.
func (p *Publisher) publishLocked(ctx context.Context, routingKey string, body []byte, now time.Time) error {
	if p.channel == nil {
		return fmt.Errorf("amqp: no channel is open")
	}

	return p.channel.PublishWithContext(ctx, p.cfg.Exchange, routingKey, false, false, amqp.Publishing{
		ContentType: "application/json",
		Timestamp:   now,
		Body:        body,
	})
}

// reconnect replaces the connection and channel, and redeclares the exchange. The caller
// holds the lock, except at construction where nothing else can see the publisher yet.
func (p *Publisher) reconnect() error {
	connection, err := amqp.Dial(p.cfg.URI)
	if err != nil {
		return fmt.Errorf("amqp: connecting to the broker: %w", err)
	}

	channel, err := connection.Channel()
	if err != nil {
		connection.Close() //nolint:errcheck // the dial is being abandoned
		return fmt.Errorf("amqp: opening a channel: %w", err)
	}

	// Declared on every connect so a rebuilt broker does not need this service restarted.
	err = channel.ExchangeDeclare(p.cfg.Exchange, "topic", p.cfg.Durable, p.cfg.AutoDelete, false, false, nil)
	if err != nil {
		channel.Close()    //nolint:errcheck // the connection is being abandoned
		connection.Close() //nolint:errcheck // as above
		return fmt.Errorf("amqp: declaring the %q exchange: %w", p.cfg.Exchange, err)
	}

	p.closeLocked()
	p.connection, p.channel = connection, channel
	return nil
}

// Close releases the connection, giving up after the publish timeout if a publish blocked by
// the broker still holds the lock, so shutdown cannot hang on it.
func (p *Publisher) Close() {
	select {
	case p.lock <- struct{}{}:
	case <-time.After(p.timeout):
		p.log.Warn("closing the AMQP connection timed out behind a publish the broker is blocking")
		return
	}
	defer func() { <-p.lock }()
	p.closeLocked()
}

// closeLocked drops whatever is currently open. The caller holds the lock.
func (p *Publisher) closeLocked() {
	if p.channel != nil {
		p.channel.Close() //nolint:errcheck // nothing to do about a failed close
		p.channel = nil
	}
	if p.connection != nil {
		p.connection.Close() //nolint:errcheck // as above
		p.connection = nil
	}
}
