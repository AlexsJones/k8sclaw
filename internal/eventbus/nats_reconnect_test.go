package eventbus

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// Opt-in real-server regression. No external NATS endpoint or credentials are
// accepted: this test owns a loopback server process and temporary storage.
func TestNATSRealServerReconnectAfterStartupContextExpires(t *testing.T) {
	binary := os.Getenv("SYMPOZIUM_NATS_SERVER")
	if binary == "" {
		t.Skip("set SYMPOZIUM_NATS_SERVER to a local nats-server binary")
	}
	t.Setenv("NATS_USERNAME", "")
	t.Setenv("NATS_PASSWORD", "")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	storage := t.TempDir()
	var server *exec.Cmd
	stop := func() {
		if server != nil {
			_ = server.Process.Kill()
			_ = server.Wait()
			server = nil
		}
	}
	defer stop()
	start := func() {
		server = exec.Command(binary, "-a", "127.0.0.1", "-p", strconv.Itoa(port), "-js", "-sd", storage)
		server.Stdout = os.Stderr
		server.Stderr = os.Stderr
		if err := server.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
			if err == nil {
				conn.Close()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("owned NATS process did not listen")
	}
	start()
	startup, cancelStartup := context.WithTimeout(context.Background(), 5*time.Second)
	bus, err := NewNATSEventBusWithContext(startup, "nats://"+address)
	cancelStartup() // This must not cancel a successfully initialized bus.
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	connection := bus.conn
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	events, err := bus.Subscribe(ctx, TopicAgentRunCompleted)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip := func(id string) {
		t.Helper()
		event := &Event{Topic: TopicAgentRunCompleted, Timestamp: time.Now(), Metadata: map[string]string{"proof": id}}
		if err := bus.Publish(ctx, TopicAgentRunCompleted, event); err != nil {
			t.Fatal(err)
		}
		for {
			select {
			case got, ok := <-events:
				if !ok {
					t.Fatal("original subscription closed")
				}
				if got.Metadata["proof"] == id {
					t.Logf("received %s on original subscription", id)
					return
				}
			case <-ctx.Done():
				t.Fatal("original subscription did not receive " + id)
			}
		}
	}
	roundTrip("before-restart")
	stop()
	deadline := time.Now().Add(5 * time.Second)
	for connection.IsConnected() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if connection.IsConnected() {
		t.Fatal("disconnect was not observed")
	}
	start()
	deadline = time.Now().Add(10 * time.Second)
	for !connection.IsConnected() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !connection.IsConnected() || bus.conn != connection {
		t.Fatal("original connection did not reconnect")
	}
	roundTrip("after-restart")
}
