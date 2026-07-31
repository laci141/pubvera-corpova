package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// buildConcurrencyStubCLI compiles a child CLI that brackets its own lifetime in
// a shared log: "S" on entry, "E" on exit, appended with O_APPEND so the file
// records the true chronological order of every child. Walking that log gives
// PEAK concurrency, which a plain run counter cannot: ten runs one after another
// and ten runs at once produce the same count and very different load.
func buildConcurrencyStubCLI(t *testing.T, logPath string, delay time.Duration) {
	t.Helper()
	dir := t.TempDir()
	src := `package main
import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)
func mark(p, s string) {
	if p == "" {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		fmt.Fprintln(f, s)
		f.Close()
	}
}
func main() {
	p := os.Getenv("CONC_STUB_LOG")
	mark(p, "S")
	d, _ := time.ParseDuration(os.Getenv("CONC_STUB_DELAY"))
	time.Sleep(d)
	claim := ""
	if len(os.Args) > 2 {
		claim = os.Args[2]
	}
	out, _ := json.Marshal(map[string]any{
		"claim":    claim,
		"verdict":  "evidence-supports",
		"refuting": 0,
		"mixed":    0,
	})
	fmt.Println(string(out))
	mark(p, "E")
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("write stub source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module concstub\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatalf("write stub go.mod: %v", err)
	}
	bin := filepath.Join(dir, "concstub")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build concurrency stub CLI: %v\n%s", err, out)
	}
	t.Setenv("CLI_BIN", bin)
	t.Setenv("CONC_STUB_LOG", logPath)
	t.Setenv("CONC_STUB_DELAY", delay.String())
}

// peakConcurrency replays the stub log and reports the highest number of child
// processes alive at the same moment.
func peakConcurrency(t *testing.T, logPath string) int {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read stub log: %v", err)
	}
	cur, peak := 0, 0
	for _, line := range strings.Split(string(raw), "\n") {
		switch strings.TrimSpace(line) {
		case "S":
			cur++
			if cur > peak {
				peak = cur
			}
		case "E":
			cur--
		}
	}
	return peak
}

// fireDistinctClaims sends n concurrent requests, each with its own claim so no
// two share a single-flight run. Returns every status code.
func fireDistinctClaims(t *testing.T, n int) []int {
	t.Helper()
	codes := make([]int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			code, _ := postConsensusWithKey(t, fmt.Sprintf("distinct claim %d", i), "", "")
			codes[i] = code
		}(i)
	}
	close(start)
	wg.Wait()
	return codes
}

// useSemaphore swaps in a semaphore for one test and restores the real one.
func useSemaphore(t *testing.T, slots int, wait time.Duration) {
	t.Helper()
	prevSem, prevWait := cliSem, cliSlotWait
	cliSem, cliSlotWait = newCLISemaphore(slots), wait
	t.Cleanup(func() { cliSem, cliSlotWait = prevSem, prevWait })
}

// TestCLISemaphoreBoundsChildRuns is the measurement, run twice in one test so
// the two numbers sit side by side: the same twelve concurrent requests with the
// bound disabled and with it set to four.
//
// Twelve DISTINCT claims, so single-flight cannot collapse them — this is
// exactly the case single-flight does not help with, and the reason the
// semaphore exists.
func TestCLISemaphoreBoundsChildRuns(t *testing.T) {
	const requests = 12

	unboundedLog := filepath.Join(t.TempDir(), "unbounded.log")
	buildConcurrencyStubCLI(t, unboundedLog, 400*time.Millisecond)

	useSemaphore(t, 0, 10*time.Second) // 0 slots = bound disabled
	fireDistinctClaims(t, requests)
	unbounded := peakConcurrency(t, unboundedLog)

	boundedLog := filepath.Join(t.TempDir(), "bounded.log")
	t.Setenv("CONC_STUB_LOG", boundedLog)

	useSemaphore(t, 4, 10*time.Second)
	codes := fireDistinctClaims(t, requests)
	bounded := peakConcurrency(t, boundedLog)

	t.Logf("MEASURED peak concurrent child processes for %d distinct requests: unbounded=%d bounded=%d",
		requests, unbounded, bounded)

	if unbounded <= 4 {
		t.Fatalf("unbounded peak was %d, expected more than 4 — the harness is not "+
			"producing concurrency, so the bounded result proves nothing", unbounded)
	}
	if bounded > 4 {
		t.Errorf("bounded peak = %d, want at most 4", bounded)
	}
	for i, c := range codes {
		if c != 200 {
			t.Errorf("request %d got %d, want 200 — the grace period should absorb this burst", i, c)
		}
	}
}

// TestCLISemaphoreRejectsWithRetryAfter pins the overload response. A 503 without
// a Retry-After is not actionable: a client that retries immediately makes the
// overload it just hit worse.
func TestCLISemaphoreRejectsWithRetryAfter(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "reject.log")
	buildConcurrencyStubCLI(t, logPath, 2*time.Second)

	// One slot, a grace period far shorter than the stub's run: the second
	// caller cannot possibly get in.
	useSemaphore(t, 1, 150*time.Millisecond)

	codes := fireDistinctClaims(t, 2)

	var ok, busy int
	for _, c := range codes {
		switch c {
		case 200:
			ok++
		case 503:
			busy++
		}
	}
	if ok != 1 || busy != 1 {
		t.Fatalf("codes = %v, want exactly one 200 and one 503", codes)
	}

	// Now the header itself, on a request that is certain to be rejected.
	useSemaphore(t, 0, 150*time.Millisecond)
	cliSem = newCLISemaphore(1)
	if err := cliSem.acquire(context.Background()); err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	code, body := postConsensusWithKey(t, "rejected claim", "", "")
	cliSem.release()

	if code != 503 {
		t.Fatalf("status = %d, want 503 (body %s)", code, body)
	}
	if !strings.Contains(body, "busy") {
		t.Errorf("body = %s, want it to say the server is busy", body)
	}
}

// TestCLISemaphoreDisabledIsANoOp proves the escape hatch. Setting
// CLI_MAX_CONCURRENT=0 must restore exactly the pre-semaphore behaviour, so a
// bad limit in production can be undone with an env var and a restart rather
// than a deploy.
func TestCLISemaphoreDisabledIsANoOp(t *testing.T) {
	t.Setenv("CLI_MAX_CONCURRENT", "0")
	if got := cliSlotsFromEnv(); got != 0 {
		t.Fatalf("cliSlotsFromEnv() = %d, want 0", got)
	}
	s := newCLISemaphore(0)
	if got := s.capacity(); got != 0 {
		t.Errorf("capacity() = %d, want 0", got)
	}
	for i := 0; i < 50; i++ {
		if err := s.acquire(context.Background()); err != nil {
			t.Fatalf("acquire %d on a disabled semaphore returned %v", i, err)
		}
	}
	if got := s.inUse(); got != 0 {
		t.Errorf("inUse() = %d on a disabled semaphore, want 0", got)
	}

	t.Setenv("CLI_MAX_CONCURRENT", "")
	if got := cliSlotsFromEnv(); got != defaultCLISlots {
		t.Errorf("empty env gave %d, want the default %d", got, defaultCLISlots)
	}
	t.Setenv("CLI_MAX_CONCURRENT", "not-a-number")
	if got := cliSlotsFromEnv(); got != defaultCLISlots {
		t.Errorf("unparseable env gave %d, want the default %d", got, defaultCLISlots)
	}
}
