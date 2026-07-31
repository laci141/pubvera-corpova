package main

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// defaultCLISlots is how many child CLI processes may run at once.
//
// Four, and the number is measured rather than chosen for roundness. The
// server is a two-core CX23; one CLI run peaks at ~12% of one core, so
// arithmetic alone suggests saturation near sixteen concurrent runs. Sixteen
// is the SATURATION point, not the SAFE one: the other seven apps share the
// same two cores, and a saturated host makes Docker's healthcheck queue behind
// the work it is supposed to be checking. Three failed checks mark a container
// unhealthy — today only a label, but a label that monitoring will eventually
// act on.
//
// The cost of NOT bounding this was measured too. With a hundred concurrent
// distinct keys and no limit, the median request paid +1.14s and the slowest
// +1.20s purely to contention — on a machine with MORE cores than the CX23 and
// against a stub that performs no real work. That figure is a floor.
const defaultCLISlots = 4

// cliSlotWait is how long a request waits for a free slot before giving up.
//
// Unlike the slot count, this number is NOT measured — it is a judgement, and
// saying so is the point. It trades a little latency for a lot of fairness: a
// brief queue absorbs bursts, while an unbounded one would let a request sit
// until its own 120s budget expired and then fail anyway, having held a
// connection the whole time. Three seconds is short enough that a caller
// learns quickly and long enough that a normal burst never sees a 503.
//
// A var, not a const, so tests can shorten it.
var cliSlotWait = 3 * time.Second

// cliSlotRetryAfter is the Retry-After value sent with a 503, in seconds.
const cliSlotRetryAfter = 30

// cliSem bounds child-process concurrency for the whole server.
var cliSem = newCLISemaphore(cliSlotsFromEnv())

// cliSlotsFromEnv reads CLI_MAX_CONCURRENT. Zero or a negative value disables
// the bound entirely, which is the pre-semaphore behaviour and therefore the
// safe escape hatch if this ever turns out to be wrong in production.
func cliSlotsFromEnv() int {
	raw := strings.TrimSpace(os.Getenv("CLI_MAX_CONCURRENT"))
	if raw == "" {
		return defaultCLISlots
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return defaultCLISlots
	}
	return n
}

// cliSemaphore is a counting semaphore over child CLI processes.
//
// A buffered channel rather than sync.Mutex bookkeeping, because acquisition
// has to be selectable against a timeout and a context at the same time, and a
// channel send is the only primitive that composes with select.
//
// FUTURE, AND DELIBERATELY NOT BUILT YET. Once requests carry an identity
// (Supabase), admin and paid tiers should not queue behind free ones. The
// intended shape is a RESERVED SLOT: of the four, one is reachable only by
// admin and Max. That keeps the call site in runCLIRaw unchanged — acquire
// simply learns a tier argument and picks between two channels.
//
// What should NOT be built is a strict priority queue. It starves: with paid
// traffic arriving steadily, a free request never reaches the head of the
// queue and eventually takes the 503 it was waiting to avoid. A reserved slot
// bounds the harm instead — paid callers never face a full queue, free callers
// still contend for the remaining three and are never locked out.
//
// This is a guard for future load, not a present need: at today's ~1600
// requests/day the queue is almost never non-empty. Build it when there is
// traffic to measure it against.
type cliSemaphore struct {
	ch chan struct{}
}

// newCLISemaphore returns a semaphore of n slots. n <= 0 yields a disabled
// semaphore whose acquire always succeeds immediately.
func newCLISemaphore(n int) *cliSemaphore {
	if n <= 0 {
		return &cliSemaphore{}
	}
	return &cliSemaphore{ch: make(chan struct{}, n)}
}

// acquire takes a slot, waiting up to cliSlotWait for one.
//
// Three outcomes, and each returns something the caller already knows how to
// handle. A free slot returns nil. Exhaustion past the grace period returns a
// *cliError carrying 503 and a Retry-After, which travels the same path as the
// 502 and 504 the CLI leg already produces. A cancelled context returns its own
// error, so a client that went away is not reported as an overload.
func (s *cliSemaphore) acquire(ctx context.Context) error {
	if s == nil || s.ch == nil {
		return nil
	}

	// Fast path: a free slot costs no timer and no second select.
	select {
	case s.ch <- struct{}{}:
		return nil
	default:
	}

	t := time.NewTimer(cliSlotWait)
	defer t.Stop()

	select {
	case s.ch <- struct{}{}:
		return nil
	case <-t.C:
		return &cliError{
			status:     http.StatusServiceUnavailable,
			retryAfter: cliSlotRetryAfter,
			msg:        "server is busy running other analyses; please retry shortly",
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release returns a slot. Safe on a disabled semaphore, so callers can defer it
// unconditionally.
func (s *cliSemaphore) release() {
	if s == nil || s.ch == nil {
		return
	}
	select {
	case <-s.ch:
	default:
	}
}

// inUse reports how many slots are currently held. For logging and tests only;
// the value is a snapshot and is stale the moment it is read.
func (s *cliSemaphore) inUse() int {
	if s == nil || s.ch == nil {
		return 0
	}
	return len(s.ch)
}

// capacity reports the configured slot count, 0 when the bound is disabled.
func (s *cliSemaphore) capacity() int {
	if s == nil || s.ch == nil {
		return 0
	}
	return cap(s.ch)
}
