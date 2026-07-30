// singleflight_test.go — the guarantees of the shared CLI run.
//
// The group tests below never sleep to "let things settle": they wait for an
// exact number of waiters (awaitWaiters) and then act, so every assertion is
// about a state the test knows it is in. The handler tests at the bottom are
// the same claim end to end, through the real HTTP handler and a real child
// process.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// awaitWaiters blocks until exactly want callers are waiting on key, or fails
// the test. This is the barrier that makes the concurrency tests deterministic.
func awaitWaiters(t *testing.T, g *flightGroup, key string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if g.waitersFor(key) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d waiters on %q (have %d)", want, key, g.waitersFor(key))
}

// TestFlightCollapsesConcurrentCallersIntoOneRun is the whole point of the
// file: N callers, one run, everyone gets the same bytes.
func TestFlightCollapsesConcurrentCallersIntoOneRun(t *testing.T) {
	const n = 10
	g := newFlightGroup()
	release := make(chan struct{})
	var runs atomic.Int64

	fn := func(ctx context.Context) ([]byte, error) {
		runs.Add(1)
		<-release
		return []byte(`{"payload":"from the one run"}`), nil
	}

	raws := make([][]byte, n)
	joined := make([]bool, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raws[i], joined[i], errs[i] = g.Do(context.Background(), "same-key", fn)
		}(i)
	}

	awaitWaiters(t, g, "same-key", n)
	close(release)
	wg.Wait()

	if got := runs.Load(); got != 1 {
		t.Errorf("fn ran %d times, want 1", got)
	}
	leaders := 0
	for i := range n {
		if errs[i] != nil {
			t.Errorf("caller %d: unexpected error %v", i, errs[i])
		}
		if string(raws[i]) != `{"payload":"from the one run"}` {
			t.Errorf("caller %d got %q, want the shared payload", i, raws[i])
		}
		if !joined[i] {
			leaders++
		}
	}
	if leaders != 1 {
		t.Errorf("%d callers reported starting the run, want exactly 1", leaders)
	}
	if g.inFlight() != 0 {
		t.Errorf("group still tracks %d calls after everyone finished", g.inFlight())
	}
}

// TestFlightSeparatesDifferentKeys: sharing must be per key. Two different
// queries running at the same time are two runs, not one.
func TestFlightSeparatesDifferentKeys(t *testing.T) {
	g := newFlightGroup()
	release := make(chan struct{})
	var runs atomic.Int64

	fn := func(key string) func(context.Context) ([]byte, error) {
		return func(ctx context.Context) ([]byte, error) {
			runs.Add(1)
			<-release
			return []byte(key), nil
		}
	}

	var wg sync.WaitGroup
	got := make([][]byte, 2)
	for i, key := range []string{"key-a", "key-b"} {
		wg.Add(1)
		go func(i int, key string) {
			defer wg.Done()
			got[i], _, _ = g.Do(context.Background(), key, fn(key))
		}(i, key)
	}
	awaitWaiters(t, g, "key-a", 1)
	awaitWaiters(t, g, "key-b", 1)
	close(release)
	wg.Wait()

	if n := runs.Load(); n != 2 {
		t.Errorf("fn ran %d times for two distinct keys, want 2", n)
	}
	if string(got[0]) != "key-a" || string(got[1]) != "key-b" {
		t.Errorf("results crossed between keys: %q and %q", got[0], got[1])
	}
}

// TestFlightFailureReachesEveryWaiter is the soft-dependency rule as it applies
// here: a failed run must fail its waiters immediately. Nobody may hang waiting
// for a retry that is not coming.
func TestFlightFailureReachesEveryWaiter(t *testing.T) {
	const n = 5
	g := newFlightGroup()
	release := make(chan struct{})
	want := &cliError{status: http.StatusBadGateway, msg: "CLI failed: boom"}

	fn := func(ctx context.Context) ([]byte, error) {
		<-release
		return nil, want
	}

	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, errs[i] = g.Do(context.Background(), "failing-key", fn)
		}(i)
	}
	awaitWaiters(t, g, "failing-key", n)
	close(release)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("waiters hung after the run failed")
	}

	for i := range n {
		var ce *cliError
		if !errors.As(errs[i], &ce) {
			t.Fatalf("caller %d got %v, want a *cliError", i, errs[i])
		}
		if ce.status != http.StatusBadGateway || !strings.Contains(ce.msg, "boom") {
			t.Errorf("caller %d got status %d msg %q", i, ce.status, ce.msg)
		}
	}
	if g.inFlight() != 0 {
		t.Errorf("failed call was not forgotten: %d still tracked", g.inFlight())
	}
}

