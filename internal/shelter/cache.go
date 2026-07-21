package shelter

import (
	"github.com/AtharvGupta360/CrisisLink/internal/platform/cache"

	"context"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

// Key layout: "shelter:v1:<uuid>".
//
// The "v1" is not decoration. If Shelter ever gains or renames a field, every
// entry written by the old binary decodes into garbage (or silently drops fields)
// under the new one. Bumping the prefix to v2 retires the entire old generation
// instantly — no FLUSHDB (which would also blow away the rate-limiter buckets that
// share this Redis), no key scan, no downtime. Old keys simply age out via TTL.
const shelterKeyPrefix = "shelter:v1:"

// shelterTTL bounds staleness when an invalidation is missed. 60s is the answer to
// "how long can a dispatcher tolerate seeing an out-of-date occupancy?" — short
// enough to be a nuisance rather than a hazard, long enough that a hot shelter is
// served from Redis for the overwhelming majority of reads.
const shelterTTL = 60 * time.Second

// ShelterCache caches shelters BY ID, and only by id.
//
// That restriction is a deliberate design decision, not laziness. We do NOT cache
// the list endpoint or the nearest-open (KNN) search, because invalidating those is
// intractable: one victim assignment changes one shelter's occupancy, but that
// shelter appears in an UNBOUNDED number of cached query results (every distinct
// lat/lng/limit/status combination anyone ever asked for). You cannot enumerate the
// keys to delete, so those entries would go stale with no way to fix them.
// Entity-by-id is the one shape where invalidation is exact: one row changes ->
// exactly one key dies.
type ShelterCache struct {
	c *cache.Cache

	// singleflight collapses concurrent MISSES of the same key into ONE Postgres
	// query. Without it, a hot key expiring under load means every in-flight request
	// misses simultaneously and they all stampede Postgres for the identical row —
	// the cache stops absorbing load and starts SYNCHRONISING it into a spike.
	// One goroutine loads; the rest block and share its result.
	sf singleflight.Group
}

func NewShelterCache(rdb *redis.Client) *ShelterCache {
	return &ShelterCache{c: cache.New(rdb)}
}

func shelterKey(id string) string {
	return shelterKeyPrefix + id
}

// GetOrLoad is the cache-aside read path: HIT -> return; MISS -> load from Postgres
// (deduplicated), backfill Redis, return.
//
// load is injected by the service layer so this package never imports the repository
// and stays free of any database dependency.
func (s *ShelterCache) GetOrLoad(ctx context.Context, id string, load func(context.Context) (*Shelter, error)) (*Shelter, error) {
	key := shelterKey(id)

	// Fast path: no singleflight bookkeeping on a hit, which is the common case.
	var hit Shelter
	if s.c.GetJSON(ctx, key, &hit) {
		return &hit, nil
	}

	loaded, err, _ := s.sf.Do(key, func() (any, error) {
		// Re-check under singleflight. We may have queued behind a leader that has
		// already loaded and backfilled this key while we were waiting — in which
		// case going to Postgres again would be pure waste.
		var again Shelter
		if s.c.GetJSON(ctx, key, &again) {
			return again, nil
		}

		sh, err := load(ctx)
		if err != nil {
			// Do NOT cache the failure. A not-found or a transient DB error written
			// into Redis would be served to everyone for a full TTL.
			return nil, err
		}
		s.c.SetJSON(ctx, key, sh, shelterTTL)
		return *sh, nil
	})
	if err != nil {
		return nil, err
	}

	// Hand every caller its OWN copy. singleflight shares one result across all the
	// goroutines that waited on it; returning a shared *Shelter would let one
	// request's mutation be visible to another's response — a data race that would
	// only ever show up under concurrency.
	sh := loaded.(Shelter)
	return &sh, nil
}

// Invalidate drops a shelter's cached entry. It MUST be called only after the
// Postgres write has COMMITTED — see the ordering rule in the package doc.
func (s *ShelterCache) Invalidate(ctx context.Context, id string) {
	s.c.Del(ctx, shelterKey(id))
}
