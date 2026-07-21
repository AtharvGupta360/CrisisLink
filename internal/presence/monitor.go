package presence

import (
	"context"
	"time"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/metrics"
)

// presencePollInterval is how often the live-fleet gauge is refreshed. Matched to
// roughly the Prometheus scrape interval — polling faster costs Redis work without
// adding any resolution to the graph.
const presencePollInterval = 15 * time.Second

// MonitorFleet publishes the number of units currently reporting heartbeats.
//
// It COUNTS KEYS rather than tracking a running total, for the same reason the
// outbox lag is counted rather than incremented: presence is shared state that
// expires on its own. Redis deletes keys when heartbeats stop, and no application
// code is notified — an in-process counter would drift upward forever, and would be
// wrong the moment a second API replica existed.
//
// SCAN, NOT KEYS. `KEYS presence:unit:*` matches the same keys but is O(n) in ONE
// blocking call, and Redis is single-threaded: on a large keyspace it stalls every
// other client, including the rate limiter that gates every request. SCAN returns a
// cursor and walks the keyspace in small batches, so other commands interleave. The
// tradeoff is that SCAN gives a slightly fuzzy snapshot of a moving keyspace — which
// is exactly right for a gauge that is a monitoring signal, not an invariant.
func (s *Service) MonitorFleet(ctx context.Context) {
	ticker := time.NewTicker(presencePollInterval)
	defer ticker.Stop()

	poll := func() {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		var count int
		var cursor uint64
		for {
			keys, next, err := s.rdb.Scan(cctx, cursor, keyPrefix+"*", 200).Result()
			if err != nil {
				common.Logger.Warnw("presence fleet scan failed", "error", err)
				return // leave the gauge at its last value rather than reporting a false 0
			}
			count += len(keys)
			cursor = next
			if cursor == 0 {
				break // full pass complete
			}
		}
		metrics.PresenceLive.Set(float64(count))
	}

	poll()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll()
		}
	}
}
