// cache_test.go — the cache's contract, with the soft-dependency guarantee as
// the centrepiece.
//
// The tests that matter most are not the round-trip ones; they are
// TestHandlerIsIdenticalWithBrokenRedis and friends, which assert that a broken,
// hung, or absent Redis produces the SAME HTTP status and the SAME response
// bytes as no cache at all. Everything here is hermetic: a stub RESP server
// stands in for Redis and a stub CLI binary stands in for the child process, so
// no test touches the network.
package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- stub Redis --------------------------------------------------------------

// stubRedis is a minimal RESP2 server speaking the four commands this cache
// uses. mode selects the failure being simulated.
type stubRedis struct {
	ln       net.Listener
	password string
	mode     string // "ok", "garbage", "hang", "closeimmediately"

	mu   sync.Mutex
	data map[string][]byte
	cmds [][]string
}

func newStubRedis(t *testing.T, mode, password string) *stubRedis {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &stubRedis{ln: ln, password: password, mode: mode, data: map[string][]byte{}}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *stubRedis) addr() string { return s.ln.Addr().String() }

func (s *stubRedis) serve() {
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(nc)
	}
}

func (s *stubRedis) handle(nc net.Conn) {
	defer func() { _ = nc.Close() }()
	switch s.mode {
	case "closeimmediately":
		return
	case "garbage":
		_, _ = io.WriteString(nc, "this is not RESP\r\n")
		return
	case "hang":
		// Accept, read nothing, answer nothing: exercises the read deadline.
		time.Sleep(30 * time.Second)
		return
	}
	br := bufio.NewReader(nc)
	for {
		args, err := readStubCommand(br)
		if err != nil {
			return
		}
		s.mu.Lock()
		s.cmds = append(s.cmds, args)
		s.mu.Unlock()

		switch strings.ToUpper(args[0]) {
		case "AUTH":
			if len(args) < 2 || args[1] != s.password {
				_, _ = io.WriteString(nc, "-WRONGPASS invalid username-password pair\r\n")
				continue
			}
			_, _ = io.WriteString(nc, "+OK\r\n")
		case "PING":
			_, _ = io.WriteString(nc, "+PONG\r\n")
		case "SET":
			if len(args) < 3 {
				_, _ = io.WriteString(nc, "-ERR wrong number of arguments\r\n")
				continue
			}
			s.mu.Lock()
			s.data[args[1]] = []byte(args[2])
			s.mu.Unlock()
			_, _ = io.WriteString(nc, "+OK\r\n")
		case "GET":
			s.mu.Lock()
			v, ok := s.data[args[1]]
			s.mu.Unlock()
			if !ok {
				_, _ = io.WriteString(nc, "$-1\r\n")
				continue
			}
			_, _ = fmt.Fprintf(nc, "$%d\r\n", len(v))
			_, _ = nc.Write(v)
			_, _ = io.WriteString(nc, "\r\n")
		default:
			_, _ = io.WriteString(nc, "-ERR unknown command\r\n")
		}
	}
}

// commandsFor returns the recorded commands whose name matches name.
func (s *stubRedis) commandsFor(name string) [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][]string
	for _, c := range s.cmds {
		if strings.EqualFold(c[0], name) {
			out = append(out, c)
		}
	}
	return out
}

func readStubCommand(br *bufio.Reader) ([]string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if len(line) == 0 || line[0] != '*' {
		return nil, fmt.Errorf("bad command header %q", line)
	}
	n, err := strconv.Atoi(line[1:])
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("bad arity in %q", line)
	}
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		hdr, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		hdr = strings.TrimRight(hdr, "\r\n")
		if len(hdr) == 0 || hdr[0] != '$' {
			return nil, fmt.Errorf("bad bulk header %q", hdr)
		}
		size, err := strconv.Atoi(hdr[1:])
		if err != nil || size < 0 {
			return nil, fmt.Errorf("bad bulk length %q", hdr)
		}
		buf := make([]byte, size+2) // payload + CRLF
		if _, err := io.ReadFull(br, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}

// ---- keys and normalization --------------------------------------------------

