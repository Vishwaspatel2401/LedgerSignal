package worker

import (
	"context"
	"errors"
	"sync/atomic" // lets multiple goroutines safely increment one counter at once
	"testing"
	"time"
)

// waitForCalls reads from `calls` exactly `want` times, failing the test if
// that many calls don't arrive within `timeout`. This is how we "wait" for
// background goroutines to finish their work in a test, without an arbitrary
// sleep that could be too short (flaky) or too long (slow test suite).
func waitForCalls(t *testing.T, calls <-chan struct{}, want int, timeout time.Duration) {
	t.Helper()
	for i := 0; i < want; i++ {
		select {
		case <-calls:
			// Got one — loop again until we've seen `want` total.
		case <-time.After(timeout):
			t.Fatalf("timed out waiting for call %d/%d", i+1, want)
		}
	}
}

// assertNoMoreCalls checks that the handler does NOT get called again within
// a short window — this is what catches bugs like "retried one time too
// many" or "kept retrying after success," which waitForCalls alone wouldn't.
func assertNoMoreCalls(t *testing.T, calls <-chan struct{}, wait time.Duration) {
	t.Helper()
	select {
	case <-calls:
		t.Fatal("handler was called more times than expected")
	case <-time.After(wait):
		// Nothing arrived in time — that's the successful case here.
	}
}

// TestPoolProcessesJobSuccessfully is the simplest case: a handler that
// always succeeds should be called exactly once per job, never retried.
func TestPoolProcessesJobSuccessfully(t *testing.T) {
	calls := make(chan struct{}, 10)

	handler := func(ctx context.Context, job Job) error {
		calls <- struct{}{}
		return nil // success — no retry should follow
	}

	// 1 worker, buffer of 1, up to 3 attempts, 1ms base delay — tiny delay
	// keeps this test fast; the actual retry math is identical to production,
	// just compressed in time.
	pool := NewPool(1, 1, 3, time.Millisecond, handler)
	pool.Enqueue(Job{ItemID: "item-1"})

	waitForCalls(t, calls, 1, time.Second)
	assertNoMoreCalls(t, calls, 100*time.Millisecond)
}

// TestPoolRetriesOnFailureThenSucceeds simulates a job that fails twice, then
// succeeds on the third attempt — confirming retries actually happen, and
// stop as soon as the handler finally succeeds.
func TestPoolRetriesOnFailureThenSucceeds(t *testing.T) {
	var attempt int32 // shared across goroutines, so it must be modified atomically
	calls := make(chan struct{}, 10)

	handler := func(ctx context.Context, job Job) error {
		n := atomic.AddInt32(&attempt, 1) // safely increment and read the new value
		calls <- struct{}{}
		if n < 3 {
			return errors.New("simulated failure")
		}
		return nil // succeeds on the 3rd attempt
	}

	pool := NewPool(1, 1, 3, time.Millisecond, handler)
	pool.Enqueue(Job{ItemID: "item-2"})

	waitForCalls(t, calls, 3, time.Second)
	assertNoMoreCalls(t, calls, 100*time.Millisecond)
}

// TestPoolGivesUpAfterMaxAttempts simulates a job that never succeeds —
// confirming the pool stops after exactly maxAttempts tries instead of
// retrying forever.
func TestPoolGivesUpAfterMaxAttempts(t *testing.T) {
	calls := make(chan struct{}, 10)

	handler := func(ctx context.Context, job Job) error {
		calls <- struct{}{}
		return errors.New("always fails")
	}

	pool := NewPool(1, 1, 3, time.Millisecond, handler)
	pool.Enqueue(Job{ItemID: "item-3"})

	waitForCalls(t, calls, 3, time.Second)
	assertNoMoreCalls(t, calls, 100*time.Millisecond)
}
