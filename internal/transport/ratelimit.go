package transport

import (
	"context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sync"
	"time"
)

type bucket struct {
	tokens  float64
	updated time.Time
}
type RateLimiter struct {
	mu          sync.Mutex
	rate, burst float64
	buckets     map[string]bucket
	now         func() time.Time
}

func NewRateLimiter(requestsPerSecond, burst int) *RateLimiter {
	if requestsPerSecond < 1 {
		requestsPerSecond = 1
	}
	if burst < requestsPerSecond {
		burst = requestsPerSecond
	}
	return &RateLimiter{rate: float64(requestsPerSecond), burst: float64(burst), buckets: map[string]bucket{}, now: time.Now}
}
func (r *RateLimiter) allow(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	value, ok := r.buckets[name]
	if !ok {
		value = bucket{tokens: r.burst, updated: now}
	}
	elapsed := now.Sub(value.updated).Seconds()
	value.tokens += elapsed * r.rate
	if value.tokens > r.burst {
		value.tokens = r.burst
	}
	value.updated = now
	if value.tokens < 1 {
		r.buckets[name] = value
		return false
	}
	value.tokens--
	r.buckets[name] = value
	return true
}
func (r *RateLimiter) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		principal, ok := PrincipalFromContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "principal unavailable to rate limiter")
		}
		if !r.allow(principal.Name) {
			return nil, status.Error(codes.ResourceExhausted, "principal request rate exceeded")
		}
		return handler(ctx, request)
	}
}