func TestCacheKeyShapeAndStability(t *testing.T) {
	claims := []string{"vitamin D reduces respiratory infections"}
	k1 := cacheKey("consensus", 10, claims)
	k2 := cacheKey("consensus", 10, claims)
	if k1 != k2 {
		t.Fatalf("key not stable: %s vs %s", k1, k2)
	}
	if want := "sc:" + cacheEngineVersion + ":"; !strings.HasPrefix(k1, want) {
		t.Errorf("key %q does not start with %q", k1, want)
	}
	// sc:<engine>:<clihash>:<64 hex chars of sha256> — four fields, in that
	// order. The clihash field is either cliHashPrefixLen hex digits or the
	// cliHashUnavailable marker (which is what an unhashed test process gets).
	fields := strings.Split(k1, ":")
	if len(fields) != 4 {
		t.Fatalf("key %q has %d colon-separated fields, want 4 (sc:<engine>:<clihash>:<paramhash>)", k1, len(fields))
	}
	if fields[0] != "sc" || fields[1] != cacheEngineVersion {
		t.Errorf("key %q: want sc:%s prefix, got %s:%s", k1, cacheEngineVersion, fields[0], fields[1])
	}
	if fields[2] != cliHashUnavailable && len(fields[2]) != cliHashPrefixLen {
		t.Errorf("clihash field is %q (%d chars), want %d hex digits or %q",
			fields[2], len(fields[2]), cliHashPrefixLen, cliHashUnavailable)
	}
	if got := len(fields[3]); got != 64 {
		t.Errorf("hash part is %d chars, want 64 (sha256 hex)", got)
	}
}

// TestCacheKeyDistinguishesEveryParameter is the guard against the worst cache
// bug there is: two different queries sharing a key and serving each other's
// verdict.
func TestCacheKeyDistinguishesEveryParameter(t *testing.T) {
	base := cacheKey("consensus", 10, []string{"claim A"})
	cases := map[string]string{
		"different endpoint": cacheKey("evidence", 10, []string{"claim A"}),
		"different limit":    cacheKey("consensus", 20, []string{"claim A"}),
		"different claim":    cacheKey("consensus", 10, []string{"claim B"}),
		"extra claim":        cacheKey("consensus", 10, []string{"claim A", "claim B"}),
		"claims swapped":     cacheKey("consensus", 10, []string{"claim B", "claim A"}),
	}
	for name, key := range cases {
		if key == base {
			t.Errorf("%s produced the same key as the base query (%s)", name, key)
		}
	}
	// Concatenation must not collide: length-prefixing the claims is what stops
	// ["ab","c"] and ["a","bc"] from hashing to the same bytes.
	if cacheKey("compare", 10, []string{"ab", "c"}) == cacheKey("compare", 10, []string{"a", "bc"}) {
		t.Error("claim boundaries collide: length prefix missing from the normalized form")
	}
}

// TestCacheKeyIsEngineVersioned is the regression test for the failure this
// design exists to prevent: an engine change (CLI commit 8bb22658f rewrote 14
// works' labels and five corpus scores) must strand every entry it invalidates,
// because no redeploy can clear a cache.
func TestCacheKeyIsEngineVersioned(t *testing.T) {
	claims := []string{"same claim", "same everything else"}
	v1 := cacheKeyWithVersion("v1", "consensus", 10, claims)
	v2 := cacheKeyWithVersion("v2", "consensus", 10, claims)
	if v1 == v2 {
		t.Fatal("bumping the engine version did not change the key: old verdicts would survive the bump")
	}
	if !strings.HasPrefix(v2, "sc:v2:") {
		t.Errorf("engine version missing from key prefix: %s", v2)
	}
	if cacheKey("consensus", 10, claims) != cacheKeyWithVersion(cacheEngineVersion, "consensus", 10, claims) {
		t.Error("cacheKey does not use cacheEngineVersion")
	}
}

// ---- automatic engine identity (the CLI binary hash) -------------------------

// withCLIEngineHash swaps the process-wide clihash for the duration of one test
// and restores whatever was there before, so these tests cannot leak a key
// prefix into the rest of the suite.
func withCLIEngineHash(t *testing.T, h string) {
	t.Helper()
	prev := cliEngineHashSlot.Load()
	t.Cleanup(func() { cliEngineHashSlot.Store(prev) })
	if h == "" {
		cliEngineHashSlot.Store(nil)
		return
	}
	cliEngineHashSlot.Store(&h)
}

// writeFakeCLI writes a file with the given content and returns its path. It
// stands in for a CLI binary: cliBinaryHash only reads bytes, so a small file
// proves the same property as a 22MB one and keeps the test instant.
func writeFakeCLI(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cli-bin")
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	return p
}

