package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/metrics"
)

// tokenBucketScript is a TOKEN BUCKET evaluated atomically inside Redis.
//
// Why Lua? The naive limiter is GET count -> check -> SET count+1, which is a
// check-then-act race: with several API replicas, two requests can both read 9,
// both decide "allowed", and both write 10. Same bug class as the double-booking
// race in P13 — the read and the write must be one indivisible step.
//
// Redis executes a script single-threaded, start to finish, with nothing
// interleaved. So the refill + check + decrement below is atomic across every
// replica, without any lock.
//
// Token bucket (not a fixed-window counter) because it refills smoothly and allows
// a controlled burst, instead of the "2x burst at the window boundary" problem a
// fixed window has.
//
//	KEYS[1] = bucket key (per client IP)
//	ARGV[1] = refill rate (tokens/sec)   ARGV[2] = capacity (burst)
//	ARGV[3] = now (unix seconds, float)  ARGV[4] = tokens requested
//	returns {allowed (1|0), tokens_remaining}
var tokenBucketScript = redis.NewScript(`
local key       = KEYS[1]
local rate      = tonumber(ARGV[1])
local capacity  = tonumber(ARGV[2])
local now       = tonumber(ARGV[3])
local requested = tonumber(ARGV[4])

local bucket = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(bucket[1])
local ts     = tonumber(bucket[2])

-- First time we see this client: a full bucket.
if tokens == nil then
  tokens = capacity
  ts     = now
end

-- Refill for the time elapsed since we last saw them, capped at capacity.
local elapsed = math.max(0, now - ts)
tokens = math.min(capacity, tokens + (elapsed * rate))

local allowed = 0
if tokens >= requested then
  tokens  = tokens - requested
  allowed = 1
end

redis.call('HMSET', key, 'tokens', tokens, 'ts', now)
-- Idle buckets expire once they'd be fully refilled anyway: keeps Redis clean.
redis.call('EXPIRE', key, math.ceil(capacity / rate) + 1)

return { allowed, math.floor(tokens) }
`)

type RedisRateLimiterConfig struct {
	RequestsPerSecond float64 // sustained refill rate
	BurstSize         int     // bucket capacity (tolerated spike)
}

// RedisRateLimiter is a per-IP token bucket whose state lives in REDIS, so the
// limit is enforced across ALL API replicas — unlike the previous in-process map,
// which gave each replica its own private budget (3 replicas => 3x the real limit).
//
// Fail-open: if Redis is unreachable we log and ALLOW the request. A broken rate
// limiter must not take down the API — availability beats enforcement here. (For a
// payment or auth guard you'd argue the opposite and fail closed.)
func RedisRateLimiter(rdb *redis.Client, cfg RedisRateLimiterConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := "ratelimit:" + c.ClientIP()
		now := float64(time.Now().UnixNano()) / 1e9

		res, err := tokenBucketScript.Run(c.Request.Context(), rdb,
			[]string{key},
			cfg.RequestsPerSecond, cfg.BurstSize, now, 1,
		).Int64Slice()
		if err != nil {
			common.Logger.Errorw("rate limiter unavailable — failing open", "error", err)
			c.Next()
			return
		}

		allowed, remaining := res[0], res[1]
		c.Header("X-RateLimit-Limit", strconv.Itoa(cfg.BurstSize))
		c.Header("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))

		if allowed == 0 {
			// Tell the client roughly how long until one token is back.
			c.Header("Retry-After", strconv.Itoa(int(1/cfg.RequestsPerSecond)+1))
			metrics.RateLimited.Inc()
			common.Error(c, http.StatusTooManyRequests, "rate limit exceeded", "RATE_LIMITED")
			c.Abort()
			return
		}
		c.Next()
	}
}
