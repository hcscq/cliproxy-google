package cluster

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestEventWatcherWakeAndBatchedAfterHook(t *testing.T) {
	repo, ctx := newTestKVRepository(t)
	var events, batches atomic.Int32
	w := NewEventWatcherFrom(repo, time.Hour, 0, func(context.Context, ClusterEventRecord) error {
		events.Add(1)
		return nil
	})
	w.SetAfterBatch(func(context.Context) error { batches.Add(1); return nil })
	for i := 0; i < 3; i++ {
		if err := repo.AppendEvent(ctx, "auth", "update", "cred", int64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = w.Start(runCtx) }()
	w.Wake() // the one-hour ticker would never fire in this test
	deadline := time.Now().Add(2 * time.Second)
	for events.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(400 * time.Millisecond) // let the follow-up poll run (finds nothing)
	if events.Load() != 3 || batches.Load() != 1 {
		t.Fatalf("events=%d batches=%d, want 3 and 1", events.Load(), batches.Load())
	}
}