// TestCLIBinaryHashIdentifiesContent is the core claim of the automatic
// identifier: the hash follows the bytes of the binary and nothing else. Same
// content (even at a different path, with a different mtime) means the same
// prefix; one changed byte means a different one.
func TestCLIBinaryHashIdentifiesContent(t *testing.T) {
	a1 := writeFakeCLI(t, "ENGINE-A: scoring build one")
	a2 := writeFakeCLI(t, "ENGINE-A: scoring build one") // identical bytes, different file
	b := writeFakeCLI(t, "ENGINE-B: scoring build two")

	hA1, err := cliBinaryHash(a1)
	if err != nil {
		t.Fatalf("hash a1: %v", err)
	}
	hA2, err := cliBinaryHash(a2)
	if err != nil {
		t.Fatalf("hash a2: %v", err)
	}
	hB, err := cliBinaryHash(b)
	if err != nil {
		t.Fatalf("hash b: %v", err)
	}

	if hA1 != hA2 {
		t.Errorf("same binary content hashed differently: %s vs %s — the cache would be invalidated by a redeploy that changes nothing", hA1, hA2)
	}
	if hA1 == hB {
		t.Fatalf("two different binaries hashed the same (%s) — a rebuilt CLI would keep serving old verdicts", hA1)
	}
	for _, h := range []string{hA1, hB} {
		if len(h) != cliHashPrefixLen {
			t.Errorf("hash %q is %d chars, want %d", h, len(h), cliHashPrefixLen)
		}
		if _, err := hex.DecodeString(h); err != nil {
			t.Errorf("hash %q is not hex: %v", h, err)
		}
	}

	// Re-reading the same file must be stable across calls, not just across
	// files: a hash that drifted per call would re-key the cache continuously.
	for i := 0; i < 3; i++ {
		again, err := cliBinaryHash(a1)
		if err != nil {
			t.Fatalf("rehash a1: %v", err)
		}
		if again != hA1 {
			t.Fatalf("hash of an unchanged file drifted on call %d: %s then %s", i+2, hA1, again)
		}
	}
}

// TestCLIBinarySwapChangesKeyPrefix is the whole point of the change, exercised
// through the real startup path: initCLIEngineHash reads the binary that
// cliBinaryPath() resolves, so swapping the binary must move every key prefix
// with no human step and no bump of cacheEngineVersion.
func TestCLIBinarySwapChangesKeyPrefix(t *testing.T) {
	withCLIEngineHash(t, "")
	claims := []string{"vitamin D reduces respiratory infections"}

	prefixFor := func(path string) (prefix, key string) {
		t.Setenv("CLI_BIN", path)
		initCLIEngineHash()
		key = cacheKey("consensus", 10, claims)
		fields := strings.Split(key, ":")
		if len(fields) != 4 {
			t.Fatalf("key %q is not sc:<engine>:<clihash>:<paramhash>", key)
		}
		return strings.Join(fields[:3], ":") + ":", key
	}

	oldBin := writeFakeCLI(t, "CLI build with the pre-8bb22658f scoring engine")
	newBin := writeFakeCLI(t, "CLI build that rewrote 14 works' labels")

	oldPrefix, oldKey := prefixFor(oldBin)
	newPrefix, newKey := prefixFor(newBin)

	if oldPrefix == newPrefix {
		t.Fatalf("swapping the CLI binary did not change the key prefix (%s): the cache would serve pre-rebuild verdicts for the full %s TTL", oldPrefix, cacheTTL)
	}
	if oldKey == newKey {
		t.Fatal("swapping the CLI binary did not change the full key")
	}
	// Both prefixes must still carry the manual version: the automatic
	// identifier is ADDITIVE, it does not replace the hand-bumped constant.
	for _, p := range []string{oldPrefix, newPrefix} {
		if !strings.HasPrefix(p, "sc:"+cacheEngineVersion+":") {
			t.Errorf("prefix %q lost the manual engine version", p)
		}
	}
	// Re-initializing from the first binary must return to the first prefix,
	// so a rollback re-uses the entries it originally wrote.
	if rolledBack, _ := prefixFor(oldBin); rolledBack != oldPrefix {
		t.Errorf("rollback to the old binary produced %q, want the original %q", rolledBack, oldPrefix)
	}

	t.Logf("old binary -> prefix %s", oldPrefix)
	t.Logf("new binary -> prefix %s", newPrefix)
}

// TestCLIHashFallsBackWhenBinaryUnreadable holds the soft-dependency line at the
// startup boundary: a binary that cannot be hashed must degrade to the manual
// version with a log line, never panic and never abort startup.
func TestCLIHashFallsBackWhenBinaryUnreadable(t *testing.T) {
	withCLIEngineHash(t, "")

	if _, err := cliBinaryHash(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("hashing a missing file returned no error")
	}

	// The startup call itself: missing binary, and it must simply return.
	t.Setenv("CLI_BIN", filepath.Join(t.TempDir(), "also-missing"))
	initCLIEngineHash() // must not panic, must not exit
	if got := cliEngineHash(); got != cliHashUnavailable {
		t.Errorf("clihash after a failed hash is %q, want the %q marker", got, cliHashUnavailable)
	}

	// The key must stay well-formed, and must still be versioned by hand — the
	// fallback loses the automatic handle, not the manual one.
	key := cacheKey("consensus", 10, []string{"claim"})
	if !strings.HasPrefix(key, "sc:"+cacheEngineVersion+":"+cliHashUnavailable+":") {
		t.Errorf("fallback key has the wrong shape: %s", key)
	}
	if len(strings.Split(key, ":")) != 4 {
		t.Errorf("fallback key is not four fields: %s", key)
	}
	// The marker must never be mistakable for a real hash prefix.
	if _, err := hex.DecodeString(cliHashUnavailable); err == nil {
		t.Errorf("%q is valid hex, so it could collide with a real binary hash", cliHashUnavailable)
	}

	// A directory is the other way a read fails (open succeeds, io.Copy does
	// not) — it must be an error, not a hash of nothing.
	if h, err := cliBinaryHash(t.TempDir()); err == nil {
		t.Errorf("hashing a directory returned %q instead of an error", h)
	}
}

