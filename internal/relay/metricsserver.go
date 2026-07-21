package relay

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

// ServeMetrics exposes the relay's own /metrics on its own port.
//
// The relay is a SEPARATE PROCESS from the API, so it has its own in-memory
// counters and needs its own scrape target — Prometheus cannot reach them through
// the API's endpoint. This is a general property of pull-based monitoring: every
// process that holds metrics must be individually scrapeable, which is also why
// per-process counters must never be used for values that describe shared state
// (the outbox backlog is read from the database for exactly that reason).
//
// Returns when ctx is cancelled, shutting the listener down gracefully.
func ServeMetrics(ctx context.Context, addr string, log *zap.SugaredLogger) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Infof("relay metrics listening on %s/metrics", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		// Not fatal: losing observability must not take down the relay itself.
		log.Warnw("relay metrics server stopped", "error", err)
	}
}
