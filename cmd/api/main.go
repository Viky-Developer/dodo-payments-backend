package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Viky-Developer/dodo-payments-backend/internal/auth"
	"github.com/Viky-Developer/dodo-payments-backend/internal/customer"
	"github.com/Viky-Developer/dodo-payments-backend/internal/db"
	"github.com/Viky-Developer/dodo-payments-backend/internal/db/generate"
	"github.com/Viky-Developer/dodo-payments-backend/internal/idgen"
	"github.com/Viky-Developer/dodo-payments-backend/internal/invoice"
	"github.com/Viky-Developer/dodo-payments-backend/internal/middleware"
	"github.com/Viky-Developer/dodo-payments-backend/internal/payment"
	"github.com/Viky-Developer/dodo-payments-backend/internal/psp"
	"github.com/Viky-Developer/dodo-payments-backend/internal/publicid"
	"github.com/Viky-Developer/dodo-payments-backend/internal/webhook"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// setupRouter configures a Gin engine instance with custom middleware and routes.
func setupRouter(pool *pgxpool.Pool) *gin.Engine {
	// Initialize Gin without default logger/recovery to use our custom middlewares
	r := gin.New()

	// Apply custom request tracking, structured logging, and safe panic recovery
	r.Use(middleware.RequestID())
	r.Use(middleware.Logger())
	r.Use(middleware.Recovery())

	// Health check endpoint verifying both API liveness and database connectivity
	r.GET("/health", func(c *gin.Context) {
		if pool != nil {
			if err := pool.Ping(c.Request.Context()); err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{
					"status":   "error",
					"database": "unreachable",
				})
				return
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
			"message":"dodo-payments backend is up and running.",
		})
	})

	if pool != nil {
		queries := generate.New(pool)
		idGen := idgen.NewSnowflake(1)
		codec := publicid.New(os.Getenv("ID_CODEC_SECRET"))
		transactor := invoice.NewPgxTransactor(pool, queries)

		custHandler := customer.NewHandler(queries, idGen, codec)
		invHandler := invoice.NewHandler(queries, transactor, idGen, codec)
		pspBaseURL := os.Getenv("PSP_BASE_URL")
		if pspBaseURL == "" {
			pspBaseURL = "http://localhost:8081"
		}
		pspTimeout := 3 * time.Second
		if configured := os.Getenv("PSP_TIMEOUT"); configured != "" {
			if parsed, err := time.ParseDuration(configured); err == nil && parsed > 0 {
				pspTimeout = parsed
			}
		}
		payHandler := payment.NewHandler(pool, idGen, codec, psp.NewClient(pspBaseURL, pspTimeout))
		webhookHandler := webhook.NewHandler(queries, idGen, codec)

		authGroup := r.Group("")
		authGroup.Use(auth.Middleware(queries))
		{
			custHandler.RegisterRoutes(authGroup)
			invHandler.RegisterRoutes(authGroup)
			payHandler.RegisterRoutes(authGroup)
			webhookHandler.RegisterRoutes(authGroup)
		}
	}

	return r
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://postgres:postgres@localhost:5432/dodo_payments?sslmode=disable"
	}

	// 1. Establish and test database connection
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	log.Printf("Connecting to database...")
	pool, err := db.Connect(ctx, databaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer pool.Close()

	// Explicitly test and verify the database is reachable and working
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pingCancel()

	if err := pool.Ping(pingCtx); err != nil {
		log.Fatalf("Database ping verification failed: %v", err)
	}

	log.Printf("Database connection verified and ready")

	// 2. Bootstrap demo business and API key idempotently
	queries := generate.New(pool)
	idGen := idgen.NewSnowflake(1)
	bootstrapCtx, bootstrapCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer bootstrapCancel()

	bootResult, err := auth.BootstrapDemoData(bootstrapCtx, queries, idGen)
	if err != nil {
		log.Fatalf("Failed to bootstrap demo data: %v", err)
	}
	if bootResult.FullApiKey != "" {
		log.Printf("Demo API Key: %s", bootResult.FullApiKey)
	}

	// 3. Setup router and HTTP server using ListenAndServe
	router := setupRouter(pool)

	// Start background webhook delivery worker
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	webhookWorker := webhook.NewWorker(pool, queries, 1*time.Second, 30*time.Second, 10, nil)
	go webhookWorker.Start(workerCtx)

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// 4. Start server in a background goroutine and manage graceful shutdown
	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("Starting Invoice & Payment API with ListenAndServe on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	// Listen for OS interrupt signals for graceful shutdown
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErrors:
		log.Fatalf("Server startup failed: %v", err)

	case sig := <-shutdown:
		log.Printf("Received signal %v, initiating graceful shutdown...", sig)

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("Graceful shutdown error: %v, forcing close", err)
			_ = srv.Close()
		}
		log.Printf("Server gracefully stopped")
	}
}