// TestCacheKeyLengthIsBounded proves the key cannot outgrow a Redis key we are
// happy to log and grep, whatever the input. Length is fixed by construction —
// claims are hashed, never appended — so the only way this breaks is a future
// field concatenated raw into the key.
func TestCacheKeyLengthIsBounded(t *testing.T) {
	withCLIEngineHash(t, strings.Repeat("a", cliHashPrefixLen))

	longClaim := strings.Repeat("a very long claim about vitamin D and respiratory infections ", 40)
	many := make([]string, 50)
	for i := range many {
		many[i] = longClaim + strconv.Itoa(i)
	}

	cases := map[string]string{
		"single short claim": cacheKey("consensus", 10, []string{"aspirin"}),
		"one huge claim":     cacheKey("consensus", 10, []string{longClaim}),
		"50 huge claims":     cacheKey("compare", 100, many),
		"no claims":          cacheKey("gaps", 10, nil),
	}
	for name, key := range cases {
		if got := len(key); got > cacheKeyMaxLen {
			t.Errorf("%s: key is %d chars, over the %d ceiling: %s", name, got, cacheKeyMaxLen, key)
		}
	}

	// Every key must be exactly the same length, since only fixed-width fields
	// reach it. That is the property that makes the ceiling meaningful.
	want := len(cases["single short claim"])
	for name, key := range cases {
		if len(key) != want {
			t.Errorf("%s: key is %d chars, but keys must be a fixed %d — an input-dependent field reached the key", name, len(key), want)
		}
	}

	// The measurement the task asks for: old shape vs new shape, in characters.
	oldShape := "sc:" + cacheEngineVersion + ":" + strings.Repeat("0", 64)
	newShape := cases["single short claim"]
	t.Logf("old key shape sc:<engine>:<paramhash>            = %d chars", len(oldShape))
	t.Logf("new key shape sc:<engine>:<clihash>:<paramhash>  = %d chars (ceiling %d)", len(newShape), cacheKeyMaxLen)
	if len(newShape) != len(oldShape)+cliHashPrefixLen+1 {
		t.Errorf("new key grew by %d chars, expected exactly %d (clihash + one colon)",
			len(newShape)-len(oldShape), cliHashPrefixLen+1)
	}
}

// TestRealCLIBinariesHashDistinctly checks the committed artefacts themselves,
// since they are what production actually keys on. Skips rather than fails when
// bin/ is absent: the binaries are large build outputs and a checkout without
// them is still a valid place to run the suite.
func TestRealCLIBinariesHashDistinctly(t *testing.T) {
	seen := map[string]string{}
	for _, name := range []string{"scientific-consensus-pp-cli.exe", "scientific-consensus-pp-cli-linux"} {
		p := filepath.Join("bin", name)
		if _, err := os.Stat(p); err != nil {
			t.Skipf("committed binary %s not present: %v", p, err)
		}
		h, err := cliBinaryHash(p)
		if err != nil {
			t.Fatalf("hash %s: %v", p, err)
		}
		if other, dup := seen[h]; dup {
			t.Errorf("%s and %s share the hash %s", other, name, h)
		}
		seen[h] = name
		t.Logf("%-40s -> sc:%s:%s:", name, cacheEngineVersion, h)
	}
}

func TestNormalizeClaim(t *testing.T) {
	cases := map[string]string{
		"  vitamin D  ":               "vitamin D",
		"vitamin\t\tD":                "vitamin D",
		"vitamin  D\n reduces":        "vitamin D reduces",
		"Vitamin D":                   "Vitamin D", // case is preserved: it is echoed to the user
		"":                            "",
		"   ":                         "",
		"single":                      "single",
		"trailing newline in claim\n": "trailing newline in claim",
	}
	for in, want := range cases {
		if got := normalizeClaim(in); got != want {
			t.Errorf("normalizeClaim(%q) = %q, want %q", in, got, want)
		}
	}
	// Equivalent spacings must land on one key; different words must not.
	if cacheKey("consensus", 10, []string{"a  b"}) != cacheKey("consensus", 10, []string{" a b "}) {
		t.Error("whitespace-equivalent claims produced different keys")
	}
}

