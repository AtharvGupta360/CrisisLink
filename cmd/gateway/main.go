// Command gateway is CrisisLink's edge reverse proxy: a standalone deployable that
// sits IN FRONT of the api replicas. Clients hit the gateway; it forwards each
// request to one of N api upstreams and streams the response back. Its whole reason
// to exist is that a client never needs to know how many api replicas there are, or
// which one served it.
//
// It owns the EDGE cross-cutting concerns — the things you want to do ONCE, before
// traffic fans out to every replica:
//
//	load balancing   round-robin across the configured upstreams
//	rate limiting    the Redis token bucket, moved here from the app: shed abuse
//	                 before it reaches any replica (still Redis-backed, so the limit
//	                 is global even across multiple gateway instances)
//	request-id       minted at ingress and FORWARDED upstream, so one request is
//	                 traceable across every replica and log line it touches
//	coarse authN     optional: reject unauthenticated traffic early
//
// What it deliberately does NOT own: authorization. Which role may hit which route
// is domain knowledge (RBAC), so it stays in the app. AuthN ("is this token valid")
// is domain-agnostic and fine at the edge; authZ ("may this role do this") is not.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/AtharvGupta360/CrisisLink/internal/auth"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/config"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/database"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/middleware"
)

// publicPrefixes are the routes the edge auth check lets through unauthenticated.
//
// This list IS the coupling cost of doing authN at the edge: the gateway now has to
// know that login and register are public. It is deliberately tiny and coarse — the
// real authorization decisions still happen in the app, which knows far more. If
// this list ever grew large, that would be the signal that too much routing
// knowledge had leaked into the edge.
var publicPrefixes = []string{
	"/health",
	"/ready",
	"/api/v1/auth/",
}

func main() {
	cfg, err := config.LoadConfig(".")
	if err != nil {
		log.Fatalf("config load error: %v", err)
	}
	common.InitLogger(cfg.Server.Mode)
	defer common.Logger.Sync() //nolint:errcheck

	// Parse the upstream replica URLs ONCE at boot. A malformed upstream is a
	// deployment error, so fail fast rather than discover it on the first request.
	if len(cfg.Gateway.Upstreams) == 0 {
		common.Logger.Fatal("gateway: no upstreams configured")
	}
	targets := make([]*url.URL, 0, len(cfg.Gateway.Upstreams))
	for _, raw := range cfg.Gateway.Upstreams {
		u, perr := url.Parse(raw)
		if perr != nil || u.Host == "" {
			common.Logger.Fatalf("gateway: bad upstream %q: %v", raw, perr)
		}
		targets = append(targets, u)
	}

	// Redis powers the SAME token bucket the app used to run — same script, same
	// keys — so moving the limiter to the edge changes where it runs, not what it
	// guarantees.
	rdb, err := database.NewRedisConnection(&cfg.Redis)
	if err != nil {
		common.Logger.Fatalf("gateway: redis connection failed: %v", err)
	}
	defer rdb.Close()

	proxy := newRoundRobinProxy(targets)

	gin.SetMode(cfg.Server.Mode)
	r := gin.New()
	r.Use(
		middleware.CORS(&cfg.CORS),
		edgeRequestID(), // mint + FORWARD upstream (app reuses it), unlike the app's own
		middleware.Recovery(),
		// Rate limit at the edge. Same Redis-backed token bucket as before.
		middleware.RedisRateLimiter(rdb, middleware.RedisRateLimiterConfig{
			RequestsPerSecond: 10,
			BurstSize:         20,
		}),
	)
	if cfg.Gateway.EdgeAuth {
		r.Use(edgeAuth(&cfg.JWT))
	}

	// The gateway's OWN endpoints, handled here and never proxied:
	//   /health   is the edge alive? (does not check upstreams)
	//   /metrics  the rate-limit counter now lives in THIS process
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "gateway is healthy"})
	})
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// Everything else falls through to the proxy. NoRoute still runs the global
	// middleware chain above (rate limit, auth), so abusive/unauth traffic is shed
	// BEFORE it is ever forwarded to a replica.
	r.NoRoute(gin.WrapH(proxy))

	run(r, cfg.Gateway.ListenAddr, cfg.Server.ShutdownTimeout)
}

// newRoundRobinProxy builds a single ReverseProxy whose Director picks the next
// upstream per request. One shared proxy (not one per target) so connection pooling
// and buffering are shared; the Director is the only per-request decision.
func newRoundRobinProxy(targets []*url.URL) *httputil.ReverseProxy {
	var counter uint64
	next := func() *url.URL {
		// atomic so concurrent requests get distinct, monotonically increasing
		// indices without a mutex; wrap with modulo over the target count.
		i := atomic.AddUint64(&counter, 1)
		return targets[int(i-1)%len(targets)]
	}
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			t := next()
			req.URL.Scheme = t.Scheme
			req.URL.Host = t.Host
			req.Host = t.Host // send the upstream's host in the Host header
			// Record the original client for the app's rate limiter / logs. Without
			// this every request would look like it came from the gateway.
			if clientIP, _, ok := strings.Cut(req.RemoteAddr, ":"); ok {
				req.Header.Set("X-Forwarded-For", clientIP)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			// An upstream being down is a 502, not a gateway crash. This is where a
			// real gateway would mark the replica unhealthy and retry another; here
			// we just report it honestly.
			common.Logger.Errorw("gateway: upstream error", "err", err, "path", req.URL.Path)
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"success":false,"error":"upstream unavailable"}`))
		},
	}
}

// edgeRequestID mints a correlation id at ingress and, crucially, writes it onto
// the OUTGOING request header so it is forwarded to the chosen upstream. The app's
// own RequestID middleware then reuses an inbound id rather than minting a new one —
// so a single id follows the request from edge to replica to logs.
func edgeRequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(middleware.RequestIDHeader)
		if id == "" {
			id = uuid.NewString()
		}
		c.Request.Header.Set(middleware.RequestIDHeader, id) // forwarded upstream
		c.Header(middleware.RequestIDHeader, id)             // echoed to client
		c.Next()
	}
}

// edgeAuth is COARSE authentication: it verifies the JWT signature and expiry and
// nothing more. It does NOT inspect the role — that is the app's job. Public routes
// (login, register, health) pass through untouched.
func edgeAuth(jwtCfg *config.JWTConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		for _, p := range publicPrefixes {
			if strings.HasPrefix(c.Request.URL.Path, p) {
				c.Next()
				return
			}
		}
		authz := c.GetHeader("Authorization")
		parts := strings.SplitN(authz, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.AbortWithStatusJSON(http.StatusUnauthorized,
				gin.H{"success": false, "error": "missing or malformed bearer token"})
			return
		}
		if _, err := auth.ValidateToken(parts[1], jwtCfg); err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized,
				gin.H{"success": false, "error": "invalid or expired token"})
			return
		}
		c.Next() // app re-validates + enforces RBAC (defense in depth)
	}
}

// run starts the gateway with graceful shutdown, mirroring the api server: drain
// in-flight requests on SIGINT/SIGTERM rather than cutting connections.
func run(handler http.Handler, addr string, shutdownTimeout int) {
	srv := &http.Server{Addr: addr, Handler: handler}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		common.Logger.Infow("gateway listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			common.Logger.Fatalf("gateway: listen: %v", err)
		}
	}()

	<-ctx.Done()
	common.Logger.Info("gateway: shutting down")

	shutCtx, cancel := context.WithTimeout(context.Background(), time.Duration(shutdownTimeout)*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		common.Logger.Errorf("gateway: forced shutdown: %v", err)
	}
	common.Logger.Info("gateway: stopped")
}
