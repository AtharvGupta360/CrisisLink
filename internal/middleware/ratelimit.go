package middleware

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"github.com/AtharvGupta360/CrisisLink/internal/common"
)

// RateLimiterConfig tunes the token bucket.
type RateLimiterConfig struct {
	RequestsPerSecond float64 // sustained rate: tokens refilled per second
	BurstSize         int     // bucket capacity: max tokens (absorbs short spikes)
}

// ipRateLimiter keeps one token-bucket limiter PER client IP. NOTE: this state
// is in-memory and per-process — with multiple replicas the effective limit is
// N x. Distributed rate limiting needs a shared store (Redis) with atomic counts.
type ipRateLimiter struct {
	limiters map[string]*rate.Limiter
	mu       sync.RWMutex
	rps      rate.Limit
	burst    int
}

// getLimiter returns the limiter for an IP, creating it on first sight. Read-lock
// for the common (exists) path, write-lock only to insert.
func (rl *ipRateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.RLock()
	limiter, exists := rl.limiters[ip]
	rl.mu.RUnlock()
	if exists {
		return limiter
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()
	// Re-check: another goroutine may have created it between the RUnlock and Lock.
	if limiter, exists = rl.limiters[ip]; exists {
		return limiter
	}
	limiter = rate.NewLimiter(rl.rps, rl.burst)
	rl.limiters[ip] = limiter
	return limiter
}

// RateLimiter rejects requests from an IP that exceeds its token bucket with 429.
func RateLimiter(cfg RateLimiterConfig) gin.HandlerFunc {
	limiter := &ipRateLimiter{
		limiters: make(map[string]*rate.Limiter),
		rps:      rate.Limit(cfg.RequestsPerSecond),
		burst:    cfg.BurstSize,
	}

	return func(c *gin.Context) {
		ip := c.ClientIP()
		if !limiter.getLimiter(ip).Allow() {
			common.Logger.Warnf("rate limit exceeded for IP: %s", ip)
			common.Error(c, http.StatusTooManyRequests, "rate limit exceeded", "RATE_LIMITED")
			c.Abort()
			return
		}
		c.Next()
	}
}
