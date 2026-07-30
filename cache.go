// cache.go — the Redis response cache for the CLI's OpenAlex-derived JSON.
//
// WHAT IS CACHED: the child CLI's raw JSON output for one (endpoint, limit,
// claims) triple. That payload is an OpenAlex search result plus this engine's
// heuristic scoring, and it depends on nothing else — the child CLI is always
// keyless (buildChildEnv strips every provider key), so the value is identical
// for every caller.
//
// WHAT IS NEVER CACHED: BYOK keys, the LLM synthesis computed from them, and
// anything else caller-specific. The synthesis still runs per request, on top of
// a cached or a freshly computed CLI payload, exactly as before. Cache keys are
// sha256 hashes and log lines carry only the hash — a claim string never reaches
// a log.
//
// SOFT DEPENDENCY (the load-bearing property of this file, do not weaken):
// Redis is an accelerator, never a requirement. Every failure mode — no
// REDIS_ADDR, refused connection, dead socket, wrong password, protocol garbage,
// slow server, evicted key, even a panic in this file — degrades to "cache miss"
// and the request proceeds exactly as it did before this file existed. Concretely:
//   - A nil *redisCache is a valid receiver: every method is a no-op on it.
//   - Startup never dials. The reachability probe runs in a goroutine and only
//     logs; an unreachable Redis cannot delay or block ListenAndServe.
//   - Dial, read and write each get a 2s deadline (cacheDialTimeout /
//     cacheIOTimeout), so a hung Redis costs one bounded pause, not a hung request.
//   - A short circuit breaker stops even that: after cacheBreakerThreshold
//     consecutive failures the cache switches itself off for cacheBreakerCooldown,
//     so a Redis that is down or pathologically slow costs ~0 per request instead
//     of 2s per request, and recovers on its own.
//   - No Redis error is ever written to an HTTP response. The only outward sign
//     of a broken cache is a server log line and the original (uncached) latency.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// cacheEngineVersion is the SCORING-ENGINE version stamped into every cache key
// (the "v1" in sc:v1:<hash>). It is the single knob that invalidates the whole
// cache; nothing else in this file needs to change.
//
// ===> BUMP IT ("v1" -> "v2" -> ...) IN THE SAME COMMIT AS ANY CHANGE THAT CAN
// ===> MOVE A SCORE, A STANCE, A VERDICT, A LABEL, OR THE STUDY SET BEHIND THEM.
//
// In practice that means: any rebuild of bin/scientific-consensus-pp-cli-linux
// from a CLI revision that touches the scoring/stance/relevance engine, the
// design classifier, the retraction or corpus data, or the shape of the JSON the
// UI reads. When in doubt, bump — a needless bump costs one cold fetch per
// query, a missing bump serves verdicts we have already decided are wrong.
//
// This is not hypothetical. CLI commit 8bb22658f rewrote 14 works' labels and
// five corpus scores. With an unversioned key, every cached entry would have
// kept serving the old, known-wrong verdicts for the full 7-day TTL, and no
// redeploy would have fixed it: the image changes, the cache does not. The
// version lives in the key, so bumping it strands the old entries instantly and
// LRU reclaims them.
const cacheEngineVersion = "v1"

// The manual constant above is a promise to remember; the automatic identifier
// below is what happens when the promise is broken. cacheEngineVersion still has
// a job no hash can do — the WEB layer's scoring logic (divergence rules,
// compaction, prompt) lives in this module, and changing it does not change the
// CLI binary at all. So the two are additive, and the key carries both:
//
//	sc:<engine>:<clihash>:<paramhash>
//
// clihash is the first cliHashPrefixLen hex digits of the sha256 of the CLI
// binary this process actually shells out to (cliBinaryPath()). The binary is
// committed under bin/, so it is a deterministic build artefact with a stable
// identity: swapping it — the single most likely way a verdict silently moves —
// re-keys the entire cache with no human step and no deploy note.
//
// Measured on this repo's binaries: bin/scientific-consensus-pp-cli.exe hashes
// to 04b5c539f684 and bin/scientific-consensus-pp-cli-linux to b659fff65cb9, so
// two builds of the same CLI really do land on different prefixes. The full
// sha256 is truncated to 12 hex digits (48 bits) because this is a
// cache-invalidation tag, not a security boundary: an accidental collision
// between two builds needs ~2^24 rebuilds to become likely, and the paramhash is
// still a full sha256 that includes the same 12 digits.
const cliHashPrefixLen = 12

