package home

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/router-for-me/CLIProxyAPIHome/internal/redisx"
)

type fakeUsageItemsStore struct {
	mu        sync.Mutex
	failLeft  int
	rows      map[string]string // stream id -> payload
	redeliver int
}

func (f *fakeUsageItemsStore) StoreUsageStreamItems(_ context.Context, payloads []string, _ []time.Time, ids []string, redelivered bool) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failLeft > 0 {
		f.failLeft--
		return 0, errors.New("postgres down")
	}
	n := 0
	for i := range payloads {
		if redelivered {
			f.redeliver++
			if _, ok := f.rows[ids[i]]; ok {
				continue // dedupe as AppendUsageBatch does
			}
		}
		if _, dup := f.rows[ids[i]]; dup {
			panic("duplicate row for " + ids[i])
		}
		f.rows[ids[i]] = payloads[i]
		n++
	}
	return n, nil
}

func (f *fakeUsageItemsStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

func testUsageRedis(t *testing.T) *redisx.Client {
	t.Helper()
	addr := os.Getenv("CLIPROXY_HOME_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("CLIPROXY_HOME_TEST_REDIS_ADDR is not configured")
	}
	rc := redisx.Open(redisx.Config{Enabled: true, Addr: addr, KeyPrefix: "us" + time.Now().Format("150405.000000") + ":"})
	t.Cleanup(func() { _ = rc.Close() })
	return rc
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestUsageStreamFlushesAndAcks(t *testing.T) {
	rc := testUsageRedis(t)
	store := &fakeUsageItemsStore{rows: map[string]string{}}
	s := newUsageStream(rc, store, "h1")
	go s.runAppender()
	defer s.Close(time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	for i := 0; i < 1200; i++ {
		if !s.Append(ctx, `{"i":1}`, time.Now()) {
			t.Fatal("append failed")
		}
	}
	waitFor(t, 10*time.Second, func() bool { return store.count() == 1200 }, "1200 rows")
	st := s.Status(ctx)
	waitFor(t, 3*time.Second, func() bool { st = s.Status(ctx); return st.Length == 0 && st.Pending == 0 }, "stream drained")
	if st.Flushed != 1200 || st.Appended != 1200 {
		t.Fatalf("status %+v", st)
	}
}

func TestUsageStreamRetriesWhenStoreFails(t *testing.T) {
	rc := testUsageRedis(t)
	store := &fakeUsageItemsStore{rows: map[string]string{}, failLeft: 2}
	s := newUsageStream(rc, store, "h1")
	go s.runAppender()
	defer s.Close(time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 50; i++ {
		s.Append(ctx, `{"i":1}`, time.Now())
	}
	go s.Run(ctx)
	waitFor(t, 15*time.Second, func() bool { return store.count() == 50 }, "rows after PG recovers")
	if store.redeliver == 0 {
		t.Fatal("expected the failed batch to be re-read as pending")
	}
}

func TestUsageStreamClaimsFromDeadConsumer(t *testing.T) {
	old := usageStreamClaimIdle
	usageStreamClaimIdle = 100 * time.Millisecond
	defer func() { usageStreamClaimIdle = old }()
	rc := testUsageRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dead := newUsageStream(rc, &fakeUsageItemsStore{rows: map[string]string{}}, "dead")
	go dead.runAppender()
	defer dead.Close(time.Second)
	if err := dead.ensureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		dead.Append(ctx, `{"i":1}`, time.Now())
	}
	// The dead Home read the entries and crashed before acking.
	if _, err := rc.Bulk().XReadGroup(ctx, &redis.XReadGroupArgs{Group: usageStreamGroup, Consumer: "dead", Streams: []string{dead.key(), ">"}, Count: 100}).Result(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	store := &fakeUsageItemsStore{rows: map[string]string{}}
	live := newUsageStream(rc, store, "live")
	go live.runAppender()
	defer live.Close(time.Second)
	go live.Run(ctx)
	waitFor(t, 10*time.Second, func() bool { return store.count() == 20 }, "claimed rows")
	waitFor(t, 3*time.Second, func() bool { return live.Status(ctx).Pending == 0 }, "no pending")
}

func TestUsageStreamFallsBackWhenRedisDown(t *testing.T) {
	rc := redisx.Open(redisx.Config{Enabled: true, Addr: "127.0.0.1:1"})
	defer rc.Close()
	s := newUsageStream(rc, &fakeUsageItemsStore{rows: map[string]string{}}, "h1")
	go s.runAppender()
	defer s.Close(time.Second)
	if s.Append(context.Background(), `{"i":1}`, time.Now()) {
		t.Fatal("append should fail over to the in-memory path")
	}
	if s.Status(context.Background()).Fallback != 1 {
		t.Fatal("fallback not counted")
	}
}

func TestUsageStreamConsumerNamesAreUnique(t *testing.T) {
	a := newUsageStream(nil, nil, "")
	b := newUsageStream(nil, nil, "")
	if a.consumer == b.consumer {
		t.Fatalf("consumer names collide: %q", a.consumer)
	}
}

// Two consumers draining one stream must store every entry exactly once.
func TestUsageStreamTwoConsumersNoDuplicates(t *testing.T) {
	rc := testUsageRedis(t)
	store := &fakeUsageItemsStore{rows: map[string]string{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newUsageStream(rc, store, "")
	go a.runAppender()
	defer a.Close(time.Second)
	b := newUsageStream(rc, store, "")
	go a.Run(ctx)
	go b.Run(ctx)
	for i := 0; i < 3000; i++ {
		a.Append(ctx, `{"i":1}`, time.Now())
	}
	waitFor(t, 15*time.Second, func() bool { return store.count() == 3000 }, "3000 rows")
}

// Payloads buffered when the stream closes are flushed into Redis.
func TestUsageStreamCloseFlushesBuffer(t *testing.T) {
	rc := testUsageRedis(t)
	s := newUsageStream(rc, &fakeUsageItemsStore{rows: map[string]string{}}, "")
	go s.runAppender()
	for i := 0; i < 1000; i++ {
		if !s.Append(context.Background(), `{"i":1}`, time.Now()) {
			t.Fatal("append failed")
		}
	}
	s.Close(5 * time.Second)
	if n, _ := rc.Bulk().XLen(context.Background(), s.key()).Result(); n != 1000 {
		t.Fatalf("xlen=%d want 1000", n)
	}
	if s.Append(context.Background(), `{"i":1}`, time.Now()) {
		t.Fatal("append after close must fall back")
	}
}

// A proxy resends a batch whose LPUSH reply was lost: payloads already
// appended (same usage_id) must not become a second entry.
func TestUsageStreamDropsResentUsageID(t *testing.T) {
	rc := testUsageRedis(t)
	store := &fakeUsageItemsStore{rows: map[string]string{}}
	s := newUsageStream(rc, store, "h1")
	go s.runAppender()
	defer s.Close(time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	id := fmt.Sprintf("u-%d", time.Now().UnixNano())
	for i := 0; i < 3; i++ {
		if !s.Append(ctx, `{"usage_id":"`+id+`"}`, time.Now()) {
			t.Fatal("append failed")
		}
	}
	if !s.Append(ctx, `{"usage_id":"`+id+`-other"}`, time.Now()) {
		t.Fatal("append failed")
	}
	waitFor(t, 5*time.Second, func() bool { return s.Status(ctx).Deduped == 2 }, "2 deduped")
	waitFor(t, 5*time.Second, func() bool { return store.count() == 2 }, "2 rows")
	time.Sleep(300 * time.Millisecond)
	if n := store.count(); n != 2 {
		t.Fatalf("rows=%d want 2", n)
	}
}