// ---- soft dependency ---------------------------------------------------------

// TestNilCacheIsNoOp covers the configured-off case: every entry point must be
// safe on a nil receiver, with no panic and no cache behaviour.
func TestNilCacheIsNoOp(t *testing.T) {
	var c *redisCache // exactly what main() holds when REDIS_ADDR is unset

	if c.usable() {
		t.Error("nil cache reports itself usable")
	}
	if _, hit := c.Get(t.Context(), "sc:v1:whatever"); hit {
		t.Error("nil cache reported a hit")
	}
	c.Set("sc:v1:whatever", []byte(`{"a":1}`)) // must not panic
	c.probeAsync()                             // must not panic
	c.waitForWrites()
	if hits, misses, failures := c.stats(); hits|misses|failures != 0 {
		t.Errorf("nil cache counted something: %d/%d/%d", hits, misses, failures)
	}
	if err := c.ping(); err == nil {
		t.Error("nil cache ping returned success")
	}
}

// TestNewRedisCacheFromEnv covers the step that PRODUCES the nil cache in
// production. Without it, "a nil cache is a no-op" would be proven only for a
// state nothing was shown to actually reach.
func TestNewRedisCacheFromEnv(t *testing.T) {
	t.Setenv("CACHE_DISABLED", "")
	t.Setenv("REDIS_PASSWORD", "")
	t.Setenv("REDIS_ADDR", "")
	if c := newRedisCacheFromEnv(); c != nil {
		t.Errorf("no REDIS_ADDR must mean no cache, got addr %q", c.addr)
	}

	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("REDIS_PASSWORD", "  hunter2  ")
	c := newRedisCacheFromEnv()
	if c == nil {
		t.Fatal("REDIS_ADDR is set but no cache was built")
	}
	if c.addr != "redis:6379" {
		t.Errorf("addr = %q, want %q", c.addr, "redis:6379")
	}
	if c.password != "hunter2" {
		t.Errorf("password = %q, want it trimmed to %q", c.password, "hunter2")
	}
	// Construction must be I/O-free, or an unreachable Redis could delay startup.
	if len(c.idle) != 0 {
		t.Error("newRedisCacheFromEnv opened a connection; it must not dial")
	}

	for _, v := range []string{"1", "true", "yes", "on", "YES"} {
		t.Setenv("CACHE_DISABLED", v)
		if got := newRedisCacheFromEnv(); got != nil {
			t.Errorf("CACHE_DISABLED=%s did not disable the cache", v)
		}
	}
}

func TestCacheRoundTrip(t *testing.T) {
	s := newStubRedis(t, "ok", "")
	c := &redisCache{addr: s.addr()}

	key := cacheKey("consensus", 10, []string{"round trip"})
	payload := []byte(`{"claim":"round trip","verdict":"evidence-supports"}`)

	if _, hit := c.Get(t.Context(), key); hit {
		t.Fatal("empty cache reported a hit")
	}
	c.Set(key, payload)
	c.waitForWrites()

	got, hit := c.Get(t.Context(), key)
	if !hit {
		t.Fatal("stored entry did not come back")
	}
	if string(got) != string(payload) {
		t.Errorf("payload changed in transit:\n got %s\nwant %s", got, payload)
	}
	if hits, misses, failures := c.stats(); hits != 1 || misses != 1 || failures != 0 {
		t.Errorf("counters = %d hits / %d misses / %d failures, want 1/1/0", hits, misses, failures)
	}

	// The TTL must travel with the write, or entries would never expire on an
	// instance configured with no maxmemory pressure.
	sets := s.commandsFor("SET")
	if len(sets) != 1 {
		t.Fatalf("got %d SET commands, want 1", len(sets))
	}
	if len(sets[0]) != 5 || !strings.EqualFold(sets[0][3], "EX") {
		t.Fatalf("SET did not carry an EX expiry: %v", sets[0][:min(4, len(sets[0]))])
	}
	if want := strconv.Itoa(7 * 24 * 60 * 60); sets[0][4] != want {
		t.Errorf("TTL = %s seconds, want %s (7 days)", sets[0][4], want)
	}
}

func TestCacheReusesPooledConnection(t *testing.T) {
	s := newStubRedis(t, "ok", "")
	c := &redisCache{addr: s.addr()}
	for i := 0; i < 3; i++ {
		c.Get(t.Context(), "sc:v1:pool")
	}
	if got := len(c.idle); got != 1 {
		t.Errorf("idle pool holds %d connections after 3 sequential GETs, want 1", got)
	}
}

