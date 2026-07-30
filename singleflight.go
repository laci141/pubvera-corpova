// singleflight.go — one child CLI run per cold key, however many callers want it.
//
// THE PROBLEM THIS SOLVES (the gap a83294a recorded and did not close): the
// cache turns the SECOND request for a key into a 3ms hit, but it does nothing
// for N requests that arrive together while the key is still cold. Every one of
// them missed, so every one of them spawned its own child CLI. Measured on this
// repo's harness before this file existed: 10 concurrent identical requests =
// 10 CLI runs and 10 cache writes. In production one cold run costs 2.25s of
// CPU plus its OpenAlex traffic on the heuristic path and ~120s with an LLM, so
// a link shared to ten readers at once was ten times the work for one answer,
// on one CX23.
//
// WHAT IT DOES: the first caller for a key starts the run; everyone else who
// wants the same key while it is running waits for that run and receives its
// bytes. The key is the cache key, so "same key" already means same endpoint,
// same limit, same normalized claims — and therefore the same argv.
//
// WHAT IT DELIBERATELY DOES NOT DO: it does not cover the BYOK synthesis. Only
// the keyless CLI leg is shared, because only the keyless CLI leg is
// caller-independent. Two callers sharing one paid LLM answer would be exactly
// the cross-tenant leak the cache design avoided; see runCLIJSON, where the
// synthesis stays per request, after this returns.
//
// NO WAITER CAN HANG (the soft-dependency rule, applied to concurrency):
//   - Every waiter selects on its OWN context, so a client that disconnects or
//     times out leaves immediately instead of waiting on the run.
//   - The run has its own deadline (cliFlightTimeout), independent of whoever
//     started it, so a leader that goes away cannot strand the followers and a
//     stuck child cannot hold anyone past that bound.
//   - A failed run delivers its error to every waiter, so failure propagates as
//     fast as success. Nobody waits for a retry that is not coming.
//   - A panic inside the run is recovered and delivered as an error, so a bug
//     here cannot turn into a hang either.
//   - When the LAST waiter leaves, the run is cancelled: work nobody will read
//     is stopped rather than left burning a core.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// cliFlightTimeout bounds one shared CLI run. It matches the per-request budget
// in runCLIJSON, but is deliberately NOT taken from the caller's context: the
// run belongs to every waiter, not to whichever request happened to arrive
// first, so it must not inherit that request's remaining time or cancellation.
const cliFlightTimeout = 120 * time.Second

// cliFlight is the process-wide group for the CLI leg. Tests replace it the way
// they replace cacheStore.
var cliFlight = newFlightGroup()

type flightGroup struct {
	mu    sync.Mutex
	calls map[string]*flightCall
}

func newFlightGroup() *flightGroup {
	return &flightGroup{calls: make(map[string]*flightCall)}
}

// flightCall is one in-progress run plus its result. raw and err are written
// once, before done is closed, and read only after — the channel close is the
// happens-before edge that makes them safe without a lock.
type flightCall struct {
	done chan struct{}
	raw  []byte
	err  error

	// waiters is the number of callers currently blocked on this call, guarded
	// by flightGroup.mu. It reaches zero only when nobody is left to receive the
	// result, which is when cancel is called.
	waiters int
	cancel  context.CancelFunc
}

// Do runs fn once per key and gives its result to every caller that asked for
// that key while it was running. joined reports whether this caller attached to
// an already-running call (false means this caller started it).
//
// An empty key opts out entirely: the caller runs fn on its own context, which
// is the behaviour every uncached path had before this file existed.
func (g *flightGroup) Do(ctx context.Context, key string, fn func(context.Context) ([]byte, error)) (raw []byte, joined bool, err error) {
	if key == "" {
		out, ferr := fn(ctx)
		return out, false, ferr
	}

	g.mu.Lock()
	c, joined := g.calls[key]
	if !joined {
		// The run's context is derived from the caller's for its values, but
		// explicitly NOT for its cancellation or deadline — see cliFlightTimeout.
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cliFlightTimeout)
		c = &flightCall{done: make(chan struct{}), cancel: cancel}
		g.calls[key] = c
		go g.run(key, c, runCtx, cancel, fn)
	}
	c.waiters++
	waiters := c.waiters
	g.mu.Unlock()

	if joined {
		log.Printf("single-flight: %s joined an in-flight CLI run (waiters=%d)", key, waiters)
	}
	defer g.leave(key, c)

	select {
	case <-c.done:
		return c.raw, joined, c.err
	case <-ctx.Done():
		// This caller gave up; the run continues for whoever is still waiting.
		return nil, joined, ctx.Err()
	}
}

// run executes fn and publishes its result exactly once.
func (g *flightGroup) run(key string, c *flightCall, runCtx context.Context, cancel context.CancelFunc, fn func(context.Context) ([]byte, error)) {
	var raw []byte
	var err error
	func() {
		// A panic in the run must become an error, not a permanent block on
		// every waiter — the one failure mode a channel handshake cannot
		// recover from by itself.
		defer func() {
			if r := recover(); r != nil {
				err = &cliError{status: http.StatusBadGateway, msg: fmt.Sprintf("CLI run panicked: %v", r)}
			}
		}()
		raw, err = fn(runCtx)
	}()
	c.raw, c.err = raw, err
	// Forget BEFORE publishing, so a request arriving now starts a fresh run
	// instead of attaching to one that is already finished.
	g.forget(key, c)
	close(c.done)
	cancel()
}

// leave drops one waiter and, when that was the last one, stops the run.
func (g *flightGroup) leave(key string, c *flightCall) {
	g.mu.Lock()
	c.waiters--
	abandoned := c.waiters == 0
	g.mu.Unlock()
	if !abandoned {
		return
	}
	// Every caller has gone. Cancelling kills the child process instead of
	// leaving it to finish an answer nobody will read.
	if g.forget(key, c) {
		log.Printf("single-flight: %s abandoned by every caller, run cancelled", key)
	}
	c.cancel()
}

// forget removes key from the map, but only when it still maps to c. The
// pointer check is what stops a finished call from deleting the entry of a
// newer run that reused the same key. It reports whether it removed anything.
func (g *flightGroup) forget(key string, c *flightCall) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if cur, ok := g.calls[key]; ok && cur == c {
		delete(g.calls, key)
		return true
	}
	return false
}

// inFlight reports how many runs are currently tracked. Tests only.
func (g *flightGroup) inFlight() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

// waitersFor reports how many callers are currently blocked on key. Tests only:
// it is what lets a concurrency test wait for a precise state instead of
// sleeping and hoping, so the tests below assert without a timing race.
func (g *flightGroup) waitersFor(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c, ok := g.calls[key]; ok {
		return c.waiters
	}
	return 0
}