// cliHashUnavailable is the clihash segment used when the binary cannot be
// hashed. It is deliberately not valid hex, so it can never be mistaken for a
// real prefix, and it is deliberately not empty, so the key keeps one shape
// (four colon-separated fields) for greps and for the length bound.
//
// Reaching this state costs correctness in one narrow way — entries keyed under
// it are not invalidated by a binary swap — and that is the accepted trade: the
// soft-dependency rule in this file's header says a cache concern never blocks a
// request, and by extension never blocks startup. An unreadable binary is
// already a fatal problem for the CLI itself; the cache must not be the
// component that reports it.
const cliHashUnavailable = "nohash"

// cacheKeyMaxLen is the length ceiling asserted by TestCacheKeyLengthIsBounded.
// The key is bounded by construction — claims are hashed, never concatenated —
// so this is a guard against a future field being appended raw, not against long
// input. Real keys measure 83 characters.
const cacheKeyMaxLen = 200

// cliEngineHashSlot holds the process-wide clihash, written once by
// initCLIEngineHash before the server accepts a request and read by every
// cacheKey call after that. Atomic rather than a plain string because tests
// substitute values while other goroutines (the async Set path) may still be
// reading, and a torn read here would be a wrong cache key.
var cliEngineHashSlot atomic.Pointer[string]

// cliEngineHash returns the clihash segment for the key, or cliHashUnavailable
// when nothing has been computed. The zero state resolving to a valid segment is
// what lets cacheKey work in tests and in any code path that runs before main()
// has initialized it.
func cliEngineHash() string {
	if p := cliEngineHashSlot.Load(); p != nil && *p != "" {
		return *p
	}
	return cliHashUnavailable
}

// cliBinaryHash returns the first cliHashPrefixLen hex digits of the sha256 of
// the file at path. It streams (io.Copy over a 32KB window) instead of reading
// the file in: the binary is ~22MB and there is no reason to hold that in the
// heap of a long-lived server for the 50ms it is needed.
func cliBinaryHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:cliHashPrefixLen], nil
}

// initCLIEngineHash computes the clihash once, at startup, and logs the key
// prefix it produces next to the one it supersedes.
//
// Synchronous on purpose, unlike probeAsync. Hashing the committed binary is a
// bounded local read — measured at 197ms cold and 56ms warm on a 22.6MB binary —
// and it must finish before the first request, or that request keys under
// cliHashUnavailable and strands an entry nothing will ever read again. It also
// cannot fail in a way that matters: any error resolves to the fallback marker
// plus a log line, so a missing or unreadable binary delays startup by nothing
// and stops nothing. Doing this per request instead would add that same 56ms to
// every lookup — 17x the 3.3ms a Redis hit costs — which is the whole reason it
// lives here.
func initCLIEngineHash() {
	path := cliBinaryPath()
	start := time.Now()
	sum, err := cliBinaryHash(path)
	if err != nil {
		log.Printf("cache: cannot hash CLI binary %s (%v) — key prefix falls back to sc:%s:%s:, "+
			"so a binary swap will NOT invalidate the cache on its own; bump cacheEngineVersion by hand",
			path, err, cacheEngineVersion, cliHashUnavailable)
		return
	}
	cliEngineHashSlot.Store(&sum)

	// The measurement line: what the key used to look like, what it looks like
	// now, and how long the key actually is against its ceiling.
	sample := cacheKey("consensus", 10, []string{"vitamin D reduces respiratory infections"})
	log.Printf("cache: CLI binary %s hashed in %s — key prefix was sc:%s: and is now sc:%s:%s: "+
		"(sample key %d chars, ceiling %d)",
		path, time.Since(start).Round(time.Millisecond), cacheEngineVersion,
		cacheEngineVersion, sum, len(sample), cacheKeyMaxLen)
}