func TestCacheAuthenticates(t *testing.T) {
	s := newStubRedis(t, "ok", "s3cret")
	c := &redisCache{addr: s.addr(), password: "s3cret"}
	if err := c.ping(); err != nil {
		t.Fatalf("ping with correct password failed: %v", err)
	}
	if got := len(s.commandsFor("AUTH")); got != 1 {
		t.Errorf("AUTH sent %d times, want 1", got)
	}

	// A wrong password is a cache failure, never a caller-visible error.
	bad := &redisCache{addr: s.addr(), password: "wrong"}
	if _, hit := bad.Get(t.Context(), "sc:v1:x"); hit {
		t.Error("GET with a bad password reported a hit")
	}
	if _, _, failures := bad.stats(); failures == 0 {
		t.Error("bad password was not counted as a failure")
	}
}

// TestCacheSurvivesBrokenRedis walks the failure modes: nothing listening, a
// listener that hangs up, and a listener that answers with non-RESP bytes. Every
// one must look exactly like a miss.
func TestCacheSurvivesBrokenRedis(t *testing.T) {
	dead := newStubRedis(t, "ok", "")
	deadAddr := dead.addr()
	_ = dead.ln.Close() // nothing listening on this port any more

	cases := map[string]*redisCache{
		"connection refused": {addr: deadAddr},
		"closes immediately": {addr: newStubRedis(t, "closeimmediately", "").addr()},
		"answers with junk":  {addr: newStubRedis(t, "garbage", "").addr()},
		"unresolvable host":  {addr: "no-such-host.invalid:6379"},
	}
	for name, c := range cases {
		if _, hit := c.Get(t.Context(), "sc:v1:key"); hit {
			t.Errorf("%s: reported a hit", name)
		}
		c.Set("sc:v1:key", []byte(`{"a":1}`))
		c.waitForWrites() // must not panic, must not block on a broken server
		if _, _, failures := c.stats(); failures == 0 {
			t.Errorf("%s: failure was not counted", name)
		}
		if hits, _, _ := c.stats(); hits != 0 {
			t.Errorf("%s: counted a hit against a broken server", name)
		}
	}
}

// TestCacheBreakerStopsHammeringDeadRedis proves the cost bound: without the
// breaker, a dead Redis would add a dial timeout to every single request.
func TestCacheBreakerStopsHammeringDeadRedis(t *testing.T) {
	dead := newStubRedis(t, "ok", "")
	addr := dead.addr()
	_ = dead.ln.Close()
	c := &redisCache{addr: addr}

	for i := 0; i < cacheBreakerThreshold; i++ {
		c.Get(t.Context(), "sc:v1:key")
	}
	if c.usable() {
		t.Fatalf("cache still usable after %d consecutive failures", cacheBreakerThreshold)
	}
	// While the breaker is open, calls must return without touching the network.
	start := time.Now()
	for i := 0; i < 50; i++ {
		if _, hit := c.Get(t.Context(), "sc:v1:key"); hit {
			t.Fatal("open breaker reported a hit")
		}
		c.Set("sc:v1:key", []byte(`{"a":1}`))
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("50 calls through an open breaker took %s; they should be free", elapsed)
	}
	// A recovered Redis must be picked up again.
	c.disabledUntil.Store(0)
	c.recordSuccess()
	if !c.usable() {
		t.Error("cache did not re-enable after recovery")
	}
}

// TestCacheHonoursReadTimeout pins the 2s bound on a server that accepts and
// then says nothing — the "Redis is slow" case from the requirements.
func TestCacheHonoursReadTimeout(t *testing.T) {
	c := &redisCache{addr: newStubRedis(t, "hang", "").addr()}
	start := time.Now()
	if _, hit := c.Get(t.Context(), "sc:v1:key"); hit {
		t.Fatal("hung server reported a hit")
	}
	elapsed := time.Since(start)
	if elapsed < cacheIOTimeout {
		t.Errorf("returned after %s, before the %s read timeout — deadline not applied?", elapsed, cacheIOTimeout)
	}
	// One retry on a fresh connection is allowed, so the bound is 2x, not 1x.
	if max := 2*(cacheIOTimeout+cacheDialTimeout) + time.Second; elapsed > max {
		t.Errorf("hung server cost %s, want under %s", elapsed, max)
	}
}

func TestCacheSkipsOversizedPayload(t *testing.T) {
	s := newStubRedis(t, "ok", "")
	c := &redisCache{addr: s.addr()}
	c.Set("sc:v1:big", make([]byte, cacheMaxValueBytes+1))
	c.waitForWrites()
	if got := len(s.commandsFor("SET")); got != 0 {
		t.Errorf("oversized payload was sent to redis (%d SETs)", got)
	}
}

// ---- handler-level behaviour -------------------------------------------------

