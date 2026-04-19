package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mapcluster/internal/cache"
	"mapcluster/internal/config"
	"mapcluster/internal/db"
	"mapcluster/internal/httpapi"
	"mapcluster/internal/repository"
	"mapcluster/internal/service"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg := config.Load()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect database: %v", err)
	}
	defer pool.Close()

	if err := db.EnsureSchema(ctx, pool); err != nil {
		log.Fatalf("ensure schema: %v", err)
	}

	redisClient := cache.NewRedisClient(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	defer func() {
		if err := redisClient.Close(); err != nil {
			log.Printf("close redis: %v", err)
		}
	}()

	repo := repository.NewMapRepository(pool)
	mapService := service.NewMapService(repo, redisClient, cfg.CacheTTL)
	handler := httpapi.NewHandler(mapService)

	httpServer := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      handler.Routes(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("api listening on %s", cfg.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve http: %v", err)
		}
	}()

	<-ctx.Done()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown http: %v", err)
	}
}