const (
	// cacheTTL is how long one CLI payload stays valid. 7 days: the literature
	// behind a claim does not move meaningfully faster than that, and the CLI's
	// own on-disk HTTP cache (5 minutes, in the container filesystem) is wiped
	// by every image replacement — this layer is what survives a redeploy.
	cacheTTL = 7 * 24 * time.Hour

	// cacheDialTimeout / cacheIOTimeout bound every Redis interaction. Both are
	// deliberately short: the fallback (run the CLI) is known to work, so waiting
	// on Redis is never worth more than a couple of seconds.
	cacheDialTimeout = 2 * time.Second
	cacheIOTimeout   = 2 * time.Second

	// cacheMaxValueBytes skips absurd payloads instead of pushing them at a
	// 512MB-maxmemory instance where they would evict everything useful. A
	// consensus payload is ~20-200KB; 8MB is far above any real one.
	cacheMaxValueBytes = 8 << 20

	// cacheMaxIdleConns caps the idle connection pool. Reusing connections keeps a
	// cache hit at one round trip instead of dial+auth+round trip.
	cacheMaxIdleConns = 8

	// cacheBreakerThreshold / cacheBreakerCooldown define the circuit breaker: N
	// consecutive failures switch the cache off for a cooldown, so a dead or
	// crawling Redis stops costing a timeout on every single request.
	cacheBreakerThreshold = 3
	cacheBreakerCooldown  = 30 * time.Second

	// cacheMaxLineBytes caps one RESP protocol line, so a misbehaving or
	// non-Redis listener on the port cannot make us read without bound.
	cacheMaxLineBytes = 512
)

// cacheStore is the process-wide cache. nil means "no cache configured", which
// every method below treats as a silent no-op — so the zero state is the
// pre-cache behaviour, not a crash.
var cacheStore *redisCache

// redisCache is a minimal, dependency-free Redis client: GET, SET ... EX, PING,
// AUTH over RESP2. Hand-rolled on purpose — the web module is stdlib-only (no
// go.sum, and the Dockerfile's build stage never runs `go mod download`), so a
// client library would mean a new dependency, a lockfile and a build change for
// four commands.
type redisCache struct {
	addr     string
	password string

	mu   sync.Mutex
	idle []*cacheConn

	// wg tracks in-flight asynchronous writes; only tests wait on it.
	wg sync.WaitGroup

	hits     atomic.Int64
	misses   atomic.Int64
	failures atomic.Int64

	consecFails   atomic.Int64
	disabledUntil atomic.Int64 // unix nanos; 0 = enabled
}

// newRedisCacheFromEnv builds the cache from the environment, or returns nil
// when caching is not configured or has been switched off. It does NOT connect:
// nothing here can fail, so nothing here can delay or break startup.
//
//	REDIS_ADDR      host:port (e.g. redis:6379). Empty => no cache.
//	REDIS_PASSWORD  optional; sent with AUTH on each new connection.
//	CACHE_DISABLED  set to 1/true/yes => no cache, without touching REDIS_ADDR.
func newRedisCacheFromEnv() *redisCache {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CACHE_DISABLED"))) {
	case "1", "true", "yes", "on":
		log.Printf("cache: disabled by CACHE_DISABLED, serving every request uncached")
		return nil
	}
	addr := strings.TrimSpace(os.Getenv("REDIS_ADDR"))
	if addr == "" {
		log.Printf("cache: REDIS_ADDR not set, serving every request uncached")
		return nil
	}
	return &redisCache{addr: addr, password: strings.TrimSpace(os.Getenv("REDIS_PASSWORD"))}
}

// probeAsync logs, in the background, whether Redis answers. It exists so the
// operator sees the cache state in the startup log; it never blocks the caller
// and its outcome changes nothing (a failed probe does not disable the cache —
// the next request tries again).
func (c *redisCache) probeAsync() {
	if c == nil {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("cache: probe panicked (%v), continuing without cache", r)
			}
		}()
		if err := c.ping(); err != nil {
			log.Printf("cache: redis %s did not answer PING (%s) — requests run uncached until it does", c.addr, c.safeErr(err))
			return
		}
		log.Printf("cache: redis %s ready (engine=%s, cli=%s, ttl=%s)", c.addr, cacheEngineVersion, cliEngineHash(), cacheTTL)
	}()
}

// Get returns the cached payload for key. The second result is false for every
// miss AND every failure — callers cannot distinguish them, which is the point:
// there is exactly one fallback path and it is the normal one.
func (c *redisCache) Get(ctx context.Context, key string) (val []byte, hit bool) {
	if !c.usable() {
		return nil, false
	}
	// Belt and braces: a panic in the cache layer must not take down a request
	// that would otherwise have succeeded.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("cache: GET panicked (%v), treating as miss", r)
			val, hit = nil, false
		}
	}()

	reply, isNil, err := c.command(ctx, []byte("GET"), []byte(key))
	if err != nil {
		c.recordFailure("GET", err)
		return nil, false
	}
	c.recordSuccess()
	if isNil {
		c.misses.Add(1)
		c.logOutcome("miss", key)
		return nil, false
	}
	c.hits.Add(1)
	c.logOutcome("hit", key)
	return reply, true
}

