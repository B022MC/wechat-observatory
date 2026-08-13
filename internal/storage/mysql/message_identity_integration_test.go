package mysql

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"wechat-observatory/internal/bridge"
)

func TestMessageIdentityV2MySQLIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv(offlineOutboxIntegrationDSNEnv))
	if dsn == "" {
		t.Skipf("set %s to run the disposable MySQL integration test", offlineOutboxIntegrationDSNEnv)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var database string
	if err := store.db.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&database); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(database), "test") {
		t.Fatalf("refusing to run integration cleanup against non-test database %q", database)
	}
	if err := store.ApplyMigrations(ctx); err != nil {
		t.Fatal(err)
	}

	device := fmt.Sprintf("identity_v2_it_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM bridge_message_events WHERE device = ?`, device)
	})
	base := bridge.MessageEvent{
		ID: "882", EventID: 882, ChatRecordID: 882, Device: device,
		OwnerWxID: "wxid_self", From: "wxid_self", To: "filehelper", Text: "下39",
		Direction: bridge.DirectionSent, MessageType: 1, RawProvider: "lsposed", CreateTime: 1_786_530_000,
	}
	base.EventKey = base.CanonicalEventKeyV2()

	// Two replicas racing the same module retry must converge on one stored row.
	const replicas = 2
	ids := make(chan int64, replicas)
	errs := make(chan error, replicas)
	var wg sync.WaitGroup
	for range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stored, recordErr := store.RecordInboundEvent(ctx, base)
			if recordErr != nil {
				errs <- recordErr
				return
			}
			ids <- stored.Sequence
		}()
	}
	wg.Wait()
	close(errs)
	for recordErr := range errs {
		t.Fatal(recordErr)
	}
	close(ids)
	var sequence int64
	for id := range ids {
		if sequence == 0 {
			sequence = id
		}
		if id != sequence {
			t.Fatalf("same v2 retry produced sequences %d and %d", sequence, id)
		}
	}

	changed := base
	changed.CreateTime++
	changed.EventKey = changed.CanonicalEventKeyV2()
	second, err := store.RecordInboundEvent(ctx, changed)
	if err != nil {
		t.Fatal(err)
	}
	if second.Sequence == sequence || second.EventKey == base.EventKey {
		t.Fatalf("same local ID at a different source time was not distinct: first=%d/%s second=%d/%s", sequence, base.EventKey, second.Sequence, second.EventKey)
	}

	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bridge_message_events WHERE device = ?`, device).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("stored rows = %d, want one retry row plus one later occurrence", count)
	}
}
