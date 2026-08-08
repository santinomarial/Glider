package transport

import (
	"testing"
	"time"
)

func TestRateLimiterIsPerPrincipalAndRefills(t *testing.T) {
	limiter := NewRateLimiter(2, 2)
	now := time.Unix(1, 0)
	limiter.now = func() time.Time { return now }
	if !limiter.allow("a") || !limiter.allow("a") || limiter.allow("a") {
		t.Fatal("burst not enforced")
	}
	if !limiter.allow("b") {
		t.Fatal("principals share a bucket")
	}
	now = now.Add(time.Second)
	if !limiter.allow("a") || !limiter.allow("a") {
		t.Fatal("bucket did not refill")
	}
}