// buildStubCLI compiles a stand-in for the child CLI: it appends a line to a
// counter file on every run and prints a fixed JSON payload. This keeps the
// handler tests hermetic — no OpenAlex, no network — while still exercising the
// real exec path in runCLIRaw.
func buildStubCLI(t *testing.T, counterPath string) string {
	t.Helper()
	dir := t.TempDir()
	src := `package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	if p := os.Getenv("STUB_CLI_COUNTER"); p != "" {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(f, "run")
			f.Close()
		}
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
	})
	fmt.Println(string(out))
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("write stub source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module stubcli\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatalf("write stub go.mod: %v", err)
	}
	bin := filepath.Join(dir, "stubcli")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build stub CLI: %v\n%s", err, out)
	}
	t.Setenv("CLI_BIN", bin)
	t.Setenv("STUB_CLI_COUNTER", counterPath)
	return bin
}

// postConsensus drives the real handler and returns status + body.
func postConsensus(t *testing.T, claim string) (int, string) {
	t.Helper()
	body := fmt.Sprintf(`{"claim":%q,"limit":10}`, claim)
	req := httptest.NewRequest(http.MethodPost, "/api/consensus", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handleConsensus(rec, req)
	return rec.Code, rec.Body.String()
}

func cliRunCount(t *testing.T, counterPath string) int {
	t.Helper()
	data, err := os.ReadFile(counterPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read counter: %v", err)
	}
	return len(strings.Fields(string(data)))
}

// useCache swaps the process-wide cache for the duration of one test.
func useCache(t *testing.T, c *redisCache) {
	t.Helper()
	prev := cacheStore
	cacheStore = c
	t.Cleanup(func() { cacheStore = prev })
}

// TestHandlerCacheHitSkipsTheCLI is the performance claim, asserted structurally
// rather than by timing: a hit must not spawn the child process at all.
func TestHandlerCacheHitSkipsTheCLI(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "runs.txt")
	buildStubCLI(t, counter)
	useCache(t, &redisCache{addr: newStubRedis(t, "ok", "").addr()})

	status1, body1 := postConsensus(t, "cached claim")
	if status1 != http.StatusOK {
		t.Fatalf("first request: status %d, body %s", status1, body1)
	}
	cacheStore.waitForWrites()

	status2, body2 := postConsensus(t, "cached claim")
	if status2 != http.StatusOK {
		t.Fatalf("second request: status %d, body %s", status2, body2)
	}
	if body1 != body2 {
		t.Errorf("cache hit changed the response:\nmiss: %s\nhit:  %s", body1, body2)
	}
	if got := cliRunCount(t, counter); got != 1 {
		t.Errorf("CLI ran %d times, want 1 (the second request should have been served from cache)", got)
	}
	if hits, misses, _ := cacheStore.stats(); hits != 1 || misses != 1 {
		t.Errorf("counters = %d hits / %d misses, want 1/1", hits, misses)
	}
}

// TestHandlerIsIdenticalWithBrokenRedis is the requirement in one test: with a
// dead Redis the endpoint must return the same status and the same bytes as with
// no cache configured, and must still run the CLI every time.
func TestHandlerIsIdenticalWithBrokenRedis(t *testing.T) {
	const claim = "soft dependency claim"

	counterNoCache := filepath.Join(t.TempDir(), "runs-nocache.txt")
	buildStubCLI(t, counterNoCache)
	useCache(t, nil) // no cache at all: the pre-cache behaviour
	statusNoCache, bodyNoCache := postConsensus(t, claim)
	// Asserted directly, not inferred: the nil-cache path is the reference every
	// other case is compared against, so it must be independently known good.
	if statusNoCache != http.StatusOK {
		t.Fatalf("nil cacheStore did not serve a normal response: status %d, body %s", statusNoCache, bodyNoCache)
	}
	if !json.Valid([]byte(bodyNoCache)) {
		t.Fatalf("nil cacheStore produced invalid JSON: %s", bodyNoCache)
	}
	runsNoCache := cliRunCount(t, counterNoCache)

	counterBroken := filepath.Join(t.TempDir(), "runs-broken.txt")
	buildStubCLI(t, counterBroken)
	dead := newStubRedis(t, "ok", "")
	deadAddr := dead.addr()
	_ = dead.ln.Close()
	broken := &redisCache{addr: deadAddr}
	useCache(t, broken)
	statusBroken, bodyBroken := postConsensus(t, claim)

	if statusBroken != statusNoCache {
		t.Errorf("status with broken redis = %d, without cache = %d", statusBroken, statusNoCache)
	}
	if bodyBroken != bodyNoCache {
		t.Errorf("body differs with a broken redis:\nno cache: %s\nbroken:   %s", bodyNoCache, bodyBroken)
	}
	if statusBroken != http.StatusOK {
		t.Errorf("broken redis leaked into the HTTP status: %d", statusBroken)
	}
	if strings.Contains(strings.ToLower(bodyBroken), "redis") ||
		strings.Contains(strings.ToLower(bodyBroken), "cache") ||
		strings.Contains(strings.ToLower(bodyBroken), "connect") {
		t.Errorf("a redis diagnostic leaked into the response body: %s", bodyBroken)
	}
	if got := cliRunCount(t, counterBroken); got != runsNoCache {
		t.Errorf("CLI ran %d times with a broken redis, want %d (same as uncached)", got, runsNoCache)
	}
	// The response must still be the documented shape, not a degraded one.
	var resp consensusResponse
	if err := json.Unmarshal([]byte(bodyBroken), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if resp.StanceSource != "heuristic" {
		t.Errorf("stance_source = %q, want %q", resp.StanceSource, "heuristic")
	}
}

// TestHandlerIsIdenticalWithHungRedis is the silent-Redis case at the HTTP
// level: a listener that accepts the connection and then says nothing. This is
// the failure a refused-connection test does NOT cover (that one fails
// instantly) and that probeAsync cannot cover at all — probeAsync only reports
// startup reachability, and says nothing about a Redis that goes quiet later.
//
// The request must return the normal result, not a 5xx, and must give up inside
// the read timeout instead of waiting on the cache indefinitely.
func TestHandlerIsIdenticalWithHungRedis(t *testing.T) {
	const claim = "hung redis claim"

	counterNoCache := filepath.Join(t.TempDir(), "runs-nocache.txt")
	buildStubCLI(t, counterNoCache)
	useCache(t, nil)
	statusNoCache, bodyNoCache := postConsensus(t, claim)
	if statusNoCache != http.StatusOK {
		t.Fatalf("uncached baseline is already broken: status %d, body %s", statusNoCache, bodyNoCache)
	}

	counterHung := filepath.Join(t.TempDir(), "runs-hung.txt")
	buildStubCLI(t, counterHung)
	useCache(t, &redisCache{addr: newStubRedis(t, "hang", "").addr()})

	start := time.Now()
	status, body := postConsensus(t, claim)
	elapsed := time.Since(start)

	if status != http.StatusOK {
		t.Errorf("silent redis produced HTTP %d, want %d (body %s)", status, http.StatusOK, body)
	}
	if status >= 500 {
		t.Errorf("a cache timeout surfaced as a server error (%d)", status)
	}
	if body != bodyNoCache {
		t.Errorf("silent redis changed the response:\nno cache: %s\nhung:     %s", bodyNoCache, body)
	}
	if got := cliRunCount(t, counterHung); got != 1 {
		t.Errorf("CLI ran %d times, want 1 — the request must fall through to the CLI", got)
	}
	if _, _, failures := cacheStore.stats(); failures == 0 {
		t.Error("the read timeout was not counted as a cache failure")
	}
	// A silent Redis may cost one bounded pause, never an unbounded wait.
	if maxWait := 2*(cacheIOTimeout+cacheDialTimeout) + 5*time.Second; elapsed > maxWait {
		t.Errorf("request took %s against a silent redis, want under %s", elapsed, maxWait)
	}
}

// TestHandlerIgnoresCorruptCachedEntry: a stored value that is not valid JSON
// must be recomputed, never forwarded. A cache may only make a response faster.
func TestHandlerIgnoresCorruptCachedEntry(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "runs.txt")
	buildStubCLI(t, counter)
	s := newStubRedis(t, "ok", "")
	useCache(t, &redisCache{addr: s.addr()})

	const claim = "corrupt entry claim"
	key := cacheKey("consensus", 10, []string{claim})
	s.mu.Lock()
	s.data[key] = []byte("{ this is not json")
	s.mu.Unlock()

	status, body := postConsensus(t, claim)
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s", status, body)
	}
	if !json.Valid([]byte(body)) {
		t.Fatalf("corrupt cache entry produced an invalid response: %s", body)
	}
	if got := cliRunCount(t, counter); got != 1 {
		t.Errorf("CLI ran %d times, want 1 (corrupt entry should force a recompute)", got)
	}
}

// TestHandlerDoesNotCacheCLIFailure: a failed CLI run must not be memoized, or
// one upstream blip would be served for a week.
func TestHandlerDoesNotCacheCLIFailure(t *testing.T) {
	s := newStubRedis(t, "ok", "")
	useCache(t, &redisCache{addr: s.addr()})
	t.Setenv("CLI_BIN", filepath.Join(t.TempDir(), "does-not-exist"))

	status, body := postConsensus(t, "failing claim")
	if status != http.StatusBadGateway {
		t.Errorf("status = %d, want %d; body %s", status, http.StatusBadGateway, body)
	}
	cacheStore.waitForWrites()
	if got := len(s.commandsFor("SET")); got != 0 {
		t.Errorf("a failed CLI run was written to the cache (%d SETs)", got)
	}
}