// TestFlightWaiterHonoursItsOwnContext: a caller that gives up must return at
// once, and must not take the run down with it while others are still waiting.
func TestFlightWaiterHonoursItsOwnContext(t *testing.T) {
	g := newFlightGroup()
	release := make(chan struct{})
	fn := func(ctx context.Context) ([]byte, error) {
		<-release
		return []byte(`{"leader":"finished"}`), nil
	}

	leaderDone := make(chan struct{})
	var leaderRaw []byte
	var leaderErr error
	go func() {
		defer close(leaderDone)
		leaderRaw, _, leaderErr = g.Do(context.Background(), "ctx-key", fn)
	}()
	awaitWaiters(t, g, "ctx-key", 1)

	ctx, cancel := context.WithCancel(context.Background())
	followerDone := make(chan error, 1)
	go func() {
		_, _, err := g.Do(ctx, "ctx-key", fn)
		followerDone <- err
	}()
	awaitWaiters(t, g, "ctx-key", 2)

	cancel()
	select {
	case err := <-followerDone:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled follower got %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled follower kept waiting on the leader's run")
	}

	// The run must have survived its follower leaving.
	close(release)
	select {
	case <-leaderDone:
	case <-time.After(5 * time.Second):
		t.Fatal("leader never finished after the follower cancelled")
	}
	if leaderErr != nil {
		t.Errorf("leader failed after an unrelated follower cancelled: %v", leaderErr)
	}
	if string(leaderRaw) != `{"leader":"finished"}` {
		t.Errorf("leader got %q", leaderRaw)
	}
}

// TestFlightCancelsRunAbandonedByEveryCaller: work nobody will read must stop,
// or a disconnected browser tab costs a full CLI run on a one-core box.
func TestFlightCancelsRunAbandonedByEveryCaller(t *testing.T) {
	g := newFlightGroup()
	runCancelled := make(chan struct{})
	fn := func(ctx context.Context) ([]byte, error) {
		<-ctx.Done()
		close(runCancelled)
		return nil, ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _, _, _ = g.Do(ctx, "abandoned-key", fn) }()
	awaitWaiters(t, g, "abandoned-key", 1)

	cancel()
	select {
	case <-runCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("run kept going after its last caller left")
	}
}

