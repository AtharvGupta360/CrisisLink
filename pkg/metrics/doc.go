// Package metrics holds the Prometheus registry and the shared counters,
// gauges, and histograms every module records into: request latency, dispatch
// decision time, outbox lag, reservation-retry count (P25). Depends on no
// internal module.
package metrics
