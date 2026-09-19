package main

import (
	"testing"
	"time"
)

// The server timeouts are constants, so the thing worth guarding is not that
// http.Server honours them — the standard library does that — but the one
// relationship between them that can be got wrong.
//
// WriteTimeout covers the whole response. A request is allowed requestBudget to
// produce it, so a WriteTimeout at or below requestBudget does not stop attacks:
// it cuts off legitimate slow analyses, and it does so intermittently, only on
// the runs that take longest. That failure is much harder to diagnose than the
// slow-body exposure these timeouts exist to close.

func TestWriteTimeoutOutlastsTheRequestBudget(t *testing.T) {
	if srvWriteTimeout <= requestBudget {
		t.Fatalf("srvWriteTimeout = %s, requestBudget = %s: the server would abort its own\nresponse while the request budget has not yet expired", srvWriteTimeout, requestBudget)
	}

	// Room to write the response after the request has used its full budget. A
	// margin this size is arbitrary in its exact value but not in its
	// presence: without one, the two deadlines race.
	const minMargin = 15 * time.Second
	if margin := srvWriteTimeout - requestBudget; margin < minMargin {
		t.Errorf("margin over requestBudget = %s, want at least %s", margin, minMargin)
	}
}

// TestRequestTimeoutsAreSet is the regression guard for the original gap:
// ReadHeaderTimeout was set on its own, which bounded the headers and left the
// body unbounded. Each of these being non-zero is what closes that.
func TestRequestTimeoutsAreSet(t *testing.T) {
	cases := []struct {
		name  string
		value time.Duration
	}{
		{"ReadHeaderTimeout", srvReadHeaderTimeout},
		{"ReadTimeout", srvReadTimeout},
		{"WriteTimeout", srvWriteTimeout},
		{"IdleTimeout", srvIdleTimeout},
	}
	for _, tc := range cases {
		if tc.value <= 0 {
			t.Errorf("%s = %s, want a positive deadline: zero means no limit", tc.name, tc.value)
		}
	}
}

// TestReadTimeoutCoversTheHeaderTimeout keeps the two read deadlines coherent.
// ReadTimeout measures from the start of the connection and therefore includes
// the header phase; a ReadTimeout below ReadHeaderTimeout would make the
// header deadline unreachable and silently dead.
func TestReadTimeoutCoversTheHeaderTimeout(t *testing.T) {
	if srvReadTimeout <= srvReadHeaderTimeout {
		t.Errorf("srvReadTimeout = %s is not greater than srvReadHeaderTimeout = %s:\nthe header deadline could never fire", srvReadTimeout, srvReadHeaderTimeout)
	}
}
