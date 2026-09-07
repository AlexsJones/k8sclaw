// Package sessionkey builds canonical AgentRun session keys and short
// deterministic hashes derived from them.
//
// AgentRun.spec.sessionKey is free-form and remains so — nothing in this
// package is enforced by webhook or CRD validation. Callers that create
// AgentRuns from a known source (channel message, schedule, web proxy
// request, sub-agent spawn) SHOULD use the constructors here so that
// session keys are stable per logical conversation and consistent across
// the codebase. Hash is used to derive bounded-length identifiers (PVC
// names, label values) from an arbitrary session key.
package sessionkey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Hash returns a 16-character hex digest of key (first 8 bytes of SHA-256).
// Suitable for use as a DNS-1123 label component when the original key may
// be too long, contain disallowed characters, or vary in format. Collision
// space is 2^64 — adequate for per-agent/per-namespace identifier suffixes.
func Hash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// ForChannel returns the session key for an inbound channel message. When
// threadID is non-empty the session is scoped to the thread; otherwise it
// is scoped to the chat/channel as a whole. This is the threading-off
// semantic: the whole channel becomes one continuous session.
func ForChannel(channelType, chatID, threadID string) string {
	if threadID != "" {
		return ForChannelThread(channelType, chatID, threadID)
	}
	return ForChannelChat(channelType, chatID)
}

// ForChannelThread returns the session key for a threaded conversation
// within a channel (e.g. a Slack thread anchored at threadTS).
func ForChannelThread(channelType, chatID, threadID string) string {
	return fmt.Sprintf("chan:%s:%s:%s", channelType, chatID, threadID)
}

// ForChannelChat returns the session key for an entire chat/channel/DM
// when no threading is in use.
func ForChannelChat(channelType, chatID string) string {
	return fmt.Sprintf("chan:%s:%s", channelType, chatID)
}

// ForSchedule returns the session key shared across all runs of a single
// Schedule. Heartbeat/recurring runs see the same memory slice, so the
// agent can build state over time ("last time I noticed X…").
func ForSchedule(scheduleName string) string {
	return fmt.Sprintf("sched:%s", scheduleName)
}

// ForScheduleRun returns a session key unique to one run of a Schedule.
// Use when each invocation should be isolated (fire-and-forget sweeps
// that must not accumulate state).
func ForScheduleRun(scheduleName, runName string) string {
	return fmt.Sprintf("sched:%s:run:%s", scheduleName, runName)
}

// ForWebProxy returns the session key for an OpenAI-compatible request
// served by the web proxy. requestHash is the proxy's idempotency hash;
// retries of the same logical request share a session.
func ForWebProxy(instance, requestHash string) string {
	return fmt.Sprintf("web:%s:%s", instance, requestHash)
}

// ForMCP returns the session key for an MCP SSE connection.
func ForMCP(instance, sessionID string) string {
	return fmt.Sprintf("mcp:%s:%s", instance, sessionID)
}

// ForWebEndpoint returns the session key for the long-lived server-mode
// AgentRun that backs an Agent's web-endpoint skill. Per-instance so two
// Agents using the same skill do not share memory.
func ForWebEndpoint(instance string) string {
	return fmt.Sprintf("web-endpoint:%s", instance)
}

// ForSub returns the session key for a sub-agent spawned by a parent
// AgentRun. The format preserves the parent key as a prefix so subtree
// traversal by string match is straightforward.
func ForSub(parentSessionKey, childRunName string) string {
	return fmt.Sprintf("%s:sub:%s", parentSessionKey, childRunName)
}

// ForAPIServerDefault returns a session key for ad-hoc API server runs
// where the caller did not supply one. Uses crypto/rand so concurrent
// requests cannot collide.
func ForAPIServerDefault() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// rand.Read on a healthy system does not fail; if it does, fall
		// back to a fixed prefix — the caller will still get a unique
		// key via GenerateName on the AgentRun itself.
		return "adhoc:nonce-unavailable"
	}
	return "adhoc:" + hex.EncodeToString(buf[:])
}
