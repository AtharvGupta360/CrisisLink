// Package metrics defines every Prometheus metric this system exports.
//
// They are declared in ONE place, not scattered across the modules that record
// them, because a metric name is a public API: dashboards, alerts and runbooks are
// written against it. Renaming one silently breaks a dashboard you will not think
// to update. Keeping them together also makes accidental duplicates impossible —
// registering the same name twice panics at startup.
//
// PROMETHEUS IS PULL-BASED. The app never sends anything anywhere; it maintains
// counters in memory and exposes them at /metrics, and Prometheus scrapes that
// endpoint on its own schedule. That is why a crashed app simply goes missing from
// the graph rather than losing data it was trying to push.
//
// A CONSEQUENCE OF DECLARING THEM ALL HERE, worth knowing: promauto registers every
// metric in this package at INIT time, so ANY process that imports it (directly or
// transitively) exports ALL of them — including ones it never sets. The relay
// therefore publishes crisislink_rate_limited_total = 0, and the API publishes
// crisislink_relay_published_total = 0. Those phantom zeros are harmless on their
// own but pollute a graph and quietly corrupt any sum() across jobs.
//
// The fix is at query time: every dashboard query carries a job selector
// (`{job="crisislink-api"}`) so a metric is only ever read from the process that
// actually owns it. The alternative — splitting this into per-process packages with
// explicit registration — buys tidier /metrics output at the cost of losing the
// single place where every metric name is defined. Naming things once matters more.
//
// The three metric types, and when each is correct:
//
//	COUNTER   only goes up (resets to 0 on restart). "How many X have happened."
//	          Never graph it directly — a monotonic line says nothing. Graph
//	          rate(counter[5m]) to get "X per second".
//
//	GAUGE     goes up AND down. "What is the current value of X." Outbox lag is
//	          the canonical example: it rises when publishing stalls and falls when
//	          the relay catches up.
//
//	HISTOGRAM buckets observations so quantiles can be computed. Latency must be a
//	          histogram, never an average: an average of 200ms can hide a p99 of
//	          8 seconds, and the p99 is the experience you actually get paged for.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// --- HTTP (the RED metrics: Rate, Errors, Duration) --------------------------

// HTTPRequests counts every request by method, route and status.
//
// CARDINALITY WARNING, and this is the classic Prometheus mistake: the `route`
// label MUST be the route TEMPLATE ("/api/v1/incidents/:id"), never the actual
// path ("/api/v1/incidents/9f3c...-uuid"). Prometheus creates a separate time
// series per unique label combination, so labelling by raw path would create one
// series per incident id — unbounded cardinality that eventually kills the
// Prometheus server. The middleware uses gin's c.FullPath() for exactly this.
var HTTPRequests = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "crisislink_http_requests_total",
	Help: "Total HTTP requests by method, route template and status code.",
}, []string{"method", "route", "status"})

// HTTPDuration is request latency. A HISTOGRAM, so p50/p95/p99 can be derived.
//
// Buckets are the resolution of the answer: a quantile is ESTIMATED by
// interpolating within the bucket the quantile falls into, so the numbers are only
// as precise as the bucket edges near them. These are tuned for an API expected to
// answer in milliseconds, with headroom up to 10s for pathological cases.
var HTTPDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "crisislink_http_request_duration_seconds",
	Help:    "HTTP request latency by method and route template.",
	Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
}, []string{"method", "route"})

// --- The outbox: the number to alarm on --------------------------------------

// OutboxPending is the transactional outbox backlog — events written but not yet
// published to Kafka.
//
// This is the single most important metric in the system, and it is a GAUGE
// because it moves in both directions. A steadily climbing lag means the relay is
// down or Kafka is unreachable, and every downstream consumer is silently working
// from stale data. Nothing else fails visibly when that happens: the API keeps
// returning 200s while the event stream quietly rots. That silence is exactly why
// it needs a graph.
var OutboxPending = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "crisislink_outbox_pending_events",
	Help: "Outbox events written but not yet published (the relay's backlog).",
})

// OutboxDead is the count of events that exhausted their retry budget. Unlike the
// lag, this should be FLAT AT ZERO forever — any increase means a human must look.
var OutboxDead = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "crisislink_outbox_dead_events",
	Help: "Outbox events dead-lettered after exhausting retries (should stay 0).",
})

// Relay counters. Per-process, exported by the relay's own /metrics endpoint.
var (
	RelayPublished = promauto.NewCounter(prometheus.CounterOpts{
		Name: "crisislink_relay_published_total",
		Help: "Events successfully published to Kafka by the relay.",
	})
	RelayFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "crisislink_relay_publish_failures_total",
		Help: "Publish attempts that failed and were scheduled for retry.",
	})
	RelayDeadLettered = promauto.NewCounter(prometheus.CounterOpts{
		Name: "crisislink_relay_dead_lettered_total",
		Help: "Events dead-lettered by the relay after exhausting their retries.",
	})
)

// --- Domain metrics ----------------------------------------------------------

// ReservationConflicts counts losses of a concurrency race — a dispatcher who was
// beaten to a unit, or a booking that found no room.
//
// A counter, not an error metric: conflicts are CORRECT behaviour, they are the
// system refusing to double-book. What matters is the RATE. A sudden spike means
// contention is up (too many operators chasing too few units), which is an
// operational signal about the disaster, not a bug in the code.
var ReservationConflicts = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "crisislink_reservation_conflicts_total",
	Help: "Reservations rejected because the resource was already taken or full.",
}, []string{"resource", "reason"})

// DispatchDecision measures how long it takes to produce a ranked candidate list:
// the geospatial query plus scoring. Labelled by position source so the cost of
// the live (Redis GEO) path can be compared against the registry (PostGIS) path.
var DispatchDecision = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "crisislink_dispatch_decision_seconds",
	Help:    "Time to compute ranked dispatch candidates.",
	Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
}, []string{"source"})

// CacheOps counts cache lookups by outcome. The hit RATIO is derived in PromQL
// rather than stored, because a ratio computed at record time cannot be
// re-aggregated across replicas or re-windowed; raw counters can.
var CacheOps = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "crisislink_cache_operations_total",
	Help: "Cache lookups by cache name and outcome (hit/miss).",
}, []string{"cache", "result"})

// RateLimited counts requests rejected by the token bucket. Its rate is an abuse
// signal; a jump means either an attack or a client stuck in a retry loop.
var RateLimited = promauto.NewCounter(prometheus.CounterOpts{
	Name: "crisislink_rate_limited_total",
	Help: "Requests rejected with 429 by the rate limiter.",
})

// PresenceLive is the number of units currently reporting heartbeats — the live
// fleet size. A gauge that quietly falling is a fleet going dark.
var PresenceLive = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "crisislink_units_present",
	Help: "Units with a live presence key (heartbeat within TTL).",
})
