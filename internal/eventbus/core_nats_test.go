package eventbus

import (
	"context"
	"testing"
	"time"
)

func TestCoreNATSFlushContextHasBoundedDeadline(t *testing.T) {
	ctx, cancel := coreNATSFlushContext(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("flush context must have a deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > coreNATSFlushTimeout {
		t.Fatalf("flush deadline remaining = %s, want within (0, %s]", remaining, coreNATSFlushTimeout)
	}
}

func TestCoreNATSFlushContextPreservesEarlierCallerDeadline(t *testing.T) {
	parent, cancelParent := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelParent()
	ctx, cancel := coreNATSFlushContext(parent)
	defer cancel()

	parentDeadline, _ := parent.Deadline()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("flush context must have a deadline")
	}
	if deadline.After(parentDeadline) {
		t.Fatalf("flush deadline %s must not extend caller deadline %s", deadline, parentDeadline)
	}
}