// TestFlightPanicBecomesAnError: a panic in the run is the one failure a
// channel handshake cannot recover from by itself — it would close nothing and
// hang every waiter until their own deadline.
func TestFlightPanicBecomesAnError(t *testing.T) {
	g := newFlightGroup()
	fn := func(ctx context.Context) ([]byte, error) { panic("something went very wrong") }

	done := make(chan error, 1)
	go func() {
		_, _, err := g.Do(context.Background(), "panic-key", fn)
		done <- err
	}()

	select {
	case err := <-done:
		var ce *cliError
		if !errors.As(err, &ce) {
			t.Fatalf("got %v, want a *cliError", err)
		}
		if !strings.Contains(ce.msg, "panicked") {
			t.Errorf("error message %q does not mention the panic", ce.msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a panicking run hung its waiter")
	}
	if g.inFlight() != 0 {
		t.Errorf("panicking call was not forgotten: %d still tracked", g.inFlight())
	}
}

// TestFlightEmptyKeyDoesNotShare: an unkeyed call is the pre-single-flight
// path, one run per caller.
func TestFlightEmptyKeyDoesNotShare(t *testing.T) {
	g := newFlightGroup()
	var runs atomic.Int64
	fn := func(ctx context.Context) ([]byte, error) {
		runs.Add(1)
		return []byte(`{}`), nil
	}
	for range 3 {
		if _, joined, err := g.Do(context.Background(), "", fn); err != nil || joined {
			t.Fatalf("unkeyed call: joined=%v err=%v", joined, err)
		}
	}
	if n := runs.Load(); n != 3 {
		t.Errorf("unkeyed calls ran fn %d times, want 3", n)
	}
}

// ---- handler level ------------------------------------------------------------

// buildSlowStubCLI compiles a child CLI that takes its time and stamps a unique
// run_id into its output. The delay is what makes concurrent requests overlap;
// the run_id is the probe — identical response bodies can only come from ONE
// child run.
func buildSlowStubCLI(t *testing.T, counterPath string, delay time.Duration) {
	t.Helper()
	dir := t.TempDir()
	src := `package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func main() {
	if p := os.Getenv("SLOW_STUB_COUNTER"); p != "" {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(f, "run")
			f.Close()
		}
	}
	d, _ := time.ParseDuration(os.Getenv("SLOW_STUB_DELAY"))
	time.Sleep(d)
	if os.Getenv("SLOW_STUB_FAIL") == "1" {
		fmt.Fprintln(os.Stderr, "stub CLI: forced failure")
		os.Exit(3)
	}
	claim := ""
	if len(os.Args) > 2 {
		claim = os.Args[2]
	}
	out, _ := json.Marshal(map[string]any{
		"claim":    claim,
		"verdict":  "evidence-supports",
		"refuting": 0,
		"mixed":    0,
		"run_id":   fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano()),
	})
	fmt.Println(string(out))
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("write stub source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module slowstub\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatalf("write stub go.mod: %v", err)
	}
	bin := filepath.Join(dir, "slowstub")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build slow stub CLI: %v\n%s", err, out)
	}
	t.Setenv("CLI_BIN", bin)
	t.Setenv("SLOW_STUB_COUNTER", counterPath)
	t.Setenv("SLOW_STUB_DELAY", delay.String())
}

// postConsensusWithKey drives the real handler with a BYOK key + provider.
func postConsensusWithKey(t *testing.T, claim, provider, key string) (int, string) {
	t.Helper()
	body := fmt.Sprintf(`{"claim":%q,"limit":10,"provider":%q}`, claim, provider)
	req := httptest.NewRequest(http.MethodPost, "/api/consensus", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-LLM-Key", key)
	}
	rec := httptest.NewRecorder()
	handleConsensus(rec, req)
	return rec.Code, rec.Body.String()
}

// useFakeProvider points one provider name at a local test server.
func useFakeProvider(t *testing.T, name, baseURL string) {
	t.Helper()
	prev, existed := providers[name]
	providers[name] = providerSpec{BaseURL: baseURL, DefaultModel: "test-model", Style: styleOpenAI}
	t.Cleanup(func() {
		if existed {
			providers[name] = prev
			return
		}
		delete(providers, name)
	})
}

// TestHandlerSingleFlightRunsOneCLIForConcurrentRequests is the production
// measurement as a test: 10 concurrent requests for one cold key must produce
// one child run, one cache write, and ten byte-identical responses.
func TestHandlerSingleFlightRunsOneCLIForConcurrentRequests(t *testing.T) {
	const n = 10
	counter := filepath.Join(t.TempDir(), "runs.txt")
	buildSlowStubCLI(t, counter, 500*time.Millisecond)
	stub := newStubRedis(t, "ok", "")
	useCache(t, &redisCache{addr: stub.addr()})

	statuses := make([]int, n)
	bodies := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i], bodies[i] = postConsensus(t, "concurrent cold claim")
		}(i)
	}
	wg.Wait()
	cacheStore.waitForWrites()

	if got := cliRunCount(t, counter); got != 1 {
		t.Errorf("CLI ran %d times for %d concurrent identical requests, want 1", got, n)
	}
	for i := range n {
		if statuses[i] != http.StatusOK {
			t.Fatalf("request %d: status %d, body %s", i, statuses[i], bodies[i])
		}
		if bodies[i] != bodies[0] {
			t.Errorf("request %d got a different body than request 0:\n%s\n%s", i, bodies[i], bodies[0])
		}
	}
	if sets := stub.commandsFor("SET"); len(sets) != 1 {
		t.Errorf("%d cache writes for one shared run, want 1", len(sets))
	}
}

// TestHandlerSingleFlightStillRunsTheCLIPerDistinctClaim guards the obvious
// over-collapse bug: sharing must never merge two different questions.
func TestHandlerSingleFlightStillRunsTheCLIPerDistinctClaim(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "runs.txt")
	buildSlowStubCLI(t, counter, 300*time.Millisecond)
	useCache(t, &redisCache{addr: newStubRedis(t, "ok", "").addr()})

	claims := []string{"first distinct claim", "second distinct claim"}
	bodies := make([]string, len(claims))
	var wg sync.WaitGroup
	for i, claim := range claims {
		wg.Add(1)
		go func(i int, claim string) {
			defer wg.Done()
			_, bodies[i] = postConsensus(t, claim)
		}(i, claim)
	}
	wg.Wait()

	if got := cliRunCount(t, counter); got != 2 {
		t.Errorf("CLI ran %d times for 2 distinct claims, want 2", got)
	}
	if bodies[0] == bodies[1] {
		t.Error("two different claims returned the same body")
	}
}

// TestHandlerSingleFlightDoesNotShareTheBYOKSynthesis is the tenancy rule, and
// the reason single-flight stops at the CLI leg. Two concurrent callers with
// their OWN keys share the keyless CLI payload — and must each still get their
// own paid LLM call. One synthesis serving two callers would be exactly the
// cross-tenant leak the cache design was built to avoid.
func TestHandlerSingleFlightDoesNotShareTheBYOKSynthesis(t *testing.T) {
	var llmCalls atomic.Int64
	var mu sync.Mutex
	seenKeys := map[string]int{}

	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmCalls.Add(1)
		mu.Lock()
		seenKeys[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"stance\":\"supports\",\"confidence\":0.9,\"reasoning\":\"ok\",\"key_evidence\":[]}"}}]}`))
	}))
	defer llm.Close()
	useFakeProvider(t, "testprovider", llm.URL)

	counter := filepath.Join(t.TempDir(), "runs.txt")
	buildSlowStubCLI(t, counter, 500*time.Millisecond)
	useCache(t, &redisCache{addr: newStubRedis(t, "ok", "").addr()})

	const n = 2
	statuses := make([]int, n)
	bodies := make([]string, n)
	keys := []string{"key-of-caller-one", "key-of-caller-two"}
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i], bodies[i] = postConsensusWithKey(t, "shared cli leg claim", "testprovider", keys[i])
		}(i)
	}
	wg.Wait()

	if got := cliRunCount(t, counter); got != 1 {
		t.Errorf("CLI ran %d times, want 1 (the keyless leg is shared)", got)
	}
	if got := llmCalls.Load(); got != n {
		t.Errorf("LLM was called %d times for %d callers, want %d (the synthesis is NOT shared)", got, n, n)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, k := range keys {
		if seenKeys[k] != 1 {
			t.Errorf("provider saw key %q %d times, want exactly 1 — each caller must use its own", k, seenKeys[k])
		}
	}
	for i := range n {
		if statuses[i] != http.StatusOK {
			t.Fatalf("caller %d: status %d body %s", i, statuses[i], bodies[i])
		}
		var resp consensusResponse
		if err := json.Unmarshal([]byte(bodies[i]), &resp); err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
		if resp.StanceSource != "llm:testprovider" {
			t.Errorf("caller %d: stance_source = %q, want its own synthesis", i, resp.StanceSource)
		}
	}
}

