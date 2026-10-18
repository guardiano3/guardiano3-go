package client

import (
	"math/rand"
	"time"
)

// RetryPolicy decides what is safe to repeat. Same rules as the Python and
// TypeScript cores, deliberately.
//
//   - Only 429 and 502/503/504 are retried. A 400 or a 404 will fail again;
//     retrying wastes the caller's time and the API's capacity. A 500 is not
//     retried either -- the API returns it for an unexpected fault, not a
//     transient one, and hammering it makes an incident worse.
//   - Retry-After is honoured. The server knows when its window resets;
//     guessing produces either a wasted wait or a second refusal.
//   - Backoff is jittered. Without jitter every client refused at the same
//     instant retries at the same instant and re-creates the overload the
//     limit existed to prevent.
type RetryPolicy struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Multiplier     float64
	// Jitter is the fraction of the computed delay randomised in each direction.
	Jitter float64
	// rand is injectable so the tests can assert bounds rather than flake.
	rand func() float64
}

// DefaultRetryPolicy matches the other SDKs: 3 attempts, 0.5s doubling to 20s,
// +/-25%.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:    3,
		InitialBackoff: 500 * time.Millisecond,
		MaxBackoff:     20 * time.Second,
		Multiplier:     2,
		Jitter:         0.25,
	}
}

var retryableStatuses = map[int]bool{429: true, 502: true, 503: true, 504: true}

// nonRetryableCodes: a daily allowance does not clear by waiting 400ms, so an
// SDK must not sit in a loop until midnight. Retrying it burns the caller's
// remaining budget on requests that cannot succeed.
var nonRetryableCodes = map[string]bool{"QUOTA_EXCEEDED": true}

// ShouldRetry reports whether attempt (1-based) may be repeated.
func (p RetryPolicy) ShouldRetry(attempt, status int, code string) bool {
	if attempt >= p.MaxAttempts {
		return false
	}
	if nonRetryableCodes[code] {
		return false
	}
	return retryableStatuses[status]
}

// Backoff is how long to wait before the given attempt.
func (p RetryPolicy) Backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > p.MaxBackoff {
			return p.MaxBackoff
		}
		return retryAfter
	}

	base := float64(p.InitialBackoff)
	for i := 1; i < attempt; i++ {
		base *= p.Multiplier
	}
	if base > float64(p.MaxBackoff) {
		base = float64(p.MaxBackoff)
	}

	random := p.rand
	if random == nil {
		random = rand.Float64
	}
	delay := base + (random()*2-1)*base*p.Jitter
	if delay < 0 {
		delay = 0
	}
	return time.Duration(delay)
}
