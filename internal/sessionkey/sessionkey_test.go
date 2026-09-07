package sessionkey

import (
	"strings"
	"testing"
)

func TestHash(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{name: "empty", in: ""},
		{name: "short", in: "x"},
		{name: "channel-key", in: "chan:slack:C123:1700000000.000200"},
		{name: "long", in: strings.Repeat("a", 1024)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Hash(tt.in)
			if len(got) != 16 {
				t.Fatalf("Hash(%q) length = %d, want 16", tt.in, len(got))
			}
			// Determinism.
			if again := Hash(tt.in); again != got {
				t.Fatalf("Hash(%q) not deterministic: %q vs %q", tt.in, got, again)
			}
			// Hex only.
			for _, r := range got {
				if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
					t.Fatalf("Hash(%q) = %q contains non-hex rune %q", tt.in, got, r)
				}
			}
		})
	}

	if Hash("a") == Hash("b") {
		t.Fatal("Hash collision on trivial inputs")
	}
}

func TestForChannel(t *testing.T) {
	tests := []struct {
		name     string
		channel  string
		chatID   string
		threadID string
		want     string
	}{
		{name: "threaded", channel: "slack", chatID: "C123", threadID: "1700000000.000200",
			want: "chan:slack:C123:1700000000.000200"},
		{name: "unthreaded", channel: "slack", chatID: "C123", threadID: "",
			want: "chan:slack:C123"},
		{name: "telegram-chat", channel: "telegram", chatID: "-100123", threadID: "",
			want: "chan:telegram:-100123"},
		{name: "telegram-topic", channel: "telegram", chatID: "-100123", threadID: "42",
			want: "chan:telegram:-100123:42"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ForChannel(tt.channel, tt.chatID, tt.threadID); got != tt.want {
				t.Errorf("ForChannel = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestForSchedule(t *testing.T) {
	if got, want := ForSchedule("heartbeat"), "sched:heartbeat"; got != want {
		t.Errorf("ForSchedule = %q, want %q", got, want)
	}
	if got, want := ForScheduleRun("heartbeat", "heartbeat-7"), "sched:heartbeat:run:heartbeat-7"; got != want {
		t.Errorf("ForScheduleRun = %q, want %q", got, want)
	}
}

func TestForWebProxy(t *testing.T) {
	if got, want := ForWebProxy("inst", "abc123"), "web:inst:abc123"; got != want {
		t.Errorf("ForWebProxy = %q, want %q", got, want)
	}
}

func TestForMCP(t *testing.T) {
	if got, want := ForMCP("inst", "mcp-99"), "mcp:inst:mcp-99"; got != want {
		t.Errorf("ForMCP = %q, want %q", got, want)
	}
}

func TestForWebEndpoint(t *testing.T) {
	if got, want := ForWebEndpoint("inst-a"), "web-endpoint:inst-a"; got != want {
		t.Errorf("ForWebEndpoint = %q, want %q", got, want)
	}
	if ForWebEndpoint("a") == ForWebEndpoint("b") {
		t.Error("ForWebEndpoint must differ per instance")
	}
}

func TestForSub(t *testing.T) {
	parent := ForChannelThread("slack", "C1", "1700000000.0")
	child := ForSub(parent, "sub-run-2")
	if !strings.HasPrefix(child, parent+":sub:") {
		t.Errorf("ForSub = %q, expected prefix %q", child, parent+":sub:")
	}
}

func TestForAPIServerDefault(t *testing.T) {
	a := ForAPIServerDefault()
	b := ForAPIServerDefault()
	if a == b {
		t.Fatal("ForAPIServerDefault returned identical keys on consecutive calls")
	}
	if !strings.HasPrefix(a, "adhoc:") {
		t.Errorf("ForAPIServerDefault = %q, expected adhoc: prefix", a)
	}
}
