// Package cache implements the cache-aside pattern over Redis.
//
// The contract, and the reasoning behind it:
//
//   - READ:  look in Redis; on a miss, read Postgres and backfill Redis with a TTL.
//   - WRITE: commit to Postgres FIRST, then DELETE the key.
//
// Two rules are load-bearing and are easy to get subtly wrong:
//
//  1. Writes DELETE, they never SET. Two concurrent writers can commit to Postgres
//     in one order and then reach Redis in the OPPOSITE order, leaving the cache
//     holding the older value permanently. DEL is idempotent and order-independent:
//     whoever runs last, the key is still gone and the next reader repopulates from
//     the source of truth.
//
//  2. Postgres first, THEN invalidate. Deleting before the commit opens a window
//     where a reader misses, reads the pre-write row, and caches it — with no
//     invalidation left to fire.
//
// Even done correctly, cache-aside has an unavoidable race: a reader can read the
// old row, stall, and write it to Redis AFTER a concurrent writer's DEL. The TTL is
// what makes that survivable — it is the correctness backstop, not a perf knob. Any
// missed invalidation (that race, a crash between COMMIT and DEL, a Redis restart,
// a plain bug) degrades to BOUNDED staleness instead of permanent corruption.
//
// Finally: a cache is a read-view. It must never be allowed to weaken an invariant.
// The shelter overflow guard stays in Postgres (UPDATE ... WHERE occupancy < capacity).
// A stale cache may make a screen wrong; it must never let us overflow a shelter.
package cache

import (
	"context"
	"encoding/json"
	"math/rand"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
)

// Cache is a thin JSON codec over Redis.
//
// Every method FAILS OPEN: a Redis error is logged and reported as a miss, never
// returned to the caller. Same reflex as the P23 rate limiter — a degraded cache
// must cost us latency (we fall through to Postgres), never availability. A cache
// outage that takes the API down with it is worse than having no cache at all.
type Cache struct {
	rdb *redis.Client
}

func New(rdb *redis.Client) *Cache {
	return &Cache{rdb: rdb}
}

// GetJSON decodes key into dst. Reports whether it was a usable HIT.
//
// It deliberately returns a bool rather than an error: to the caller there is no
// difference between "not cached", "Redis is down", and "the cached bytes are
// garbage" — all three mean the same thing, go ask Postgres.
func (c *Cache) GetJSON(ctx context.Context, key string, dst any) bool {
	raw, err := c.rdb.Get(ctx, key).Bytes()
	if err != nil {
		// redis.Nil is the ordinary "key absent" miss, not a fault — don't log it.
		if err != redis.Nil {
			common.Logger.Warnw("cache read failed, falling through to postgres", "key", key, "error", err)
		}
		return false
	}

	if err := json.Unmarshal(raw, dst); err != nil {
		// A corrupt or schema-drifted entry must not wedge the endpoint forever.
		// Evict it and treat it as a miss; the next read rebuilds it correctly.
		common.Logger.Warnw("cache decode failed, evicting entry", "key", key, "error", err)
		c.Del(ctx, key)
		return false
	}
	return true
}

// SetJSON backfills a key with a JITTERED TTL.
//
// The jitter is the cheap half of stampede defence. If a burst of requests all
// populate the same set of keys at the same instant with an identical TTL, those
// keys all expire at the same instant too — and the herd hits Postgres in lockstep,
// forever, in waves. Spreading expiry over a window breaks that synchronisation.
func (c *Cache) SetJSON(ctx context.Context, key string, val any, ttl time.Duration) {
	raw, err := json.Marshal(val)
	if err != nil {
		common.Logger.Warnw("cache encode failed, skipping write", "key", key, "error", err)
		return
	}
	if err := c.rdb.Set(ctx, key, raw, jitter(ttl)).Err(); err != nil {
		common.Logger.Warnw("cache write failed", "key", key, "error", err)
	}
}

// Del removes keys. This is the invalidation primitive, called AFTER the Postgres
// commit succeeds.
//
// A failure here is the dangerous case — it means Postgres moved on but the cache
// did not, so the key now serves stale data. We cannot roll Postgres back (it is
// already committed and there is no transaction spanning both systems: this is the
// dual-write problem again). So we log loudly and lean on the TTL to bound the
// damage. That is the honest limit of cache-aside.
func (c *Cache) Del(ctx context.Context, keys ...string) {
	if len(keys) == 0 {
		return
	}
	if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
		common.Logger.Errorw("cache invalidation failed, entry is stale until its TTL expires",
			"keys", keys, "error", err)
	}
}

// jitter spreads a TTL by +/-20% so keys written together don't expire together.
func jitter(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return ttl
	}
	spread := float64(ttl) * 0.2
	return time.Duration(float64(ttl) - spread + rand.Float64()*2*spread)
}
