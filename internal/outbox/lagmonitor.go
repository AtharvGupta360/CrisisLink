package outbox

import (
	"context"
	"time"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/metrics"
)

// lagPollInterval is how often the backlog gauge is refreshed. A gauge is only as
// fresh as its last write, and Prometheus scrapes roughly every 15s, so polling
// faster than that adds database load without adding resolution.
const lagPollInterval = 10 * time.Second

// MonitorLag periodically publishes the outbox backlog as a gauge.
//
// WHY A POLLER AND NOT A COUNTER AT THE WRITE SITE: the backlog is a property of
// the DATABASE, not of any one process. Incrementing on write and decrementing on
// publish would drift the moment a process restarted or a second relay joined,
// because in-memory counters are per-process. Reading the true value with a cheap
// COUNT keeps the number correct no matter how many processes exist or how often
// they crash.
//
// It also means the API can report lag even when the relay is dead — which is
// precisely the situation you most want a graph for.
//
// Runs until ctx is cancelled; intended to be started in its own goroutine.
func (r *OutboxRepository) MonitorLag(ctx context.Context) {
	ticker := time.NewTicker(lagPollInterval)
	defer ticker.Stop()

	poll := func() {
		// A short timeout of its own: a slow database must never wedge the monitor,
		// and a missed sample is harmless (the next one is 10s away).
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()

		pending, err := r.PendingCount(cctx)
		if err != nil {
			common.Logger.Warnw("outbox lag poll failed", "error", err)
			return
		}
		metrics.OutboxPending.Set(float64(pending))

		var dead int
		if err := r.pool.QueryRow(cctx,
			`SELECT count(*) FROM outbox_events WHERE dead_at IS NOT NULL`).Scan(&dead); err != nil {
			return
		}
		metrics.OutboxDead.Set(float64(dead))
	}

	poll() // publish immediately so the gauge isn't 0 until the first tick
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll()
		}
	}
}
