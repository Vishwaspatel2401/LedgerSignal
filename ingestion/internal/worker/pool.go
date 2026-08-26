// Package worker implements a small, generic goroutine worker pool.
// It knows nothing about Plaid, Postgres, or HTTP — it only knows how to run
// jobs concurrently, with retries, using whatever function it's handed at
// creation time. That's deliberate: keeping this package dependency-free means
// it can be reused for any future kind of background work, not just transaction syncs.
package worker

import (
	"context"
	"log"
	"time"
)

// Job is one unit of background work. Right now it only needs to carry an
// item_id, since that's all our sync logic needs to know which account to
// process — but this struct is where you'd add more fields later if a
// different kind of job needed more context.
type Job struct {
	ItemID string
}

// HandlerFunc is the shape of function that knows how to actually process one
// Job. The pool itself never defines what "processing" means — whoever creates
// the pool passes in a HandlerFunc, and the pool just calls it for every job
// that comes through. This is what keeps `worker` decoupled from Plaid/Postgres.
type HandlerFunc func(ctx context.Context, job Job) error

// Pool is a fixed number of goroutines ("workers") that all pull jobs off one
// shared channel and process them concurrently, retrying failures with
// increasing delay between attempts.
type Pool struct {
	jobs        chan Job      // the shared queue every worker reads from
	handler     HandlerFunc   // what to do with each job
	maxAttempts int           // total tries per job before giving up (includes the first try)
	baseDelay   time.Duration // starting delay between retries; doubles each attempt
}

// NewPool creates a pool and immediately starts `workerCount` goroutines running
// in the background. `bufferSize` sets how many jobs can wait in the channel at
// once before Enqueue starts blocking the caller. `maxAttempts` and `baseDelay`
// control retry behavior — see runWorker/processWithRetry below.
func NewPool(workerCount, bufferSize, maxAttempts int, baseDelay time.Duration, handler HandlerFunc) *Pool {
	p := &Pool{
		// make(chan Job, bufferSize) creates a "buffered channel" — a thread-safe
		// queue that can hold up to `bufferSize` jobs before Enqueue has to wait.
		jobs:        make(chan Job, bufferSize),
		handler:     handler,
		maxAttempts: maxAttempts,
		baseDelay:   baseDelay,
	}

	// Start `workerCount` goroutines. The `go` keyword is what actually launches
	// a goroutine — it means "run this function concurrently, don't wait for it
	// to finish before continuing." All of these start immediately and run for
	// as long as the program does.
	for i := 0; i < workerCount; i++ {
		go p.runWorker(i)
	}

	return p
}

// runWorker is the loop each worker goroutine runs forever.
func (p *Pool) runWorker(id int) {
	// `for job := range p.jobs` is Go's way of looping over a channel: it blocks
	// (uses no CPU) whenever the channel is empty, and wakes up automatically the
	// instant a new Job is enqueued. Multiple workers ranging over the SAME channel
	// is exactly what makes this a "pool" — Go guarantees each job only ever goes
	// to exactly one worker, so work is naturally spread across whichever workers
	// happen to be free, with no extra coordination code needed from us.
	for job := range p.jobs {
		p.processWithRetry(id, job)
	}
}

// processWithRetry runs the handler for one job, retrying on failure with
// exponentially increasing delay between attempts (baseDelay, then 2x, 4x, ...).
// Note: this worker goroutine is blocked (via time.Sleep) for the entire retry
// process — with a small pool (3 workers), one job stuck retrying temporarily
// reduces how many workers are free to pick up other jobs. That's an accepted
// tradeoff for now: simple and correct, at the cost of some concurrency during
// a retry storm. A more advanced version would re-enqueue the job with a delay
// instead of sleeping in place, freeing the worker immediately — worth
// revisiting if retries become frequent enough to matter in practice.
func (p *Pool) processWithRetry(id int, job Job) {
	ctx := context.Background()

	var err error
	for attempt := 1; attempt <= p.maxAttempts; attempt++ {
		err = p.handler(ctx, job)
		if err == nil {
			// Success — nothing more to do for this job.
			return
		}

		log.Printf("worker %d: job for item_id=%s failed (attempt %d/%d): %v",
			id, job.ItemID, attempt, p.maxAttempts, err)

		if attempt < p.maxAttempts {
			// Exponential backoff: baseDelay * 2^(attempt-1) — so with a 2s
			// base, attempts wait 2s, then 4s, then 8s, etc. before retrying.
			// `1<<uint(attempt-1)` is a bit shift, a fast way to compute
			// powers of two (1, 2, 4, 8, ...).
			backoff := p.baseDelay * time.Duration(1<<uint(attempt-1))
			time.Sleep(backoff)
		}
	}

	// Every attempt failed — log it clearly as permanently failed, so it's
	// visible in logs rather than silently disappearing. There's no dead-letter
	// queue yet (that's a natural fit for Kafka in Phase 4) — for now, a
	// permanently failed job is simply lost after this point.
	log.Printf("worker %d: job for item_id=%s permanently failed after %d attempts: %v",
		id, job.ItemID, p.maxAttempts, err)
}

// Enqueue adds a Job to the queue for some worker to pick up.
// `p.jobs <- job` is channel "send" syntax — it places `job` onto the channel.
// If the channel's buffer is already full, this line blocks (waits) until a
// worker frees up space, rather than growing memory without limit.
func (p *Pool) Enqueue(job Job) {
	p.jobs <- job
}