// Set stores val under key with cacheTTL, asynchronously. Writing in the
// background is deliberate: a slow Redis must not add its latency to a response
// the caller already has in hand. Failures are counted and logged, never
// returned — there is nothing a caller could do about them.
func (c *redisCache) Set(key string, val []byte) {
	if !c.usable() || len(val) == 0 || len(val) > cacheMaxValueBytes {
		return
	}
	// Copy: the caller's buffer may be reused once this returns.
	stored := make([]byte, len(val))
	copy(stored, val)

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		// A panic in a goroutine kills the process, so this recover is not
		// optional — it is what keeps a cache bug from being an outage.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("cache: SET panicked (%v), entry not cached", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), cacheDialTimeout+cacheIOTimeout)
		defer cancel()
		ttl := strconv.FormatInt(int64(cacheTTL/time.Second), 10)
		if _, _, err := c.command(ctx, []byte("SET"), []byte(key), stored, []byte("EX"), []byte(ttl)); err != nil {
			c.recordFailure("SET", err)
			return
		}
		c.recordSuccess()
	}()
}

// ping issues one PING. Used only by probeAsync and by tests.
func (c *redisCache) ping() error {
	if c == nil {
		return errors.New("no cache configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), cacheDialTimeout+cacheIOTimeout)
	defer cancel()
	_, _, err := c.command(ctx, []byte("PING"))
	return err
}

// stats reports the counters behind the hit-rate log line.
func (c *redisCache) stats() (hits, misses, failures int64) {
	if c == nil {
		return 0, 0, 0
	}
	return c.hits.Load(), c.misses.Load(), c.failures.Load()
}

// waitForWrites blocks until in-flight asynchronous Sets finish. Tests only —
// request handling never waits for a write.
func (c *redisCache) waitForWrites() {
	if c == nil {
		return
	}
	c.wg.Wait()
}

// ---- availability / circuit breaker -----------------------------------------

// usable reports whether it is worth touching Redis at all: a nil cache never
// is, and neither is one inside its post-failure cooldown.
func (c *redisCache) usable() bool {
	if c == nil {
		return false
	}
	until := c.disabledUntil.Load()
	return until == 0 || time.Now().UnixNano() >= until
}

// recordFailure counts one failed operation and, once failures pile up, opens
// the breaker so the next requests skip Redis entirely instead of each paying a
// timeout. The failure is logged once per trip, not once per request, so a dead
// Redis cannot flood the log either.
func (c *redisCache) recordFailure(op string, err error) {
	c.failures.Add(1)
	if c.consecFails.Add(1) < cacheBreakerThreshold {
		return
	}
	c.consecFails.Store(0)
	c.disabledUntil.Store(time.Now().Add(cacheBreakerCooldown).UnixNano())
	log.Printf("cache: %s to redis %s failed (%s) — cache off for %s, requests continue uncached",
		op, c.addr, c.safeErr(err), cacheBreakerCooldown)
}

// recordSuccess closes the breaker's failure streak.
func (c *redisCache) recordSuccess() {
	c.consecFails.Store(0)
	c.disabledUntil.Store(0)
}

// safeErr renders an error for a log line with the Redis password removed.
// Redis does not echo credentials, but a wrapped dial/URL error is exactly the
// kind of string that could, and redact() is cheap.
func (c *redisCache) safeErr(err error) string {
	if err == nil {
		return "<nil>"
	}
	return redact(err.Error(), c.password)
}

// logOutcome emits the per-lookup hit/miss line with the running hit rate. The
// key is a hash, so this is safe to log; the claim behind it never is.
func (c *redisCache) logOutcome(outcome, key string) {
	hits, misses, failures := c.stats()
	rate := 0.0
	if total := hits + misses; total > 0 {
		rate = 100 * float64(hits) / float64(total)
	}
	log.Printf("cache: %s %s (hits=%d misses=%d failures=%d hit-rate=%.1f%%)",
		outcome, key, hits, misses, failures, rate)
}

// ---- key construction --------------------------------------------------------

// cacheKey returns the Redis key for one query:
// sc:<engine>:<clihash>:<sha256(params)>.
//
// Two independent invalidation handles, for two independent ways a verdict
// moves: <engine> is bumped by hand when this module's scoring logic changes,
// <clihash> changes by itself when the CLI binary does. Both appear twice — in
// the prefix, so entries from a superseded engine or binary are visible and
// greppable, and inside the hash, so it is impossible to collide across them.
func cacheKey(endpoint string, limit int, claims []string) string {
	return cacheKeyWithVersion(cacheEngineVersion, endpoint, limit, claims)
}

// cacheKeyWithVersion is cacheKey with an explicit engine version, so tests can
// prove that bumping the version really does strand old entries.
func cacheKeyWithVersion(version, endpoint string, limit int, claims []string) string {
	return cacheKeyFull(version, cliEngineHash(), endpoint, limit, claims)
}

// cacheKeyFull is the key builder with both invalidation handles passed in, so a
// test can prove that a different binary yields a different key without having to
// replace the running process's binary.
func cacheKeyFull(version, cliHash, endpoint string, limit int, claims []string) string {
	// Normalized parameter form: fixed field order, one field per line, length-
	// prefixed claims. The length prefix is what stops two different claim lists
	// from serializing to the same bytes.
	var b strings.Builder
	b.WriteString("engine=" + version)
	b.WriteString("\ncli=" + cliHash)
	b.WriteString("\nendpoint=" + endpoint)
	b.WriteString("\nlimit=" + strconv.Itoa(limit))
	for i, claim := range claims {
		n := normalizeClaim(claim)
		fmt.Fprintf(&b, "\nclaim%d=%d:%s", i, len(n), n)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "sc:" + version + ":" + cliHash + ":" + hex.EncodeToString(sum[:])
}

// normalizeClaim is the claim normalization used for BOTH the cache key and the
// argument handed to the CLI. Using one form for both is what guarantees a hit
// is byte-identical to the miss it replaces: the CLI echoes the claim back in
// its JSON, so keying on a normalized claim while running the CLI on the raw one
// would let a cached response come back with someone else's spacing.
//
// The normalization itself is whitespace-only (collapse runs of any whitespace
// to single spaces, trim the ends), which OpenAlex's search parameter already
// treats as equivalent. Case is deliberately NOT folded: the claim is echoed to
// the user, and "Vitamin D" must not come back as "vitamin d".
func normalizeClaim(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ---- minimal RESP2 client ----------------------------------------------------

// cacheConn is one Redis connection plus its read buffer. The buffer must travel
// with the connection: it can hold bytes of a reply we have not parsed yet, so
// pairing a pooled connection with a fresh reader would silently lose them.
type cacheConn struct {
	nc     net.Conn
	br     *bufio.Reader
	pooled bool // came from the idle pool, so a dead-socket error is expected and retryable
}

func (cc *cacheConn) close() {
	if cc != nil && cc.nc != nil {
		_ = cc.nc.Close()
	}
}

// command runs one Redis command and returns (payload, isNil, error).
//
// A connection taken from the pool may have been closed by Redis while it sat
// idle, which surfaces as an I/O error on first use rather than at dial time.
// That case is retried exactly once on a fresh connection, so an idle timeout on
// the server never shows up as a spurious miss or a breaker failure.
func (c *redisCache) command(ctx context.Context, args ...[]byte) ([]byte, bool, error) {
	for attempt := 0; attempt < 2; attempt++ {
		cc, err := c.acquire(ctx)
		if err != nil {
			return nil, false, err
		}
		val, isNil, err := cc.roundTrip(ctx, args...)
		if err == nil {
			c.release(cc)
			return val, isNil, nil
		}
		retryable := cc.pooled
		cc.close() // never reuse a connection whose stream may be desynced
		if !retryable {
			return nil, false, err
		}
	}
	return nil, false, errors.New("redis: connection unusable after retry")
}

// roundTrip writes one command and reads one reply, under a deadline that is the
// sooner of cacheIOTimeout and the caller's context deadline.
func (cc *cacheConn) roundTrip(ctx context.Context, args ...[]byte) ([]byte, bool, error) {
	if err := cc.nc.SetDeadline(ioDeadline(ctx)); err != nil {
		return nil, false, err
	}
	if err := cc.writeCommand(args...); err != nil {
		return nil, false, err
	}
	return cc.readReply()
}

// ioDeadline is now+cacheIOTimeout, clamped to the caller's deadline so a
// cancelled request is not held open by the cache.
func ioDeadline(ctx context.Context) time.Time {
	d := time.Now().Add(cacheIOTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(d) {
		return ctxDeadline
	}
	return d
}

// acquire returns a pooled connection, or dials (and authenticates) a new one.
func (c *redisCache) acquire(ctx context.Context) (*cacheConn, error) {
	c.mu.Lock()
	if n := len(c.idle); n > 0 {
		cc := c.idle[n-1]
		c.idle = c.idle[:n-1]
		c.mu.Unlock()
		cc.pooled = true
		return cc, nil
	}
	c.mu.Unlock()
	return c.dial(ctx)
}

// release returns a healthy connection to the pool, closing it when the pool is
// already full. Only connections whose last reply parsed cleanly get here.
func (c *redisCache) release(cc *cacheConn) {
	cc.pooled = false
	c.mu.Lock()
	if len(c.idle) < cacheMaxIdleConns {
		c.idle = append(c.idle, cc)
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	cc.close()
}

// dial opens a connection and, when a password is configured, authenticates it
// before it is ever used or pooled.
func (c *redisCache) dial(ctx context.Context) (*cacheConn, error) {
	dialer := net.Dialer{Timeout: cacheDialTimeout}
	dialCtx, cancel := context.WithTimeout(ctx, cacheDialTimeout)
	defer cancel()
	nc, err := dialer.DialContext(dialCtx, "tcp", c.addr)
	if err != nil {
		return nil, err
	}
	cc := &cacheConn{nc: nc, br: bufio.NewReader(nc)}
	if c.password != "" {
		if _, _, err := cc.roundTrip(ctx, []byte("AUTH"), []byte(c.password)); err != nil {
			cc.close()
			return nil, err
		}
	}
	return cc, nil
}

// writeCommand emits one RESP2 array of bulk strings. Built into a single buffer
// and written once, so a command is one syscall and cannot be interleaved.
func (cc *cacheConn) writeCommand(args ...[]byte) error {
	size := 16
	for _, a := range args {
		size += len(a) + 16
	}
	buf := make([]byte, 0, size)
	buf = append(buf, '*')
	buf = strconv.AppendInt(buf, int64(len(args)), 10)
	buf = append(buf, '\r', '\n')
	for _, a := range args {
		buf = append(buf, '$')
		buf = strconv.AppendInt(buf, int64(len(a)), 10)
		buf = append(buf, '\r', '\n')
		buf = append(buf, a...)
		buf = append(buf, '\r', '\n')
	}
	_, err := cc.nc.Write(buf)
	return err
}

// readReply parses one RESP2 reply: simple string, integer, error, or bulk
// string (including the $-1 null that means "no such key"). Anything else is an
// error, which the caller turns into a miss — we speak exactly the four commands
// this cache uses and treat everything unexpected as "cache unavailable".
func (cc *cacheConn) readReply() ([]byte, bool, error) {
	line, err := cc.readLine()
	if err != nil {
		return nil, false, err
	}
	if len(line) == 0 {
		return nil, false, errors.New("redis: empty reply")
	}
	switch line[0] {
	case '+', ':':
		return line[1:], false, nil
	case '-':
		return nil, false, errors.New("redis replied: " + string(line[1:]))
	case '$':
		n, err := strconv.Atoi(string(line[1:]))
		if err != nil {
			return nil, false, errors.New("redis: bad bulk length")
		}
		if n < 0 {
			return nil, true, nil // key absent or expired
		}
		if n > cacheMaxValueBytes {
			return nil, false, fmt.Errorf("redis: bulk reply too large (%d bytes)", n)
		}
		buf := make([]byte, n+2) // payload + trailing CRLF
		if _, err := io.ReadFull(cc.br, buf); err != nil {
			return nil, false, err
		}
		return buf[:n], false, nil
	default:
		return nil, false, fmt.Errorf("redis: unsupported reply type %q", line[0])
	}
}

// readLine reads one CRLF-terminated protocol line, bounded by
// cacheMaxLineBytes so a non-Redis listener cannot stream at us forever.
func (cc *cacheConn) readLine() ([]byte, error) {
	line, err := cc.br.ReadSlice('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) || len(line) > cacheMaxLineBytes {
			return nil, errors.New("redis: protocol line too long")
		}
		return nil, err
	}
	if len(line) > cacheMaxLineBytes {
		return nil, errors.New("redis: protocol line too long")
	}
	return bytes.TrimRight(line, "\r\n"), nil
}
