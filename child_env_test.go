package main

import (
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

// The stub CLIs in cache_test.go, semaphore_test.go and singleflight_test.go
// take their settings from these variables. They exist only in tests, so they
// are added to the allowlist only in the test binary; the production
// allowlist in main.go stays exactly what the real CLI needs.
var testStubEnvVars = []string{
	"STUB_CLI_COUNTER",
	"CONC_STUB_LOG", "CONC_STUB_DELAY",
	"SLOW_STUB_COUNTER", "SLOW_STUB_DELAY", "SLOW_STUB_FAIL",
}

func init() { childEnvAllowlist = append(childEnvAllowlist, testStubEnvVars...) }

// envNames returns the variable names in an environment list, sorted.
func envNames(env []string) []string {
	var names []string
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			names = append(names, kv[:i])
		}
	}
	sort.Strings(names)
	return names
}

// TestFilterChildEnvAllowlist: only listed variables survive, values and
// order are kept, and the server's secrets never reach the child.
func TestFilterChildEnvAllowlist(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"REDIS_PASSWORD=redis-secret",
		"HOME=/home/app",
		"INTERNAL_RECORD_TOKEN=record-secret",
		"SUPABASE_PUBLISHABLE_KEY=sb-key",
		"DEEPSEEK_API_KEY=sk-server",
		"ANTHROPIC_API_KEY=sk-ant",
		"SCIENTIFIC_CONSENSUS_BASE_URL=http://evil.example",
		"NCBI_API_KEY=ncbi",
		"FUTURE_SECRET=x",
		"HTTPS_PROXY=http://proxy:3128",
		"=C:=C:\\odd-windows-entry",
		"NOEQUALS",
	}
	got := filterChildEnv(base)
	want := []string{"PATH=/usr/bin", "HOME=/home/app", "NCBI_API_KEY=ncbi", "HTTPS_PROXY=http://proxy:3128"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("filterChildEnv:\n got  %q\n want %q", got, want)
	}
	joined := strings.Join(got, "\n")
	for _, secret := range []string{"redis-secret", "record-secret", "sb-key", "sk-server", "sk-ant", "evil.example", "FUTURE_SECRET"} {
		if strings.Contains(joined, secret) {
			t.Errorf("child env leaks %q", secret)
		}
	}
}

// TestChildEnvAllowlistHasNoProviderKey: no provider key may ever be on the
// allowlist, and the deny check still removes one if it were.
func TestChildEnvAllowlistHasNoProviderKey(t *testing.T) {
	for _, a := range childEnvAllowlist {
		for _, p := range allProviderEnvVars {
			if a == p {
				t.Errorf("provider key %s is on childEnvAllowlist", a)
			}
		}
	}
	saved := childEnvAllowlist
	defer func() { childEnvAllowlist = saved }()
	childEnvAllowlist = append(append([]string{}, saved...), serverKeyEnvVar)
	if got := filterChildEnv([]string{serverKeyEnvVar + "=sk-server"}); len(got) != 0 {
		t.Fatalf("deny list did not catch an allowlisted provider key: %q", got)
	}
}

// TestBuildChildEnvRealProcess: set secrets in this process, start a real
// child with buildChildEnv() exactly as runCLI does, and read the child's
// own environment back.
func TestBuildChildEnvRealProcess(t *testing.T) {
	envBin, err := exec.LookPath("env")
	if err != nil {
		t.Skip("no env binary on this system")
	}
	t.Setenv("REDIS_PASSWORD", "redis-secret")
	t.Setenv("INTERNAL_RECORD_TOKEN", "record-secret")
	t.Setenv(serverKeyEnvVar, "sk-server")
	t.Setenv("NCBI_API_KEY", "ncbi")
	t.Setenv("TZ", "UTC")
	cmd := exec.Command(envBin)
	cmd.Env = buildChildEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("child: %v", err)
	}
	names := envNames(strings.Split(strings.TrimSpace(string(out)), "\n"))
	allow := map[string]bool{}
	for _, a := range childEnvAllowlist {
		allow[a] = true
	}
	for _, n := range names {
		if !allow[n] {
			t.Errorf("child saw %s, which is not on the allowlist", n)
		}
	}
	s := string(out)
	for _, secret := range []string{"redis-secret", "record-secret", "sk-server"} {
		if strings.Contains(s, secret) {
			t.Errorf("child env leaks %q", secret)
		}
	}
	if !strings.Contains(s, "NCBI_API_KEY=ncbi") || !strings.Contains(s, "TZ=UTC") {
		t.Errorf("allowlisted variables missing in child: %s", s)
	}
	if os.Getenv("PATH") != "" && !strings.Contains(s, "PATH=") {
		t.Errorf("PATH not passed to child")
	}
}
