package main

import (
	"reflect"
	"testing"
)

func TestClaimArgsEndFlagParsingBeforeClaims(t *testing.T) {
	got := claimArgs("consensus", 10, "--help")
	want := []string{"consensus", "--json", "--limit", "10", "--", "--help"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("claimArgs = %q, want %q", got, want)
	}
	got = claimArgs("compare", 40, "-80C is fine", "--no-cache")
	want = []string{"compare", "--json", "--limit", "40", "--", "-80C is fine", "--no-cache"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("claimArgs compare = %q, want %q", got, want)
	}
}

func TestWithPacingArgsGoesBeforeSeparator(t *testing.T) {
	in := claimArgs("consensus", 10, "--")
	got := withPacingArgs(in)
	want := []string{"consensus", "--json", "--limit", "10", "--rate-limit", "0.15", "--timeout", "100s", "--", "--"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("withPacingArgs = %q, want %q", got, want)
	}
	if len(in) != 6 {
		t.Errorf("input modified: %q", in)
	}
	// No separator: appended, like before.
	got = withPacingArgs([]string{"x", "--json"})
	want = []string{"x", "--json", "--rate-limit", "0.15", "--timeout", "100s"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("no separator = %q, want %q", got, want)
	}
}
