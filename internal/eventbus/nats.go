package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	streamName    = "sympozium"
	consumerGroup = "sympozium-workers"

	// reconnectBackoff is how long a Subscribe loop waits before recreating its
	// consumer after a fetch error (e.g. the consumer or stream was lost because
	// NATS was restarted/recreated).
	reconnectBackoff = 2 * time.Second
)

// NATSEventBus implements EventBus using NATS JetStream.
type NATSEventBus struct {
	conn *nats.Conn
	js   jetstream.JetStream
}

// NewNATSEventBus creates a new NATS JetStream event bus.
func NewNATSEventBus(url string) (*NATSEventBus, error) {
	return NewNATSEventBusWithContext(context.Background(), url)
}

// NewNATSEventBusWithContext bounds initial stream provisioning. Cancellation
// closes the connection, including its background reconnect loop. A successfully
// returned bus retains the existing lifetime/reconnect policy after ctx expires.
func NewNATSEventBusWithContext(ctx context.Context, url string) (*NATSEventBus, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n := &NATSEventBus{}
	initialized := make(chan struct{})

	// MaxReconnects(-1) keeps the client reconnecting indefinitely. A bounded
	// limit means a NATS pod recreation that takes longer than
	// limit*ReconnectWait permanently kills the connection, silently breaking
	// every subscription until the process restarts.
	opts := append(connectOptions(),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			log.Printf("eventbus: disconnected from NATS: %v", err)
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			// The first reconnect can race constructor initialization.
			<-initialized
			if n.js == nil {
				return
			}
			log.Printf("eventbus: reconnected to NATS at %s", c.ConnectedUrl())
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := n.ensureStream(ctx); err != nil {
				log.Printf("eventbus: failed to ensure stream after reconnect: %v", err)
			}
		}),
		nats.ClosedHandler(func(_ *nats.Conn) { log.Printf("eventbus: NATS connection closed") }),
	)
	connectTimeout := 2 * time.Second
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < connectTimeout {
		connectTimeout = time.Until(deadline)
		if connectTimeout <= 0 {
			close(initialized)
			return nil, context.DeadlineExceeded
		}
	}
	opts = append(opts, nats.Timeout(connectTimeout))
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		close(initialized)
		return nil, fmt.Errorf("connecting to NATS: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		close(initialized)
		nc.Close()
		return nil, fmt.Errorf("creating JetStream context: %w", err)
	}

	n.conn = nc
	n.js = js
	close(initialized)

	// Retry stream creation — NATS may not be fully ready yet.
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		if _, lastErr = n.ensureStream(ctx); lastErr == nil {
			break
		}
		if attempt == 9 {
			break
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			nc.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if lastErr != nil {
		nc.Close()
		return nil, fmt.Errorf("creating JetStream stream after retries: %w", lastErr)
	}

	return n, nil
}

// Publish sends an event to the NATS JetStream stream.
// Trace context from ctx is automatically injected into NATS message headers.
func (n *NATSEventBus) Publish(ctx context.Context, topic string, event *Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshalling event: %w", err)
	}

	subject := topicToSubject(topic)
	msg := &nats.Msg{
		Subject: subject,
		Data:    data,
		Header:  nats.Header{},
	}
	InjectTraceContext(ctx, msg.Header)

	_, err = n.js.PublishMsg(ctx, msg)
	if err != nil {
		return fmt.Errorf("publishing to %s: %w", subject, err)
	}

	return nil
}

// Subscribe returns a channel that receives events for the given topic.
//
// The subscription is resilient to NATS restarts/recreations: if a fetch fails
// because the consumer or stream no longer exists, the loop recreates them
// (re-creating the stream first if needed) and resumes, so the subscription
// recovers without requiring the process to restart.
func (n *NATSEventBus) Subscribe(ctx context.Context, topic string) (<-chan *Event, error) {
	subject := topicToSubject(topic)

	consumer, err := n.createConsumer(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("creating consumer for %s: %w", subject, err)
	}

	ch := make(chan *Event, 64)

	go func() {
		defer close(ch)
		for {
			if ctx.Err() != nil {
				return
			}

			msgs, err := consumer.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
			if err == nil {
				for msg := range msgs.Messages() {
					var event Event
					if uerr := json.Unmarshal(msg.Data(), &event); uerr != nil {
						// Terminate (not Nak) an unparseable message. The consumer
						// has no MaxDeliver/AckWait backoff, so Nak would redeliver
						// the same poison message immediately and live-lock this
						// subscription forever. Term drops it and advances.
						msg.Term()
						continue
					}

					// Extract trace context from NATS message headers so consumers
					// can continue the distributed trace started by the publisher.
					event.Ctx = ExtractTraceContext(ctx, msg.Headers())

					select {
					case ch <- &event:
						msg.Ack()
					case <-ctx.Done():
						return
					}
				}
				// A fetch that simply timed out with no messages reports no
				// error here; only a real consumer/stream problem does.
				err = msgs.Error()
				if err == nil {
					continue
				}
			}

			// Fetch (or batch) error. This usually means the consumer or the
			// stream was lost because NATS was restarted/recreated. Back off,
			// then recreate the consumer so the subscription self-heals instead
			// of spinning forever on a dead consumer.
			select {
			case <-ctx.Done():
				return
			case <-time.After(reconnectBackoff):
			}

			newConsumer, rerr := n.createConsumer(ctx, subject)
			if rerr != nil {
				log.Printf("eventbus: failed to recreate consumer for %s: %v (after %v)", subject, rerr, err)
				continue
			}
			log.Printf("eventbus: recreated consumer for %s after error: %v", subject, err)
			consumer = newConsumer
		}
	}()

	return ch, nil
}

// Close shuts down the NATS connection.
func (n *NATSEventBus) Close() error {
	n.conn.Close()
	return nil
}

// ensureStream creates or updates the sympozium stream and returns a handle to
// it. It is safe to call repeatedly — after a NATS recreate the stream no longer
// exists, and calling this recreates it.
func (n *NATSEventBus) ensureStream(ctx context.Context) (jetstream.Stream, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return n.js.CreateOrUpdateStream(cctx, streamConfig())
}

// createConsumer (re)creates a pull consumer for the given subject, ensuring the
// stream exists first so it works even after NATS has been recreated.
func (n *NATSEventBus) createConsumer(ctx context.Context, subject string) (jetstream.Consumer, error) {
	stream, err := n.ensureStream(ctx)
	if err != nil {
		return nil, err
	}

	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return stream.CreateOrUpdateConsumer(cctx, jetstream.ConsumerConfig{
		FilterSubject: subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverNewPolicy,
	})
}

// streamConfig returns the JetStream stream configuration for Sympozium events.
func streamConfig() jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:      streamName,
		Subjects:  []string{"sympozium.>"},
		Retention: jetstream.LimitsPolicy,
		MaxAge:    24 * time.Hour,
		Storage:   jetstream.FileStorage,
		Replicas:  1,
	}
}

// topicToSubject converts a dotted topic (e.g. "agent.run.completed")
// to a NATS subject under the sympozium namespace (e.g. "sympozium.agent.run.completed").
func topicToSubject(topic string) string {
	return "sympozium." + topic
}
