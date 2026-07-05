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

	"github.com/AtharvGupta360/CrisisLink/internal/auth"
	"github.com/AtharvGupta360/CrisisLink/internal/common"
	"github.com/AtharvGupta360/CrisisLink/internal/config"
	"github.com/AtharvGupta360/CrisisLink/internal/handlers"
	"github.com/AtharvGupta360/CrisisLink/internal/middleware"
	"github.com/AtharvGupta360/CrisisLink/internal/repository"
	"github.com/AtharvGupta360/CrisisLink/internal/service"
)

// NewServer builds the Gin engine: base middleware chain, health/ready probes,
// and (in later phases) the /api/v1 route groups with their injected
// handler -> service -> repository stacks.
func NewServer(cfg *config.Config, pool *pgxpool.Pool) *gin.Engine {
	gin.SetMode(cfg.Server.Mode)

	// gin.New() (bare) not gin.Default() — we own the chain explicitly. Order
	// matters: CORS first (answer/reject cross-origin before doing any work),
	// request-id next (so everything downstream can log it), rate-limit (cheaply
	// shed abusive traffic early), recovery (catch panics), logging last.
	r := gin.New()
	r.Use(
		middleware.CORS(&cfg.CORS),
		middleware.RequestID(),
		middleware.RateLimiter(middleware.RateLimiterConfig{
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
	userRepo := repository.NewUserRepository(pool)
	authService := auth.NewService(userRepo, &cfg.JWT)
	authHandler := handlers.NewAuthHandler(authService)

	incidentRepo := repository.NewIncidentRepository(pool)
	incidentService := service.NewIncidentService(incidentRepo)
	incidentHandler := handlers.NewIncidentHandler(incidentService)

	unitRepo := repository.NewUnitRepository(pool)
	unitService := service.NewUnitService(unitRepo)
	unitHandler := handlers.NewUnitHandler(unitService)

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
		protected.PATCH("/incidents/:id/status", incidentHandler.UpdateStatus)

		// Rescue units — reads for any authenticated user, writes admin-only
		// (per-route AdminRequired runs after the group's AuthRequired).
		protected.GET("/units", unitHandler.List)
		protected.GET("/units/:id", unitHandler.GetByID)
		protected.POST("/units", middleware.AdminRequired(), unitHandler.Create)
		protected.PATCH("/units/:id/status", middleware.AdminRequired(), unitHandler.UpdateStatus)

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
