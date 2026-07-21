// Package server wires the Gin engine (middleware chain + routes + dependency
// injection) and owns the HTTP server lifecycle including graceful shutdown.
// main.go bootstraps infra (config, logger, db) and hands them here.
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/AtharvGupta360/CrisisLink/internal/auth"
	"github.com/AtharvGupta360/CrisisLink/internal/dispatch"
	"github.com/AtharvGupta360/CrisisLink/internal/incident"
	"github.com/AtharvGupta360/CrisisLink/internal/notification"
	"github.com/AtharvGupta360/CrisisLink/internal/outbox"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/config"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/middleware"
	"github.com/AtharvGupta360/CrisisLink/internal/presence"
	"github.com/AtharvGupta360/CrisisLink/internal/shelter"
	"github.com/AtharvGupta360/CrisisLink/internal/unit"
	"github.com/AtharvGupta360/CrisisLink/internal/victim"
)

// NewServer builds the Gin engine: base middleware chain, health/ready probes,
// and (in later phases) the /api/v1 route groups with their injected
// handler -> service -> repository stacks.
func NewServer(cfg *config.Config, pool *pgxpool.Pool, rdb *redis.Client) *gin.Engine {
	gin.SetMode(cfg.Server.Mode)

	// gin.New() (bare) not gin.Default() — we own the chain explicitly. Order
	// matters: CORS first (answer/reject cross-origin before doing any work),
	// request-id next (so everything downstream can log it), rate-limit (cheaply
	// shed abusive traffic early), recovery (catch panics), logging last.
	r := gin.New()
	r.Use(
		middleware.CORS(&cfg.CORS),
		middleware.RequestID(),
		// Rate limit state lives in REDIS, not in this process's memory, so the
		// limit holds across every API replica (an in-process map would give each
		// replica its own private budget). Token bucket, evaluated atomically.
		middleware.RedisRateLimiter(rdb, middleware.RedisRateLimiterConfig{
			RequestsPerSecond: 10, // sustained per-IP rate
			BurstSize:         20, // tolerate short spikes
		}),
		middleware.Recovery(),
		middleware.RequestLogger(),
	)

	// Liveness: is the process up? No dependencies checked.
	r.GET("/health", func(c *gin.Context) {
		common.Success(c, http.StatusOK, "server is healthy", gin.H{"status": "up"})
	})

	// Readiness: can we serve, i.e. is the DB reachable? Used by an orchestrator
	// to decide routing. Alive-but-not-ready (DB blip) stops traffic without a
	// process restart.
	r.GET("/ready", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			common.Error(c, http.StatusServiceUnavailable, "database unavailable", "NOT_READY")
			return
		}
		common.Success(c, http.StatusOK, "server is ready", gin.H{"status": "ready"})
	})

	// --- dependency injection: repository -> service -> handler ---
	// This is the composition root for the HTTP layer. Each domain's stack is
	// wired here and mounted under /api/v1.
	userRepo := auth.NewUserRepository(pool)
	authService := auth.NewService(userRepo, &cfg.JWT)
	authHandler := auth.NewAuthHandler(authService)

	incidentRepo := incident.NewIncidentRepository(pool)
	incidentService := incident.NewIncidentService(incidentRepo)
	incidentHandler := incident.NewIncidentHandler(incidentService)

	// Presence is Redis-only: no repository, no migration, nothing durable. It
	// answers "is this unit reachable right now", which Postgres cannot.
	presenceService := presence.NewService(rdb)
	presenceHandler := presence.NewHandler(presenceService)

	unitRepo := unit.NewUnitRepository(pool)
	unitService := unit.NewUnitService(unitRepo)
	unitHandler := unit.NewUnitHandler(unitService)

	// The outbox repository is constructed FIRST because it satisfies the EventWriter
	// seam that every event-emitting module depends on. Modules receive it as an
	// interface, so they can record domain events without knowing the outbox table.
	outboxRepo := outbox.NewOutboxRepository(pool)

	dispatchRepo := dispatch.NewDispatchRepository(pool, unitRepo, incidentRepo, outboxRepo)
	dispatchService := dispatch.NewDispatchService(incidentRepo, unitRepo, dispatchRepo, presenceService)
	dispatchHandler := dispatch.NewDispatchHandler(dispatchService)

	// ONE shared ShelterCache, injected into every service that reads or writes a
	// shelter. It must be a single instance: two instances would mean two separate
	// singleflight groups (so duplicate loads), and — far worse — it would be easy to
	// hand a writer a cache whose keys it never invalidates.
	shelterCache := shelter.NewShelterCache(rdb)

	shelterRepo := shelter.NewShelterRepository(pool)
	shelterService := shelter.NewShelterService(shelterRepo, shelterCache)
	shelterHandler := shelter.NewShelterHandler(shelterService)

	victimRepo := victim.NewVictimRepository(pool, shelterRepo, outboxRepo)
	victimService := victim.NewVictimService(victimRepo, shelterRepo, shelterCache)
	victimHandler := victim.NewVictimHandler(victimService)

	outboxService := outbox.NewOutboxService(outboxRepo)
	outboxHandler := outbox.NewOutboxHandler(outboxService)

	notificationRepo := notification.NewNotificationRepository(pool)
	notificationService := notification.NewNotificationService(notificationRepo)
	notificationHandler := notification.NewNotificationHandler(notificationService)

	api := r.Group("/api/v1")
	{
		// Public auth routes (no token required).
		api.POST("/auth/register", authHandler.Register)
		api.POST("/auth/login", authHandler.Login)
	}

	// Protected routes — AuthRequired verifies the Bearer token and puts the
	// caller's identity on the context. Everything in this group needs a token.
	protected := api.Group("")
	protected.Use(middleware.AuthRequired(&cfg.JWT))
	{
		// /me echoes the identity carried by the token — proof the JWT round-trips.
		protected.GET("/me", func(c *gin.Context) {
			common.Success(c, http.StatusOK, "authenticated user", gin.H{
				"userID":   c.GetString("userID"),
				"username": c.GetString("username"),
				"role":     c.GetString("role"),
			})
		})

		// Incident routes — any authenticated user may report and read incidents.
		protected.POST("/incidents", incidentHandler.Create)
		protected.GET("/incidents", incidentHandler.List)
		protected.GET("/incidents/nearby", incidentHandler.Nearby) // static route before :id
		protected.GET("/incidents/:id", incidentHandler.GetByID)
		protected.GET("/incidents/:id/candidates", dispatchHandler.Candidates) // nearest available units (KNN)
		// Dispatch a unit — the no-double-booking reservation. Admin-only, since it
		// mutates fleet state (like the other unit-status writes below).
		protected.POST("/incidents/:id/dispatch", middleware.AdminRequired(), dispatchHandler.Dispatch)
		protected.GET("/incidents/:id/dispatches", dispatchHandler.ListByIncident) // an incident's dispatches
		protected.PATCH("/incidents/:id/status", incidentHandler.UpdateStatus)

		// Dispatch lifecycle (P15). Reads for any authenticated user; advancing the
		// state machine is admin-only (it mutates unit + incident state).
		protected.GET("/dispatches/:id", dispatchHandler.Get)
		protected.PATCH("/dispatches/:id/status", middleware.AdminRequired(), dispatchHandler.AdvanceStatus)

		// Rescue units — reads for any authenticated user, writes admin-only
		// (per-route AdminRequired runs after the group's AuthRequired).
		// static route registered before /units/:id so it is not captured as an id
		protected.GET("/units/nearby", presenceHandler.NearbyLive)
		protected.GET("/units", unitHandler.List)
		protected.GET("/units/:id", unitHandler.GetByID)
		protected.POST("/units", middleware.AdminRequired(), unitHandler.Create)
		protected.PATCH("/units/:id/status", middleware.AdminRequired(), unitHandler.UpdateStatus)

		// Live tracking. The heartbeat is a fleet-state write, so it is admin-gated
		// like the other unit mutations. In production each unit would carry its own
		// responder identity and be authorised to report only for itself — that needs
		// the responder role we have not built yet.
		protected.POST("/units/:id/heartbeat", middleware.AdminRequired(), presenceHandler.Heartbeat)
		protected.GET("/units/:id/presence", presenceHandler.GetPresence)

		// Shelters — reads for any authenticated user, writes admin-only (mirrors
		// the unit registry). Occupancy changes come from P18 assignment, not here.
		protected.GET("/shelters", shelterHandler.List)
		protected.GET("/shelters/:id", shelterHandler.GetByID)
		protected.POST("/shelters", middleware.AdminRequired(), shelterHandler.Create)
		protected.PATCH("/shelters/:id/status", middleware.AdminRequired(), shelterHandler.UpdateStatus)

		// Victims — intake and reads for any authenticated user (like incident
		// reporting). Assignment to a shelter (with the capacity guard) is P18.
		protected.POST("/victims", victimHandler.Create)
		protected.GET("/victims", victimHandler.List)
		protected.GET("/victims/:id", victimHandler.GetByID)
		protected.GET("/victims/:id/shelters", victimHandler.NearestShelters) // nearest open shelters (KNN)
		// Assign a victim to a shelter — the no-overflow transaction. Admin-only
		// (mutates shelter occupancy), mirroring the dispatch reservation.
		protected.POST("/victims/:id/assign", middleware.AdminRequired(), victimHandler.Assign)

		// Admin-only routes: AdminRequired runs AFTER AuthRequired and checks the
		// role it set. Real admin routes (rescue-unit CRUD, etc.) attach here later.
		admin := protected.Group("/admin")
		admin.Use(middleware.AdminRequired())
		{
			admin.GET("/ping", func(c *gin.Context) {
				common.Success(c, http.StatusOK, "admin access granted", gin.H{
					"role": c.GetString("role"),
				})
			})

			// Ops view onto the transactional outbox (P19). The relay (P20) will
			// publish these and flip published_at.
			admin.GET("/outbox", outboxHandler.List)
			admin.GET("/outbox/dead", outboxHandler.ListDead)

			// What the idempotent consumer produced (P21) — exactly one per event,
			// even if Kafka delivered it twice.
			admin.GET("/notifications", notificationHandler.List)
		}
	}

	return r
}

// Run serves the engine under a hardened http.Server and blocks until a fatal
// server error OR a shutdown signal, then drains in-flight requests within
// ShutdownTimeout.
func Run(router *gin.Engine, cfg *config.Config) error {
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       time.Duration(cfg.Server.ReadTimeout) * time.Second,
		WriteTimeout:      time.Duration(cfg.Server.WriteTimeout) * time.Second,
		IdleTimeout:       time.Duration(cfg.Server.IdleTimeout) * time.Second,
	}

	// Serve in a goroutine so we can block on signals below. Buffered(1) => the
	// goroutine can send and exit even if nobody receives (no leak).
	serverErr := make(chan error, 1)
	go func() {
		common.Logger.Infof("server listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		return fmt.Errorf("server error: %w", err)
	case sig := <-quit:
		common.Logger.Infof("shutdown signal received: %s", sig)
	}

	// Fresh context carrying the drain deadline. Shutdown stops accepting new
	// connections, then waits for active ones, returning early if this hits.
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Server.ShutdownTimeout)*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("graceful shutdown timed out, forcing exit: %w", err)
	}
	return nil
}
