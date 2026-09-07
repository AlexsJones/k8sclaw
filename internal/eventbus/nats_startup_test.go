package eventbus

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestNATSStartupDeadlineClosesUnavailableConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	// Keep the port reserved without speaking NATS: exercise initial handshake
	// timeout as well as stream provisioning/reconnect waiting.
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	bus, err := NewNATSEventBusWithContext(ctx, "nats://"+address)
	if bus != nil || err == nil {
		t.Fatalf("unexpected initialized bus: %v %v", bus, err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("startup deadline not respected")
	}
}

func TestNATSStartupCancelledBeforeDial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	bus, err := NewNATSEventBusWithContext(ctx, "invalid URL that must never be dialled")
	if bus != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("%v %v", bus, err)
	}
}

func TestNATSStartupUnavailableStreamHonorsDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	bus, err := NewNATSEventBusWithContext(ctx, "nats://"+address)
	if bus != nil || err == nil {
		t.Fatalf("%v %v", bus, err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("reconnect/stream wait exceeded deadline")
	}
}
