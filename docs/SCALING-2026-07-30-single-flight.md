# Single-flight scaling measurement — 2026-07-30

Measured on the single-flight implementation committed in `c7fba01`. **No code
changed for these numbers.** The one exception is stated at the bottom
(a temporary goroutine counter, removed again before this file was written).

Everything below ran **locally on the Windows dev box**. Nothing was run on the
CX23 server.

## Method, and its limits

Harness: the real server binary, a stub Redis started EMPTY, a counting wrapper
in front of the child CLI (`$CLI_BIN` → `clicount` → `$REAL_CLI`), and a stub
child that sleeps a fixed time and stamps a unique `run_id` into its output.
Identical `run_id`s across responses are what make "one run" a fact rather than
an inference. A load generator releases all N requests from one barrier.

**Limit to keep in mind when reading these numbers: the load generator and the
server share this machine and its cores.** The generator therefore reports two
separate figures — `dispatch` (how long after the barrier each goroutine
actually issued its request, i.e. client-side spread) and `latency` (the request
itself). Where dispatch stays flat while latency moves, the server is the
subject; where dispatch moves too, the box is. In every measurement below except
the deliberately staggered one, dispatch stayed at 0–2 ms, so the latencies are
about the server.

Second limit: the harness spawns **two** processes per CLI run (the counting
wrapper plus the stub child), where production spawns one. Process-creation
costs in test B are therefore overstated relative to production — while the real
CLI's own CPU and OpenAlex work, which the stub does not do at all, is
understated by far more.

## A) Same key — the structural guarantee

| N | CLI runs | Redis SET | distinct bodies | latency min / med / p95 / max | goroutine peak |
|---:|---:|---:|---:|---|---:|
| 100 | **1** | **1** | **1** | 2.052 / 2.056 / 2.057 / 2.057 s | 206 |
| 1000 (staggered) | **1** | **1** | **1** | 9.778 / 10.155 / 10.529 / 10.531 s | 2006 |

At N=100 the slowest response is **5 ms** behind the fastest, across 100
callers woken from one run. That is the number the measurement was for: waking
the waiters does not serialize. (The wake path is a single `close(done)`; every
waiter is already parked in its own `select`.)

The N=1000 row used a 10 s child so that all 1000 requests fell inside one run
window — see the next section for why. Its latency spread is fully explained by
the deliberate dispatch stagger: latency + dispatch ≈ 10.53 s for every one of
the 1000, i.e. they all completed within ~23 ms of each other in absolute time.
Waiters: 999 joins + 1 leader = 1000, and `runs-counter = 1`.

### Why N=1000 needed staggering, and what it is NOT

Fired all at once, N=1000 produced 501 failures. Those failures are **not the
server failing requests** — they never reached it:

```
Post "http://127.0.0.1:8894/api/consensus": dial tcp 127.0.0.1:8894:
connectex: No connection could be made because the target machine actively refused it.
```

That is `WSAECONNREFUSED` at connect: the OS rejected the TCP connection because
the listen/accept queue was full. Staged runs locate the onset precisely:

| N fired at once | connected & served | refused at connect |
|---:|---:|---:|
| 100 | 100 | 0 |
| 200 | 200 | 0 |
| 300 | 274 | 26 |
| 500 | 352 | 148 |
| 700 | 377 | 323 |
| 1000 | 499 | 501 |

**This is not ephemeral-port / TIME_WAIT exhaustion.** That hypothesis was
tested and rejected: the error would be `WSAEADDRINUSE`, not "actively refused";
`netstat` showed **one** TIME_WAIT socket on the port straight after the bursts;
and N=200 ran with zero errors immediately *after* several 1000-request bursts,
which port exhaustion could not survive. Refusals also resolve in 8–38 ms (an
instant RST) while accepted requests take the full ~2.06 s.

Nor is it a client-side stall: `dispatch` max was 13 ms at N=1000.

The honest reading is: **on this box, ~200 simultaneous connection attempts is
what the loopback listener admits before the accept queue overflows; beyond
that, the excess is refused at the TCP layer.** It is a property of this
Windows machine's socket queue, not of the server logic, and it does not
transfer to the Linux CX23, whose backlog defaults differ.

What the guarantee did on every single burst, including the refused ones:

| burst | HTTP 200 | `joined` log lines | joins + 1 | CLI runs |
|---|---:|---:|---:|---:|
| N=1000 (a) | 445 | 444 | **445** | **1** |
| N=1000 (b) | 499 | 498 | **499** | **1** |
| N=700 | 377 | 376 | **377** | **1** |
| N=500 | 352 | 351 | **352** | **1** |
| N=300 | 274 | 273 | **274** | **1** |
| N=200 | 200 | 199 | **200** | **1** |
| N=100 | 100 | 99 | **100** | **1** |

Every request that reached the handler is accounted for as either the leader or
a joiner, and the run count is 1 regardless. Single-flight held at 499
concurrent handler entries before staggering, and at 1000 after.

## B) Distinct keys — the case single-flight deliberately does not help

100 requests, 100 different claims, cold cache:

| metric | value |
|---|---|
| CLI runs | **100** (as designed — no false sharing) |
| Redis SET | **100** |
| distinct bodies | 100 |
| errors | 0 |
| wall | **3.195 s** |
| latency min / med / p95 / max | **2.633 / 3.142 / 3.187 / 3.195 s** |
| dispatch max | 1 ms |
| goroutine peak | 602 |

**This is the number that motivates a concurrency limit.** The child sleeps a
fixed 2.0 s, so everything above 2.0 s is contention: the median request paid
**+1.14 s** and the slowest **+1.20 s** purely because 100 runs (200 processes,
per the harness caveat) started at once. Nothing here is queued or bounded —
100 concurrent requests for 100 cold keys means 100 concurrent child processes,
on a box with more cores than the CX23's two, running a stub that does no work.

Production shape for comparison: one real cold run costs 2.25 s of CPU plus its
OpenAlex traffic on the heuristic path, ~120 s with an LLM. The measured
degradation above is therefore a floor, not an estimate.

**Reference point for the next step (semaphore / concurrency cap):** 100
concurrent distinct keys, no cap, median 3.142 s against a 2.0 s floor,
goroutine peak 602, 100 simultaneous child processes.

## C) Mixed — proof that the isolation is per key

100 requests: 50 for one shared claim, 50 distinct.

| metric | value |
|---|---|
| CLI runs | **51** |
| Redis SET | **51** |
| distinct bodies overall | 51 |
| distinct bodies among the 50 shared-claim requests | **1** |
| errors | 0 |
| wall | 2.770 s |
| latency min / med / p95 / max | 2.346 / 2.347 / 2.765 / 2.770 s |
| goroutine peak | 406 |

51 = 50 distinct claims each running once, plus one run shared by 50 callers.
The 50 sharers got byte-identical responses; the 50 distinct callers did not
wait on each other or on the shared run. Sharing is per key, and only per key.

## Temporary instrumentation, and its removal

The goroutine peaks came from a temporary `/debug/goroutines` endpoint plus a
2 ms in-process sampler added to `main.go` for these runs — sampled inside the
process rather than polled from outside, so the measurement did not add the load
it was measuring. It was **removed immediately afterwards**: `main.go` is back to
its `c7fba01` content (`git diff` against HEAD is empty, and the file contains no
occurrence of `debug/goroutines`, `sampleGoroutines`, `goroutinePeak` or
`sync/atomic`). `go build ./...`, `go vet ./...` and `gofmt -l` were re-run clean
after the revert. The harness itself (counting wrapper, stub CLI, stub Redis,
load generator) lives outside the repo and was never committed.
