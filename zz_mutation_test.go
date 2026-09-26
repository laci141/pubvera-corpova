package main

import "testing"

// Control mutation for the pipefail change: this test always fails, so the
// "Test with race detector" step must turn red on its own. Reverted before
// merge.
func TestPipefailControlMutation(t *testing.T) {
	t.Fatal("control mutation: this test must fail")
}
