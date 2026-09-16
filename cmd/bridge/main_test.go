package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"wechat-observatory/internal/config"
	mysqlstore "wechat-observatory/internal/storage/mysql"
)

func TestPrepareMySQLStoreSkipsSeedWhenAutoMigrateDisabled(t *testing.T) {
	store := &fakeSetupStore{}
	cfg := config.Config{MySQL: config.MySQLConfig{AutoMigrate: false}}

	if err := prepareMySQLStore(context.Background(), store, cfg); err != nil {
		t.Fatalf("prepare mysql store: %v", err)
	}
	if store.migrated || store.seeded {
		t.Fatalf("expected no migration or seed, got migrated=%v seeded=%v", store.migrated, store.seeded)
	}
}

func TestPrepareMySQLStoreSeedsOnlyAfterMigration(t *testing.T) {
	store := &fakeSetupStore{}
	cfg := config.Config{MySQL: config.MySQLConfig{AutoMigrate: true}}

	if err := prepareMySQLStore(context.Background(), store, cfg); err != nil {
		t.Fatalf("prepare mysql store: %v", err)
	}
	if !store.migrated || !store.seeded {
		t.Fatalf("expected migration and seed, got migrated=%v seeded=%v", store.migrated, store.seeded)
	}
}

func TestPrepareMySQLStoreStopsWhenMigrationFails(t *testing.T) {
	store := &fakeSetupStore{migrateErr: errors.New("migration failed")}
	cfg := config.Config{MySQL: config.MySQLConfig{AutoMigrate: true}}

	if err := prepareMySQLStore(context.Background(), store, cfg); err == nil {
		t.Fatal("expected migration error")
	}
	if store.seeded {
		t.Fatal("seed should not run after migration failure")
	}
}

func TestHistoryRetentionRunsImmediately(t *testing.T) {
	store := &fakeHistoryRetentionStore{called: make(chan int, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		startHistoryRetention(ctx, store, 15, time.Hour)
		close(done)
	}()

	select {
	case days := <-store.called:
		if days != 15 {
			t.Fatalf("retention days=%d", days)
		}
	case <-time.After(time.Second):
		t.Fatal("retention cleanup did not run immediately")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retention cleanup did not stop")
	}
}

func TestOfflineOutboxCancellationRunsImmediately(t *testing.T) {
	store := &fakeOfflineOutboxStore{called: make(chan time.Duration, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		startOfflineOutboxCancellation(ctx, store, 5*time.Minute, time.Hour)
		close(done)
	}()

	select {
	case after := <-store.called:
		if after != 5*time.Minute {
			t.Fatalf("offline duration=%s", after)
		}
	case <-time.After(time.Second):
		t.Fatal("offline cancellation did not run immediately")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("offline cancellation did not stop")
	}
}

type fakeSetupStore struct {
	migrated   bool
	seeded     bool
	migrateErr error
	seedErr    error
}

type fakeHistoryRetentionStore struct {
	called chan int
}

type fakeOfflineOutboxStore struct {
	called chan time.Duration
}

func (s *fakeHistoryRetentionStore) PurgeExpiredHistory(_ context.Context, days int) (mysqlstore.RetentionCleanup, error) {
	s.called <- days
	return mysqlstore.RetentionCleanup{}, nil
}

func (s *fakeOfflineOutboxStore) CancelOfflineOutbox(_ context.Context, after time.Duration) (int64, error) {
	s.called <- after
	return 0, nil
}

func (s *fakeSetupStore) ApplyMigrations(context.Context) error {
	s.migrated = true
	return s.migrateErr
}

func (s *fakeSetupStore) SeedFromConfig(context.Context, config.Config) error {
	s.seeded = true
	return s.seedErr
}
