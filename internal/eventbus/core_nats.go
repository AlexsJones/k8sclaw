package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
)

// CoreNATSEventBus is an EventBus backed by core NATS rather than the
// JetStream management API. It exists for intentionally constrained clients
// such as the IPC bridge: publishing to a subject is still captured by the
// sympozium JetStream stream, but the client never needs permission to create
// streams or consumers.
type CoreNATSEventBus struct {
	conn *nats.Conn
}

// NewCoreNATSEventBus creates a core-NATS client. NATS_USERNAME and
// NATS_PASSWORD are optional so existing unauthenticated external NATS
// installations remain supported during migration.
func NewCoreNATSEventBus(url string) (*CoreNATSEventBus, error) {
	nc, err := connect(url)
	if err != nil {
		return nil, err
	}
	return &CoreNATSEventBus{conn: nc}, nil
}

func (n *CoreNATSEventBus) Publish(ctx context.Context, topic string, event *Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshalling event: %w", err)
	}
	msg := &nats.Msg{Subject: topicToSubject(topic), Data: data, Header: nats.Header{}}
	InjectTraceContext(ctx, msg.Header)
	if err := n.conn.PublishMsg(msg); err != nil {
		return fmt.Errorf("publishing to %s: %w", msg.Subject, err)
	}
	// Unlike JetStream Publish, core NATS publish is asynchronous. Flush before
	// returning because the bridge may terminate immediately after writing a
	// terminal result and must not lose that result during connection close.
	if err := n.conn.FlushWithContext(ctx); err != nil {
		return fmt.Errorf("flushing publish to %s: %w", msg.Subject, err)
	}
	return nil
}

func (n *CoreNATSEventBus) Subscribe(ctx context.Context, topic string) (<-chan *Event, error) {
	ch := make(chan *Event, 64)
	sub, err := n.conn.Subscribe(topicToSubject(topic), func(msg *nats.Msg) {
		var event Event
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			return
		}
		event.Ctx = ExtractTraceContext(ctx, msg.Header)
		select {
		case ch <- &event:
		case <-ctx.Done():
		}
	})
	if err != nil {
		return nil, fmt.Errorf("subscribing to %s: %w", topicToSubject(topic), err)
	}
	go func() {
		<-ctx.Done()
		sub.Unsubscribe()
		close(ch)
	}()
	return ch, nil
}

func (n *CoreNATSEventBus) Close() error { n.conn.Close(); return nil }

// connect applies optional username/password credentials and the reconnect
// behaviour shared by both trusted JetStream clients and constrained bridges.
func connect(url string) (*nats.Conn, error) {
	nc, err := nats.Connect(url, connectOptions()...)
	if err != nil {
		return nil, fmt.Errorf("connecting to NATS: %w", err)
	}
	return nc, nil
}

func connectOptions() []nats.Option {
	opts := []nats.Option{
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
	}
	if user, password := os.Getenv("NATS_USERNAME"), os.Getenv("NATS_PASSWORD"); user != "" || password != "" {
		opts = append(opts, nats.UserInfo(user, password))
	}
	return opts
}
