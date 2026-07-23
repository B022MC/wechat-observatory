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

	"wechat-observatory/internal/bridge"
	"wechat-observatory/internal/config"
	mysqlstore "wechat-observatory/internal/storage/mysql"
)

func main() {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	var persistence bridge.Persistence
	var store *mysqlstore.Store
	if cfg.MySQL.Enabled() {
		var err error
		store, err = initializeMySQL(cfg)
		if err != nil {
			log.Fatalf("initialize mysql: %v", err)
		}
		defer func() {
			if err := store.Close(); err != nil {
				log.Printf("close mysql: %v", err)
			}
		}()
		snapshot, err := store.LoadSnapshot(context.Background())
		if err != nil {
			log.Fatalf("load mysql snapshot: %v", err)
		}
		cfg.Devices = snapshot.Devices
		cfg.APIKeys = snapshot.APIKeys
		persistence = store
	}
	if err := cfg.EnsureRuntimeReady(); err != nil {
		log.Fatalf("runtime config: %v", err)
	}

	var opts []bridge.Option
	if persistence != nil {
		opts = append(opts, bridge.WithPersistence(persistence))
	}
	if store != nil {
		opts = append(opts, bridge.WithOutbox(store))
		opts = append(opts, bridge.WithAdminReader(store))
	}
	service := bridge.NewService(bridge.Config{
		DefaultDevice: cfg.DefaultDevice,
		Devices:       cfg.Devices,
		APIKeys:       cfg.APIKeys,
		InstanceID:    cfg.InstanceID,
		SessionTTL:    cfg.SessionTTL,
		PollInterval:  cfg.PollInterval,
	}, opts...)

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           bridge.NewHTTPServer(service, cfg.AdminPassword).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if store != nil {
		go startHistoryRetention(ctx, store, cfg.RetentionDays, cfg.RetentionPoll)
	}

	errs := make(chan error, 1)
	go func() {
		log.Printf("WeChat gateway console listening on %s", cfg.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errs:
		log.Printf("server error: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
}

type historyRetentionStore interface {
	PurgeExpiredHistory(context.Context, int) (mysqlstore.RetentionCleanup, error)
}

func startHistoryRetention(ctx context.Context, store historyRetentionStore, retentionDays int, interval time.Duration) {
	if store == nil || retentionDays <= 0 {
		return
	}
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		result, err := store.PurgeExpiredHistory(cleanupCtx, retentionDays)
		cancel()
		if err != nil {
			log.Printf("retained history cleanup failed: %v", err)
		} else if result.MessageEvents > 0 || result.SentOutbox > 0 {
			log.Printf("retained history cleanup complete: messages=%d sent_outbox=%d", result.MessageEvents, result.SentOutbox)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func initializeMySQL(cfg config.Config) (*mysqlstore.Store, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := mysqlstore.Open(ctx, cfg.MySQL.DSN)
	if err != nil {
		return nil, err
	}
	if err := prepareMySQLStore(ctx, store, cfg); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

type mysqlSetupStore interface {
	ApplyMigrations(context.Context) error
	SeedFromConfig(context.Context, config.Config) error
}

func prepareMySQLStore(ctx context.Context, store mysqlSetupStore, cfg config.Config) error {
	if !cfg.MySQL.AutoMigrate {
		return nil
	}
	if err := store.ApplyMigrations(ctx); err != nil {
		return err
	}
	return store.SeedFromConfig(ctx, cfg)
}