// TestHandlerSingleFlightFailureDoesNotHangConcurrentCallers is the failure
// path at HTTP level: a failing CLI must fail all N callers promptly, with the
// same status they would have got on their own, and nothing may be cached.
func TestHandlerSingleFlightFailureDoesNotHangConcurrentCallers(t *testing.T) {
	const n = 5
	counter := filepath.Join(t.TempDir(), "runs.txt")
	buildSlowStubCLI(t, counter, 200*time.Millisecond)
	t.Setenv("SLOW_STUB_FAIL", "1")
	stub := newStubRedis(t, "ok", "")
	useCache(t, &redisCache{addr: stub.addr()})

	statuses := make([]int, n)
	bodies := make([]string, n)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				statuses[i], bodies[i] = postConsensus(t, "failing concurrent claim")
			}(i)
		}
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent callers hung on a failing CLI run")
	}

	if got := cliRunCount(t, counter); got != 1 {
		t.Errorf("failing CLI ran %d times, want 1", got)
	}
	for i := range n {
		if statuses[i] != http.StatusBadGateway {
			t.Errorf("caller %d: status %d, want %d — body %s", i, statuses[i], http.StatusBadGateway, bodies[i])
		}
	}
	cacheStore.waitForWrites()
	if sets := stub.commandsFor("SET"); len(sets) != 0 {
		t.Errorf("a failed run was cached (%d SETs)", len(sets))
	}
}
